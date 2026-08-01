// Package client 实现 /client/* 协议接口。
//
// **线格式以架构协议文档(magirecocn-architecture-protocol-document)为唯一真理。**
// 本包早期版本以 Android 客户端的 Java 源码为锚点,该锚点已失效——Android 客户端
// 已弃维,不再有"照着实现"的对象。
//
// 端点集合:
//
//	/client/init            请求 {version, device_id, signature, channel, protocol_versions}
//	                        响应 {protocol_version, protocol_versions, access_token,
//	                              server_time_at, banned, ban_reason,
//	                              server:{status, message, end_time},
//	                              features:{account_enabled, disabled_message},
//	                              asset_auth:{type, ...}, services?, directory?}
//	authTriple              {device_id, access_token, signature}
//	/client/heartbeat       请求 authTriple
//	                        响应 {action: ok|ban|maintenance, ...}
//	/client/scene-manifest  请求 authTriple + {scene_id}
//	                        响应 {scene_id, assets:[{path}]}
//
// Android 专有的整包资源准备(method-select / online-download / offline-package)
// 与 APK 热更新(hot-update)已移除:Web 端按需流式取用单个资产,无批量下载端点。
package client

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/api-server/internal/api/respond"
	"magirecocn-revival/api-server/internal/auth"
	"magirecocn-revival/api-server/internal/autoban"
	"magirecocn-revival/api-server/internal/clienttoken"
	"magirecocn-revival/api-server/internal/middleware"
	"magirecocn-revival/api-server/internal/resourceauth"
	"magirecocn-revival/api-server/internal/store"
	"magirecocn-revival/api-server/internal/totentanz"
)

// Handler 把 store 与配置注入到 HTTP handler。
type Handler struct {
	St                  *store.Store
	ResourceTokenSecret []byte // resource_token 的 HMAC 签名根密钥
	// Discovery 上游 Totentanz 端点发现的后台客户端；nil 或未启用时
	// services 里不会出现 resource_base / game_max_threads。
	Discovery *totentanz.Client
	// SignatureAllowed:APK 签名证书 SHA-256 白名单(64 字符小写 hex)。
	// 必须是从 keystore 用 keytool -exportcert | sha256 得到的 DER 摘要,
	// 与客户端 IntegrityGuard.EXPECTED_SIGNATURE_SHA256 / CI 注入的值一致。
	// 见 docs/cnv-anti-tamper-server-side.md。
	SignatureAllowed []string
	// ChannelAllowed:渠道白名单(BuildChannel),空 = 放行所有。
	ChannelAllowed []string
	// RequireSignature:true 时强制请求必须带非空 signature(防止配置漏白名单
	// 即默默放行所有改包客户端)。
	RequireSignature bool
	TokenWindowSec   int           // 资源 token 时间窗(秒)
	ClientSessionTTL time.Duration // access_token 有效期
	Heartbeats       *Heartbeats
	AutoBan          *autoban.Service // 自动封禁判定器(可空 = 不启用)
	// DirectoryJSON 是离线私钥签好的节点目录原始 JSON,原样嵌入 /client/init 响应
	// 的 "directory" 字段。nil = 不下发(客户端沿用上次缓存或内置列表)。
	// 节点从 CNV_DIRECTORY_FILE 加载并注入。
	DirectoryJSON json.RawMessage

	// TokenIssuer 签发自包含的 access_token(见 internal/clienttoken)。
	//
	// 本服务端是**账号与身份的源头**:它签出的令牌会被资源分发服务端凭公钥直接
	// 采信,那边不查库、也不需要跟这里共用一个数据库。所以这把签名私钥的价值
	// 等同于"能凭空造出任意用户的会话",绝不可外泄(§5)。
	TokenIssuer *clienttoken.Issuer
	// TokenVerifier 校验自包含令牌。始终信任本节点自己的公钥;
	// CNV_CLIENT_TOKEN_TRUSTED_KEYS 可再叠加外部签发方。
	TokenVerifier *clienttoken.Verifier

	// BootstrapEndpoint 是 /magica/api/snaa 下发给 Android 底包的业务服务器地址。
	// 空串 = 本节点不接管 Android 底包,该端点返回 503。
	// 由 CNV_BOOTSTRAP_ENDPOINT 注入,源码不得硬编码(铁律二)。
	BootstrapEndpoint string
	// BootstrapMaxThreads 下发给底包的并发下载线程数建议值。
	BootstrapMaxThreads int
	// BootstrapVersion 当前底包版本号(r128 → 128),客户端据此自行判断是否提示更新。
	BootstrapVersion int

	// SceneAssets 返回进入某场景所需的资产相对路径列表。
	// 返回 nil 表示未知场景（客户端得到 404）。nil 函数 = 场景清单功能未启用。
	SceneAssets func(ctx context.Context, sceneID string) ([]string, error)

	// DevMode 开发模式。协议里的**开发期临时值**只在它为 true 时才允许下发,
	// 这是协议文档 06-dev-mode「生产守卫」在服务端侧的落点。
	// 当前受它管辖的只有场景清单的最小形状(见 sceneManifest)。
	DevMode bool
}

