// Package account 实现 /account/* 与 /auth/* 玩家账号相关接口。
package account

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/api-server/internal/api/respond"
	"magirecocn-revival/api-server/internal/auth"
	"magirecocn-revival/api-server/internal/autoban"
	"magirecocn-revival/api-server/internal/capworker"
	"magirecocn-revival/api-server/internal/email"
	"magirecocn-revival/api-server/internal/middleware"
	"magirecocn-revival/api-server/internal/store"
)

var emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// Middleware 是 chi 兼容的中间件签名;由 main 注入限流器。nil 表示不限流。
type Middleware = func(http.Handler) http.Handler

type Handler struct {
	St                *store.Store
	Cap               *capworker.Service
	CaptchaEnabled    bool
	ClientSessionTTL  time.Duration
	AccountSessionTTL time.Duration
	AdminSessionTTL   time.Duration
	WebDir            string

	// PanelPublicURL 非空时,客户端入口页(register/forgot/verify-email)302 跳转到面板托管的同名页;
	// 留空时回落到本地 WebDir 静态文件(保持 §0 客户端契约不破)。
	PanelPublicURL string

	// SaveLimiter:单会话(per account_token)上传云存档的限速器,
	// 由 main 注入。nil = 不限速。每次 savePut 在校验完 token 后直接
	// Allow(token);存档 token 是 64 字符 hex,直接做 map 键,无 hash 成本。
	SaveLimiter *middleware.Limiter

	// 限流中间件(由 main 注入,缺省 nil = 不限流)
	LoginLimiter Middleware // 登录(/account/login、/auth/login)
	AuthLimiter  Middleware // 注册 / 找回 等写操作
	CodeLimiter  Middleware // 邮箱验证码发送/校验

	// Mailer SMTP 发送器。nil 或未配置时降级为开发模式日志。
	Mailer *email.Sender

	// AutoBan 自动封禁判定器(可空 = 不启用)。
	AutoBan *autoban.Service
}

// with 在 mw 非 nil 时套一层,否则原样返回 handler。
func with(mw Middleware, h http.HandlerFunc) http.HandlerFunc {
	if mw == nil {
		return h
	}
	wrapped := mw(h)
	return wrapped.ServeHTTP
}

// Routes /account/*
func (h *Handler) Routes(r chi.Router) {
	r.Post("/login", with(h.LoginLimiter, h.login))
	r.Post("/save/put", h.savePut)
	r.Post("/save/get", h.saveGet)
	r.Get("/register", h.staticPage("register.html"))
	r.Get("/forgot", h.staticPage("forgot.html"))
	r.Get("/verify-email", h.staticPage("verify-email.html"))
}

// AuthRoutes /auth/*
func (h *Handler) AuthRoutes(r chi.Router) {
	r.Post("/login", with(h.LoginLimiter, h.authLogin))
	r.Post("/register", with(h.AuthLimiter, h.authRegister))
	r.Post("/logout", h.authLogout)
	r.Post("/send-code", with(h.CodeLimiter, h.sendCode))
	r.Post("/verify-code", with(h.CodeLimiter, h.verifyCode))
	r.Post("/forgot", with(h.AuthLimiter, h.authForgot))
}

