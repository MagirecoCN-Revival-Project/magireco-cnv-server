package main

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"magirecocn-revival/cnv-server/internal/panelstore"
)

// webUI 让面板托管游戏前端(登录/注册/找回/管理后台/用户中心)。
//
// 架构:面板只做**静态托管 + 注入业务节点 API 基址 + 放宽 CSP**;前端在浏览器里
// 跨域直连业务节点的 /auth /admin /user/api /account /api 等接口(节点侧开 CORS)。
// 面板自身不承载游戏数据库流量。
type webUI struct {
	ps  *panelstore.Store
	dir string
	fs  http.Handler

	mu       sync.Mutex
	cached   string // 缓存的业务节点 API 基址(去尾斜杠)
	cachedAt time.Time
}

func newWebUI(ps *panelstore.Store, dir string) *webUI {
	// api.jsx 以普通 <script src> 加载,需被识别为 JavaScript;Go 标准库未内置
	// .jsx 扩展名映射,默认嗅探成 text/plain 会被浏览器严格 MIME 检查拒绝执行。
	_ = mime.AddExtensionType(".jsx", "application/javascript")
	return &webUI{ps: ps, dir: dir, fs: http.FileServer(http.Dir(dir))}
}

// apiBase 返回注册表里首个业务节点的 API 基址(去尾斜杠);无则空串(同源回落)。
// 带 3s 进程内缓存,避免每个静态资源请求都打一次本地 SQLite。
func (u *webUI) apiBase(ctx context.Context) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.cachedAt.IsZero() && time.Since(u.cachedAt) < 3*time.Second {
		return u.cached
	}
	base := ""
	if nodes, err := u.ps.NodeList(ctx); err == nil {
		for _, n := range nodes {
			if n.Role == "business" && n.APIURL != "" {
				base = strings.TrimRight(n.APIURL, "/")
				break
			}
		}
	}
	u.cached, u.cachedAt = base, time.Now()
	return base
}

// connectOrigin 从 API 基址提取 scheme://host 作为 CSP connect-src 放行项。
func connectOrigin(apiBase string) string {
	if apiBase == "" {
		return ""
	}
	if pu, err := url.Parse(apiBase); err == nil && pu.Scheme != "" && pu.Host != "" {
		return pu.Scheme + "://" + pu.Host
	}
	return ""
}

// setCSP 为面板托管的前端页面下发 CSP:在全站安全头基础上放宽 connect-src,
// 允许浏览器跨域 XHR 到业务节点;脚本/样式与节点侧保持一致(unpkg + 内联 Babel)。
func (u *webUI) setCSP(w http.ResponseWriter, apiBase string) {
	connect := "'self'"
	if o := connectOrigin(apiBase); o != "" {
		connect += " " + o
	}
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self' 'unsafe-inline' 'unsafe-eval' https://unpkg.com; "+
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
			"font-src 'self' data: https://fonts.gstatic.com; "+
			"img-src 'self' data: https:; "+
			"connect-src "+connect+"; "+
			"frame-ancestors 'none'; "+
			"base-uri 'self'; "+
			"form-action 'self'")
}

// appConfig 下发 /app-config.js,把业务节点 API 基址注入前端全局。
// 前端 api.jsx 读取 window.MR_API_BASE 作为所有请求前缀;空串=同源。
func (u *webUI) appConfig(w http.ResponseWriter, r *http.Request) {
	base := u.apiBase(r.Context())
	u.setCSP(w, base)
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// base 来自运维在面板注册表里登记的节点 URL,非用户输入;仍做 JSON 编码转义防注入。
	fmt.Fprintf(w, "window.MR_API_BASE = %q;\n", base)
}

// static 托管前端静态资源,并对每个响应附带放宽后的 CSP。
func (u *webUI) static(w http.ResponseWriter, r *http.Request) {
	u.setCSP(w, u.apiBase(r.Context()))
	u.fs.ServeHTTP(w, r)
}

// serveFile 直接服务前端目录下的某个文件(用于根路径回 index.html)。
func (u *webUI) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	u.setCSP(w, u.apiBase(r.Context()))
	http.ServeFile(w, r, u.dir+"/"+name)
}