// Routes 注册到 chi router。除 /init 外的所有端点都强制校验 authTriple。
//
// 端点集合按协议文档重写：Android 专有的整包资源准备（method-select /
// online-download / offline-package）与 APK 热更新（hot-update）已移除，
// Web 端按需流式取用单个资产，无批量下载端点。
func (h *Handler) Routes(r chi.Router) {
	r.Post("/init", h.init)
	r.Group(func(g chi.Router) {
		g.Use(h.requireClientSession)
		g.Post("/heartbeat", h.heartbeat)
		g.Post("/scene-manifest", h.sceneManifest)
	})
}

// ── /client/init ────────────────────────────────────────────────────────

// initReq 握手请求体。
//
// version / signature / channel 是客户端形态相关的完整性凭据:Android 端分别是
// APK 版本号、签名证书 SHA-256 与构建渠道。Web 端没有等价的不可绕过凭据
// (源码在玩家浏览器里),这三项对它是可选的,校验强度由部署配置决定。
type initReq struct {
	Version   string `json:"version"`
	DeviceID  string `json:"device_id"`
	Signature string `json:"signature"`
	Channel   string `json:"channel"`
	// ProtocolVersions 是客户端支持的协议版本集合。缺省（老客户端）视为 [1]。
	// 服务端在交集中选一个回 protocol_version；无交集则握手失败，客户端不得降级。
	ProtocolVersions []int `json:"protocol_versions"`
}