// ── /account/login(游戏客户端登录)──────────────────────────────────────

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	CapToken string `json:"cap_token"`
	DeviceID string `json:"device_id"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.Username == "" || req.Password == "" {
		respond.Fail(w, http.StatusBadRequest, "missing_credentials", "请填写用户名和密码")
		return
	}
	if len(req.Password) > 256 {
		respond.Fail(w, http.StatusBadRequest, "password_too_long", "密码过长")
		return
	}
	if h.CaptchaEnabled {
		if req.CapToken == "" {
			respond.Fail(w, http.StatusForbidden, "cap_missing", "未提供 cap_token")
			return
		}
		ok, err := h.Cap.Consume(r.Context(), req.CapToken, true)
		if err != nil || !ok {
			// 自动封禁信号:验证码连续失败累计到阈值即封。
			h.AutoBan.OnCaptchaFail(r.Context(), req.DeviceID)
			respond.Fail(w, http.StatusForbidden, "cap_invalid", "人机验证未通过或已失效")
			return
		}
	}

	// 设备封禁优先
	if req.DeviceID != "" {
		if ban, _ := h.St.BanActive(r.Context(), req.DeviceID); ban != nil {
			respond.FailExtra(w, http.StatusForbidden, "device_banned", "设备已被封禁", map[string]any{
				"ban": banPayload(ban),
			})
			return
		}
	}

	acc, err := h.St.AccountByUsernameOrEmail(r.Context(), req.Username)
	if err != nil {
		auth.DummyVerifyTiming(req.Password)
		respond.Fail(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	if !auth.VerifyPassword(req.Password, acc.PasswordHash) {
		respond.Fail(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	if acc.Status == "disabled" {
		respond.Fail(w, http.StatusForbidden, "account_disabled", "账号已被停用,请联系管理员")
		return
	}
	if !acc.EmailVerified {
		respond.Fail(w, http.StatusForbidden, "email_not_verified", "请先验证邮箱后再登录")
		return
	}

	token, _ := auth.NewToken()
	now := time.Now().UnixMilli()
	ip := middleware.ClientIP(r)
	if err := h.St.AccountSessionInsert(r.Context(), store.AccountSession{
		Token: token, AccountID: acc.ID,
		DeviceName: "游戏客户端", OS: "Android", IP: ip, Region: "未知",
		CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now + h.AccountSessionTTL.Milliseconds(),
	}); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	_ = h.St.AccountTouchLogin(r.Context(), acc.ID)
	_ = h.St.AuditInsert(r.Context(), store.AuditEntry{
		ID: newAuditID(), Ts: now, Actor: "system",
		Type: "account.login", Target: &acc.ID,
		Details: mustJSON(map[string]string{"ip": ip}),
	})
	// 自动封禁信号:单设备短时登录过多不同账号 → 养号/共享。
	h.AutoBan.OnAccountLogin(r.Context(), req.DeviceID, acc.ID)

	respond.OKRaw(w, map[string]any{
		"success":    true,
		"account_id": acc.ID,
		"token":      token,
	})
}

// ── /account/save/put ───────────────────────────────────────────────────

type savePutReq struct {
	Token string         `json:"token"`
	Data  map[string]any `json:"data"`
}

func (h *Handler) savePut(w http.ResponseWriter, r *http.Request) {
	var req savePutReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if !auth.IsWellFormedToken(req.Token) {
		respond.Fail(w, http.StatusUnauthorized, "invalid_token", "存档令牌无效")
		return
	}
	ses, acc, err := h.St.AccountSessionLookup(r.Context(), req.Token)
	if err != nil || ses.ExpiresAt < time.Now().UnixMilli() {
		respond.Fail(w, http.StatusUnauthorized, "invalid_token", "存档令牌无效或已过期")
		return
	}
	if acc.Status == "disabled" {
		respond.Fail(w, http.StatusForbidden, "account_disabled", "")
		return
	}
	if !acc.EmailVerified {
		respond.Fail(w, http.StatusForbidden, "email_not_verified", "请先验证邮箱后才能使用云存档")
		return
	}
	// 单会话上传限速:防大量请求阻塞服务器。检查放在 token 校验之后,
	// 这样攻击者用随机 token 灌不到限流器(他们会先在上面拿 401)。
	// 鉴权过的会话才会消耗配额,正常每分钟 2 次足够覆盖游戏打存档节奏。
	if h.SaveLimiter != nil && !h.SaveLimiter.Allow(req.Token) {
		respond.Fail(w, http.StatusTooManyRequests, "rate_limited",
			"云存档上传过于频繁,请稍后再试")
		return
	}
	// 与 RequireAccount 一致的滑动续期 —— 游戏运行中频繁打存档,
	// 这里也是让"记住登录"持续生效的关键路径。
	h.St.AccountSessionTouch(r.Context(), req.Token, h.AccountSessionTTL)
	data := req.Data
	if data == nil {
		data = map[string]any{}
	}
	raw := mustJSON(data)
	if err := h.St.SavePut(r.Context(), acc.ID, raw); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	respond.OK(w, nil)
}

// ── /account/save/get ───────────────────────────────────────────────────

type tokenOnly struct {
	Token string `json:"token"`
}

func (h *Handler) saveGet(w http.ResponseWriter, r *http.Request) {
	var req tokenOnly
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if !auth.IsWellFormedToken(req.Token) {
		respond.Fail(w, http.StatusUnauthorized, "invalid_token", "")
		return
	}
	ses, acc, err := h.St.AccountSessionLookup(r.Context(), req.Token)
	if err != nil || ses.ExpiresAt < time.Now().UnixMilli() {
		respond.Fail(w, http.StatusUnauthorized, "invalid_token", "")
		return
	}
	if !acc.EmailVerified {
		respond.Fail(w, http.StatusForbidden, "email_not_verified", "请先验证邮箱后才能使用云存档")
		return
	}
	// 滑动续期(同 savePut):活跃玩家的"记住登录"不会因为只读存档而过期。
	h.St.AccountSessionTouch(r.Context(), req.Token, h.AccountSessionTTL)
	raw, _, _, _ := h.St.SaveGet(r.Context(), acc.ID)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	// 拼一份顶层 JSON 不重新解析 raw
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"success":true,"data":`))
	w.Write(raw)
	w.Write([]byte(`}`))
}

