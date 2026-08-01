// Package admin 实现 /admin/* 全部管理后台接口。
// 路由分两层:RequireWritableAdmin 是默认写权限,/admins/* 额外要求 super_admin。
// 字段名遵循《服务端编写指南》§6,响应统一 JSON。
package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/api-server/internal/api/client"
	"magirecocn-revival/api-server/internal/api/respond"
	"magirecocn-revival/api-server/internal/auth"
	"magirecocn-revival/api-server/internal/autoban"
	"magirecocn-revival/api-server/internal/capworker"
	"magirecocn-revival/api-server/internal/middleware"
	"magirecocn-revival/api-server/internal/store"
)

type Handler struct {
	St           *store.Store
	Hearts       *client.Heartbeats
	Cap          *capworker.Service
	RequireSuper func(http.Handler) http.Handler // 超管校验中间件

	// 离线包打包参数,由 main 注入。Trigger 是 nil 时 /admin/auto-package/run
	// 直接 503(没配 SourceDir 等于关掉了打包能力)。
	Pack PackerConfig

	// 热更新包自托管参数,由 main 注入。Dir 为空 = 未配置托管目录,发布/上传 503。
	HotUpdate HotUpdateConfig

	// Limits 运行时可调的大小上限(全局请求体 / 热更新包),由 main 注入并与
	// BodyLimitFunc 中间件共享同一份 atomic。
	Limits *Limits
}

// HotUpdateConfig 热更新包"下载到本地 + 自托管"所需参数。
// 大小上限不在此(改由 Handler.Limits 运行时提供)。
type HotUpdateConfig struct {
	Dir     string                       // CNV_HOTUPDATE_DIR,落盘目录
	URLBase func(r *http.Request) string // <scheme>://<host><CNV_HOTUPDATE_URL_PATH>
}

// PackerConfig 暴露给 admin 包用的打包参数。内嵌 packer.Config 加上"产物在
// 哪个 URL 下能下载到"的拼接函数。
type PackerConfig struct {
	SourceDir string                       // CNV_PRIMARY_RES_DIR
	DestDir   string                       // CNV_OFFLINE_DIR
	URLBase   func(r *http.Request) string // <scheme>://<host><CNV_OFFLINE_URL_PATH>
}

// Routes 全部 /admin/* 子路由。
func (h *Handler) Routes(r chi.Router) {
	r.Get("/state", h.aggregateState)

	r.Get("/server/status", h.serverStatusGet)
	r.Put("/server/status", h.serverStatusPut)
	r.Put("/server/download", h.serverDownloadPut)

	r.Get("/versions", h.versionsGet)
	r.Put("/versions", h.versionsPut)

	r.Get("/services", h.servicesGet)
	r.Put("/services", h.servicesPut)

	r.Get("/accounts", h.accountsList)
	r.Post("/accounts", h.accountsCreate)
	r.Get("/accounts/{id}", h.accountsDetail)
	r.Patch("/accounts/{id}", h.accountsPatch)
	r.Post("/accounts/{id}/reset-password", h.accountsResetPwd)
	r.Delete("/accounts/{id}", h.accountsDelete)

	r.Get("/bans", h.bansList)
	r.Post("/bans", h.bansCreate)
	r.Delete("/bans/{id}", h.bansLift)

	r.Get("/heartbeats", h.heartbeatsList)
	r.Post("/heartbeats/{deviceId}/ban", h.heartbeatsBan)

	r.Get("/unverified-purge", h.unverifiedPurgeGet)
	r.Put("/unverified-purge", h.unverifiedPurgePut)

	r.Get("/limits", h.limitsGet)
	r.Put("/limits", h.limitsPut)

	r.Get("/audit", h.auditList)
	r.Get("/audit/export", h.auditExport)

	r.Get("/captcha", h.captchaGet)
	r.Put("/captcha", h.captchaPut)
	r.Get("/autoban", h.autobanGet)
	r.Put("/autoban", h.autobanPut)
	r.Post("/captcha/test", h.captchaTest)

	// 超管专属
	if h.RequireSuper != nil {
		r.Group(func(sg chi.Router) {
			sg.Use(h.RequireSuper)
			sg.Get("/admins", h.adminsList)
			sg.Post("/admins", h.adminsCreate)
			sg.Patch("/admins/{id}", h.adminsPatch)
			sg.Delete("/admins/{id}", h.adminsDelete)
			sg.Post("/admins/{id}/reset-password", h.adminsResetPwd)
		})
	}
}

// 当前操作管理员的 username。
func actorOf(r *http.Request) string {
	a := middleware.AdminFrom(r.Context())
	if a == nil {
		return "system"
	}
	return a.Username
}