func (h *Handler) init(w http.ResponseWriter, r *http.Request) {
	var req initReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	// 头部兜底(老客户端可能用 X-* 头传递)
	if req.DeviceID == "" {
		req.DeviceID = r.Header.Get("X-Device-Id")
	}
	if req.Version == "" {
		req.Version = r.Header.Get("X-Client-Version")
	}
	if req.Signature == "" {
		req.Signature = r.Header.Get("X-Signature")
	}
	if req.DeviceID == "" {
		respond.Fail(w, http.StatusBadRequest, "missing_device_id", "缺少 device_id")
		return
	}

	ctx := r.Context()
	clientIP := middleware.ClientIP(r)

	// ── 协议版本协商:取客户端支持集合与服务端实现的交集。
	//    无交集时握手失败,客户端应提示更新;**不得降级尝试**。
	negotiated := negotiateProtocol(req.ProtocolVersions)
	if negotiated == 0 {
		respond.Fail(w, http.StatusBadRequest, "protocol_version_unsupported",
			"客户端与服务端没有共同支持的协议版本,请更新客户端")
		return
	}

	// ── 签名校验(防改包根因防御,见 cnv-anti-tamper-server-side.md §3)──
	// 任何重打包都要用攻击者自己的 key 重签,签名会变。这是整条防线里唯一
	// 攻击者无法在客户端绕过的环节 —— 私钥不在客户端。
	if reason := h.checkSignature(req.Signature); reason != "" {
		h.recordIntegrityViolation(ctx, "signature", reason, req, clientIP)
		respond.Fail(w, http.StatusForbidden, "signature_rejected", "未授权的客户端签名")
		return
	}

	// 渠道白名单(可选)。空 channel 不强制(老客户端可能不填);非空时必须在白名单内。
	if len(h.ChannelAllowed) > 0 && req.Channel != "" && !contains(h.ChannelAllowed, req.Channel) {
		h.recordIntegrityViolation(ctx, "channel", "not_whitelisted", req, clientIP)
		respond.Fail(w, http.StatusForbidden, "channel_rejected", "渠道未授权")
		return
	}

	_ = h.St.DeviceTouch(ctx, req.DeviceID, req.Signature, req.Version)

	// ── 封禁:HTTP 200 + {banned:true, ban_reason}。
	//    封禁是**正常的业务结果**而非传输/请求错误,故不走错误模型的 4xx——
	//    客户端需要读到 body 里的理由与到期时间才能给玩家一个可理解的提示。
	if ban, _ := h.St.BanActive(ctx, req.DeviceID); ban != nil {
		out := map[string]any{"success": true, "banned": true}
		putIfNonEmpty(out, "ban_reason", ban.Reason)
		putIfNonZero(out, "expire_time", banExpireSeconds(ban))
		respond.OKRaw(w, out)
		return
	}

	srv := getServerConfig(ctx, h.St)
	dl := getFeatures(ctx, h.St)

	// APK 版本闸门(allowed_versions + force_update + update_url_*)已移除:
	// 它下发的是 APK 安装包地址,浏览器自行更新,Web 端无此概念。
	// 版本相关的唯一机制是上面的**协议版本协商**。

	// 签发会话:自包含令牌 "cnv1.<载荷>.<签名>"。校验只需公钥,不必与签发方共库——
	// 这正是资源分发服务端能在不连本服务端、不共享数据库的前提下认得这个身份的原因。
	//
	// 照旧写一行 client_sessions:管理后台要按设备列会话,撤销也要有落点。
	if h.TokenIssuer == nil {
		// 失败关闭。签发方在 cmd/node 启动时必定构造成功(种子没配会自动生成),
		// 走到这里说明装配漏了——宁可 500 也不能签出无签名的凭证。
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	ttl := h.ClientSessionTTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	issuedAt := time.Now()
	now := issuedAt.UnixMilli()

	// sessionKey 是 client_sessions 的主键,存的是令牌里的 jti 而**不是令牌本身**:
	// 自包含令牌约 400 字节,而该列在 MySQL 迁移里是 VARCHAR(128) 塞不下;
	// jti 是 64 个十六进制字符,天然合身,且撤销本来就该按 jti 匹配。
	sessionKey, err := auth.NewToken()
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	accessToken, err := h.TokenIssuer.Issue(clienttoken.Claims{
		Sub: req.DeviceID,
		Sig: req.Signature,
		CV:  req.Version,
		Ch:  req.Channel,
	}, issuedAt, ttl, sessionKey)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	if err := h.St.ClientSessionInsert(ctx, store.ClientSession{
		AccessToken:   sessionKey,
		DeviceID:      req.DeviceID,
		Signature:     req.Signature,
		ClientVersion: req.Version,
		Channel:       req.Channel,
		CreatedAt:     now,
		ExpiresAt:     now + ttl.Milliseconds(),
		LastSeenAt:    now,
	}); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	// ── 响应结构。
	//
	// 约定:**可选字符串字段未设置时省略 key**,不发送 JSON null。
	// bool/number 字段不受此约束(false/0 是合法业务值)。
	srvObj := map[string]any{"status": srv.Status}
	putIfNonEmpty(srvObj, "message", srv.Message)
	putIfNonZero(srvObj, "end_time", srv.EndTime) // Unix 秒

	featuresObj := map[string]any{"account_enabled": dl.AccountEnabled}
	putIfNonEmpty(featuresObj, "disabled_message", dl.DisabledMessage)

	body := map[string]any{
		"success": true,
		"banned":  false,
		// protocol_version 是本次会话协商出的版本;protocol_versions 是服务端
		// 支持的全集,客户端据此判断"升级到哪一版才能继续对话"。
		"protocol_version":  negotiated,
		"protocol_versions": SupportedProtocolVersions(),
		"access_token":      accessToken,
		"server_time_at":    time.Now().Unix(),
		"server":            srvObj,
		"features":          featuresObj,
	}
	// asset_auth:资产鉴权信封。必有 type 判别字段,其余字段形状由 type 决定;
	// 客户端遇到不认识的 type 必须明确失败,禁止猜测或静默降级为无鉴权。
	// 当前取值 bearer,承载既有的 resource_token(HMAC 签名、绑设备、可轮换)。
	//
	// **省略本字段的语义是"客户端拿不到资产",不是"不需要鉴权"**——后者必须显式
	// 下发 type:"none"(开发期临时值)。故此处签不出令牌时省略,即是 fail-closed。
	if tok, exp := h.signResourceToken(req.DeviceID); tok != "" {
		body["asset_auth"] = map[string]any{
			"type":       "bearer",
			"token":      tok,
			"expires_at": exp,
		}
	}
	// services 对象:全部三个字段都未配置时直接省略整个 services 键。
	if svc := getServicesConfig(ctx, h.St, h.Discovery).toResponseMap(); svc != nil {
		body["services"] = svc
	}
	// directory:已签名的节点目录,客户端用钉扎的 Ed25519 根公钥验签后更新本地缓存。
	// 未配置时省略,客户端沿用上次缓存或内置节点列表。
	if len(h.DirectoryJSON) > 0 {
		body["directory"] = h.DirectoryJSON
	}
	respond.OKRaw(w, body)
}

// ProtocolVersion 是本服务端优先选用的协议版本。
const ProtocolVersion = 1

// supportedProtocolVersions 是服务端能讲的全部版本,**按优先级降序**。
// 将来同时支持多版本时在此追加,协商逻辑与响应字段都无需再改。
var supportedProtocolVersions = []int{ProtocolVersion}

// SupportedProtocolVersions 返回副本,避免调用方改到内部切片。
func SupportedProtocolVersions() []int {
	out := make([]int, len(supportedProtocolVersions))
	copy(out, supportedProtocolVersions)
	return out
}

// negotiateProtocol 在客户端支持集合与服务端支持集合之间取交集,
// 按**服务端**的优先级顺序选择——版本策略属服务端职权,不由客户端的排序决定。
// 客户端未上报时视为只支持 ProtocolVersion(向后兼容)。
// 返回 0 表示无交集——调用方必须让握手失败,不得降级。
func negotiateProtocol(clientSupported []int) int {
	if len(clientSupported) == 0 {
		return ProtocolVersion
	}
	for _, mine := range supportedProtocolVersions {
		for _, theirs := range clientSupported {
			if mine == theirs {
				return mine
			}
		}
	}
	return 0
}

// putIfNonEmpty 字符串非空时才写入 map(否则省略 key,而不是下发空串)。
func putIfNonEmpty(m map[string]any, key, val string) {
	if val != "" {
		m[key] = val
	}
}

// putIfNonZero 整数非 0 时才写入 map(end_time=0 表示"无估计时间")。
func putIfNonZero(m map[string]any, key string, val int64) {
	if val != 0 {
		m[key] = val
	}
}

// ── authTriple 中间件 ──────────────────────────────────────────────────

// authTripleBody 除 /client/init 外所有 /client/* 端点共有的鉴权三元组。
type authTripleBody struct {
	DeviceID    string `json:"device_id"`
	AccessToken string `json:"access_token"`
	Signature   string `json:"signature"`
}

// ctxKey 用于把已校验的 session 注入 context。
type ctxKey int

const ctxClientSession ctxKey = iota

// requireClientSession 解析 authTriple,校验 access_token 仍然有效且与
// device_id 绑定。校验通过的 session 通过 context 传下去,handler 可以
// 直接读 sessionFromCtx() 拿到 device_id 等元数据。
//
// 这里要在不破坏后续 handler 读取 body 的前提下做校验:body 只能读一次。
// 我们用 ReadJSONAllowUnknown 读取后再把 body 重新塞回 r.Body。
func (h *Handler) requireClientSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := readAndRewind(r)
		if err != nil {
			respond.Fail(w, http.StatusBadRequest, "bad_request", "请求体无法解析")
			return
		}
		var tri authTripleBody
		_ = json.Unmarshal(body, &tri)
		// X-* 头兜底,便于老客户端调试
		if tri.DeviceID == "" {
			tri.DeviceID = r.Header.Get("X-Device-Id")
		}
		if tri.AccessToken == "" {
			tri.AccessToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if tri.Signature == "" {
			tri.Signature = r.Header.Get("X-Signature")
		}
		sess, sessionKey, err := h.resolveSession(r.Context(), tri.AccessToken, tri.DeviceID)
		if err != nil {
			respond.Fail(w, http.StatusUnauthorized, "session_invalid",
				"access_token 失效或与 device_id 不匹配,请重新握手")
			return
		}
		// 防改包深度防御:任何后续 /client/* 请求的 signature 都必须与 /client/init
		// 时校验通过、写入 client_sessions 的那个 signature 保持一致。Android APK 的
		// 签名证书在单次安装期间不可能变化;若变了说明会话被劫持或客户端被换包,直接拒绝。
		// 见 cnv-anti-tamper-server-side.md §2 末尾"同样的 signature 字段也随
		// /client/offline-package、/client/method-select 上送,可在这些端点复核"。
		if sess.Signature != "" && tri.Signature != sess.Signature {
			req := initReq{DeviceID: tri.DeviceID, Signature: tri.Signature,
				Version: sess.ClientVersion, Channel: sess.Channel}
			h.recordIntegrityViolation(r.Context(), "signature",
				"changed_mid_session", req, middleware.ClientIP(r))
			_ = h.St.ClientSessionDelete(r.Context(), sessionKey)
			respond.Fail(w, http.StatusForbidden, "signature_rejected",
				"客户端签名异常,会话已作废,请重新握手")
			return
		}
		// 顺便校验封禁(authTriple 必走的所有端点都应当受 ban 影响)
		if ban, _ := h.St.BanActive(r.Context(), sess.DeviceID); ban != nil {
			out := map[string]any{"success": true, "action": "ban"}
			putIfNonEmpty(out, "reason", ban.Reason)
			putIfNonZero(out, "expire_time", banExpireSeconds(ban))
			respond.OKRaw(w, out)
			return
		}
		h.St.ClientSessionTouch(r.Context(), sessionKey)
		ctx := context.WithValue(r.Context(), ctxClientSession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveSession 把 access_token 解析成会话。
// 返回的第二个值是 client_sessions 的主键,调用方拿它做 Touch / Delete。
//
// 只接受自包含令牌("cnv1." 前缀 + Ed25519 签名)。这里**没有**任何降级分支:
// 令牌解析或验签失败就是失败,不会退回别的校验方式。旧的 64 位 hex 令牌 + 查库
// 校验已随 Android 专有 API 一并废弃——公开发行的是上一代产物,这一代因依赖上游
// 未上线,装机量为零,兼容层保护不到任何人,却给伪造令牌留了一条降级入口。
func (h *Handler) resolveSession(ctx context.Context, token, deviceID string) (*store.ClientSession, string, error) {
	if !clienttoken.Looks(token) {
		return nil, "", errNoSession
	}
	if h.TokenVerifier == nil {
		return nil, "", errNoSession
	}
	c, err := h.TokenVerifier.VerifyForDevice(token, deviceID, time.Now())
	if err != nil {
		return nil, "", err
	}
	// 撤销:本节点自己签发的令牌,client_sessions 里必须还有对应的行——
	// 管理后台"踢下线"删的就是那一行,删掉即视为已撤销。
	//
	// 外部签发方(若配置了)签的令牌本地不会有行,那时"查不到"必须视为有效,
	// 否则一开启联邦就会把所有远端令牌全拒掉。这就是撤销判定必须知道签发方的原因。
	if h.TokenIssuer != nil && c.Iss == h.TokenIssuer.ID() {
		sess, lookupErr := h.St.ClientSessionLookup(ctx, c.JTI, deviceID)
		if lookupErr != nil {
			return nil, "", lookupErr
		}
		return sess, c.JTI, nil
	}
	// 远端签发:会话元数据全部来自已验签的载荷,不落库。
	return &store.ClientSession{
		AccessToken:   c.JTI,
		DeviceID:      c.Sub,
		Signature:     c.Sig,
		ClientVersion: c.CV,
		Channel:       c.Ch,
		CreatedAt:     c.Iat,
		ExpiresAt:     c.Exp,
		LastSeenAt:    c.Iat,
	}, c.JTI, nil
}

var errNoSession = errors.New("client: 缺少或非法的 access_token")

func sessionFromCtx(ctx context.Context) *store.ClientSession {
	v, _ := ctx.Value(ctxClientSession).(*store.ClientSession)
	return v
}

// readAndRewind 把 r.Body 读到内存,允许后续 handler 再次 Decode。
func readAndRewind(r *http.Request) ([]byte, error) {
	const maxBodyBytes = 1 << 20 // 1 MiB(/client/heartbeat 文件列表最大场景)
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, err
		}
	}
	r.Body = readCloserBytes(buf)
	return buf, nil
}

type bodyBuf struct {
	b   []byte
	pos int
}

func (b *bodyBuf) Read(p []byte) (int, error) {
	if b.pos >= len(b.b) {
		return 0, errEOF
	}
	n := copy(p, b.b[b.pos:])
	b.pos += n
	return n, nil
}
func (b *bodyBuf) Close() error { return nil }

var errEOF = &eofErr{}

type eofErr struct{}

func (*eofErr) Error() string { return "EOF" }

func readCloserBytes(b []byte) *bodyBuf { return &bodyBuf{b: b} }

// ── /client/heartbeat ───────────────────────────────────────────────────

// heartbeatReq 精简版心跳:只有 authTriple,没有任何上报载荷。
//
// 原先随心跳上送的逐文件下载进度(name/status/percent/speed_bps)已移除——
// 它服务于 Android 端"先下完整包再进游戏"的模型,Web 端不存在那个阶段。
type heartbeatReq struct {
	authTripleBody
}

// heartbeat 是握手之外唯一的服务端推送时机:下发封禁与维护状态。
//
// 它不承载任何客户端上报的业务数据。这既是精简,也顺带消掉一类问题:
// 凡"客户端报什么就存什么"的字段都需要服务端侧的合理性校验,而这些字段
// 本来就没有 Web 端的用途。
func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	_ = req // authTriple 已由 requireClientSession 校验,此处只为拒绝畸形 body
	sess := sessionFromCtx(r.Context())
	ctx := r.Context()

	// 精简版：Web 端按需流式取用资产，既无"下载阶段"也无镜像切换，
	// 故不再上报逐文件进度、不做速度归因、不下发换线指令。
	// 保留的价值是**封禁与维护状态的实时下发**——这是握手之外唯一的推送时机。
	h.Heartbeats.Update(sess.DeviceID, &HBState{
		Phase:    "game",
		LastSeen: time.Now().UnixMilli(),
	})

	// 自动封禁信号:单设备心跳频率远超正常节律 → 伪造/刷包。
	h.AutoBan.OnHeartbeat(ctx, sess.DeviceID)

	// ── 封禁:立即下发,客户端应中断会话。
	if ban, _ := h.St.BanActive(ctx, sess.DeviceID); ban != nil {
		out := map[string]any{"success": true, "action": "ban"}
		putIfNonEmpty(out, "reason", ban.Reason)
		putIfNonZero(out, "expire_time", banExpireSeconds(ban))
		respond.OKRaw(w, out)
		return
	}

	// ── 维护
	srv := getServerConfig(ctx, h.St)
	if srv.Status == "maintenance" || srv.Status == "warn" {
		out := map[string]any{"success": true, "action": "maintenance"}
		putIfNonEmpty(out, "message", srv.Message)
		putIfNonZero(out, "end_time", srv.EndTime)
		respond.OKRaw(w, out)
		return
	}

	respond.OKRaw(w, map[string]any{"success": true, "action": "ok"})
}