// staticPage 返回客户端入口网页。
//
// 接入面板(PanelPublicURL 非空)时,这些人类可见页面已由面板统一托管,
// 节点改为 302 跳转到面板的同名页——客户端 APK 内置浏览器跟随重定向即可,
// /account/register、/account/forgot 等契约路由仍然有效(§0 不破)。
// 未接入面板时回落到本地静态文件,保持单机零依赖可用。
func (h *Handler) staticPage(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.PanelPublicURL != "" {
			http.Redirect(w, r, h.PanelPublicURL+"/"+name, http.StatusFound)
			return
		}
		http.ServeFile(w, r, filepath.Join(h.WebDir, name))
	}
}

// ── /auth/login(面板登录)───────────────────────────────────────────────

type authLoginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	CapToken string `json:"cap_token"`
}

func (h *Handler) authLogin(w http.ResponseWriter, r *http.Request) {
	var req authLoginReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if req.Email == "" || req.Password == "" {
		respond.Fail(w, http.StatusBadRequest, "missing_credentials", "请填写邮箱和密码")
		return
	}
	if len(req.Password) > 256 {
		respond.Fail(w, http.StatusBadRequest, "password_too_long", "")
		return
	}
	if h.CaptchaEnabled {
		ok, err := h.Cap.Consume(r.Context(), req.CapToken, true)
		if err != nil || !ok {
			respond.Fail(w, http.StatusForbidden, "cap_invalid", "人机验证未通过或已失效")
			return
		}
	}

	admin, errAdmin := h.St.AdminByEmail(r.Context(), req.Email)
	acc, errAcc := h.St.AccountByEmail(r.Context(), req.Email)

	if errAdmin == nil && admin != nil {
		if !auth.VerifyPassword(req.Password, admin.PasswordHash) {
			respond.Fail(w, http.StatusUnauthorized, "invalid_credentials", "邮箱或密码错误")
			return
		}
		token, _ := auth.NewToken()
		now := time.Now().UnixMilli()
		_ = h.St.AdminSessionInsert(r.Context(), store.AdminSession{
			Token: token, AdminID: admin.ID,
			CreatedAt: now, ExpiresAt: now + h.AdminSessionTTL.Milliseconds(),
		})
		_ = h.St.AdminTouchLogin(r.Context(), admin.ID)
		setSessionCookie(w, r, token, h.AdminSessionTTL)
		respond.OKRaw(w, map[string]any{"success": true, "role": admin.Role, "token": token})
		return
	}

	if errAcc != nil || acc == nil {
		auth.DummyVerifyTiming(req.Password)
		respond.Fail(w, http.StatusUnauthorized, "invalid_credentials", "邮箱或密码错误")
		return
	}
	if !auth.VerifyPassword(req.Password, acc.PasswordHash) {
		respond.Fail(w, http.StatusUnauthorized, "invalid_credentials", "邮箱或密码错误")
		return
	}
	if acc.Status == "disabled" {
		respond.Fail(w, http.StatusForbidden, "account_disabled", "账号已被停用")
		return
	}
	token, _ := auth.NewToken()
	now := time.Now().UnixMilli()
	ip := middleware.ClientIP(r)
	_ = h.St.AccountSessionInsert(r.Context(), store.AccountSession{
		Token: token, AccountID: acc.ID,
		DeviceName: "网页登录", OS: "Web", IP: ip, Region: "未知",
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now + h.AccountSessionTTL.Milliseconds(),
	})
	_ = h.St.AccountTouchLogin(r.Context(), acc.ID)
	setSessionCookie(w, r, token, h.AccountSessionTTL)
	respond.OKRaw(w, map[string]any{"success": true, "role": "user", "token": token})
}

// ── /auth/register(玩家自助注册)────────────────────────────────────────
// 新流程:先注册(不要求邮箱验证码),注册成功后服务端发送验证邮件。
// 未验证账号无法使用游戏客户端登录和云存档功能。

type authRegReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	CapToken string `json:"cap_token"`
}

