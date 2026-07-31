package client

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"magirecocn-revival/cnv-server/internal/store"
	"magirecocn-revival/cnv-server/internal/totentanz"
)

// ── config 包装(读 KV 表)────────────────────────────────────────────────

// serverCfg 握手响应里的 server 子对象:{status, message, end_time} 平级。
// end_time 是 **Unix 秒**,不是毫秒。
type serverCfg struct {
	Status  string `json:"status"`   // ok | maintenance | err
	Message string `json:"message"`  // 维护/故障提示
	EndTime int64  `json:"end_time"` // Unix 秒;0 = 未知/无
	// 旧字段名兼容(管理后台 PUT 进来的可能是这两个)
	MaintenanceMessage string `json:"maintenance_message,omitempty"`
	EstimatedEnd       *int64 `json:"estimated_end,omitempty"`
}

func getServerConfig(ctx context.Context, st *store.Store) serverCfg {
	var c serverCfg
	_, _ = st.ConfigGet(ctx, "server", &c)
	if c.Status == "" {
		c.Status = "ok"
	}
	// 兼容旧字段名:把 maintenance_message → message,estimated_end(ms) → end_time(s)
	if c.Message == "" && c.MaintenanceMessage != "" {
		c.Message = c.MaintenanceMessage
	}
	if c.EndTime == 0 && c.EstimatedEnd != nil {
		c.EndTime = *c.EstimatedEnd / 1000
	}
	return c
}

// featuresCfg 握手响应里的 features 子对象。
//
// Android 的 online_download / offline_package(两种整包资源准备方式)已随
// 相应端点一并移除:Web 端按需流式取用,没有"资源准备阶段"这个概念。
type featuresCfg struct {
	// AccountEnabled 账号系统总开关。false = 跳过登录与存档同步,直接进游戏。
	// 缺省(或字段不存在)等同于 true,向后兼容。
	AccountEnabled  bool   `json:"account_enabled"`
	DisabledMessage string `json:"disabled_message"`
}

func getFeatures(ctx context.Context, st *store.Store) featuresCfg {
	c := featuresCfg{AccountEnabled: true}
	_, _ = st.ConfigGet(ctx, "features", &c)
	return c
}

// servicesCfg /client/init 响应里的可选 services 对象。
//
// 三字段全部可选,**未配置时省略 key**(不发送 JSON null)。
//
// ⚠️ proxy_backends / game_server_host 是 Android 端把原版游戏 WebView 流量
// 导向本项目代理用的,Web 端没有对应概念,属待清理项;因其配置面(管理 API +
// 面板 UI)不属协议层,留待单独一次改动移除。cap_worker_url(注册用的 PoW
// 验证码服务)仍然需要。
//
//	cap_worker_url    string   — 人机验证服务地址(含协议,不带尾斜杠)
//	proxy_backends    []string — 游戏代理后端列表(按数组顺序尝试)
//	game_server_host  string   — 原版游戏服务器 scheme+host,用于代理精确匹配
type servicesCfg struct {
	CapWorkerURL   string   `json:"cap_worker_url,omitempty"`
	ProxyBackends  []string `json:"proxy_backends,omitempty"`
	GameServerHost string   `json:"game_server_host,omitempty"`

	// GameMaxThreads 透传发现响应里的 max_threads(上游建议的 HTTP/2 并发数,
	// 实测该值会动态变化)。0 = 不下发,客户端沿用自身默认。
	GameMaxThreads int `json:"game_max_threads,omitempty"`

	// ResourceBase 是上游 Totentanz 的**资源**基址(完整 URL,含路径),来自端点发现。
	//
	// 与 GameServerBase 严格区分:那个是游戏 **API** 服务器,这个是**资源 CDN**。
	// 二者语义不同、来源不同,混用会让代理耗尽时的 API 回退打到静态资源机上。
	ResourceBase string `json:"resource_base,omitempty"`
}

func getServicesConfig(ctx context.Context, st *store.Store, disc *totentanz.Client) servicesCfg {
	var c servicesCfg
	_, _ = st.ConfigGet(ctx, "services", &c)
	// 过滤掉空字符串和明显非法的代理 URL
	filtered := make([]string, 0, len(c.ProxyBackends))
	for _, p := range c.ProxyBackends {
		if p = strings.TrimRight(strings.TrimSpace(p), "/"); p != "" {
			filtered = append(filtered, p)
		}
	}
	c.ProxyBackends = filtered
	c.CapWorkerURL = strings.TrimRight(strings.TrimSpace(c.CapWorkerURL), "/")
	// game_server_host:客户端期望纯 host(2026-05-29 修复说明)。
	// 老配置可能写成 https://host/,这里向后兼容地剥离 scheme + 路径,
	// 让旧库不需要手工迁移就能继续给客户端正确的值。
	c.GameServerHost = normalizeGameServerHost(c.GameServerHost)
	if ep := disc.Get(); ep != nil {
		c.ResourceBase = ep.Base
		if ep.MaxThreads > 0 {
			c.GameMaxThreads = ep.MaxThreads
		}
	}
	return c
}

