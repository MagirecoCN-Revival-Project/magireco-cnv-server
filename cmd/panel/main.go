// Command panel 是面板入口。
//
// 面板负责：
//   - 节点注册表管理（SQLite 本地存储）
//   - 经 WebSocket 管控协议连接并监控所有注册节点
//   - 托管全部人类可见前端（登录/注册/找回/游戏管理后台/用户中心，静态目录 CNV_WEB_DIR）
//   - 面板级管理员认证（独立于游戏 admin）
//
// 面板不直接承载游戏 API：游戏客户端经签名目录找到节点后直连；浏览器里的管理后台 /
// 用户中心也是从面板加载前端后**跨域直连业务节点 API**（节点侧按面板来源放行 CORS）。
// 面板自身的节点注册表只读总览在 /panel，管理 API 在 /api/panel/*。
//
// 环境变量：
//
//	CNV_ADDR         面板监听地址（默认 :8090）
//	CNV_PANEL_DB_FILE 面板本地 SQLite 路径（默认 ./data/panel.db）
//	CNV_WEB_DIR       面板托管的游戏前端静态目录（默认 ./web；缺失时根路径回落内置节点状态页）
//	CNV_PANEL_KEY    面板 cookie 签名密钥（≥16 字节，必填）
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/api-server/internal/config"
	"magirecocn-revival/api-server/internal/middleware"
	"magirecocn-revival/api-server/internal/panelstore"
)

var version = "dev"

func main() {
	// --version / -v / version 子命令:与 magireco-node 一致,仅打印版本号一行。
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			os.Stdout.WriteString(version + "\n")
			return
		}
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Error("加载配置失败", "err", err)
		os.Exit(2)
	}
	// 面板默认端口 :8090，避免与节点的 :8080 冲突
	if cfg.Addr == ":8080" {
		cfg.Addr = ":8090"
	}
	if err := cfg.MustValidatePanel(); err != nil {
		log.Error("面板配置不完整", "err", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 面板本地存储
	ps, err := panelstore.Open(ctx, cfg.PanelDBFile)
	if err != nil {
		log.Error("打开面板数据库失败", "file", cfg.PanelDBFile, "err", err)
		os.Exit(2)
	}
	defer ps.Close()
	log.Info("面板数据库已就绪", "file", cfg.PanelDBFile)

	// 节点连接管理器
	mgr := newManager(ps, log)
	go mgr.run(ctx)

	// HTTP 路由
	middleware.ConfigureTrustProxy(cfg.TrustProxy)
	r := chi.NewRouter()
	r.Use(middleware.Recovery, middleware.Logger, middleware.SecurityHeaders)
	r.Use(middleware.BodyLimit(2 << 20))

	h := &Handler{
		ps:  ps,
		mgr: mgr,
		key: []byte(cfg.PanelKey),
		ttl: 7 * 24 * time.Hour,
		log: log,
	}
	r.Route("/api/panel", h.Routes)

	// WordPress 式安装模块：仅在面板「未初始化」（无 super_admin）时挂载。
	// 安装完成的瞬间会把自己从下面这个可热插拔的 mount 里摘除并释放；
	// 重启后若已初始化，这里根本不会再构造它（模块永久不再出现，无需请求期 flag）。
	install := &installMount{}
	if n, cErr := ps.AdminCountSuper(ctx); cErr == nil && n == 0 {
		newInstallModule(ps, install, log)
		log.Info("面板未初始化，已挂载安装向导", "url", "/install/")
	}
	r.Mount("/install", install)

	// 面板托管的游戏前端(登录/注册/找回/管理后台/用户中心)。
	// 面板接管这些人类入口后,业务节点不再托管 WebUI;前端在浏览器内跨域直连节点 API。
	ui := newWebUI(ps, cfg.WebDir)
	webDirOK := false
	if st, serr := os.Stat(cfg.WebDir); serr == nil && st.IsDir() {
		webDirOK = true
		log.Info("面板已托管游戏前端", "dir", cfg.WebDir)
	}

	// 面板自身的节点注册表总览(只读 HTML)挪到 /panel,根目录让给游戏前端。
	r.Get("/panel", h.statusPage)
	r.Get("/panel/status", h.statusPage)

	if webDirOK {
		// 注入业务节点 API 基址,供前端跨域直连。
		r.Get("/app-config.js", ui.appConfig)
	}

	// 根路径：未初始化→安装向导；已初始化→游戏前端首页(无前端目录时回落内置节点状态页)。
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		if install.active() {
			http.Redirect(w, req, "/install/", http.StatusSeeOther)
			return
		}
		if webDirOK {
			ui.serveFile(w, req, "index.html")
			return
		}
		h.statusPage(w, req)
	})

	if webDirOK {
		// 其余路径交给前端静态资源(已在上面排除 /api、/install、/panel、/app-config.js、/）。
		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			p := req.URL.Path
			if strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/install") {
				http.NotFound(w, req)
				return
			}
			ui.static(w, req)
		})
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    512 << 10,
	}
	go func() {
		<-ctx.Done()
		shCtx, sc := context.WithTimeout(context.Background(), 10*time.Second)
		defer sc()
		_ = srv.Shutdown(shCtx)
	}()

	log.Info("面板已启动", "addr", cfg.Addr)
	serveErr := srv.ListenAndServe()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		log.Error("面板 HTTP 异常退出", "err", serveErr)
		os.Exit(1)
	}
}
