// Package scheduler 跑后台定时任务:封禁过期清扫、会话 GC、心跳超时清理。
// 全部任务从 config 表 tasks 键读取间隔(毫秒),默认值见 defaults。
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"magirecocn-revival/api-server/internal/api/client"
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

	// 离线包自动打包已随资源分发面移交资源分发服务端(magirecocn-resource-server)。
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