// 写一条审计日志(失败仅日志)。
func (h *Handler) audit(r *http.Request, typ, target string, details map[string]any) {
	var d json.RawMessage
	if details != nil {
		d, _ = json.Marshal(details)
	} else {
		d = json.RawMessage("{}")
	}
	tID, _ := auth.NewToken()
	var tgtPtr *string
	if target != "" {
		tgtPtr = &target
	}
	_ = h.St.AuditInsert(r.Context(), store.AuditEntry{
		ID:      "log_" + tID[:24],
		Ts:      time.Now().UnixMilli(),
		Actor:   actorOf(r),
		Type:    typ,
		Target:  tgtPtr,
		Details: d,
	})
}

// ── 6.1 服务器控制 ─────────────────────────────────────────────────────

type serverStatusCfg struct {
	Status             string `json:"status"`
	MaintenanceMessage string `json:"maintenance_message"`
	EstimatedEnd       *int64 `json:"estimated_end"`
}

func (h *Handler) serverStatusGet(w http.ResponseWriter, r *http.Request) {
	c := serverStatusCfg{Status: "ok"}
	_, _ = h.St.ConfigGet(r.Context(), "server", &c)
	respond.OKRaw(w, map[string]any{"success": true,
		"status": c.Status, "maintenance_message": c.MaintenanceMessage, "estimated_end": c.EstimatedEnd})
}

type serverStatusReq struct {
	Status             string `json:"status"`
	MaintenanceMessage string `json:"message"`
	EstimatedEnd       *int64 `json:"estimated_end"`
}

func (h *Handler) serverStatusPut(w http.ResponseWriter, r *http.Request) {
	var req serverStatusReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.Status != "ok" && req.Status != "warn" && req.Status != "err" {
		respond.Fail(w, http.StatusBadRequest, "bad_status", "status 取值不合法")
		return
	}
	if len(req.MaintenanceMessage) > 2048 {
		respond.Fail(w, http.StatusBadRequest, "message_too_long", "")
		return
	}
	cfg := serverStatusCfg{
		Status: req.Status, MaintenanceMessage: req.MaintenanceMessage, EstimatedEnd: req.EstimatedEnd,
	}
	if err := h.St.ConfigSet(r.Context(), "server", cfg); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "server.status.change", req.Status, map[string]any{
		"message": req.MaintenanceMessage, "estimated_end": req.EstimatedEnd,
	})
	respond.OK(w, nil)
}

type featuresCfg struct {
	OnlineEnabled   bool   `json:"online_enabled"`
	OfflineEnabled  bool   `json:"offline_enabled"`
	AccountEnabled  bool   `json:"account_enabled"`
	DisabledMessage string `json:"disabled_message"`
}

func (h *Handler) serverDownloadPut(w http.ResponseWriter, r *http.Request) {
	var req featuresCfg
	req.OnlineEnabled = true
	req.OfflineEnabled = true
	req.AccountEnabled = true
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if len(req.DisabledMessage) > 2048 {
		respond.Fail(w, http.StatusBadRequest, "message_too_long", "")
		return
	}
	if err := h.St.ConfigSet(r.Context(), "features", req); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "feature.toggle", "", map[string]any{
		"online": req.OnlineEnabled, "offline": req.OfflineEnabled, "account": req.AccountEnabled,
	})
	respond.OK(w, nil)
}

// ── 6.2 版本管理 ───────────────────────────────────────────────────────

type versionsCfg struct {
	AllowedVersions             []string `json:"allowed_versions"`
	LatestVersion               string   `json:"latest_version"`
	FakeVersion                 string   `json:"fake_version"`
	FakeAppName                 string   `json:"fake_app_name"`
	UpdateURLNormal             string   `json:"update_url_normal"`
	UpdateURLInternalTest       string   `json:"update_url_internal_test"`
	UpdateAPKSHA256             string   `json:"update_apk_sha256"`
	UpdateAPKSHA256InternalTest string   `json:"update_apk_sha256_internal_test"`
}

func (h *Handler) versionsGet(w http.ResponseWriter, r *http.Request) {
	c := versionsCfg{AllowedVersions: []string{}}
	_, _ = h.St.ConfigGet(r.Context(), "versions", &c)
	if c.AllowedVersions == nil {
		c.AllowedVersions = []string{}
	}
	respond.JSON(w, http.StatusOK, map[string]any{"success": true,
		"allowed_versions": c.AllowedVersions, "latest_version": c.LatestVersion,
		"fake_version": c.FakeVersion, "fake_app_name": c.FakeAppName,
		"update_url_normal":               c.UpdateURLNormal,
		"update_url_internal_test":        c.UpdateURLInternalTest,
		"update_apk_sha256":               c.UpdateAPKSHA256,
		"update_apk_sha256_internal_test": c.UpdateAPKSHA256InternalTest,
	})
}

func (h *Handler) versionsPut(w http.ResponseWriter, r *http.Request) {
	var req versionsCfg
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	for _, u := range []string{req.UpdateURLNormal, req.UpdateURLInternalTest} {
		if u != "" && !isSafeHTTPURL(u) {
			respond.Fail(w, http.StatusBadRequest, "bad_url", "更新 URL 必须为 http(s)://")
			return
		}
	}
	if req.AllowedVersions == nil {
		req.AllowedVersions = []string{}
	}
	if err := h.St.ConfigSet(r.Context(), "versions", req); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "version.fake.update", "", nil)
	respond.OK(w, nil)
}