// normalizeGameServerHost 把任意输入归一化成纯 host(无 scheme/路径/尾斜杠)。
// 用于向后兼容老配置里残留的完整 URL 格式。
func normalizeGameServerHost(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// 剥离 scheme
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// 剥离路径 / 查询 / 片段
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, "/")
}

// toResponseMap 把 services 配置序列化成响应里的对象,**空字段省略 key**。
// 全部为空时返回 nil,调用方应直接省略 services 键。
func (c servicesCfg) toResponseMap() map[string]any {
	out := map[string]any{}
	if c.CapWorkerURL != "" {
		out["cap_worker_url"] = c.CapWorkerURL
	}
	if len(c.ProxyBackends) > 0 {
		out["proxy_backends"] = c.ProxyBackends
	}
	if c.GameServerHost != "" {
		out["game_server_host"] = c.GameServerHost
	}
	if c.GameMaxThreads > 0 {
		out["game_max_threads"] = c.GameMaxThreads
	}
	if c.ResourceBase != "" {
		out["resource_base"] = c.ResourceBase
	}
	// 空判必须放在**所有**字段都填完之后。
	// 曾经它卡在 game_server_host 之后：只配了端点发现（resource_base /
	// game_max_threads）而没配那三个旧字段时，整个 services 会被判空吞掉，
	// 发现结果永远到不了客户端——而且没有任何报错。
	if len(out) == 0 {
		return nil
	}
	return out
}

// versions / offline_pack 两组 config 的读取器已随 APK 版本闸门与离线包端点移除。
// 管理后台仍可写这两个 key(见 internal/api/admin),但 /client/* 不再读取。

// ── 心跳内存存储 ────────────────────────────────────────────────────────

// hbFile 单个文件的下载进度。
//
// ⚠️ **新协议下不再有任何来源会填充它**:精简版心跳不接收上报载荷,
// Web 端按需流式取用资产,不存在"下载阶段"。此处保留仅因管理面板的在线设备页
// 与换线 UI 仍引用这套结构;它们与本类型将在一次单独的改动中一并移除。
type hbFile struct {
	Name     string  `json:"name"`
	Status   string  `json:"status"` // pending|downloading|done|failed
	Percent  float64 `json:"percent"`
	SpeedBps int64   `json:"speed_bps"`
}

// HBState 一个设备的最新心跳快照。
//
// 新协议下只有 Phase="game" 与 LastSeen 有意义:心跳的唯一职责是给服务端一个
// 下发封禁/维护状态的时机。其余字段恒为零值,见 hbFile 的说明。
type HBState struct {
	Progress    float64 `json:"progress"`
	SpeedBps    int64   `json:"speed_bps"`
	CurrentFile string  `json:"current_file"`
	Files       []hbFile
	Phase       string `json:"phase"` // download | game
	LastSeen    int64  `json:"last_seen"`
}

// Kind 区分这条心跳代表的客户端活动,供管理面板正确呈现:
//   - "game"：游戏内心跳(空 Files,仅用于收取封禁/维护指令),无下载进度/速度。
//   - "hotupdate"：热更新(客户端固定下载 cn_js_update.zip / cn_scenario_update.zip)。
//   - "online"：在线资源下载(客户端多线程下载游戏资源)。
//
// 离线整包由系统浏览器下载、客户端只做导入,不发心跳,故不在此枚举内。
func (s HBState) Kind() string {
	if s.Phase == "game" || len(s.Files) == 0 {
		return "game"
	}
	for _, f := range s.Files {
		if f.Name != "cn_js_update.zip" && f.Name != "cn_scenario_update.zip" {
			return "online"
		}
	}
	return "hotupdate"
}

// aggregateHBFiles 从客户端上报的逐文件状态聚合出整体进度、瞬时总速度与当前文件。
//   - 进度：所有文件 percent 的算术平均（客户端按文件粒度并行下载,等权近似总体）。
//   - 速度：仅 downloading 状态文件的 speed_bps 之和（done/pending/failed 不计）。
//   - 当前文件：第一个 downloading 文件名；没有则回退首个未完成文件。
func aggregateHBFiles(files []hbFile) (progress float64, speedBps int64, currentFile string) {
	if len(files) == 0 {
		return 0, 0, ""
	}
	var sumPct float64
	for _, f := range files {
		sumPct += f.Percent
		if f.Status == "downloading" {
			speedBps += f.SpeedBps
			if currentFile == "" {
				currentFile = f.Name
			}
		}
	}
	if currentFile == "" {
		for _, f := range files {
			if f.Status != "done" {
				currentFile = f.Name
				break
			}
		}
	}
	return sumPct / float64(len(files)), speedBps, currentFile
}

