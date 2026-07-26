// Package middleware 集中 HTTP 中间件:Panic 恢复、请求日志、鉴权、限流。
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"magirecocn-revival/cnv-server/internal/api/respond"
	"magirecocn-revival/cnv-server/internal/auth"
	"magirecocn-revival/cnv-server/internal/store"
)

type ctxKey int

const (
	ctxAdmin ctxKey = iota
	ctxAccount
	ctxAccountToken
)

// ── 请求体大小限制 ─────────────────────────────────────────────────────────

// BodyLimit 用 http.MaxBytesReader 给所有请求体设上限,防止超大 body 撑爆内存。
// 默认对全站生效;云存档(/account/save/put)体量较大,调用方可单独放宽。
//
// skipPrefixes 命中的路径不套全局上限——用于热更新包上传等大体量端点,这些端点
// 必须在各自 handler 内**自行**用 http.MaxBytesReader 设置(更大的)边界,否则
// 会失去 body 上限保护。
func BodyLimit(maxBytes int64, skipPrefixes ...string) func(http.Handler) http.Handler {
	return BodyLimitFunc(func() int64 { return maxBytes }, skipPrefixes...)
}

// BodyLimitFunc 同 BodyLimit,但上限由 get() 每次请求动态返回——用于后台运行时可调的
// 全局上限(业务节点把它接到 atomic.Int64,管理员在后台改后即时生效,无需重启)。
func BodyLimitFunc(get func() int64, skipPrefixes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range skipPrefixes {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}
			if r.Body != nil {
				if n := get(); n > 0 {
					r.Body = http.MaxBytesReader(w, r.Body, n)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── Recovery ──────────────────────────────────────────────────────────────

// Recovery 把任何 panic 转为 500 + 结构化日志。
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "method", r.Method, "url", r.URL.Path,
					"recover", rec, "stack", string(debug.Stack()))
				if w.Header().Get("Content-Type") == "" {
					respond.Fail(w, http.StatusInternalServerError, "internal_error", "服务端内部错误")
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ── 请求日志 ──────────────────────────────────────────────────────────────

type loggingRW struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (l *loggingRW) WriteHeader(s int) { l.status = s; l.ResponseWriter.WriteHeader(s) }
func (l *loggingRW) Write(b []byte) (int, error) {
	if l.status == 0 {
		l.status = http.StatusOK
	}
	n, err := l.ResponseWriter.Write(b)
	l.bytes += n
	return n, err
}

// Logger 每次请求一行 INFO。
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingRW{ResponseWriter: w}
		next.ServeHTTP(lw, r)
		slog.Info("http",
			"method", r.Method, "path", r.URL.Path,
			"status", lw.status, "bytes", lw.bytes,
			"dur_ms", time.Since(start).Milliseconds(),
			"ip", clientIP(r))
	})
}

// ── 安全响应头 ────────────────────────────────────────────────────────────

// SecurityHeaders 注入常用安全响应头。HSTS 仅在 TLS 终结于本进程时下发。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=()")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				// 浏览器内 Babel 转译需要 unsafe-eval;React/Babel 走 unpkg CDN(template 自带 SRI)
				"script-src 'self' 'unsafe-inline' 'unsafe-eval' https://unpkg.com; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' data: https://fonts.gstatic.com; "+
				"img-src 'self' data: https:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// ── CORS（面板托管前端、浏览器跨域直连节点 API）─────────────────────────────