// ── 6.2b services:握手期下发给客户端的运行时地址 ──────────────────────
//
// 三字段全部可选,/client/init 响应会按 "省略空键" 规则下发。详见
// internal/api/client/state.go 的 servicesCfg / 客户端 API 变动说明文档。

type servicesCfg struct {
	CapWorkerURL   string   `json:"cap_worker_url"`
	ProxyBackends  []string `json:"proxy_backends"`
	GameServerHost string   `json:"game_server_host"`
}

func (h *Handler) servicesGet(w http.ResponseWriter, r *http.Request) {
	var c servicesCfg
	_, _ = h.St.ConfigGet(r.Context(), "services", &c)
	if c.ProxyBackends == nil {
		c.ProxyBackends = []string{}
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"cap_worker_url":   c.CapWorkerURL,
		"proxy_backends":   c.ProxyBackends,
		"game_server_host": c.GameServerHost,
	})
}

func (h *Handler) servicesPut(w http.ResponseWriter, r *http.Request) {
	var req servicesCfg
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	// cap_worker_url:完整 URL(含 scheme,不含尾斜杠)。允许为空 → 不下发,
	// 客户端回退内置 URL。
	if req.CapWorkerURL != "" && !isSafeHTTPURL(req.CapWorkerURL) {
		respond.Fail(w, http.StatusBadRequest, "bad_cap_worker_url", "cap_worker_url 必须为 http(s):// URL")
		return
	}
	// game_server_host:**纯 host**(不含 scheme),客户端写死了 https:// 前缀。
	// 见 2026-05-29 客户端代理回退逻辑修复说明:代理耗尽时拼成 https://<host>
	// 打到私服后端;若缺失则客户端静默放弃重定向,落到死域名。
	gameHost := strings.TrimSpace(req.GameServerHost)
	if gameHost != "" {
		if strings.Contains(gameHost, "://") {
			respond.Fail(w, http.StatusBadRequest, "bad_game_server_host",
				"game_server_host 必须是纯 host,不含 scheme;客户端会自动拼 https://")
			return
		}
		if strings.ContainsAny(gameHost, "/?#") {
			respond.Fail(w, http.StatusBadRequest, "bad_game_server_host",
				"game_server_host 必须是纯 host,不含路径/查询/片段")
			return
		}
		if !isValidHostname(gameHost) {
			respond.Fail(w, http.StatusBadRequest, "bad_game_server_host",
				"game_server_host 格式不合法,期望 host[:port]")
			return
		}
	}
	// proxy_backends:完整 URL 数组(含 scheme,不含尾斜杠)。
	clean := make([]string, 0, len(req.ProxyBackends))
	for _, p := range req.ProxyBackends {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		if !isSafeHTTPURL(p) {
			respond.Fail(w, http.StatusBadRequest, "bad_proxy_backend",
				"proxy_backends 每条都必须是 http(s):// URL: "+p)
			return
		}
		clean = append(clean, p)
	}
	out := servicesCfg{
		CapWorkerURL:   strings.TrimRight(strings.TrimSpace(req.CapWorkerURL), "/"),
		ProxyBackends:  clean,
		GameServerHost: gameHost, // 纯 host,不去尾斜杠(本就没有)
	}
	if err := h.St.ConfigSet(r.Context(), "services", out); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "services.update", "", map[string]any{
		"cap_worker_url":   out.CapWorkerURL,
		"proxy_backends":   len(out.ProxyBackends),
		"game_server_host": out.GameServerHost,
	})
	respond.OK(w, nil)
}

// isValidHostname RFC 1123 风格的相对宽松校验:由 . 分隔的标签构成,
// 每个标签 1-63 字符,字母数字 + 连字符,不以连字符开头/结尾;
// 末尾允许 :port (1-5 数字)。不接受 scheme / 路径 / 查询。
func isValidHostname(s string) bool {
	if len(s) == 0 || len(s) > 253+6 {
		return false
	}
	host := s
	if i := strings.LastIndex(s, ":"); i > 0 {
		port := s[i+1:]
		if len(port) < 1 || len(port) > 5 {
			return false
		}
		for _, c := range port {
			if c < '0' || c > '9' {
				return false
			}
		}
		host = s[:i]
	}
	if host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-'
			if !ok {
				return false
			}
		}
	}
	return true
}

func (h *Handler) accountsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	accs, total, err := h.St.AccountList(r.Context(), q, status, (page-1)*limit, limit)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	respond.OKRaw(w, map[string]any{"success": true, "total": total,
		"accounts": mapAccounts(accs)})
}

type accountCreateReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) accountsCreate(w http.ResponseWriter, r *http.Request) {
	var req accountCreateReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.Username == "" || req.Email == "" {
		respond.Fail(w, http.StatusBadRequest, "missing_field", "用户名/邮箱必填")
		return
	}
	pwd := req.Password
	if pwd == "" {
		rb := make([]byte, 6)
		_, _ = rand.Read(rb)
		pwd = hex.EncodeToString(rb)
	}
	hash, err := auth.HashPassword(pwd)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	idBytes := make([]byte, 6)
	_, _ = rand.Read(idBytes)
	id := "acc-" + hex.EncodeToString(idBytes)
	if err := h.St.AccountInsert(r.Context(), store.Account{
		ID: id, Username: req.Username, Email: req.Email, PasswordHash: hash,
		Status: "active", CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		respond.Fail(w, http.StatusConflict, "conflict", "用户名或邮箱已存在")
		return
	}
	h.audit(r, "account.create", req.Username, nil)
	body := map[string]any{"success": true, "id": id}
	if req.Password == "" {
		body["temp_password"] = pwd
	}
	respond.OKRaw(w, body)
}

func (h *Handler) accountsDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	acc, err := h.St.AccountByID(r.Context(), id)
	if err != nil {
		respond.Fail(w, http.StatusNotFound, "not_found", "账号不存在")
		return
	}
	respond.OKRaw(w, map[string]any{"success": true, "account": mapAccount(acc)})
}

type accountPatchReq struct {
	Status string `json:"status"`
	Email  string `json:"email"`
}

func (h *Handler) accountsPatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req accountPatchReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	acc, err := h.St.AccountByID(r.Context(), id)
	if err != nil {
		respond.Fail(w, http.StatusNotFound, "not_found", "账号不存在")
		return
	}
	if req.Status == "active" || req.Status == "disabled" {
		_ = h.St.AccountUpdateStatus(r.Context(), id, req.Status)
		if req.Status == "disabled" {
			_ = h.St.AccountSessionDeleteByAccount(r.Context(), id)
			h.audit(r, "account.disable", acc.Username, nil)
		} else {
			h.audit(r, "account.enable", acc.Username, nil)
		}
	}
	if req.Email != "" {
		_ = h.St.AccountUpdateEmail(r.Context(), id, req.Email)
	}
	respond.OK(w, nil)
}

func (h *Handler) accountsResetPwd(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	acc, err := h.St.AccountByID(r.Context(), id)
	if err != nil {
		respond.Fail(w, http.StatusNotFound, "not_found", "账号不存在")
		return
	}
	rb := make([]byte, 6)
	_, _ = rand.Read(rb)
	pwd := hex.EncodeToString(rb)
	hash, _ := auth.HashPassword(pwd)
	_ = h.St.AccountUpdatePassword(r.Context(), id, hash)
	_ = h.St.AccountSessionDeleteByAccount(r.Context(), id)
	h.audit(r, "account.reset_pwd", acc.Username, nil)
	respond.OKRaw(w, map[string]any{"success": true, "temp_password": pwd})
}

func (h *Handler) accountsDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	acc, err := h.St.AccountByID(r.Context(), id)
	if err != nil {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
		return
	}
	_ = h.St.AccountDelete(r.Context(), id)
	h.audit(r, "account.delete", acc.Username, nil)
	respond.OK(w, nil)
}

// ── 6.6 设备封禁 ──────────────────────────────────────────────────────

func (h *Handler) bansList(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	var list []store.Ban
	var err error
	if tab == "history" {
		list, err = h.St.BanListHistory(r.Context())
	} else {
		list, err = h.St.BanListActive(r.Context())
	}
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	respond.OKRaw(w, map[string]any{"success": true, "bans": mapBans(list)})
}

type banCreateReq struct {
	DeviceID   string `json:"device_id"`
	Reason     string `json:"reason"`
	ExpireTime *int64 `json:"expire_time"` // null = 永久
}

func (h *Handler) bansCreate(w http.ResponseWriter, r *http.Request) {
	var req banCreateReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	deviceID := strings.ToLower(req.DeviceID)
	if len(deviceID) < 8 {
		respond.Fail(w, http.StatusBadRequest, "bad_device", "设备 ID 不合法")
		return
	}
	if req.Reason == "" {
		respond.Fail(w, http.StatusBadRequest, "missing_reason", "缺少封禁理由")
		return
	}
	rb := make([]byte, 6)
	_, _ = rand.Read(rb)
	id := "ban-" + hex.EncodeToString(rb)
	if err := h.St.BanInsert(r.Context(), store.Ban{
		ID: id, DeviceID: deviceID, Reason: req.Reason,
		IssuedAt: time.Now().UnixMilli(), ExpireTime: req.ExpireTime,
		IssuedBy: actorOf(r),
	}); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "device.ban", short(deviceID, 16), map[string]any{"reason": req.Reason})
	respond.OKRaw(w, map[string]any{"success": true, "id": id})
}

func (h *Handler) bansLift(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ban, err := h.St.BanByID(r.Context(), id)
	if err != nil {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
		return
	}
	_ = h.St.BanLift(r.Context(), id, actorOf(r))
	h.audit(r, "device.lift", short(ban.DeviceID, 16), nil)
	respond.OK(w, nil)
}

