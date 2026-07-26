// Package control 实现面板与子节点之间的 WebSocket 管控协议。
//
// 连接方向：面板（client）主动拨号到子节点（server），用子节点自持的连接
// 密钥鉴权。建立后是一条长连接，承载：
//   - 鉴权握手（auth / auth_ok / auth_fail）
//   - 节点周期推送的遥测（telemetry）
//   - 面板下发指令、节点回执（cmd / resp，按 id 关联）
//   - 节点向面板推送的流式输出（subscribe / stream，如日志/控制台）
//   - 应用层保活（ping / pong）
//
// 笨重的后台 CRUD 不走这里——那部分由面板用节点密钥反代到节点的 HTTP
// 接口（见架构文档）。本通道只管实时遥测、生命周期指令与流式输出。
package control

import "encoding/json"

// 帧类型。
const (
	TypeAuth      = "auth"      // 面板→节点：{key}
	TypeAuthOK    = "auth_ok"   // 节点→面板：握手成功，附 NodeInfo
	TypeAuthFail  = "auth_fail" // 节点→面板：握手失败，附 error 后关闭
	TypeTelemetry = "telemetry" // 节点→面板：周期遥测
	TypeCmd       = "cmd"       // 面板→节点：{id, action, payload}
	TypeResp      = "resp"      // 节点→面板：{id, ok, data|error}
	TypeStream    = "stream"    // 节点→面板：{channel, line}
	TypeSubscribe = "subscribe" // 面板→节点：{channel}
	TypePing      = "ping"      // 面板→节点
	TypePong      = "pong"      // 节点→面板
)

// 内建指令 action 常量。业务节点可注册更多（如 admin.* 由反代承担，不在此列）。
const (
	ActionInfo       = "info"        // 返回节点基础信息
	ActionRestart    = "restart"     // 请求节点重启
	ActionRepack     = "repack"      // 触发离线整包打包
	ActionResync     = "resync"      // 触发资源同步
	ActionRotateKey  = "rotate_key"  // 轮换连接密钥
	ActionPurgeCache = "purge_cache" // 清理缓存
)

// Envelope 是所有帧的 JSON 线格式。按 Type 取用不同字段，未用字段省略。
type Envelope struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`      // cmd/resp 关联用请求 id
	Action  string          `json:"action,omitempty"`  // cmd
	Channel string          `json:"channel,omitempty"` // stream/subscribe
	OK      *bool           `json:"ok,omitempty"`      // resp：成功与否
	Error   string          `json:"error,omitempty"`   // auth_fail / resp 失败原因
	Line    string          `json:"line,omitempty"`    // stream：一行输出
	Payload json.RawMessage `json:"payload,omitempty"` // auth 的 key、cmd 的入参、resp 的 data、telemetry 等
}

// NodeInfo 在 auth_ok 时由节点上报，描述节点身份。
type NodeInfo struct {
	ID      string `json:"id"`
	Role    string `json:"role"` // business | edge
	Version string `json:"version,omitempty"`
}

// Telemetry 节点周期推送的运行指标。
type Telemetry struct {
	CPUPct      float64 `json:"cpu_pct"`
	MemPct      float64 `json:"mem_pct"`
	UptimeSec   int64   `json:"uptime_sec"`
	Goroutines  int     `json:"goroutines"`
	BytesServed int64   `json:"bytes_served,omitempty"`
	ActiveDL    int     `json:"active_dl,omitempty"`
	NodeRole    string  `json:"node_role,omitempty"`
}

// authPayload 是 TypeAuth 帧的 payload。
type authPayload struct {
	Key string `json:"key"`
}

// boolPtr 便于填 Envelope.OK。
func boolPtr(b bool) *bool { return &b }
