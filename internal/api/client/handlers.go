// Package client 实现 /client/* 协议接口。
//
// 字段名以 magireco-cnv-client 仓库的 Java 源码为唯一真理。具体核对点:
//
//	ClientInit.java:
//	  /client/init        请求 {version, device_id, signature, channel}
//	                      响应 {banned, ban_reason, access_token,
//	                            server:{status, message, end_time},
//	                            client:{allowed_versions, update_url_*},
//	                            spoof:{fake_version, fake_name},
//	                            features:{online_download, offline_package, disabled_message}}
//	  authTriple          {device_id, access_token, signature}
//	  /client/online-download 响应 {resource_token, groups:[{name, mirrors:[{url, files}]}] | mirrors:[string]}
//	  /client/offline-package 响应 {download_url, package_version, sha256}
//	  /client/hot-update      响应 {js:{version, sha256, download_url, size}, scenario:{...}}
//
//	ResourceFlow.java(心跳):
//	  /client/heartbeat   请求 authTriple + {files:[{name, status, percent, speed_bps}]}
//	                      响应 {action: ok|switch_mirrors|ban|maintenance,
//	                            switch_mirrors: assignments:[{mirror, files:[name]}],
//	                            ban:           reason, expire_time(秒),
//	                            maintenance:   message, end_time(秒)}
package client

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/cnv-server/internal/api/respond"
	"magirecocn-revival/cnv-server/internal/auth"
	"magirecocn-revival/cnv-server/internal/autoban"
	"magirecocn-revival/cnv-server/internal/middleware"
	"magirecocn-revival/cnv-server/internal/store"
)

// Handler 把 store 与配置注入到 HTTP handler。
type Handler struct {
	St                  *store.Store
	ResourceTokenSecret []byte // resource_token 的 HMAC 签名根密钥
	// SignatureAllowed:APK 签名证书 SHA-256 白名单(64 字符小写 hex)。
	// 必须是从 keystore 用 keytool -exportcert | sha256 得到的 DER 摘要,
	// 与客户端 IntegrityGuard.EXPECTED_SIGNATURE_SHA256 / CI 注入的值一致。
	// 见 docs/cnv-anti-tamper-server-side.md。
	SignatureAllowed []string
	// ChannelAllowed:渠道白名单(BuildChannel),空 = 放行所有。
	ChannelAllowed []string
	// RequireSignature:true 时强制请求必须带非空 signature(防止配置漏白名单
	// 即默默放行所有改包客户端)。
	RequireSignature  bool
	TokenWindowSec    int           // 资源 token 时间窗(秒)
	ClientSessionTTL  time.Duration // access_token 有效期
	PrimaryResBaseURL func(*http.Request) string
	Heartbeats        *Heartbeats
	AutoBan           *autoban.Service // 自动封禁判定器(可空 = 不启用)
	// DirectoryJSON 是离线私钥签好的节点目录原始 JSON,原样嵌入 /client/init 响应
	// 的 "directory" 字段。nil = 不下发(客户端沿用上次缓存或内置列表)。
	// 节点从 CNV_DIRECTORY_FILE 加载并注入。
	DirectoryJSON json.RawMessage

	// MirrorEnabled 检查镜像是否允许派发（日限额/速度未超限）。nil = 全部放行。
	MirrorEnabled func(mirrorURL string) bool
	// OnMirrorSpeed 心跳时上报各镜像的瞬时速度；mirrorURLs 来自该设备上次 online-download 分派列表。nil = 忽略。
	OnMirrorSpeed func(mirrorURLs []string, totalBps int64)
}

// Routes 注册到 chi router。除 /init 外的所有端点都强制校验 authTriple。
func (h *Handler) Routes(r chi.Router) {
	r.Post("/init", h.init)
	r.Group(func(g chi.Router) {
		g.Use(h.requireClientSession)
		g.Post("/method-select", h.methodSelect)
		g.Post("/online-download", h.onlineDownload)
		g.Post("/offline-package", h.offlinePackage)
		g.Post("/heartbeat", h.heartbeat)
		g.Post("/hot-update", h.hotUpdate)
	})
}