// ── 6.7 心跳监控 ──────────────────────────────────────────────────────

func (h *Handler) heartbeatsList(w http.ResponseWriter, r *http.Request) {
	snap := h.Hearts.Snapshot()
	list := make([]map[string]any, 0, len(snap))
	for did, st := range snap {
		files := make([]map[string]any, 0, len(st.Files))
		for _, f := range st.Files {
			files = append(files, map[string]any{
				"name":      f.Name,
				"status":    f.Status,
				"percent":   f.Percent,
				"speed_bps": f.SpeedBps,
			})
		}
		list = append(list, map[string]any{
			"device_id":      did,
			"type":           st.Kind(), // online | hotupdate | game(见 client.HBState.Kind)
			"progress":       st.Progress,
			"speed_bps":      st.SpeedBps,
			"current_file":   st.CurrentFile,
			"phase":          st.Phase,
			"files":          files,
			"last_heartbeat": st.LastSeen,
		})
	}
	respond.OKRaw(w, map[string]any{"success": true, "heartbeats": list})
}

type switchMirrorReq struct {
	MirrorAssignments map[string]string `json:"mirror_assignments"`
	Message           string            `json:"message"`
}

type hbBanReq struct {
	Reason string `json:"reason"`
}

func (h *Handler) heartbeatsBan(w http.ResponseWriter, r *http.Request) {
	did := chi.URLParam(r, "deviceId")
	var req hbBanReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.Reason == "" {
		req.Reason = "管理员从心跳页面直接封禁"
	}
	exp := time.Now().Add(24 * time.Hour).UnixMilli()
	rb := make([]byte, 6)
	_, _ = rand.Read(rb)
	id := "ban-" + hex.EncodeToString(rb)
	_ = h.St.BanInsert(r.Context(), store.Ban{
		ID: id, DeviceID: did, Reason: req.Reason,
		IssuedAt: time.Now().UnixMilli(), ExpireTime: &exp,
		IssuedBy: actorOf(r),
	})
	h.audit(r, "device.ban", short(did, 16), map[string]any{"reason": req.Reason, "source": "heartbeat"})
	respond.OKRaw(w, map[string]any{"success": true, "id": id})
}

// ── 6.8 定时任务 ──────────────────────────────────────────────────────

type tasksCfg struct {
	HeartbeatTimeoutSec  int `json:"heartbeat_timeout_sec"`
	BanAutoExpireScanSec int `json:"ban_auto_expire_scan_sec"`
	BanSweepSec          int `json:"ban_sweep_sec"`
	MetricsFlushSec      int `json:"metrics_flush_sec"`
	SessionGCSec         int `json:"session_gc_sec"`
	CapWorkerHealthSec   int `json:"cap_worker_health_sec"`
}

type unverifiedPurgeCfg struct {
	Enabled    bool  `json:"enabled"`
	RetainDays int   `json:"retainDays"` // 0 视为 1,最少保留 1 天
	ScanSec    int64 `json:"scanSec"`    // 扫描间隔秒数,默认 86400(每日)
}

func defaultUnverifiedPurge() unverifiedPurgeCfg {
	return unverifiedPurgeCfg{Enabled: false, RetainDays: 30, ScanSec: 86400}
}

func (h *Handler) unverifiedPurgeGet(w http.ResponseWriter, r *http.Request) {
	c := defaultUnverifiedPurge()
	_, _ = h.St.ConfigGet(r.Context(), "unverified_purge", &c)
	respond.OKRaw(w, map[string]any{
		"success":     true,
		"enabled":     c.Enabled,
		"retain_days": c.RetainDays,
		"scan_sec":    c.ScanSec,
	})
}

func (h *Handler) unverifiedPurgePut(w http.ResponseWriter, r *http.Request) {
	var req unverifiedPurgeCfg
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.RetainDays < 1 {
		req.RetainDays = 1
	}
	if req.ScanSec < 60 {
		req.ScanSec = 60
	}
	if err := h.St.ConfigSet(r.Context(), "unverified_purge", req); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "account.unverified_purge.update", "", map[string]any{
		"enabled": req.Enabled, "retain_days": req.RetainDays,
	})
	respond.OK(w, nil)
}

// ── 6.9 审计日志 ──────────────────────────────────────────────────────

func (h *Handler) auditList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ := q.Get("type")
	actor := q.Get("actor")
	days, _ := strconv.Atoi(q.Get("days"))
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit := 50
	rows, total, err := h.St.AuditList(r.Context(), typ, actor, days, (page-1)*limit, limit)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		var det any
		_ = json.Unmarshal(e.Details, &det)
		out = append(out, map[string]any{
			"id": e.ID, "ts": e.Ts, "actor": e.Actor, "type": e.Type,
			"target": e.Target, "details": det,
		})
	}
	respond.OKRaw(w, map[string]any{"success": true, "total": total, "audit": out})
}