// SwitchAssignment 管理员通过 /admin/heartbeats/:id/switch-mirror 入队的换线指令。
//
// 内部用 file→mirror 映射方便管理面板编辑;下发给客户端时按 mirror 聚合成数组
// ([{mirror, files:[name]}]),即 ResourceFlow.java 期望的格式。
type SwitchAssignment struct {
	MirrorAssignments map[string]string `json:"mirror_assignments"` // key=file, val=mirror
	Message           string            `json:"message"`
}

// toClientList 转成客户端 ResourceFlow.java 期望的 assignments 数组形状:
//
//	[{mirror: "https://...", files: ["a.bin", "b.bin"]}, ...]
func (s *SwitchAssignment) toClientList() []map[string]any {
	if s == nil || len(s.MirrorAssignments) == 0 {
		return []map[string]any{}
	}
	byMirror := map[string][]string{}
	for file, mirror := range s.MirrorAssignments {
		byMirror[mirror] = append(byMirror[mirror], file)
	}
	out := make([]map[string]any, 0, len(byMirror))
	for mirror, files := range byMirror {
		out = append(out, map[string]any{"mirror": mirror, "files": files})
	}
	return out
}

// maxHeartbeatDevices 在线设备内存表的硬上限。防止攻击者用海量不同 device_id
// 灌爆内存(每个 /client/init 会签发 client_session,signature 白名单为空时尤其
// 廉价)。达到上限时,新设备会挤掉最久未上报的那个。
const maxHeartbeatDevices = 50000

// Heartbeats 在线设备的内存状态(进程内,不跨节点)。
type Heartbeats struct {
	mu         sync.Mutex
	states     map[string]*HBState
	switches   map[string]*SwitchAssignment
	dispatched map[string][]string // deviceID → 上次 online-download 下发的镜像 URL 列表
}

func NewHeartbeats() *Heartbeats {
	return &Heartbeats{
		states:     map[string]*HBState{},
		switches:   map[string]*SwitchAssignment{},
		dispatched: map[string][]string{},
	}
}

func (h *Heartbeats) Update(deviceID string, st *HBState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// 已存在的设备直接更新,不受上限影响。
	if _, ok := h.states[deviceID]; !ok && len(h.states) >= maxHeartbeatDevices {
		h.evictOldestLocked()
	}
	h.states[deviceID] = st
}

// evictOldestLocked 在持锁状态下剔除 LastSeen 最早的一个设备,给新设备腾位。
func (h *Heartbeats) evictOldestLocked() {
	var oldestKey string
	var oldest int64 = 1<<63 - 1
	for k, v := range h.states {
		if v.LastSeen < oldest {
			oldest = v.LastSeen
			oldestKey = k
		}
	}
	if oldestKey != "" {
		delete(h.states, oldestKey)
		delete(h.switches, oldestKey)
		delete(h.dispatched, oldestKey)
	}
}

func (h *Heartbeats) Remove(deviceID string) {
	h.mu.Lock()
	delete(h.states, deviceID)
	delete(h.switches, deviceID)
	delete(h.dispatched, deviceID)
	h.mu.Unlock()
}

// RecordDispatch 记录 online-download 向某设备下发的镜像 URL 列表；
// 之后该设备的心跳速度会被归因到这些镜像（均摊）。
func (h *Heartbeats) RecordDispatch(deviceID string, mirrorURLs []string) {
	if len(mirrorURLs) == 0 {
		return
	}
	h.mu.Lock()
	h.dispatched[deviceID] = mirrorURLs
	h.mu.Unlock()
}

// GetDispatched 返回指定设备上次 online-download 收到的镜像 URL 列表。
func (h *Heartbeats) GetDispatched(deviceID string) []string {
	h.mu.Lock()
	urls := h.dispatched[deviceID]
	h.mu.Unlock()
	return urls
}

// QueueSwitch 入队一条换线指令,下次该设备心跳会拿到并清掉。
func (h *Heartbeats) QueueSwitch(deviceID string, sa *SwitchAssignment) {
	h.mu.Lock()
	h.switches[deviceID] = sa
	h.mu.Unlock()
}

func (h *Heartbeats) TakeSwitch(deviceID string) *SwitchAssignment {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.switches[deviceID]
	if ok {
		delete(h.switches, deviceID)
	}
	return v
}

// Snapshot 拷一份当前所有心跳;给管理面板用。
func (h *Heartbeats) Snapshot() map[string]HBState {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]HBState, len(h.states))
	for k, v := range h.states {
		out[k] = *v
	}
	return out
}

// Sweep 清掉超时(>timeout)未上报的设备。
func (h *Heartbeats) Sweep(timeout time.Duration) {
	cutoff := time.Now().Add(-timeout).UnixMilli()
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.states {
		if v.LastSeen < cutoff {
			delete(h.states, k)
		}
	}
}

// SerializeFiles 把 files 序列化为 JSON,避免 json marshalling 拿不到字段(因 hbFile 在 client 包)。
func SerializeFiles(files []hbFile) json.RawMessage {
	b, _ := json.Marshal(files)
	return b
}