func (h *Handler) authRegister(w http.ResponseWriter, r *http.Request) {
	var req authRegReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if !emailRE.MatchString(req.Email) {
		respond.Fail(w, http.StatusBadRequest, "bad_email", "邮箱格式不正确")
		return
	}
	if len(req.Password) < 8 {
		respond.Fail(w, http.StatusBadRequest, "weak_password", "密码至少 8 位")
		return
	}
	if len(req.Password) > 256 {
		respond.Fail(w, http.StatusBadRequest, "password_too_long", "")
		return
	}
	if h.CaptchaEnabled {
		ok, err := h.Cap.Consume(r.Context(), req.CapToken, true)
		if err != nil || !ok {
			respond.Fail(w, http.StatusForbidden, "cap_invalid", "")
			return
		}
	}
	if a, _ := h.St.AccountByEmail(r.Context(), req.Email); a != nil && a.ID != "" {
		respond.Fail(w, http.StatusConflict, "email_taken", "该邮箱已注册")
		return
	}

	username := strings.SplitN(req.Email, "@", 2)[0]
	for i := 0; i < 5; i++ {
		if a, _ := h.St.AccountByUsernameOrEmail(r.Context(), username); a == nil || a.ID == "" {
			break
		}
		rb := make([]byte, 2)
		_, _ = rand.Read(rb)
		username = strings.SplitN(req.Email, "@", 2)[0] + "_" + hex.EncodeToString(rb)
	}
	idBytes := make([]byte, 6)
	_, _ = rand.Read(idBytes)
	id := "acc-" + hex.EncodeToString(idBytes)

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	now := time.Now().UnixMilli()
	if err := h.St.AccountInsert(r.Context(), store.Account{
		ID: id, Username: username, Email: req.Email, PasswordHash: hash,
		Status: "active", EmailVerified: false, CreatedAt: now, LoginCount: 1,
	}); err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	_ = h.St.AccountTouchLogin(r.Context(), id)
	_ = h.St.AuditInsert(r.Context(), store.AuditEntry{
		ID: newAuditID(), Ts: now, Actor: "system",
		Type: "account.create", Target: &id,
		Details: mustJSON(map[string]string{"email": req.Email}),
	})
	token, _ := auth.NewToken()
	_ = h.St.AccountSessionInsert(r.Context(), store.AccountSession{
		Token: token, AccountID: id,
		DeviceName: "网页注册", OS: "Web", IP: middleware.ClientIP(r), Region: "未知",
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now + h.AccountSessionTTL.Milliseconds(),
	})
	// 发送邮箱验证邮件(注册后验证模式)
	h.issueAndSendCode(r, req.Email, "verify_email")
	setSessionCookie(w, r, token, h.AccountSessionTTL)
	respond.OKRaw(w, map[string]any{"success": true, "role": "user", "token": token, "email_verified": false})
}

func (h *Handler) authLogout(w http.ResponseWriter, r *http.Request) {
	token := tokenFromCookieOrBearer(r)
	if auth.IsWellFormedToken(token) {
		_ = h.St.AdminSessionDelete(r.Context(), token)
		_ = h.St.AccountSessionDelete(r.Context(), token)
	}
	clearSessionCookie(w, r)
	respond.OK(w, nil)
}

// ── /auth/send-code 与 /auth/verify-code ─────────────────────────────────

type sendCodeReq struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose"`
}

func (h *Handler) sendCode(w http.ResponseWriter, r *http.Request) {
	var req sendCodeReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if !emailRE.MatchString(req.Email) {
		respond.Fail(w, http.StatusBadRequest, "bad_email", "邮箱格式不正确")
		return
	}
	purpose := req.Purpose
	if purpose != "register" && purpose != "change_email" && purpose != "reset" && purpose != "verify_email" {
		purpose = "register"
	}
	if purpose == "register" {
		if a, _ := h.St.AccountByEmail(r.Context(), req.Email); a != nil && a.ID != "" {
			respond.OK(w, nil)
			return
		}
	}
	code := newDigitCode(6)
	_ = h.St.EmailCodeIssue(r.Context(), req.Email, purpose, code, 10*time.Minute)
	h.sendEmailCode(req.Email, purpose, code)
	respond.OK(w, nil)
}

type verifyCodeReq struct {
	Email   string `json:"email"`
	Code    string `json:"code"`
	Purpose string `json:"purpose"`
}

func (h *Handler) verifyCode(w http.ResponseWriter, r *http.Request) {
	var req verifyCodeReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	purpose := req.Purpose
	if purpose == "" {
		purpose = "register"
	}
	// verify_email:消费验证码并将账号标记为已验证。
	if purpose == "verify_email" {
		ok, _ := h.St.EmailCodeCheck(r.Context(), req.Email, req.Code, "verify_email", true)
		if ok {
			_ = h.St.AccountSetEmailVerified(r.Context(), req.Email)
		}
		respond.OKRaw(w, map[string]any{"success": ok})
		return
	}
	ok, _ := h.St.EmailCodeCheck(r.Context(), req.Email, req.Code, purpose, false)
	respond.OKRaw(w, map[string]any{"success": ok})
}

