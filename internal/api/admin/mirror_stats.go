package admin

import (
	"context"
	"sync"
	"time"

	"magirecocn-revival/api-server/internal/store"
)

// MirrorTracker 追踪每个镜像的瞬时速度与累计流量。
//
// 数据来源：心跳上报的 speed_bps × 心跳间隔（≈5s），按设备上次收到的分派镜像均摊。
// 每次 Flush（默认 30 秒）把积累字节写入 mirror_traffic 表，并更新超限标志。
type MirrorTracker struct {
	mu     sync.Mutex
	perURL map[string]*mtEntry
	st     *store.Store
}

type mtEntry struct {
	speedBps    int64 // 当前瞬时速度（上次心跳写入值，bytes/s）
	pendingBytes int64 // 自上次 Flush 以来积累的估算字节数
	limitBytes  int64 // 日限额（0=不限）
	limitBps    int64 // 速度上限（0=不限）
	exceeded    bool  // 本日是否已超限
	// 来自 DB 的持久化字段（Snapshot 用）
	todayBytes  int64
	monthBytes  int64
	cumBytes    int64
}

// MirrorStatSnapshot 单镜像的统计快照（给管理面板用）。
type MirrorStatSnapshot struct {
	SpeedBps    int64 `json:"speed_bps"`
	TodayBytes  int64 `json:"today_bytes"`
	MonthBytes  int64 `json:"month_bytes"`
	CumBytes    int64 `json:"cum_bytes"`
	LimitBytes  int64 `json:"daily_limit_bytes"`
	LimitBps    int64 `json:"speed_limit_bps"`
	Exceeded    bool  `json:"exceeded"`
}

func NewMirrorTracker(st *store.Store) *MirrorTracker {
	return &MirrorTracker{
		perURL: map[string]*mtEntry{},
		st:     st,
	}
}

// Load 从数据库载入历史流量与限额配置。
func (t *MirrorTracker) Load(ctx context.Context) {
	rows, err := t.st.MirrorTrafficGetAll(ctx)
	if err != nil {
		return
	}
	today := time.Now().Format("2006-01-02")
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, row := range rows {
		e := &mtEntry{
			limitBytes: row.DailyLimitBytes,
			limitBps:   row.SpeedLimitBps,
			todayBytes: row.TodayBytes,
			monthBytes: row.MonthBytes,
			cumBytes:   row.CumBytes,
		}
		// 若跨日了，今日字节归零
		if row.TodayDate != today {
			e.todayBytes = 0
		}
		if row.DailyLimitBytes > 0 && row.TodayDate == today && row.TodayBytes >= row.DailyLimitBytes {
			e.exceeded = true
		}
		t.perURL[row.URL] = e
	}
}

// RecordSpeed 记录某组镜像的当前速度，并累计估算字节（速度 × 5s 心跳间隔）。
// mirrorURLs 是本次心跳设备对应的分派镜像列表；速度均摊到每个镜像。
func (t *MirrorTracker) RecordSpeed(mirrorURLs []string, totalBps int64) {
	if len(mirrorURLs) == 0 || totalBps <= 0 {
		return
	}
	perBps := totalBps / int64(len(mirrorURLs))
	// 心跳间隔约 5 秒，估算字节
	perBytes := perBps * 5

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, url := range mirrorURLs {
		e, ok := t.perURL[url]
		if !ok {
			e = &mtEntry{}
			t.perURL[url] = e
		}
		e.speedBps = perBps
		e.pendingBytes += perBytes
	}
}

// IsEnabled 检查镜像是否可被派发（未超日限额、未超速度上限）。
// 速度上限以当前 speedBps 为准（软限制，下次 Flush 前生效）。
func (t *MirrorTracker) IsEnabled(url string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.perURL[url]
	if !ok {
		return true
	}
	if e.exceeded {
		return false
	}
	if e.limitBps > 0 && e.speedBps > e.limitBps {
		return false
	}
	return true
}

// Snapshot 返回所有已追踪镜像的统计快照。
func (t *MirrorTracker) Snapshot() map[string]MirrorStatSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]MirrorStatSnapshot, len(t.perURL))
	for url, e := range t.perURL {
		out[url] = MirrorStatSnapshot{
			SpeedBps:   e.speedBps,
			TodayBytes: e.todayBytes,
			MonthBytes: e.monthBytes,
			CumBytes:   e.cumBytes,
			LimitBytes: e.limitBytes,
			LimitBps:   e.limitBps,
			Exceeded:   e.exceeded,
		}
	}
	return out
}

// SetLimits 更新镜像的日限额与速度上限（持久化 + 内存立即生效）。
func (t *MirrorTracker) SetLimits(ctx context.Context, url string, dailyLimitBytes, speedLimitBps int64) error {
	if err := t.st.MirrorTrafficSetLimits(ctx, url, dailyLimitBytes, speedLimitBps); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.perURL[url]
	if !ok {
		e = &mtEntry{}
		t.perURL[url] = e
	}
	e.limitBytes = dailyLimitBytes
	e.limitBps = speedLimitBps
	// 重新评估超限状态
	if dailyLimitBytes > 0 && e.todayBytes >= dailyLimitBytes {
		e.exceeded = true
	} else {
		e.exceeded = false
	}
	return nil
}

// Flush 把积累字节写入数据库，同步更新各镜像的今日/月/累计字节与超限状态。
// 应由调度器定期调用（建议 30 秒）。
func (t *MirrorTracker) Flush(ctx context.Context) {
	type pending struct {
		url   string
		bytes int64
	}
	t.mu.Lock()
	var items []pending
	for url, e := range t.perURL {
		if e.pendingBytes > 0 {
			items = append(items, pending{url, e.pendingBytes})
			e.pendingBytes = 0
		}
	}
	t.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	month := time.Now().Format("2006-01")
	for _, p := range items {
		row, err := t.st.MirrorTrafficAddBytes(ctx, p.url, p.bytes, today, month)
		if err != nil {
			continue
		}
		t.mu.Lock()
		e, ok := t.perURL[p.url]
		if ok {
			e.todayBytes = row.TodayBytes
			e.monthBytes = row.MonthBytes
			e.cumBytes = row.CumBytes
			e.exceeded = e.limitBytes > 0 && row.TodayBytes >= e.limitBytes
		}
		t.mu.Unlock()
	}
}
