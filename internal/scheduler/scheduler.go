// Package scheduler 跑后台定时任务:封禁过期清扫、会话 GC、心跳超时清理。
// 全部任务从 config 表 tasks 键读取间隔(毫秒),默认值见 defaults。
package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"magirecocn-revival/api-server/internal/api/client"
	"magirecocn-revival/api-server/internal/packer"
	"magirecocn-revival/api-server/internal/store"
)

// Config 各定时任务的运行间隔(毫秒)。
type Config struct {
	BanSweepMs         int64 `json:"ban_sweep_ms"`
	SessionGCMs        int64 `json:"session_gc_ms"`
	HeartbeatSweepMs   int64 `json:"heartbeat_sweep_ms"`
	HeartbeatTimeoutMs int64 `json:"heartbeat_timeout_ms"`
}

func defaults() Config {
	return Config{
		BanSweepMs:         60_000,
		SessionGCMs:        300_000,
		HeartbeatSweepMs:   30_000,
		HeartbeatTimeoutMs: 120_000,
	}
}

// MirrorFlusher 是 MirrorTracker 的最小接口,避免 scheduler 直接依赖 admin 包（循环引用）。
type MirrorFlusher interface {
	Flush(ctx context.Context)
}

type Scheduler struct {
	St     *store.Store
	Hearts *client.Heartbeats
	Log    *slog.Logger

	// 离线包打包参数,由 main 注入。SourceDir 为空时跳过这个 goroutine。
	PackSourceDir string
	PackDestDir   string
	// OfflineURL 拼对外下载 URL 的回调;失败时只更 sha256/version,不动 URL。
	OfflineURL func() string

	// MirrorStats 若非 nil,每 30 秒调用一次 Flush 把积累字节写入数据库。
	MirrorStats MirrorFlusher
}

// Start 派生若干 goroutine 跑各任务,直到 ctx 结束。
func (s *Scheduler) Start(ctx context.Context) {
	cfg := defaults()
	_, _ = s.St.ConfigGet(ctx, "tasks", &cfg)

	go s.runEvery(ctx, time.Duration(cfg.BanSweepMs)*time.Millisecond, "ban_sweep", s.sweepBans)
	go s.runEvery(ctx, time.Duration(cfg.SessionGCMs)*time.Millisecond, "session_gc", s.sweepSessions)
	go s.runEvery(ctx, time.Duration(cfg.HeartbeatSweepMs)*time.Millisecond, "heartbeat_sweep",
		func(ctx context.Context) error {
			s.Hearts.Sweep(time.Duration(cfg.HeartbeatTimeoutMs) * time.Millisecond)
			return nil
		})
	go s.runUnverifiedPurge(ctx)

	// 镜像流量统计持久化（每 30 秒写库一次）。
	if s.MirrorStats != nil {
		go s.runEvery(ctx, 30*time.Second, "mirror_stats_flush", func(ctx context.Context) error {
			s.MirrorStats.Flush(ctx)
			return nil
		})
	}

	// 离线包自动打包(从 config.auto_package 读 enabled + intervalSec)。
	if s.PackSourceDir != "" && s.PackDestDir != "" {
		go s.runAutoPackage(ctx)
	}
}

// unverifiedPurgeCfg 对应 config 表 "unverified_purge" 键。
type unverifiedPurgeCfg struct {
	Enabled    bool  `json:"enabled"`
	RetainDays int   `json:"retainDays"`
	ScanSec    int64 `json:"scanSec"`
}

// runUnverifiedPurge 每 5 分钟检查一次配置,若已启用且距上次运行超过 scanSec,
// 则删除注册超过 retainDays 天仍未验证邮箱的账号。
func (s *Scheduler) runUnverifiedPurge(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	var lastRun time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var cfg unverifiedPurgeCfg
			cfg.Enabled = false
			cfg.RetainDays = 30
			cfg.ScanSec = 86400
			_, _ = s.St.ConfigGet(ctx, "unverified_purge", &cfg)
			if !cfg.Enabled || cfg.RetainDays < 1 {
				continue
			}
			scanInterval := time.Duration(cfg.ScanSec) * time.Second
			if scanInterval < time.Minute {
				scanInterval = time.Minute
			}
			if !lastRun.IsZero() && time.Since(lastRun) < scanInterval {
				continue
			}
			cutoff := time.Now().AddDate(0, 0, -cfg.RetainDays)
			n, err := s.St.AccountPurgeUnverified(ctx, cutoff)
			if err != nil {
				if s.Log != nil {
					s.Log.Warn("purge unverified accounts failed", "err", err)
				}
			} else if n > 0 && s.Log != nil {
				s.Log.Info("purged unverified accounts", "count", n, "older_than_days", cfg.RetainDays)
			}
			lastRun = time.Now()
		}
	}
}