// ── /client/scene-manifest ──────────────────────────────────────────────

type sceneManifestReq struct {
	authTripleBody
	SceneID string `json:"scene_id"`
}

// sceneManifest 返回进入某场景所需的资产清单。
//
// 场景包是**清单与调度单位**，文件是**传输与缓存单位**：客户端拿到清单后与本地
// 缓存做差集，只拉缺失的文件。边缘节点因此保持为纯对象存储，不理解游戏结构。
//
// 当前为协议文档 06-dev-mode 规定的**开发期最小形状**，只含 path。
// 清单的正式形状（hash / size / 增量 / 场景 ID 命名空间）仍是待决项 R2；
// 定稿后按扩展性规则**新增字段**即可，客户端忽略未知字段，不破坏既有实现。
//
// ⚠️ 因为最小形状是**开发期临时值**，本端点受「生产守卫」管辖：DevMode=false
// 时一律拒绝，哪怕 SceneAssets 已经接好。见下方注释。
func (h *Handler) sceneManifest(w http.ResponseWriter, r *http.Request) {
	var req sceneManifestReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.SceneID == "" {
		respond.Fail(w, http.StatusBadRequest, "missing_scene_id", "缺少 scene_id")
		return
	}
	// ── 生产守卫（协议文档 06-dev-mode）─────────────────────────────────
	// 清单的最小形状（只含 path）是 R2 定稿前的**开发期临时值**，生产环境
	// **不得下发**。这里在 SceneAssets 判空之前拦，是为了让"生产环境不该有这个
	// 端点"这件事与"清单还没接进来"区分开——两者的修法完全不同。
	//
	// 临时值的危险不在于它们存在，而在于**它们可能不被发现地留在生产里**：
	// 一个只含 path 的清单在生产里跑得好好的，直到某天需要靠 hash 做缓存失效
	// 才发现它从来没有过。守卫必须先于临时值生效。
	if !h.DevMode {
		respond.Fail(w, http.StatusServiceUnavailable, "manifest_unavailable",
			"场景清单当前仅在开发模式下可用（清单格式待定，见 R2）")
		return
	}
	if h.SceneAssets == nil {
		// 场景清单尚未接入构建管线（待决项 R2）。明确报错，不返回空清单——
		// 空清单会被客户端理解为"该场景无需任何资产"，从而静默进入残缺场景。
		respond.Fail(w, http.StatusServiceUnavailable, "manifest_unavailable",
			"场景清单服务尚未启用")
		return
	}
	assets, err := h.SceneAssets(r.Context(), req.SceneID)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	if assets == nil {
		respond.Fail(w, http.StatusNotFound, "scene_not_found", "未知的 scene_id")
		return
	}
	list := make([]map[string]any, 0, len(assets))
	for _, p := range assets {
		list = append(list, map[string]any{"path": p})
	}
	respond.OKRaw(w, map[string]any{
		"success":  true,
		"scene_id": req.SceneID,
		"assets":   list,
	})
}

