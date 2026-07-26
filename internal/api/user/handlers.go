// Package user 实现 /user/api/* 用户中心接口。
// 全部走 RequireAccount 中间件,玩家用 mr_session cookie 或 Bearer token 鉴权。
package user

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/cnv-server/internal/api/respond"
	"magirecocn-revival/cnv-server/internal/auth"
	"magirecocn-revival/cnv-server/internal/middleware"
	"magirecocn-revival/cnv-server/internal/store"
)

var emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type Handler struct {
	St *store.Store
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/profile", h.profile)
	r.Get("/devices", h.devices)
	r.Delete("/devices/{token}", h.kickDevice)
	r.Get("/saves", h.saves)
	r.Delete("/saves", h.deleteSaves)
	r.Put("/email", h.changeEmail)
	r.Put("/password", h.changePassword)
	r.Delete("/account", h.deleteAccount)
}

// GET /profile
func (h *Handler) profile(w http.ResponseWriter, r *http.Request) {
	acc := middleware.AccountFrom(r.Context())
	_, _, sz, _ := h.St.SaveGet(r.Context(), acc.ID)
	respond.OKRaw(w, map[string]any{
		"success":     true,
		"email":       acc.Email,
		"username":    acc.Username,
		"created_at":  acc.CreatedAt,
		"login_count": acc.LoginCount,
		"save_size":   sz,
	})
}

// GET /devices
func (h *Handler) devices(w http.ResponseWriter, r *http.Request) {
	acc := middleware.AccountFrom(r.Context())
	current := middleware.AccountTokenFrom(r.Context())
	sess, err := h.St.AccountSessionListByAccount(r.Context(), acc.ID)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	out := make([]map[string]any, 0, len(sess))
	for _, s := range sess {
		h2 := sha256.Sum256([]byte(s.Token))
		fingerprint := hex.EncodeToString(h2[:])[:40]
		out = append(out, map[string]any{
			"id":            s.Token,
			"device_id":     fingerprint,
			"name":          fallback(s.DeviceName, "未知设备"),
			"os":            s.OS,
			"ip":            s.IP,
			"region":        fallback(s.Region, "未知"),
			"current":       s.Token == current,
			"last_login_at": s.LastSeenAt,
		})
	}
	respond.OKRaw(w, map[string]any{"success": true, "devices": out})
}

// DELETE /devices/:token
func (h *Handler) kickDevice(w http.ResponseWriter, r *http.Request) {
	acc := middleware.AccountFrom(r.Context())
	current := middleware.AccountTokenFrom(r.Context())
	token := chi.URLParam(r, "token")
	if token == current {
		respond.Fail(w, http.StatusBadRequest, "cannot_kick_self", "不能踢下线当前设备")
		return
	}
	if !auth.IsWellFormedToken(token) {
		respond.Fail(w, http.StatusBadRequest, "bad_token", "令牌格式不合法")
		return
	}
	// 仅允许踢自己账号下的 session
	sess, _ := h.St.AccountSessionListByAccount(r.Context(), acc.ID)
	hit := false
	for _, s := range sess {
		if s.Token == token {
			hit = true
			break
		}
	}
	if !hit {
		respond.Fail(w, http.StatusNotFound, "not_found", "设备会话不存在")
		return
	}
	_ = h.St.AccountSessionDelete(r.Context(), token)
	respond.OK(w, nil)
}

// GET /saves
func (h *Handler) saves(w http.ResponseWriter, r *http.Request) {
	acc := middleware.AccountFrom(r.Context())
	raw, updated, size, _ := h.St.SaveGet(r.Context(), acc.ID)
	var endpointCount int
	if len(raw) > 0 {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			endpointCount = len(m)
		}
	}
	saves := []any{}
	if endpointCount > 0 {
		saves = append(saves, map[string]any{
			"id":             "save_" + acc.ID,
			"name":           "云存档",
			"endpoint_count": endpointCount,
			"size_bytes":     size,
			"updated_at":     updated,
		})
	}
	respond.OKRaw(w, map[string]any{"success": true, "saves": saves})
}

// DELETE /saves
func (h *Handler) deleteSaves(w http.ResponseWriter, r *http.Request) {
	acc := middleware.AccountFrom(r.Context())
	_ = h.St.SaveDelete(r.Context(), acc.ID)
	_ = h.St.AuditInsert(r.Context(), store.AuditEntry{
		ID: newAuditID(), Ts: time.Now().UnixMilli(),
		Actor: "system", Type: "save.delete", Target: &acc.ID,
	})
	respond.OK(w, nil)
}