func (h *Handler) auditExport(w http.ResponseWriter, r *http.Request) {
	rows, _, _ := h.St.AuditList(r.Context(), "", "", 0, 0, 10000)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
	_, _ = w.Write([]byte("ts,actor,type,target,details\n"))
	for _, e := range rows {
		target := ""
		if e.Target != nil {
			target = *e.Target
		}
		line := strconv.FormatInt(e.Ts, 10) + "," + csvEsc(e.Actor) + "," +
			csvEsc(e.Type) + "," + csvEsc(target) + "," + csvEsc(string(e.Details)) + "\n"
		_, _ = w.Write([]byte(line))
	}
}

// ── 6.10 验证码配置 ──────────────────────────────────────────────────

// captcha 配置。cap-worker 是**内置**(本服务端 /api/challenge、/api/redeem 同进程),
// 没有"外部端点 URL"这一项——只保留启用开关、难度、子挑战数与上次测试结果。
type captchaCfg struct {
	Enabled        bool   `json:"enabled"`
	Difficulty     int    `json:"difficulty"`
	ChallengeCount int    `json:"challenge_count"`
	LastTestedAt   int64  `json:"last_tested_at"`
	LastResult     string `json:"last_result"`
}

func defaultCaptcha() captchaCfg {
	return captchaCfg{
		Enabled:    true,
		Difficulty: 12, ChallengeCount: 3, LastResult: "never",
	}
}

func (h *Handler) captchaGet(w http.ResponseWriter, r *http.Request) {
	c := defaultCaptcha()
	_, _ = h.St.ConfigGet(r.Context(), "captcha", &c)
	respond.JSON(w, http.StatusOK, map[string]any{"success": true,
		"enabled": c.Enabled, "difficulty": c.Difficulty,
		"challenge_count": c.ChallengeCount,
		"last_tested_at":  c.LastTestedAt, "last_result": c.LastResult,
	})
}

func (h *Handler) captchaPut(w http.ResponseWriter, r *http.Request) {
	var req captchaCfg
	req.Enabled = true
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if err := h.St.ConfigSet(r.Context(), "captcha", req); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "captcha.config.update", "", map[string]any{
		"enabled": req.Enabled, "difficulty": req.Difficulty,
	})
	respond.OK(w, nil)
}

// captchaTest 真跑一遍 PoW 链路。
func (h *Handler) captchaTest(w http.ResponseWriter, r *http.Request) {
	ch, err := h.Cap.Issue(r.Context())
	result := "ok"
	if err != nil {
		result = "fail"
	} else {
		// 自己求解。难度过高时 PoW 期望迭代次数 ~2^d,可能拖死 goroutine;
		// 给每个子挑战设一个迭代上限,超过就判定为"配置难度过高"而非死循环。
		// d=20 时上限 ~8M 次哈希,单次约几百毫秒,足够覆盖合理难度。
		const maxIter = 1 << 23
		sols := []int64{}
		solved := true
		for i := 0; i < ch.Challenge.C && solved; i++ {
			found := false
			for nonce := int64(0); nonce < maxIter; nonce++ {
				if leadingZeroBits(sha256Hex(ch.Token+"."+strconv.Itoa(i)+"."+strconv.FormatInt(nonce, 10))) >= ch.Challenge.D {
					sols = append(sols, nonce)
					found = true
					break
				}
			}
			if !found {
				solved = false
			}
		}
		if !solved {
			result = "difficulty_too_high"
		} else if res := h.Cap.Redeem(r.Context(), ch.Token, sols); !res.Success {
			result = "fail"
		}
	}
	c := defaultCaptcha()
	_, _ = h.St.ConfigGet(r.Context(), "captcha", &c)
	c.LastTestedAt = time.Now().UnixMilli()
	c.LastResult = result
	_ = h.St.ConfigSet(r.Context(), "captcha", c)
	h.audit(r, "captcha.test", "", map[string]any{"result": result})
	respond.OKRaw(w, map[string]any{"success": true, "result": result, "last_tested_at": c.LastTestedAt})
}

// ── 6.10b 自动封禁阈值 ────────────────────────────────────────────────

// autobanGet 返回当前自动封禁阈值(config 表,未配置回落默认)+ 一份默认值供前端「恢复默认」。
func (h *Handler) autobanGet(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"config":   autoban.Load(r.Context(), h.St),
		"defaults": autoban.DefaultConfig(),
	})
}

// autobanPut 校验并持久化自动封禁阈值;运行时由判定器(带 5s 缓存)读取生效。
func (h *Handler) autobanPut(w http.ResponseWriter, r *http.Request) {
	var req autoban.Config
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		respond.Fail(w, http.StatusBadRequest, "bad_config", err.Error())
		return
	}
	if err := h.St.ConfigSet(r.Context(), autoban.ConfigKey, req); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	h.audit(r, "autoban.config.update", "", map[string]any{
		"enabled":           req.Enabled,
		"captcha_max":       req.Captcha.Max,
		"heartbeat_max":     req.Heartbeat.Max,
		"resource_max":      req.Resource.Max,
		"tamper_max":        req.Tamper.Max,
		"multi_account_max": req.MultiAccount.Max,
	})
	respond.OK(w, nil)
}