// ── 内部辅助 ────────────────────────────────────────────────────────────

// signResourceToken 签发绑设备、按时间窗轮换的资产鉴权令牌。
//
// 实现在 internal/resourceauth——**那个包在 magirecocn-resource-server 里有一份
// 完全相同的拷贝**:本服务端签发,资源分发服务端的边缘节点校验。从前两边各写一份
// HMAC,单位一个用毫秒一个用秒都没人发现,因为当时**根本没有校验方**,资源目录
// 是裸挂的。现在有了,两边就必须字节级一致,靠跨仓库测试向量钉住。
//
// 密钥过短时返回空串,调用方据此**省略 asset_auth**。这是 fail-closed:
// 空密钥的 HMAC 照样能算出一个"看起来正常"的令牌,而那个令牌任何人都能自己算,
// 等于把资产完全敞开。宁可不下发让客户端明确拿不到资产,也不下发一个假的凭据。
// (正常部署下 cmd/node 会在启动时自动生成并持久化密钥,不会走到这一支。)
func (h *Handler) signResourceToken(deviceID string) (string, int64) {
	return resourceauth.Signer{
		Secret:    h.ResourceTokenSecret,
		WindowSec: h.TokenWindowSec,
	}.Sign(deviceID, time.Now())
}

// checkSignature 返回拒绝原因(非空)或空串(通过)。
//
//   - 空 signature + 白名单非空 → "empty"(必然不在白名单)
//   - 空 signature + RequireSignature=true → "empty"(强制非空)
//   - 非空 signature + 白名单非空 + 不在白名单 → "not_whitelisted"
//   - 空 signature + 白名单空 + 不强制 → 通过(dev permissive),但记 WARN
//   - 非空 signature + 白名单空 → 通过(dev permissive),但记 WARN
func (h *Handler) checkSignature(sig string) string {
	sig = strings.ToLower(strings.TrimSpace(sig))
	hasWhitelist := len(h.SignatureAllowed) > 0
	if sig == "" {
		if hasWhitelist || h.RequireSignature {
			return "empty"
		}
		slog.Warn("/client/init 收到空 signature,但未配置白名单 / RequireSignature → 放行(仅适用于开发环境)")
		return ""
	}
	if !hasWhitelist {
		slog.Warn("/client/init 接受任意 signature(未配置 CNV_SIGNATURE_WHITELIST)",
			"signature", sig)
		return ""
	}
	if !auth.SignatureAllowed(sig, h.SignatureAllowed) {
		return "not_whitelisted"
	}
	return ""
}

