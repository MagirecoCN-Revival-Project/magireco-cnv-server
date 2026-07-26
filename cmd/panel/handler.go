package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/cnv-server/internal/auth"
	"magirecocn-revival/cnv-server/internal/panelstore"
)

// Handler 挂载面板管理 API。
type Handler struct {
	ps  *panelstore.Store
	mgr *Manager
	key []byte        // cookie HMAC 签名密钥
	ttl time.Duration // 会话有效期
	log interface{ Info(string, ...any) }
}

// Routes 注册到 chi router 的 /api/panel 子树。
//
// 注：面板首个 super_admin 的创建已由 /install 安装模块（WordPress 式向导）负责，
// 安装完成后该模块即从服务移除；这里不再暴露 setup 路由。
func (h *Handler) Routes(r chi.Router) {
	r.Post("/auth/login", h.login)
	r.Post("/auth/logout", h.requireSession(h.logout))

	// 需要鉴权的路由
	r.Get("/me", h.requireSession(h.me))
	r.Get("/nodes", h.requireSession(h.listNodes))
	r.Post("/nodes", h.requireSession(h.addNode))
	r.Put("/nodes/{id}", h.requireSession(h.updateNode))
	r.Delete("/nodes/{id}", h.requireSession(h.deleteNode))
	r.Get("/nodes/{id}/status", h.requireSession(h.nodeStatus))
	r.Post("/nodes/{id}/cmd", h.requireSession(h.nodeCmd))
}