// PUT /email
type changeEmailReq struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Password string `json:"password"` // 当前密码,二次确认身份
}

func (h *Handler) changeEmail(w http.ResponseWriter, r *http.Request) {
	var req changeEmailReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	acc := middleware.AccountFrom(r.Context())
	if !emailRE.MatchString(req.Email) {
		respond.Fail(w, http.StatusBadRequest, "bad_email", "邮箱格式不正确")
		return
	}
	if req.Email == acc.Email {
		respond.Fail(w, http.StatusBadRequest, "same_email", "新邮箱与当前邮箱相同")
		return
	}
	// 换邮箱直接影响账号找回入口,要求重输当前密码,防止会话被盗后被直接改邮箱锁号。
	if len(req.Password) > 256 || !auth.VerifyPassword(req.Password, acc.PasswordHash) {
		respond.Fail(w, http.StatusUnauthorized, "wrong_password", "需要重输当前密码")
		return
	}
	ok, _ := h.St.EmailCodeCheck(r.Context(), req.Email, req.Code, "change_email", true)
	if !ok {
		respond.Fail(w, http.StatusBadRequest, "bad_code", "验证码无效或已过期")
		return
	}
	if other, _ := h.St.AccountByEmail(r.Context(), req.Email); other != nil && other.ID != "" && other.ID != acc.ID {
		respond.Fail(w, http.StatusConflict, "email_taken", "该邮箱已被占用")
		return
	}
	_ = h.St.AccountUpdateEmail(r.Context(), acc.ID, req.Email)
	_ = h.St.AuditInsert(r.Context(), store.AuditEntry{
		ID: newAuditID(), Ts: time.Now().UnixMilli(),
		Actor: "system", Type: "account.change_email", Target: &acc.ID,
		Details: mustJSON(map[string]string{"email": req.Email}),
	})
	respond.OK(w, nil)
}

// PUT /password
type changePwdReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request) {
	var req changePwdReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if len(req.OldPassword) > 256 {
		respond.Fail(w, http.StatusBadRequest, "password_too_long", "原密码过长")
		return
	}
	if len(req.NewPassword) < 8 || len(req.NewPassword) > 256 {
		respond.Fail(w, http.StatusBadRequest, "weak_password", "新密码长度不合法")
		return
	}
	acc := middleware.AccountFrom(r.Context())
	if !auth.VerifyPassword(req.OldPassword, acc.PasswordHash) {
		respond.Fail(w, http.StatusUnauthorized, "wrong_password", "原密码错误")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		respond.Fail(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	_ = h.St.AccountUpdatePassword(r.Context(), acc.ID, hash)
	current := middleware.AccountTokenFrom(r.Context())
	_ = h.St.AccountSessionDeleteAllExcept(r.Context(), acc.ID, current)
	_ = h.St.AuditInsert(r.Context(), store.AuditEntry{
		ID: newAuditID(), Ts: time.Now().UnixMilli(),
		Actor: "system", Type: "account.change_pwd", Target: &acc.ID,
	})
	respond.OK(w, nil)
}

// DELETE /account
type deleteAccountReq struct {
	Password string `json:"password"`
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var req deleteAccountReq
	if !respond.ReadJSONAllowUnknown(w, r, &req) {
		return
	}
	if len(req.Password) > 256 {
		respond.Fail(w, http.StatusBadRequest, "password_too_long", "")
		return
	}
	acc := middleware.AccountFrom(r.Context())
	if !auth.VerifyPassword(req.Password, acc.PasswordHash) {
		respond.Fail(w, http.StatusUnauthorized, "wrong_password", "需要重输当前密码才能注销")
		return
	}
	_ = h.St.AccountDelete(r.Context(), acc.ID)
	_ = h.St.AuditInsert(r.Context(), store.AuditEntry{
		ID: newAuditID(), Ts: time.Now().UnixMilli(),
		Actor: "system", Type: "account.delete_self", Target: &acc.ID,
	})
	respond.OK(w, nil)
}

func fallback(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func newAuditID() string {
	t, _ := auth.NewToken()
	return "log_" + t[:24]
}