// autoPackageCfg 与 admin.autoPackageCfg 同字段;独立定义避免 import cycle。
type autoPackageCfg struct {
	Enabled        bool   `json:"enabled"`
	IntervalSec    int    `json:"intervalSec"`
	RetainVersions int    `json:"retainVersions"`
	LastRunAt      int64  `json:"lastRunAt"`
	LastResult     string `json:"lastResult"`
	InProgress     bool   `json:"inProgress"`
}

// runAutoPackage 周期性看 auto_package 配置:enabled=true 且距上次 LastRunAt
// 超过 intervalSec 时就跑一次。检查间隔取 intervalSec 与 60s 的较小值,
// 保证配置改成更频繁时能及时生效。
func (s *Scheduler) runAutoPackage(ctx context.Context) {
	check := func() {
		var raw json.RawMessage
		// 直接读全部字段,避免和 admin 包重复定义结构体
		ok, err := s.St.ConfigGet(ctx, "auto_package", &raw)
		if err != nil || !ok {
			return
		}
		var c autoPackageCfg
		if err := json.Unmarshal(raw, &c); err != nil {
			return
		}
		if !c.Enabled || c.InProgress {
			return
		}
		interval := time.Duration(c.IntervalSec) * time.Second
		if interval < time.Minute {
			interval = time.Minute
		}
		if c.LastRunAt != 0 && time.Since(time.UnixMilli(c.LastRunAt)) < interval {
			return
		}
		s.runPackOnce(ctx, c)
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

// runPackOnce 抢锁 → 打包 → 落库 → 释放锁。与 /admin/auto-package/run 同语义。
func (s *Scheduler) runPackOnce(ctx context.Context, c autoPackageCfg) {
	c.InProgress = true
	_ = s.St.ConfigSet(ctx, "auto_package", c)
	defer func() {
		c.InProgress = false
		c.LastRunAt = time.Now().UnixMilli()
		_ = s.St.ConfigSet(ctx, "auto_package", c)
	}()
	res, err := packer.RunOnce(ctx, packer.Config{
		SourceDir:   s.PackSourceDir,
		DestDir:     s.PackDestDir,
		RetainN:     c.RetainVersions,
		Description: "scheduled auto-package",
	})
	if err != nil {
		c.LastResult = "fail"
		if s.Log != nil {
			s.Log.Warn("auto-package 失败", "err", err)
		}
		return
	}
	c.LastResult = "ok"
	url := ""
	if s.OfflineURL != nil {
		url = s.OfflineURL() + "/" + res.Filename
	}
	uploaded := time.Now().UnixMilli()
	_ = s.St.OfflinePackageSet(ctx, store.OfflinePackage{
		DownloadURL: url, PackageVersion: res.Version,
		SHA256: res.SHA256, Size: res.Size, UploadedAt: &uploaded,
	})
	if s.Log != nil {
		s.Log.Info("auto-package 完成",
			"version", res.Version, "size", res.Size,
			"sha256_prefix", res.SHA256[:12])
	}
}

func (s *Scheduler) runEvery(ctx context.Context, interval time.Duration, name string, fn func(context.Context) error) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := fn(ctx); err != nil {
				if s.Log != nil {
					s.Log.Warn("scheduler task failed", "name", name, "err", err)
				}
			}
		}
	}
}

func (s *Scheduler) sweepBans(ctx context.Context) error {
	_, err := s.St.BanSweepExpired(ctx)
	return err
}

func (s *Scheduler) sweepSessions(ctx context.Context) error {
	return s.St.SessionGC(ctx)
}