// ── 6.11 管理员管理(super_admin)─────────────────────────────────────

func (h *Handler) adminsList(w http.ResponseWriter, r *http.Request) {
	list, err := h.St.AdminListAll(r.Context())
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id": a.ID, "username": a.Username, "email": a.Email,
			"role": a.Role, "created_at": a.CreatedAt, "last_login_at": a.LastLoginAt,
		})
	}
	respond.OKRaw(w, map[string]any{"success": true, "admins": out})
}

type adminCreateReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

func (h *Handler) adminsCreate(w http.ResponseWriter, r *http.Request) {
	var req adminCreateReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.Username == "" || req.Email == "" {
		respond.Fail(w, http.StatusBadRequest, "missing_field", "")
		return
	}
	role := req.Role
	if role != "super_admin" && role != "admin" && role != "readonly" {
		role = "admin"
	}
	pwd := req.Password
	if pwd == "" {
		rb := make([]byte, 6)
		_, _ = rand.Read(rb)
		pwd = hex.EncodeToString(rb)
	}
	hash, _ := auth.HashPassword(pwd)
	idBytes := make([]byte, 6)
	_, _ = rand.Read(idBytes)
	id := "adm-" + hex.EncodeToString(idBytes)
	if err := h.St.AdminInsert(r.Context(), store.Admin{
		ID: id, Username: req.Username, Email: req.Email, PasswordHash: hash,
		Role: role, CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		respond.Fail(w, http.StatusConflict, "conflict", "用户名或邮箱已存在")
		return
	}
	h.audit(r, "admin.create", req.Username, map[string]any{"role": role})
	body := map[string]any{"success": true, "id": id}
	if req.Password == "" {
		body["temp_password"] = pwd
	}
	respond.OKRaw(w, body)
}

type adminPatchReq struct {
	Role  string `json:"role"`
	Email string `json:"email"`
}

func (h *Handler) adminsPatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req adminPatchReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	target, err := h.St.AdminByID(r.Context(), id)
	if err != nil {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
		return
	}
	if req.Role != "" {
		if req.Role != "super_admin" && req.Role != "admin" && req.Role != "readonly" {
			respond.Fail(w, http.StatusBadRequest, "bad_role", "")
			return
		}
		if target.Role == "super_admin" && req.Role != "super_admin" {
			n, _ := h.St.AdminCountSuper(r.Context())
			if n <= 1 {
				respond.Fail(w, http.StatusConflict, "last_super_admin", "至少保留一名超级管理员")
				return
			}
		}
		_ = h.St.AdminUpdateRole(r.Context(), id, req.Role)
	}
	if req.Email != "" {
		_ = h.St.AdminUpdateEmail(r.Context(), id, req.Email)
	}
	h.audit(r, "admin.role.update", target.Username, map[string]any{"role": req.Role})
	respond.OK(w, nil)
}

func (h *Handler) adminsDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := h.St.AdminByID(r.Context(), id)
	if err != nil {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
		return
	}
	me := middleware.AdminFrom(r.Context())
	if me != nil && me.ID == id {
		respond.Fail(w, http.StatusConflict, "cannot_delete_self", "不能删除自己")
		return
	}
	if target.Role == "super_admin" {
		n, _ := h.St.AdminCountSuper(r.Context())
		if n <= 1 {
			respond.Fail(w, http.StatusConflict, "last_super_admin", "不能删除最后一个超级管理员")
			return
		}
	}
	_ = h.St.AdminDelete(r.Context(), id)
	h.audit(r, "admin.remove", target.Username, nil)
	respond.OK(w, nil)
}

func (h *Handler) adminsResetPwd(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := h.St.AdminByID(r.Context(), id)
	if err != nil {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
		return
	}
	rb := make([]byte, 6)
	_, _ = rand.Read(rb)
	pwd := hex.EncodeToString(rb)
	hash, _ := auth.HashPassword(pwd)
	_ = h.St.AdminUpdatePassword(r.Context(), id, hash)
	_ = h.St.AdminSessionDeleteByAdmin(r.Context(), id)
	h.audit(r, "admin.reset_pwd", target.Username, nil)
	respond.OKRaw(w, map[string]any{"success": true, "temp_password": pwd})
}

// ── /admin/state 聚合(供面板一次拉取)─────────────────────────────

