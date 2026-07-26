package client

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"magirecocn-revival/cnv-server/internal/store"
)

// ── config 包装(读 KV 表)────────────────────────────────────────────────

// serverCfg 与客户端 ClientInit.java 期望的 server 字段顺序一致:
//
//	{status, message, end_time} 平级。end_time 是 **Unix 秒**(BootstrapActivity
//	会调 formatUnixSeconds 直接当秒解析),不是毫秒。
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

// featuresCfg 客户端 ClientInit.java 读的是顶层 features 对象,字段名
// online_download / offline_package(不是 online_enabled / offline_enabled)。
type featuresCfg struct {
	OnlineDownload bool `json:"online_download"`
	OfflinePackage bool `json:"offline_package"`
	// AccountEnabled 账号系统总开关。false = 跳过登录/存档同步/悬浮窗,直接进游戏。
	// 缺省(或字段不存在)等同于 true,向后兼容。
	AccountEnabled  bool   `json:"account_enabled"`
	DisabledMessage string `json:"disabled_message"`
	// 旧字段名兼容(管理面板 PUT 进来时仍可能用老 key)
	OnlineEnabled  *bool `json:"online_enabled,omitempty"`
	OfflineEnabled *bool `json:"offline_enabled,omitempty"`
}

func getFeatures(ctx context.Context, st *store.Store) featuresCfg {
	c := featuresCfg{OnlineDownload: true, OfflinePackage: true, AccountEnabled: true}
	_, _ = st.ConfigGet(ctx, "features", &c)
	if c.OnlineEnabled != nil {
		c.OnlineDownload = *c.OnlineEnabled
	}
	if c.OfflineEnabled != nil {
		c.OfflinePackage = *c.OfflineEnabled
	}
	return c
}

// versionCfg 客户端读的字段是 `fake_name`(不是 fake_app_name);allowed_versions/
// update_url_* 在响应里放在 client 子对象。
type versionCfg struct {
	AllowedVersions             []string `json:"allowed_versions"`
	LatestVersion               string   `json:"latest_version"` // 软提示最新版本号,空串 = 不下发
	FakeVersion                 string   `json:"fake_version"`
	FakeName                    string   `json:"fake_name"`
	UpdateURLNormal             string   `json:"update_url_normal"`
	UpdateURLInternalTest       string   `json:"update_url_internal_test"`
	UpdateAPKSHA256             string   `json:"update_apk_sha256"`
	UpdateAPKSHA256InternalTest string   `json:"update_apk_sha256_internal_test"` // 内测渠道独立 sha256
	// 旧字段名兼容
	FakeAppName string `json:"fake_app_name,omitempty"`
}

func (v versionCfg) effectiveFakeName() string {
	if v.FakeName != "" {
		return v.FakeName
	}
	return v.FakeAppName
}

// servicesCfg /client/init 响应里的可选 services 对象。
//
// 三字段全部可选,**未配置时省略 key**(客户端 Android org.json 对显式 null 会
// 错误地拿到字符串 "null")。详见 e9619a72-___API____.md。
//
//	cap_worker_url    string   — 人机验证服务地址(含协议,不带尾斜杠)
//	proxy_backends    []string — 游戏代理后端列表(按数组顺序尝试)
//	game_server_host  string   — 原版游戏服务器 scheme+host,用于代理精确匹配
type servicesCfg struct {
	CapWorkerURL   string   `json:"cap_worker_url,omitempty"`
	ProxyBackends  []string `json:"proxy_backends,omitempty"`
	GameServerHost string   `json:"game_server_host,omitempty"`
}

func getServicesConfig(ctx context.Context, st *store.Store) servicesCfg {
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
	if len(out) == 0 {
		return nil
	}
	return out
}

func getVersionConfig(ctx context.Context, st *store.Store) versionCfg {
	var c versionCfg
	_, _ = st.ConfigGet(ctx, "versions", &c)
	if c.AllowedVersions == nil {
		c.AllowedVersions = []string{}
	}
	return c
}

// offlinePackPolicy 离线包版本策略。详见 server-offline-pack-validation.md §3.1。
// 与 store.OfflinePackage(具体包的元数据)区分:OfflinePackage.PackageVersion 是
// 当前云端包的版本号,MinVersion 是服务端**要求**客户端必须安装的最低版本,
// 两者可以不同。空 MinVersion → 客户端跳过版本检查,静默继续。
type offlinePackPolicy struct {
	MinVersion string `json:"min_version"`
}

func getOfflinePackPolicy(ctx context.Context, st *store.Store) offlinePackPolicy {
	var p offlinePackPolicy
	_, _ = st.ConfigGet(ctx, "offline_pack", &p)
	p.MinVersion = strings.TrimSpace(p.MinVersion)
	return p
}

// ── 心跳内存存储 ────────────────────────────────────────────────────────

// HBState 一个设备的最新心跳快照。
//
// 字段语义对齐客户端 ResourceFlow.HeartbeatSender / SaveOverlayService.sendGameHeartbeat:
//   - 下载阶段(在线资源下载 / 热更新)：客户端自己跑下载 worker,每 5 秒上报
//     Files[]（每项 name/status/percent/speed_bps）。Progress/SpeedBps/CurrentFile
//     由 Files 聚合得到(见 heartbeat handler)。
//   - 游戏阶段：客户端在游戏内每 5 秒上报空 Files[]（仅为收取封禁/维护指令），
//     无下载进度与速度。
//
// 注意：离线整包由系统浏览器下载、客户端仅做文件导入,全程不发心跳,故不会出现在
// 心跳监控里——“离线包下载速度”在协议上并不存在。
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