// ── /client/init ────────────────────────────────────────────────────────

// initReq 严格按客户端 ClientInit.java 的 body.put 顺序:
//
//	body.put("version",   clientVersion);
//	body.put("device_id", DeviceId.get(ctx));
//	body.put("signature", ClientSignature.get(ctx));
//	body.put("channel",   BuildChannel.get(ctx));
type initReq struct {
	Version   string `json:"version"`
	DeviceID  string `json:"device_id"`
	Signature string `json:"signature"`
	Channel   string `json:"channel"`
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

	// ── 封禁:返回 HTTP 200 + {banned:true, ban_reason},客户端 Net.postJson
	//    在 ≥400 时会抛 IOException,handshake 拿不到 body —— 所以 ban 必须 200。
	if ban, _ := h.St.BanActive(ctx, req.DeviceID); ban != nil {
		out := map[string]any{"success": true, "banned": true}
		putIfNonEmpty(out, "ban_reason", ban.Reason)
		putIfNonZero(out, "expire_time", banExpireSeconds(ban))
		respond.OKRaw(w, out)
		return
	}

	srv := getServerConfig(ctx, h.St)
	dl := getFeatures(ctx, h.St)
	ver := getVersionConfig(ctx, h.St)

	// 版本闸门(只在 allowed_versions 非空时启用)。
	//
	// 协议:HTTP 200 + {success:false, force_update:true, current_version,
	// update_url_normal?, update_url_internal_test?}。客户端 ClientInit.parse
	// 在解任何其它字段之前先看 force_update,见 version-not-allowed-followup.md
	// 方案 A(客户端 2026-05-30 已确认实现)。
	//
	// 与 banned 分支同构(HTTP 200 + 顶层 flag)—— 4xx 会被 Net.postJson 抛
	// IOException,客户端读不到 body 里的 update URL,所以必须 200。
	// 不签发 access_token、不写 client_sessions,客户端不会继续走握手流程。
	if len(ver.AllowedVersions) > 0 && req.Version != "" && !contains(ver.AllowedVersions, req.Version) {
		out := map[string]any{
			"success":         false,
			"force_update":    true,
			"current_version": req.Version,
		}
		putIfNonEmpty(out, "update_url_normal", ver.UpdateURLNormal)
		putIfNonEmpty(out, "update_url_internal_test", ver.UpdateURLInternalTest)
		respond.OKRaw(w, out)
		return
	}

	// 签发 client_sessions:access_token = 32 字节 hex
	accessToken, err := auth.NewToken()
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	ttl := h.ClientSessionTTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	now := time.Now().UnixMilli()
	if err := h.St.ClientSessionInsert(ctx, store.ClientSession{
		AccessToken:   accessToken,
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

	// ── 响应结构严格按 ClientInit.java handshake 的 optString/optJSONObject。
	//
	// 重要约定(2026-05-28 客户端 API 变动说明):**所有可选字符串字段未设置时
	// 必须省略 key**,绝不发送 JSON null —— Android org.json 的 optString 对
	// 显式 null 会返回字符串 "null"。bool/number 字段不受此约束(false/0 是
	// 合法业务值)。
	srvObj := map[string]any{"status": srv.Status}
	putIfNonEmpty(srvObj, "message", srv.Message)
	putIfNonZero(srvObj, "end_time", srv.EndTime) // Unix 秒

	// 按渠道选取 sha256:内测渠道若单独配置了哈希则优先使用,否则回退到通用值。
	// 两个渠道 APK 不同时可独立维护各自哈希,防止交叉下载后校验失败(文档 §六)。
	apkSHA256 := ver.UpdateAPKSHA256
	if req.Channel == "internal-test" && ver.UpdateAPKSHA256InternalTest != "" {
		apkSHA256 = ver.UpdateAPKSHA256InternalTest
	}

	clientObj := map[string]any{"allowed_versions": ver.AllowedVersions}
	putIfNonEmpty(clientObj, "latest_version", ver.LatestVersion)
	putIfNonEmpty(clientObj, "update_url_normal", ver.UpdateURLNormal)
	putIfNonEmpty(clientObj, "update_url_internal_test", ver.UpdateURLInternalTest)
	putIfNonEmpty(clientObj, "update_apk_sha256", apkSHA256)

	spoofObj := map[string]any{}
	putIfNonEmpty(spoofObj, "fake_version", ver.FakeVersion)
	putIfNonEmpty(spoofObj, "fake_name", ver.effectiveFakeName())

	featuresObj := map[string]any{
		"online_download": dl.OnlineDownload,
		"offline_package": dl.OfflinePackage,
		"account_enabled": dl.AccountEnabled,
	}
	putIfNonEmpty(featuresObj, "disabled_message", dl.DisabledMessage)

	body := map[string]any{
		"success":      true,
		"banned":       false,
		"access_token": accessToken,
		"server":       srvObj,
		"client":       clientObj,
		"spoof":        spoofObj,
		"features":     featuresObj,
	}
	// services 对象:全部三个字段都未配置时直接省略整个 services 键。
	if svc := getServicesConfig(ctx, h.St).toResponseMap(); svc != nil {
		body["services"] = svc
	}
	// offline_pack 对象:离线包版本策略(见 server-offline-pack-validation.md §3)。
	// min_version 未配置时整体省略,客户端跳过版本检查。
	if op := getOfflinePackPolicy(ctx, h.St); op.MinVersion != "" {
		body["offline_pack"] = map[string]any{
			"min_version": op.MinVersion,
		}
	}
	// directory:已签名的节点目录,客户端用钉扎的 Ed25519 根公钥验签后更新本地缓存。
	// 未配置时省略,客户端沿用上次缓存或内置节点列表。
	if len(h.DirectoryJSON) > 0 {
		body["directory"] = h.DirectoryJSON
	}
	respond.OKRaw(w, body)
}

// putIfNonEmpty 字符串非空时才写入 map(否则省略 key,客户端 optString 返默认值)。
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

// authTripleBody 客户端 ClientInit.authTriple() 的固定形状。
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
		if !auth.IsWellFormedToken(tri.AccessToken) {
			respond.Fail(w, http.StatusUnauthorized, "missing_access_token",
				"缺少或非法的 access_token,请先调用 /client/init")
			return
		}
		sess, err := h.St.ClientSessionLookup(r.Context(), tri.AccessToken, tri.DeviceID)
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
			_ = h.St.ClientSessionDelete(r.Context(), tri.AccessToken)
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
		h.St.ClientSessionTouch(r.Context(), tri.AccessToken)
		ctx := context.WithValue(r.Context(), ctxClientSession, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

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

// ── /client/method-select ───────────────────────────────────────────────

type methodReq struct {
	authTripleBody
	Method string `json:"method"`
}

func (h *Handler) methodSelect(w http.ResponseWriter, r *http.Request) {
	var req methodReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.Method != "online" && req.Method != "offline" {
		respond.Fail(w, http.StatusBadRequest, "bad_method", "method 必须为 online 或 offline")
		return
	}
	sess := sessionFromCtx(r.Context())
	go func(deviceID, method string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.St.AuditInsert(ctx, store.AuditEntry{
			ID:      newAuditID(),
			Ts:      time.Now().UnixMilli(),
			Actor:   "system",
			Type:    "client.method-select",
			Target:  ptr(deviceID),
			Details: jsonMust(map[string]string{"method": method}),
		})
	}(sess.DeviceID, req.Method)
	respond.OK(w, nil)
}

// ── /client/online-download ─────────────────────────────────────────────

// onlineDownloadReq 客户端只发 authTriple,服务端额外的字段不读。
type onlineDownloadReq struct {
	authTripleBody
}

func (h *Handler) onlineDownload(w http.ResponseWriter, r *http.Request) {
	var req onlineDownloadReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	sess := sessionFromCtx(r.Context())
	ctx := r.Context()
	// 自动封禁信号:单设备 online-download 高频拉取 → 异常资源请求。
	h.AutoBan.OnResourceRequest(ctx, sess.DeviceID)

	// ── 装配 groups 数组。客户端 ClientInit.fetchOnlineDownload 期望:
	//   groups: [{name, mirrors:[
	//             URL_string |
	//             {url, files:[ key_string | {key,size} ]}
	//           ]}]
	//
	// 数据来源,按组优先级递减拼接:
	//   1. 管理后台配置的 mirror_groups + 内联 files(高优先级)
	//   2. 本节点本地 /res(系统兜底组)
	// 边缘节点不再通过心跳动态发现,而是经签名节点目录(/client/init 的 directory
	// 字段)下发给客户端,由客户端按 caps=resource 自行选源。
	groups := make([]map[string]any, 0)

	// 1. 管理后台镜像组（过滤超限镜像）
	var dispatchedURLs []string
	dbGroups, _ := h.St.MirrorGroupsListWithMirrors(ctx)
	for _, g := range dbGroups {
		entries := make([]any, 0, len(g.Mirrors))
		for _, m := range g.Mirrors {
			if h.MirrorEnabled != nil && !h.MirrorEnabled(m.URL) {
				continue // 日限额/速度超限，本次不派发
			}
			entries = append(entries, mirrorToEntry(m))
			dispatchedURLs = append(dispatchedURLs, m.URL)
		}
		if len(entries) > 0 {
			groups = append(groups, map[string]any{
				"name":    g.Group.Name,
				"mirrors": entries,
			})
		}
	}

	// 2. 本节点本地 res
	if h.PrimaryResBaseURL != nil {
		if u := h.PrimaryResBaseURL(r); u != "" {
			groups = append(groups, map[string]any{
				"name":    "本节点本地",
				"mirrors": []any{u}, // 没有内联清单 → 客户端走 S3 XML 自发现
			})
			dispatchedURLs = append(dispatchedURLs, u)
		}
	}

	if len(groups) == 0 {
		respond.Fail(w, http.StatusServiceUnavailable, "no_mirrors", "资源配置缺失")
		return
	}

	// 记录本次分派的镜像列表，供后续心跳速度归因
	h.Heartbeats.RecordDispatch(sess.DeviceID, dispatchedURLs)

	resourceToken, _ := h.signResourceToken(sess.DeviceID)
	respond.OKRaw(w, map[string]any{
		"success":        true,
		"groups":         groups,
		"resource_token": resourceToken,
	})
}

// mirrorToEntry 把存储层的 store.Mirror 转成客户端期望的 mirror 条目。
// 没有内联 files 时返回字符串(URL only),否则返回 {url, files} 对象。
// 这是客户端 ClientInit.java 第 219-243 行的对偶。
func mirrorToEntry(m store.Mirror) any {
	if len(m.Files) == 0 {
		return m.URL
	}
	return map[string]any{
		"url":   m.URL,
		"files": json.RawMessage(m.Files),
	}
}

// ── /client/offline-package ─────────────────────────────────────────────

type offlinePackageReq struct {
	authTripleBody
}

func (h *Handler) offlinePackage(w http.ResponseWriter, r *http.Request) {
	var req offlinePackageReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	_ = req
	p, err := h.St.OfflinePackageGet(r.Context())
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	out := map[string]any{"success": true}
	putIfNonEmpty(out, "download_url", p.DownloadURL)
	putIfNonEmpty(out, "package_version", p.PackageVersion)
	putIfNonEmpty(out, "sha256", p.SHA256)
	putIfNonZero(out, "size", p.Size)
	respond.OKRaw(w, out)
}

// ── /client/heartbeat ───────────────────────────────────────────────────

// hbFile 客户端 ResourceFlow.HeartbeatSender.sendHeartbeat 的 file JSON 形状。
type hbFile struct {
	Name     string  `json:"name"`
	Status   string  `json:"status"` // pending|downloading|done|failed (lowercase)
	Percent  float64 `json:"percent"`
	SpeedBps int64   `json:"speed_bps"`
}

type heartbeatReq struct {
	authTripleBody
	Files []hbFile `json:"files"`
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	sess := sessionFromCtx(r.Context())
	ctx := r.Context()
	downloadPhase := len(req.Files) > 0
	phase := "game"
	if downloadPhase {
		phase = "download"
	}
	// 下载阶段从逐文件状态聚合出整体进度/瞬时速度/当前文件;游戏阶段(空 Files)全为零值。
	progress, speedBps, curFile := aggregateHBFiles(req.Files)
	h.Heartbeats.Update(sess.DeviceID, &HBState{
		Files:       req.Files,
		Phase:       phase,
		Progress:    progress,
		SpeedBps:    speedBps,
		CurrentFile: curFile,
		LastSeen:    time.Now().UnixMilli(),
	})
	// 速度归因到本设备上次 online-download 分派的镜像列表（均摊）
	if h.OnMirrorSpeed != nil && downloadPhase && speedBps > 0 {
		dispatched := h.Heartbeats.GetDispatched(sess.DeviceID)
		if len(dispatched) > 0 {
			h.OnMirrorSpeed(dispatched, speedBps)
		}
	}
	// 自动封禁信号:单设备心跳频率远超正常节律 → 伪造/刷包。
	h.AutoBan.OnHeartbeat(ctx, sess.DeviceID)

	// ── 维护(仅游戏阶段提示):响应 action=maintenance + 顶层 message/end_time
	if !downloadPhase {
		srv := getServerConfig(ctx, h.St)
		if srv.Status == "maintenance" || srv.Status == "warn" {
			out := map[string]any{"success": true, "action": "maintenance"}
			putIfNonEmpty(out, "message", srv.Message)
			putIfNonZero(out, "end_time", srv.EndTime)
			respond.OKRaw(w, out)
			return
		}
	}

	// ── 换线指令(仅下载阶段)
	if downloadPhase {
		if assign := h.Heartbeats.TakeSwitch(sess.DeviceID); assign != nil {
			// 客户端期望 assignments: [{mirror, files: [name]}]
			out := map[string]any{
				"success":     true,
				"action":      "switch_mirrors",
				"assignments": assign.toClientList(),
			}
			putIfNonEmpty(out, "message", assign.Message)
			respond.OKRaw(w, out)
			return
		}
	}

	respond.OKRaw(w, map[string]any{"success": true, "action": "ok"})
}

// ── /client/hot-update ──────────────────────────────────────────────────

type hotReq struct {
	authTripleBody
	LocalJSVersion       int `json:"local_js_version"`
	LocalScenarioVersion int `json:"local_scenario_version"`
}

func (h *Handler) hotUpdate(w http.ResponseWriter, r *http.Request) {
	var req hotReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	_ = req
	js, _ := h.St.HotBundleGet(r.Context(), "js")
	scn, _ := h.St.HotBundleGet(r.Context(), "scenario")
	respond.OKRaw(w, map[string]any{
		"success":  true,
		"js":       bundlePayload(js),
		"scenario": bundlePayload(scn),
	})
}

// ── 内部辅助 ────────────────────────────────────────────────────────────

func (h *Handler) signResourceToken(deviceID string) (string, int64) {
	win := h.TokenWindowSec
	if win <= 0 {
		win = 300
	}
	now := time.Now().Unix()
	bucket := now / int64(win)
	expires := (bucket + 1) * int64(win) * 1000 // ms
	mac := hmac.New(sha256.New, h.ResourceTokenSecret)
	mac.Write([]byte(deviceID))
	mac.Write([]byte("|"))
	mac.Write([]byte(strconv.FormatInt(bucket, 10)))
	sig := mac.Sum(nil)
	tok := base64.RawURLEncoding.EncodeToString(sig)
	return tok, expires
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
//	expire_time == 0  → 永久(客户端 BanInfo.java 把 0 当作永久)
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