// statusPage 返回内置的轻量状态 HTML 页。
func (h *Handler) statusPage(w http.ResponseWriter, r *http.Request) {
	statuses := h.mgr.NodeStatuses()
	rows := strings.Builder{}
	for _, s := range statuses {
		online := "❌ 离线"
		if s.Online {
			online = "✅ 在线"
		}
		rows.WriteString(fmt.Sprintf(
			"<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%.1f%%</td><td>%ds</td></tr>\n",
			s.ID, s.Label, s.Role, s.Region, online,
			s.Telemetry.MemPct, s.Telemetry.UptimeSec))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="zh"><head><meta charset="utf-8"><title>魔法纪录 · 面板</title>
<style>body{font-family:sans-serif;padding:2rem}table{border-collapse:collapse;width:100%%}
th,td{border:1px solid #ccc;padding:6px 12px;text-align:left}th{background:#f0f0f0}</style>
</head><body>
<h1>MagirecoCN Revival · 管理面板</h1>
<h2>节点状态</h2>
<table><tr><th>ID</th><th>标签</th><th>角色</th><th>区域</th><th>状态</th><th>内存</th><th>运行时间</th></tr>
%s</table>
<p><small>面板 API：<code>/api/panel/</code>　节点数：%d</small></p>
</body></html>`, rows.String(), len(statuses))
}

// ─── 认证 ────────────────────────────────────────────────────────────────────

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式错误")
		return
	}
	a, err := h.ps.AdminByEmail(ctx, req.Email)
	if err != nil {
		// 等量计算避免枚举
		_ = auth.VerifyPassword(req.Password, "scrypt$32768$8$1$deadbeef$deadbeef")
		writeErr(w, http.StatusUnauthorized, "unauthorized", "邮箱或密码错误")
		return
	}
	if !auth.VerifyPassword(req.Password, a.PasswordHash) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "邮箱或密码错误")
		return
	}
	token := randHex(16)
	sess := panelstore.Session{
		Token:     token,
		AdminID:   a.ID,
		ExpiresAt: time.Now().Add(h.ttl).UnixMilli(),
	}
	if err := h.ps.SessionInsert(ctx, sess); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	signed := signToken(token, h.key)
	http.SetCookie(w, &http.Cookie{
		Name:     "panel_session",
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.ttl.Seconds()),
	})
	writeOK(w, map[string]string{"token": signed})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if sess := sessionFrom(r); sess != nil {
		_ = h.ps.SessionDeleteByAdmin(r.Context(), sess.AdminID)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   "panel_session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	writeOK(w, nil)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	a := adminFrom(r)
	if a == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	writeOK(w, a)
}

// ─── 节点 CRUD ────────────────────────────────────────────────────────────────

func (h *Handler) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.ps.NodeList(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, nodes)
}

type nodeReq struct {
	Label   string `json:"label"`
	Role    string `json:"role"`
	APIURL  string `json:"api_url"`
	CtrlURL string `json:"ctrl_url"`
	Key     string `json:"key"`
	Region  string `json:"region"`
}

func (h *Handler) addNode(w http.ResponseWriter, r *http.Request) {
	var req nodeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式错误")
		return
	}
	if req.APIURL == "" || req.CtrlURL == "" || req.Key == "" {
		writeErr(w, http.StatusBadRequest, "validation", "api_url / ctrl_url / key 为必填")
		return
	}
	if req.Role == "" {
		req.Role = "business"
	}
	n := panelstore.Node{
		ID:      "node_" + randHex(8),
		Label:   req.Label,
		Role:    req.Role,
		APIURL:  req.APIURL,
		CtrlURL: req.CtrlURL,
		Key:     req.Key,
		Region:  req.Region,
	}
	if err := h.ps.NodeInsert(r.Context(), n); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, n)
}

func (h *Handler) updateNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req nodeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式错误")
		return
	}
	n := panelstore.Node{
		ID:      id,
		Label:   req.Label,
		Role:    req.Role,
		APIURL:  req.APIURL,
		CtrlURL: req.CtrlURL,
		Key:     req.Key,
		Region:  req.Region,
	}
	if err := h.ps.NodeUpdate(r.Context(), n); err != nil {
		if errors.Is(err, panelstore.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "节点不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, n)
}

func (h *Handler) deleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.ps.NodeDelete(r.Context(), id); err != nil {
		if errors.Is(err, panelstore.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "节点不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeOK(w, nil)
}

func (h *Handler) nodeStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	statuses := h.mgr.NodeStatuses()
	for _, s := range statuses {
		if s.ID == id {
			writeOK(w, s)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "not_found", "节点不存在")
}

type nodeCmdReq struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
}

func (h *Handler) nodeCmd(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req nodeCmdReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式错误")
		return
	}
	if req.Action == "" {
		writeErr(w, http.StatusBadRequest, "validation", "action 不能为空")
		return
	}
	raw, err := h.mgr.Call(r.Context(), id, req.Action, req.Payload)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errNodeOffline) {
			code = http.StatusServiceUnavailable
		}
		writeErr(w, code, "cmd_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// ─── 会话中间件 ──────────────────────────────────────────────────────────────

type ctxKeySession struct{}
type ctxKeyAdmin struct{}

func (h *Handler) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		if token == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "缺少会话 token")
			return
		}
		rawToken, ok := verifyToken(token, h.key)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "token 无效")
			return
		}
		ctx := r.Context()
		sess, err := h.ps.SessionByToken(ctx, rawToken)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "会话不存在或已过期")
			return
		}
		if sess.ExpiresAt < time.Now().UnixMilli() {
			_ = h.ps.SessionDeleteByAdmin(ctx, sess.AdminID)
			writeErr(w, http.StatusUnauthorized, "session_expired", "会话已过期")
			return
		}
		a, err := h.ps.AdminByID(ctx, sess.AdminID)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "管理员不存在")
			return
		}
		ctx2 := context.WithValue(ctx, ctxKeySession{}, sess)
		ctx2 = context.WithValue(ctx2, ctxKeyAdmin{}, a)
		next(w, r.WithContext(ctx2))
	}
}

func sessionFrom(r *http.Request) *panelstore.Session {
	v, _ := r.Context().Value(ctxKeySession{}).(*panelstore.Session)
	return v
}

func adminFrom(r *http.Request) *panelstore.Admin {
	v, _ := r.Context().Value(ctxKeyAdmin{}).(*panelstore.Admin)
	return v
}

func extractToken(r *http.Request) string {
	// 优先 Authorization: Bearer <token>
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		return strings.TrimPrefix(ah, "Bearer ")
	}
	// 其次 cookie
	if c, err := r.Cookie("panel_session"); err == nil {
		return c.Value
	}
	return ""
}

// signToken 在 token 后附上 HMAC 以防篡改。格式：<token>.<hmac8hex>
func signToken(token string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	sig := hex.EncodeToString(mac.Sum(nil))[:16]
	return token + "." + sig
}

func verifyToken(signed string, key []byte) (raw string, ok bool) {
	dot := strings.LastIndex(signed, ".")
	if dot < 0 {
		return "", false
	}
	raw = signed[:dot]
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(raw))
	expected := hex.EncodeToString(mac.Sum(nil))[:16]
	return raw, hmac.Equal([]byte(signed[dot+1:]), []byte(expected))
}

// ─── 工具 ────────────────────────────────────────────────────────────────────

func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]any{"ok": true, "data": data})
	w.Write(b)
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]any{"ok": false, "error": errCode, "message": msg})
	w.Write(b)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