// ── /auth/forgot ─────────────────────────────────────────────────────────

type forgotReq struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
	CapToken    string `json:"cap_token"`
}

func (h *Handler) authForgot(w http.ResponseWriter, r *http.Request) {
	var req forgotReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if !emailRE.MatchString(req.Email) {
		respond.Fail(w, http.StatusBadRequest, "bad_email", "邮箱格式不正确")
		return
	}
	if len(req.NewPassword) < 8 || len(req.NewPassword) > 256 {
		respond.Fail(w, http.StatusBadRequest, "weak_password", "新密码长度不合法")
		return
	}
	if h.CaptchaEnabled {
		ok, err := h.Cap.Consume(r.Context(), req.CapToken, true)
		if err != nil || !ok {
			respond.Fail(w, http.StatusForbidden, "cap_invalid", "")
			return
		}
	}
	ok, _ := h.St.EmailCodeCheck(r.Context(), req.Email, req.Code, "reset", true)
	if !ok {
		respond.Fail(w, http.StatusBadRequest, "bad_code", "验证码无效或已过期")
		return
	}
	admin, _ := h.St.AdminByEmail(r.Context(), req.Email)
	acc, _ := h.St.AccountByEmail(r.Context(), req.Email)
	if (admin == nil || admin.ID == "") && (acc == nil || acc.ID == "") {
		respond.OK(w, nil)
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	if admin != nil && admin.ID != "" {
		_ = h.St.AdminUpdatePassword(r.Context(), admin.ID, hash)
		_ = h.St.AdminSessionDeleteByAdmin(r.Context(), admin.ID)
	}
	if acc != nil && acc.ID != "" {
		_ = h.St.AccountUpdatePassword(r.Context(), acc.ID, hash)
		_ = h.St.AccountSessionDeleteByAccount(r.Context(), acc.ID)
	}
	respond.OK(w, nil)
}

// ── 内部工具 ────────────────────────────────────────────────────────────

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: "mr_session", Value: token, Path: "/",
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
		MaxAge: int(ttl.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: "mr_session", Value: "", Path: "/",
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
		MaxAge: -1,
	})
}

func tokenFromCookieOrBearer(r *http.Request) string {
	if c, err := r.Cookie("mr_session"); err == nil && c.Value != "" {
		return c.Value
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

func newDigitCode(n int) string {
	buf := make([]byte, n)
	bin := make([]byte, n*4)
	_, _ = rand.Read(bin)
	for i := 0; i < n; i++ {
		buf[i] = '0' + (bin[i] % 10)
	}
	return string(buf)
}

func newAuditID() string {
	t, _ := auth.NewToken()
	return "log_" + strings.ToLower(t[:24])
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func banPayload(b *store.Ban) map[string]any {
	return map[string]any{
		"reason":       b.Reason,
		"issued_at":    b.IssuedAt,
		"expire_time":  b.ExpireTime,
		"is_permanent": b.ExpireTime == nil,
	}
}

// sendEmailCode 通过 SMTP 发送验证码邮件;SMTP 未配置时降级为开发模式日志。
func (h *Handler) sendEmailCode(to, purpose, code string) {
	if h.Mailer != nil {
		var err error
		switch purpose {
		case "reset":
			err = h.Mailer.SendPasswordReset(to, code)
		default:
			err = h.Mailer.SendVerification(to, code)
		}
		if err != nil && err.Error() != "SMTP 未配置" {
			slog.Warn("邮件发送失败", "to", to, "purpose", purpose, "err", err)
		}
	}
	// 开发模式:将验证码打印到日志(CNV_EMAIL_DEV_MODE=1)。
	switch strings.ToLower(os.Getenv("CNV_EMAIL_DEV_MODE")) {
	case "1", "true", "on", "yes":
		slog.Info("[email][dev] 验证码", "code", code, "to", to, "purpose", purpose)
	}
}

// issueAndSendCode 生成验证码、存库并发送邮件。在注册后自动触发邮箱验证时使用。
func (h *Handler) issueAndSendCode(r *http.Request, email, purpose string) {
	code := newDigitCode(6)
	_ = h.St.EmailCodeIssue(r.Context(), email, purpose, code, 10*time.Minute)
	h.sendEmailCode(email, purpose, code)
}