// CORS 放行指定来源(面板对外 URL)的跨域请求。
//
// 设计取舍:
//   - allowOrigin 为空时返回直通中间件(同源部署,零副作用)。
//   - 仅当请求 Origin 精确匹配 allowOrigin 时回显放行,不使用通配 "*",避免放行任意站点。
//   - 鉴权走 Authorization: Bearer(localStorage),不依赖跨域 Cookie,故不设
//     Access-Control-Allow-Credentials(也就无需在带凭证时回显具体 Origin 的限制冲突)。
//   - 预检 OPTIONS 直接 204 短路,不进入业务/限流链。
func CORS(allowOrigin string) func(http.Handler) http.Handler {
	allowOrigin = strings.TrimRight(allowOrigin, "/")
	if allowOrigin == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && strings.TrimRight(origin, "/") == allowOrigin {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
				h.Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── 鉴权(玩家 / 管理员)──────────────────────────────────────────────────

// extractToken 优先取 cookie mr_session,然后是 Authorization Bearer。
func extractToken(r *http.Request) string {
	if c, err := r.Cookie("mr_session"); err == nil && c.Value != "" {
		return c.Value
	}
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return ""
}

// RequireAdmin 仅放行有效 admin session。
func RequireAdmin(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if !auth.IsWellFormedToken(token) {
				respond.Fail(w, http.StatusUnauthorized, "unauthorized", "未登录或会话已过期")
				return
			}
			ses, admin, err := st.AdminSessionLookup(r.Context(), token)
			if err != nil || ses.ExpiresAt < time.Now().UnixMilli() {
				if ses != nil {
					_ = st.AdminSessionDelete(r.Context(), token)
				}
				respond.Fail(w, http.StatusUnauthorized, "unauthorized", "未登录或会话已过期")
				return
			}
			ctx := context.WithValue(r.Context(), ctxAdmin, admin)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireWritableAdmin 拒绝 readonly 角色。
func RequireWritableAdmin(st *store.Store) func(http.Handler) http.Handler {
	mid := RequireAdmin(st)
	return func(next http.Handler) http.Handler {
		return mid(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a := AdminFrom(r.Context())
			if a == nil {
				respond.Fail(w, http.StatusUnauthorized, "unauthorized", "未登录")
				return
			}
			if a.Role == "readonly" {
				respond.Fail(w, http.StatusForbidden, "forbidden", "只读账户不可执行写操作")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// RequireSuperAdmin 仅 super_admin。
func RequireSuperAdmin(st *store.Store) func(http.Handler) http.Handler {
	mid := RequireAdmin(st)
	return func(next http.Handler) http.Handler {
		return mid(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a := AdminFrom(r.Context())
			if a == nil || a.Role != "super_admin" {
				respond.Fail(w, http.StatusForbidden, "forbidden", "需要超级管理员权限")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// RequireAccount 玩家会话鉴权。accountTTL 决定滑动续期窗口:每次命中且剩余
// 寿命不足 ttl/2 时,会把 expires_at 重新推到 now+ttl,实现"记住登录"。
// 传 0 时不做续期(仅刷新 last_seen_at)。
func RequireAccount(st *store.Store, accountTTL time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if !auth.IsWellFormedToken(token) {
				respond.Fail(w, http.StatusUnauthorized, "unauthorized", "未登录或会话已过期")
				return
			}
			ses, acc, err := st.AccountSessionLookup(r.Context(), token)
			if err != nil || ses.ExpiresAt < time.Now().UnixMilli() {
				if ses != nil {
					_ = st.AccountSessionDelete(r.Context(), token)
				}
				respond.Fail(w, http.StatusUnauthorized, "unauthorized", "未登录或会话已过期")
				return
			}
			if acc.Status == "disabled" {
				respond.Fail(w, http.StatusForbidden, "account_disabled", "账号已被停用")
				return
			}
			st.AccountSessionTouch(r.Context(), token, accountTTL)
			ctx := context.WithValue(r.Context(), ctxAccount, acc)
			ctx = context.WithValue(ctx, ctxAccountToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AdminFrom 从 ctx 取出已鉴权的 admin(nil 表示中间件未通过)。
func AdminFrom(ctx context.Context) *store.Admin {
	v, _ := ctx.Value(ctxAdmin).(*store.Admin)
	return v
}

// AccountFrom 同 AdminFrom 但取玩家。
func AccountFrom(ctx context.Context) *store.Account {
	v, _ := ctx.Value(ctxAccount).(*store.Account)
	return v
}

// AccountTokenFrom 当前请求的 account_token。
func AccountTokenFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxAccountToken).(string)
	return v
}

// ── 限流 ──────────────────────────────────────────────────────────────────

// Limiter 简易内存令牌桶,key 由 keyFn 提供(IP / email / token 等)。
// 不依赖外部 Redis,单实例足够;多实例水平扩展时改用共享存储。
type Limiter struct {
	name    string
	limit   int
	window  time.Duration
	keyFn   func(*http.Request) string
	msg     string
	mu      sync.Mutex
	buckets map[string]*bucket
	tickIdx int
}

type bucket struct {
	count int
	reset time.Time
}

// NewLimiter 构造一个限流器。limit/window 表示窗内最多 limit 次。
func NewLimiter(name string, limit int, window time.Duration, msg string, keyFn func(*http.Request) string) *Limiter {
	return &Limiter{
		name:    name,
		limit:   limit,
		window:  window,
		keyFn:   keyFn,
		msg:     msg,
		buckets: map[string]*bucket{},
	}
}

// IPKey 按 client ip 限流。
func IPKey(r *http.Request) string { return clientIP(r) }

// Allow 检查 key 在窗口内是否还有配额。返回 true=放行,false=已超限。
// 给 handler 直接调用(例如 /account/save/put 的 token 在 JSON body 内,
// keyFn 不便构造,直接 Allow(token) 即可)。
func (l *Limiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		b = &bucket{reset: now.Add(l.window)}
		l.buckets[key] = b
	}
	b.count++
	// 顺手清理:每 1000 次操作回收过期桶
	l.tickIdx++
	if l.tickIdx%1000 == 0 {
		for k, v := range l.buckets {
			if now.After(v.reset) {
				delete(l.buckets, k)
			}
		}
	}
	return b.count <= l.limit
}

// Middleware 包装为 chi 兼容的 middleware。
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(l.keyFn(r)) {
			respond.Fail(w, http.StatusTooManyRequests, "rate_limited", l.msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── 工具 ──────────────────────────────────────────────────────────────────

// ── 受信任代理(trust proxy)─────────────────────────────────────────────
//
// 默认 **不信任** 任何 X-Forwarded-For / X-Real-IP —— 直接用 TCP RemoteAddr,
// 否则任意客户端都能伪造 IP,绕过限流、污染审计 IP、伪装设备来源。
// 仅当 ConfigureTrustProxy 设置了受信任来源,且本次请求的直连对端命中该来源时,
// 才解析转发头。这与 express 的 trust proxy 语义一致。
//
// 取值(CNV_TRUST_PROXY):
//   - 空 / "off" / "false"     → 不信任,只用 RemoteAddr(默认)
//   - "all" / "true" / "*"     → 信任所有上游(仅在确有可信前置网关时使用)
//   - "loopback"               → 信任 127.0.0.0/8 与 ::1
//   - CIDR 列表(逗号分隔)     → 仅信任列出的网段,如 "10.0.0.0/8,192.168.0.0/16"
type trustProxyConfig struct {
	all  bool
	nets []*net.IPNet
}

var trustProxy trustProxyConfig

// ConfigureTrustProxy 在启动时由 main 调用,解析 CNV_TRUST_PROXY。
func ConfigureTrustProxy(spec string) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	trustProxy = trustProxyConfig{}
	switch spec {
	case "", "off", "false", "0", "none":
		return
	case "all", "true", "1", "*":
		trustProxy.all = true
		return
	}
	add := func(cidr string) {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			trustProxy.nets = append(trustProxy.nets, n)
		}
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		switch part {
		case "":
		case "loopback":
			add("127.0.0.0/8")
			add("::1/128")
		default:
			if strings.Contains(part, "/") {
				add(part)
			} else if ip := net.ParseIP(part); ip != nil {
				// 单 IP → /32 或 /128
				if ip.To4() != nil {
					add(part + "/32")
				} else {
					add(part + "/128")
				}
			}
		}
	}
}

// peerTrusted 判断本次请求的直连对端是否在受信任来源内。
func peerTrusted(r *http.Request) bool {
	if trustProxy.all {
		return true
	}
	if len(trustProxy.nets) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trustProxy.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP 提取请求来源 IP。仅当直连对端受信任时才解析转发头,
// 否则一律用 TCP RemoteAddr(防伪造)。
func clientIP(r *http.Request) string {
	direct := r.RemoteAddr
	if i := strings.LastIndex(direct, ":"); i > 0 {
		// 仅当看起来像 host:port 才截断(避免裸 IPv6 被破坏)
		if _, _, err := net.SplitHostPort(direct); err == nil {
			direct, _, _ = net.SplitHostPort(r.RemoteAddr)
		}
	}
	if !peerTrusted(r) {
		return direct
	}
	// 受信任前置:优先 X-Real-IP,其次 XFF 最右侧可信链外的第一个客户端 IP。
	// 取 XFF 的**第一个**段(最初客户端),前置网关需保证已剥离客户端伪造头。
	if v := r.Header.Get("X-Real-IP"); v != "" {
		if ip := net.ParseIP(strings.TrimSpace(v)); ip != nil {
			return ip.String()
		}
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if ip := net.ParseIP(p); ip != nil {
				return ip.String()
			}
		}
	}
	return direct
}

// ClientIP 暴露给调用方。
func ClientIP(r *http.Request) string { return clientIP(r) }
