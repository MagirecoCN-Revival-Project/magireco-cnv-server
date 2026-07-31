package client

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/api-server/internal/api/respond"
)

// ── /magica/api/snaa ────────────────────────────────────────────────────
//
// Android 底包（io.kamihama.totentanz 系）的**引导端点**：客户端启动时先打这里
// 问"真正的业务服务器在哪",拿到 endpoint 后才去打其余 /magica/api/*。
//
// 调用链（据 1.2.0_r128 底包反编译确认）：
//
//	libuwasa.so  ──JNI──▶  io/kamihama/magianative/RestClient.GetEndpoint(I)
//	                       POST {snaa}  {"version": N}
//	                       ◀── {"message":"snaa","response":{...},"status":200}
//	libuwasa.so  hook UrlConfig::resource(Resource::Type) 用返回的 endpoint 供给引擎
//
// 客户端侧的失败语义（字符串取自 libuwasa.so，决定了本端点不能怎么答）：
//
//   - 响应体长度为 0        → "Unable to connect to the server. (Response length: 0)"
//   - 缺 endpoint 字段      → "No endpoint URL found in JSON response."
//   - endpoint 为空串       → "Empty endpoint URL."
//
// 因此**任何情况下都不能返回空体**：即便拒绝服务，也要返回结构完整、
// endpoint 非空判定明确的 JSON，否则玩家只会看到"连不上服务器"这种无信息的弹窗。

// MagicaRoutes 注册 /magica/api/* 下由服务端直接应答的端点。
//
// 只挂引导端点：其余 /magica/api/* 是游戏业务面，由资源/业务层各自承担，
// 不在本包内实现。挂在这里是因为引导端点与 /client/init 共用同一份节点配置。
func (h *Handler) MagicaRoutes(r chi.Router) {
	r.Post("/snaa", h.snaa)
}

// snaaReq 引导请求体。version 是底包自己的打包版本号（r128 → 128），
// 与游戏版本（3.1.9）无关，也与 /client/init 的 protocol_versions 无关。
type snaaReq struct {
	Version int `json:"version"`
}

// snaaResp 引导响应体。字段名与嵌套形状由底包的解析逻辑固定，**不可改**——
// 底包已分发到玩家设备，改了线格式等于把老客户端全部踢下线（铁律四）。
type snaaResp struct {
	Message  string    `json:"message"`
	Response snaaInner `json:"response"`
	Status   int       `json:"status"`
}

type snaaInner struct {
	Endpoint   string `json:"endpoint"`
	MaxThreads int    `json:"max_threads"`
	Version    int    `json:"version"`
}

// snaa 处理引导请求。
//
// 安全说明：本端点**无鉴权**——它必须在登录之前就能应答，否则客户端根本找不到
// 登录接口在哪。正因如此，它只下发"去哪台服务器"这一个公开事实，
// 不接受、不回显任何账号相关信息。
func (h *Handler) snaa(w http.ResponseWriter, r *http.Request) {
	var req snaaReq
	// 底包未来可能追加字段;用 AllowUnknown 避免新增字段把老服务端打成 400。
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}

	// 未配置引导端点 = 本节点没打算接管 Android 底包。
	// 返回 503 而不是 200+空 endpoint:后者会让客户端弹"Empty endpoint URL",
	// 把一次配置缺失误报成客户端故障。
	if strings.TrimSpace(h.BootstrapEndpoint) == "" {
		respond.Fail(w, http.StatusServiceUnavailable, "bootstrap_not_configured",
			"本节点未配置 Android 引导端点")
		return
	}

	// 版本号仅作合理性上界校验后回显。这里刻意**不做版本闸门**:
	// 真实底包对任意 version 都返回同一 endpoint,由客户端自己比对
	// response.version 决定是否提示更新。服务端擅自拒答会让老客户端
	// 连"该更新了"这个提示都收不到,直接卡在连不上服务器。
	if req.Version < 0 || req.Version > 1_000_000 {
		req.Version = 0
	}

	respond.OKRaw(w, snaaResp{
		Message: "snaa",
		Response: snaaInner{
			Endpoint:   h.BootstrapEndpoint,
			MaxThreads: h.BootstrapMaxThreads,
			Version:    h.BootstrapVersion,
		},
		Status: http.StatusOK,
	})
}
