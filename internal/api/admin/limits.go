package admin

import (
	"context"
	"net/http"
	"sync/atomic"

	"magirecocn-revival/api-server/internal/api/respond"
	"magirecocn-revival/api-server/internal/store"
)

// Limits 持有运行时可调的大小上限(全局请求体 / 热更新包),用 atomic 存字节数:
//   - 中间件(BodyLimitFunc)按请求读 GlobalBodyBytes(),管理员后台改后即时生效;
//   - 热更新 handler 读 HotUpdateBytes()。
//
// 值持久化在 config 表 "limits" 键,启动时从那里加载(缺失则用注入的默认值)。
type Limits struct {
	st     *store.Store
	global atomic.Int64 // 全局请求体上限(字节)
	hot    atomic.Int64 // 热更新包上传/下载上限(字节)
}

// limitsCfg 是 config 表 "limits" 键的落库结构(以 MiB 存,便于人读 / 后台编辑)。
type limitsCfg struct {
	GlobalBodyMB int64 `json:"global_body_mb"`
	HotUpdateMB  int64 `json:"hotupdate_mb"`
}

const (
	limitGlobalMinMB = 1
	limitGlobalMaxMB = 4096
	limitHotMinMB    = 1
	limitHotMaxMB    = 8192
)

// NewLimits 用默认字节值初始化,再尝试用 config 表 "limits" 覆盖。
func NewLimits(ctx context.Context, st *store.Store, defGlobalBytes, defHotBytes int64) *Limits {
	l := &Limits{st: st}
	l.global.Store(defGlobalBytes)
	l.hot.Store(defHotBytes)
	var c limitsCfg
	if ok, _ := st.ConfigGet(ctx, "limits", &c); ok {
		if c.GlobalBodyMB > 0 {
			l.global.Store(clampMB(c.GlobalBodyMB, limitGlobalMinMB, limitGlobalMaxMB) << 20)
		}
		if c.HotUpdateMB > 0 {
			l.hot.Store(clampMB(c.HotUpdateMB, limitHotMinMB, limitHotMaxMB) << 20)
		}
	}
	return l
}

func (l *Limits) GlobalBodyBytes() int64 { return l.global.Load() }
func (l *Limits) HotUpdateBytes() int64  { return l.hot.Load() }

// SnapshotMB 返回当前两个上限的 MiB 值(向下取整),供 GET / 聚合状态使用。
func (l *Limits) SnapshotMB() (globalMB, hotMB int64) {
	return l.global.Load() >> 20, l.hot.Load() >> 20
}

// Set 校验并钳制后持久化到 config 表,再更新 atomic。返回钳制后的 MiB 值。
func (l *Limits) Set(ctx context.Context, globalMB, hotMB int64) (int64, int64, error) {
	globalMB = clampMB(globalMB, limitGlobalMinMB, limitGlobalMaxMB)
	hotMB = clampMB(hotMB, limitHotMinMB, limitHotMaxMB)
	if err := l.st.ConfigSet(ctx, "limits", limitsCfg{GlobalBodyMB: globalMB, HotUpdateMB: hotMB}); err != nil {
		return 0, 0, err
	}
	l.global.Store(globalMB << 20)
	l.hot.Store(hotMB << 20)
	return globalMB, hotMB, nil
}

func clampMB(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// limitsState 给 /admin/state 聚合用;Limits 未注入时回退到展示用默认值。
func limitsState(l *Limits) map[string]any {
	if l == nil {
		return map[string]any{"global_body_mb": 8, "hotupdate_mb": 1024}
	}
	g, ht := l.SnapshotMB()
	return map[string]any{"global_body_mb": g, "hotupdate_mb": ht}
}

// ── /admin/limits ─────────────────────────────────────────────────────

func (h *Handler) limitsGet(w http.ResponseWriter, r *http.Request) {
	if h.Limits == nil {
		respond.Fail(w, http.StatusServiceUnavailable, "limits_unavailable", "")
		return
	}
	g, ht := h.Limits.SnapshotMB()
	respond.OKRaw(w, map[string]any{"success": true, "global_body_mb": g, "hotupdate_mb": ht})
}

func (h *Handler) limitsPut(w http.ResponseWriter, r *http.Request) {
	if h.Limits == nil {
		respond.Fail(w, http.StatusServiceUnavailable, "limits_unavailable", "")
		return
	}
	var req limitsCfg
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	g, ht, err := h.Limits.Set(r.Context(), req.GlobalBodyMB, req.HotUpdateMB)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "limits.update", "", map[string]any{"global_body_mb": g, "hotupdate_mb": ht})
	respond.OKRaw(w, map[string]any{"success": true, "global_body_mb": g, "hotupdate_mb": ht})
}