// recordIntegrityViolation 把改包/渠道异常事件写审计日志,供运维做风控。
// 字段:device_id、IP、被拒的 signature(前 12 位)、version、reason。
// 不记完整 signature 防误用真有效证书摘要;但前缀足够运营回查日志去重。
func (h *Handler) recordIntegrityViolation(ctx context.Context, field, reason string, req initReq, clientIP string) {
	slog.Warn("client integrity rejected",
		"field", field, "reason", reason,
		"device_id", req.DeviceID, "ip", clientIP,
		"version", req.Version, "channel", req.Channel,
		"signature_prefix", safePrefix(req.Signature, 12))
	sigPrefix := safePrefix(req.Signature, 12)
	target := req.DeviceID
	_ = h.St.AuditInsert(ctx, store.AuditEntry{
		ID:     newAuditID(),
		Ts:     time.Now().UnixMilli(),
		Actor:  "system",
		Type:   "client.integrity_rejected",
		Target: &target,
		Details: jsonMust(map[string]any{
			"field":            field,
			"reason":           reason,
			"ip":               clientIP,
			"version":          req.Version,
			"channel":          req.Channel,
			"signature_prefix": sigPrefix,
		}),
	})
	// 自动封禁信号:签名在会话中途变更是强劫持/换包信号,即时封;init 处的
	// 签名/渠道白名单不过则累计计数到阈值再封(可能是误配,给容错)。
	severe := field == "signature" && reason == "changed_mid_session"
	h.AutoBan.OnIntegrityViolation(ctx, req.DeviceID, severe)
}

// safePrefix 返回字符串前 n 字符;长度不足时返回完整原串。空串返回 "(empty)"。
func safePrefix(s string, n int) string {
	if s == "" {
		return "(empty)"
	}
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func contains(arr []string, v string) bool {
	for _, x := range arr {
		if x == v {
			return true
		}
	}
	return false
}

func ptr[T any](v T) *T { return &v }

// banExpireSeconds 把封禁过期时间(ms)转成客户端期望的 Unix 秒。
//
//	expire_time == 0  → 永久封禁
//	expire_time > 0   → 解封时间戳(秒)
func banExpireSeconds(b *store.Ban) int64 {
	if b == nil || b.ExpireTime == nil {
		return 0
	}
	return *b.ExpireTime / 1000
}

func bundlePayload(b *store.HotBundle) map[string]any {
	out := map[string]any{"version": b.Version, "size": b.Size}
	putIfNonEmpty(out, "sha256", b.SHA256)
	putIfNonEmpty(out, "download_url", b.DownloadURL)
	return out
}

func jsonMust(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func newAuditID() string {
	t, _ := auth.NewToken()
	return "log_" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "_" + t[:8]
}