func (h *Handler) aggregateState(w http.ResponseWriter, r *http.Request) {
	me := middleware.AdminFrom(r.Context())
	srv := serverStatusCfg{Status: "ok"}
	_, _ = h.St.ConfigGet(r.Context(), "server", &srv)
	dl := featuresCfg{OnlineEnabled: true, OfflineEnabled: true, AccountEnabled: true}
	_, _ = h.St.ConfigGet(r.Context(), "features", &dl)
	versions := versionsCfg{AllowedVersions: []string{}}
	_, _ = h.St.ConfigGet(r.Context(), "versions", &versions)
	captcha := defaultCaptcha()
	_, _ = h.St.ConfigGet(r.Context(), "captcha", &captcha)
	unverifiedPurge := defaultUnverifiedPurge()
	_, _ = h.St.ConfigGet(r.Context(), "unverified_purge", &unverifiedPurge)
	var svc servicesCfg
	_, _ = h.St.ConfigGet(r.Context(), "services", &svc)
	if svc.ProxyBackends == nil {
		svc.ProxyBackends = []string{}
	}
	activeBans, _ := h.St.BanListActive(r.Context())
	banHistory, _ := h.St.BanListHistory(r.Context())
	accs, total, _ := h.St.AccountList(r.Context(), "", "", 0, 50)
	rows, _, _ := h.St.AuditList(r.Context(), "", "", 0, 0, 100)
	admins, _ := h.St.AdminListAll(r.Context())

	respond.OKRaw(w, map[string]any{
		"success": true,
		"current_admin": func() any {
			if me == nil {
				return nil
			}
			return map[string]any{
				"id": me.ID, "username": me.Username, "email": me.Email, "role": me.Role,
			}
		}(),
		"server":           map[string]any{"status": srv.Status, "maintenance_message": srv.MaintenanceMessage, "estimated_end": srv.EstimatedEnd},
		"download":         dl,
		"versions":         versions,
		"services":         svc,
		"captcha":          captcha,
		"autoban":          autoban.Load(r.Context(), h.St),
		"unverified_purge": map[string]any{"enabled": unverifiedPurge.Enabled, "retain_days": unverifiedPurge.RetainDays, "scan_sec": unverifiedPurge.ScanSec},
		"limits":           limitsState(h.Limits),
		"active_bans":      mapBans(activeBans),
		"ban_history":      mapBans(banHistory),
		"accounts":         mapAccounts(accs),
		"accounts_total":   total,
		"audit":            mapAuditRows(rows),
		"admins":           mapAdmins(admins),
	})
}

// ── 工具 ────────────────────────────────────────────────────────────

func isSafeHTTPURL(s string) bool {
	if s == "" || len(s) > 2048 {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func csvEsc(s string) string {
	// 防 CSV 公式注入(Excel/Sheets):以 = + - @ Tab CR 开头的字段会被当成公式执行,
	// 前置一个单引号让其降级为纯文本。参考 OWASP CSV Injection。
	if s != "" {
		switch s[0] {
		case '=', '+', '-', '@', '\t', '\r':
			s = "'" + s
		}
	}
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func mapAccount(a *store.Account) map[string]any {
	return map[string]any{
		"id": a.ID, "username": a.Username, "email": a.Email,
		"status": a.Status, "created_at": a.CreatedAt,
		"last_login_at": a.LastLoginAt, "login_count": a.LoginCount,
	}
}

func mapAccounts(list []store.Account) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, mapAccount(&list[i]))
	}
	return out
}

func mapBans(list []store.Ban) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, b := range list {
		out = append(out, map[string]any{
			"id": b.ID, "device_id": b.DeviceID, "reason": b.Reason,
			"issued_at": b.IssuedAt, "expire_time": b.ExpireTime,
			"is_permanent": b.ExpireTime == nil,
			"issued_by":    b.IssuedBy, "auto": b.Auto,
			"lifted_at": b.LiftedAt, "lifted_by": b.LiftedBy, "active": b.Active,
		})
	}
	return out
}

func mapAuditRows(list []store.AuditEntry) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		var det any
		_ = json.Unmarshal(e.Details, &det)
		out = append(out, map[string]any{
			"id": e.ID, "ts": e.Ts, "actor": e.Actor, "type": e.Type,
			"target": e.Target, "details": det,
		})
	}
	return out
}

func mapAdmins(list []store.Admin) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, map[string]any{
			"id": a.ID, "username": a.Username, "email": a.Email,
			"role": a.Role, "created_at": a.CreatedAt, "last_login_at": a.LastLoginAt,
		})
	}
	return out
}

// 计算 hex 串前导零位数(给 captchaTest 自检用)。
func leadingZeroBits(hexStr string) int {
	n := 0
	for i := 0; i+1 < len(hexStr); i += 2 {
		var b byte
		for j := 0; j < 2; j++ {
			c := hexStr[i+j]
			var v byte
			switch {
			case c >= '0' && c <= '9':
				v = c - '0'
			case c >= 'a' && c <= 'f':
				v = c - 'a' + 10
			case c >= 'A' && c <= 'F':
				v = c - 'A' + 10
			}
			b = b*16 + v
		}
		if b == 0 {
			n += 8
			continue
		}
		// 计算 b 的前导零位
		for mask := byte(0x80); mask > 0; mask >>= 1 {
			if b&mask != 0 {
				return n
			}
			n++
		}
		break
	}
	return n
}

// SHA-256 hex 字符串。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
