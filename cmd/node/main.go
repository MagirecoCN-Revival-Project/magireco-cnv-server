// Command node 是业务节点或边缘节点的统一入口。
//
// 角色由 CNV_NODE_ROLE 决定：
//   - business（默认）：承载完整游戏 API（/client/* /account/* /auth/* /admin/* /user/* /setup/*）
//   - 数据库 + 管控 WS 服务端
//   - edge：仅提供静态资源下载 + 管控 WS 服务端
//
// 节点在首次启动时自动在 CNV_NODE_KEY_FILE 生成随机密钥并打印到日志。
// 管理员将密钥复制到面板的节点注册表后，面板即可经 CNV_CONTROL_ADDR 连接管控。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/cnv-server/internal/api/account"
	"magirecocn-revival/cnv-server/internal/api/admin"
	"magirecocn-revival/cnv-server/internal/api/captcha"
	"magirecocn-revival/cnv-server/internal/api/client"
	"magirecocn-revival/cnv-server/internal/api/respond"
	"magirecocn-revival/cnv-server/internal/api/setup"
	"magirecocn-revival/cnv-server/internal/api/user"
	"magirecocn-revival/cnv-server/internal/autoban"
	"magirecocn-revival/cnv-server/internal/capworker"
	"magirecocn-revival/cnv-server/internal/config"
	"magirecocn-revival/cnv-server/internal/control"
	"magirecocn-revival/cnv-server/internal/directory"
	"magirecocn-revival/cnv-server/internal/email"
	"magirecocn-revival/cnv-server/internal/middleware"
	"magirecocn-revival/cnv-server/internal/scheduler"
	"magirecocn-revival/cnv-server/internal/store"
	"magirecocn-revival/cnv-server/internal/totentanz"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z"
var version = "dev"

var startTime = time.Now()

func main() {
	// --version / -v / version 子命令:面板 install 用它探测节点版本做硬匹配。
	// 输出格式与 magireco-panel --version 一致(仅一行,无装饰)。
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
	if err := cfg.MustValidateNode(); err != nil {
		log.Error("节点配置不完整", "err", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 节点密钥：首次启动自动生成并持久化，之后复用同一把密钥。
	nodeKey, created, err := control.LoadOrCreateKey(cfg.NodeKeyFile)
	if err != nil {
		log.Error("节点密钥初始化失败", "err", err)
		os.Exit(2)
	}
	if created {
		log.Info("节点密钥已生成（首次启动）—— 请将以下密钥复制到面板节点注册表",
			"node_key", nodeKey,
			"control_addr", cfg.ControlAddr,
			"node_key_file", cfg.NodeKeyFile)
	}

	// 已签名节点目录（可选）：业务节点在 /client/init 响应里下发给客户端。
	var directoryJSON json.RawMessage
	if cfg.DirectoryFile != "" {
		data, readErr := os.ReadFile(cfg.DirectoryFile)
		if readErr != nil {
			log.Error("读取节点目录文件失败", "file", cfg.DirectoryFile, "err", readErr)
			os.Exit(2)
		}
		// 启动自检:节点只是原样转发离线签好的字节,手里没有根公钥(公钥钉在客户端
		// APK 里)所以**验不了签**;但至少要把 payload 解出来看一眼结构。
		//
		// 不检查的话,写错路径、文件被截断、或旧版 admintool(没有 Validate)签出的
		// 违规目录都会被照单下发。客户端那边只会静默丢弃并回退 API_HOST——服务
		// 一切正常、日志一片安静,而按 caps 的能力隔离已经失效了。
		var sd directory.SignedDirectory
		if err := json.Unmarshal(data, &sd); err != nil {
			log.Error("节点目录文件不是合法的 {payload,sig} JSON,拒绝下发",
				"file", cfg.DirectoryFile, "err", err)
			os.Exit(2)
		}
		dir, err := directory.DecodeUnverified(sd)
		if err != nil {
			log.Error("节点目录 payload 解析失败,拒绝下发",
				"file", cfg.DirectoryFile, "err", err)
			os.Exit(2)
		}
		// 能力分配违规(如边缘节点持有 save)必须拦住:一旦下发,客户端只认签名,
		// 会老老实实把凭证发到那台边缘机上。
		if err := dir.Validate(); err != nil {
			log.Error("节点目录未通过校验,拒绝下发(请用新版 admintool 重新签发)",
				"file", cfg.DirectoryFile, "err", err)
			os.Exit(2)
		}
		// 过期只告警不拦:目录过期时客户端会自行忽略并回退,服务本身仍可用,
		// 但运维需要知道该续签了,否则多节点路由就这么悄悄退化了。
		if now := time.Now().Unix(); dir.ExpiresAt <= now {
			log.Warn("节点目录已过期,客户端会忽略它并回退到 API_HOST,请尽快重新签发",
				"file", cfg.DirectoryFile, "expires_at", dir.ExpiresAt, "now", now)
		}
		directoryJSON = data
		log.Info("已加载签名节点目录",
			"file", cfg.DirectoryFile, "seq", dir.Seq,
			"nodes", len(dir.Nodes), "expires_at", dir.ExpiresAt)
	}

	// 运行指标采集:管控通道周期推送与只读状态页共用同一份快照逻辑。
	tel := telemetryFn(cfg.NodeRole)

	// 管控 WS 服务端：在独立端口（CNV_CONTROL_ADDR）提供面板连接。
	nodeInfo := control.NodeInfo{ID: cfg.NodeID, Role: cfg.NodeRole, Version: version}
	ctrlSrv := &control.Server{
		Key:               nodeKey,
		Node:              nodeInfo,
		Commands:          buildCommands(cancel),
		Telemetry:         tel,
		TelemetryInterval: 5 * time.Second,
		Log:               log,
	}
	ctrlMux := http.NewServeMux()
	ctrlMux.HandleFunc("/ctrl", ctrlSrv.Handler)
	ctrlHTTP := &http.Server{
		Addr:              cfg.ControlAddr,
		Handler:           ctrlMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Info("管控通道已启动", "addr", cfg.ControlAddr, "role", cfg.NodeRole)
		if serr := ctrlHTTP.ListenAndServe(); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			log.Error("管控通道异常退出", "err", serr)
		}
	}()
	go func() {
		<-ctx.Done()
		shCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
		defer sc()
		_ = ctrlHTTP.Shutdown(shCtx)
	}()

	switch cfg.NodeRole {
	case "edge":
		runEdge(ctx, cfg, tel, log)
	default:
		runBusiness(ctx, cfg, directoryJSON, tel, log)
	}
}

// telemetryFn 返回一个采集本进程运行指标的闭包;NodeRole 固定为启动角色。
func telemetryFn(role string) func() control.Telemetry {
	return func() control.Telemetry {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return control.Telemetry{
			MemPct:     float64(ms.HeapInuse) / float64(max(ms.HeapSys, 1)) * 100,
			UptimeSec:  int64(time.Since(startTime).Seconds()),
			Goroutines: runtime.NumGoroutine(),
			NodeRole:   role,
		}
	}
}

// buildCommands 返回管控协议支持的指令集。
func buildCommands(cancelProcess context.CancelFunc) map[string]control.CommandFunc {
	return map[string]control.CommandFunc{
		control.ActionInfo: func(_ context.Context, _ json.RawMessage) (any, error) {
			return map[string]string{
				"version": version,
				"uptime":  time.Since(startTime).Round(time.Second).String(),
			}, nil
		},
		control.ActionRestart: func(_ context.Context, _ json.RawMessage) (any, error) {
			go func() {
				time.Sleep(300 * time.Millisecond)
				cancelProcess()
			}()
			return map[string]string{"status": "shutting_down"}, nil
		},
		control.ActionPurgeCache: func(_ context.Context, _ json.RawMessage) (any, error) {
			runtime.GC()
			return map[string]string{"status": "gc_triggered"}, nil
		},
	}
}

// runEdge 以边缘角色运行：仅提供静态资源下载。
func runEdge(ctx context.Context, cfg *config.Config, tel func() control.Telemetry, log *slog.Logger) {
	middleware.ConfigureTrustProxy(cfg.TrustProxy)

	r := chi.NewRouter()
	r.Use(middleware.Recovery, middleware.Logger, middleware.SecurityHeaders)
	r.Use(middleware.BodyLimit(8 << 20))

	resDir := os.Getenv("CNV_SECONDARY_RES_DIR")
	if resDir == "" {
		resDir = cfg.PrimaryResDir
	}
	if resDir != "" {
		serveResources(r, cfg.PrimaryResPath, resDir)
		log.Info("资源目录已挂载", "path", cfg.PrimaryResPath, "dir", resDir)
	}
	if cfg.OfflineDir != "" {
		serveResources(r, cfg.OfflineURLPath, cfg.OfflineDir)
	}

	// 根目录只读实时状态页(所有节点统一)。
	mountStatus(r, cfg.NodeID, version, startTime, tel)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
	})

	log.Info("边缘节点启动", "addr", cfg.Addr)
	startHTTP(ctx, cfg, r, log)
}

// runBusiness 以业务角色运行：完整游戏 API 栈 + DB。
func runBusiness(ctx context.Context, cfg *config.Config, dirJSON json.RawMessage, tel func() control.Telemetry, log *slog.Logger) {
	st, err := store.Open(ctx, cfg.DBURL)
	if err != nil {
		log.Error("连接数据库失败", "err", err)
		os.Exit(2)
	}
	defer st.Close()
	log.Info("数据库已连接", "driver", st.Dialect().Driver())

	if !cfg.SkipMigrate {
		if err := st.Migrate(ctx); err != nil {
			log.Error("迁移失败", "err", err)
			os.Exit(2)
		}
	}

	capSvc := capworker.New(st, capworker.DefaultConfig())
	hearts := client.NewHeartbeats()

	// 镜像流量统计：从 DB 载入历史数据与限额配置。
	mirrorStats := admin.NewMirrorTracker(st)
	mirrorStats.Load(ctx)

	// 运行时可调的大小上限(全局请求体 / 热更新包):初值取 env/默认,启动时尝试用
	// config 表 "limits" 覆盖;管理员在后台「服务器控制」改后即时生效(共享 atomic)。
	limits := admin.NewLimits(ctx, st, cfg.BodyLimitMaxBytes, cfg.HotUpdateMaxBytes)

	// 自动封禁:从篡改/心跳伪造/资源高频/验证码连败/多账号切换等信号判定,命中阈值即写
	// bans(system/auto)。阈值存 config 表 "autoban",管理员可在后台「设备封禁」页运行时调整。
	autoBan := autoban.New(st, log)
	autoBan.Start(ctx)

	signKey := cfg.ResourceTokenSecret
	if len(signKey) < 16 {
		var skErr error
		signKey, skErr = resolveResourceTokenSecret(ctx, st, log)
		if skErr != nil {
			log.Error("初始化 resource_token 密钥失败", "err", skErr)
			os.Exit(2)
		}
	}

	middleware.ConfigureTrustProxy(cfg.TrustProxy)

	r := chi.NewRouter()
	r.Use(middleware.Recovery, middleware.Logger, middleware.SecurityHeaders)
	// 全局请求体上限运行时可调(读 limits 的 atomic);热更新上传路径从全局上限豁免,
	// 由各自 handler 按 limits 的热更新上限自卡。
	r.Use(middleware.BodyLimitFunc(limits.GlobalBodyBytes,
		"/admin/hot-update/js/upload",
		"/admin/hot-update/scenario/upload"))
	// 面板托管前端、浏览器跨域直连本节点 API:放行面板来源的 CORS 预检与实际请求。
	// 未配置 CNV_PANEL_PUBLIC_URL 时为同源部署,中间件零副作用直通。
	r.Use(middleware.CORS(cfg.PanelPublicURL))

	authLimiter := middleware.NewLimiter("auth", 30, time.Minute, "操作过于频繁,请稍后再试", middleware.IPKey)
	loginLimiter := middleware.NewLimiter("login", 10, time.Minute, "登录尝试过于频繁,请稍后再试", middleware.IPKey)
	codeLimiter := middleware.NewLimiter("email-code", 8, 10*time.Minute, "验证码请求/校验过于频繁,请稍后再试", middleware.IPKey)
	capLimiter := middleware.NewLimiter("captcha", 60, time.Minute, "验证码请求过于频繁", middleware.IPKey)
	saveLimiter := middleware.NewLimiter("save-put", 2, time.Minute, "云存档上传过于频繁,请稍后再试", nil)

	// 上游 Totentanz 端点发现:后台周期拉取,结果经 services 下发给客户端。
	// 刻意放在后台而非请求路径上——上游不受我们控制,它慢/挂都不该连累握手。
	disc := totentanz.New(cfg.TotentanzDiscoveryURL, cfg.TotentanzClientVersion, log)
	if disc.Enabled() {
		go disc.Run(ctx, time.Duration(cfg.TotentanzRefreshSec)*time.Second)
		log.Info("已启用 Totentanz 端点发现",
			"url", cfg.TotentanzDiscoveryURL,
			"version", cfg.TotentanzClientVersion,
			"refresh_sec", cfg.TotentanzRefreshSec)
	}

	clientH := &client.Handler{
		Discovery:           disc,
		St:                  st,
		ResourceTokenSecret: signKey,
		SignatureAllowed:    cfg.SignatureAllowed,
		ChannelAllowed:      cfg.ChannelAllowed,
		RequireSignature:    cfg.RequireSignature,
		TokenWindowSec:      30,
		ClientSessionTTL:    cfg.ClientSessionTTL,
		Heartbeats:          hearts,
		AutoBan:             autoBan,
		DirectoryJSON:       dirJSON,
		DevMode:             cfg.DevMode,
		// SceneAssets 尚未接入构建管线：场景清单的正式形状是待决项 R2。
		// nil = 该功能未启用，/client/scene-manifest 明确返回 503 而非空清单——
		// 空清单会被客户端理解为"该场景无需任何资产",从而静默进入残缺场景。
		SceneAssets:         nil,
		BootstrapEndpoint:   cfg.BootstrapEndpoint,
		BootstrapMaxThreads: cfg.BootstrapMaxThreads,
		BootstrapVersion:    cfg.BootstrapVersion,
	}
	r.Route("/client", clientH.Routes)
	// Android 底包的引导端点。挂在 /magica/api 下是底包写死的路径,不可改。
	r.Route("/magica/api", clientH.MagicaRoutes)

	mailer := email.New(email.Config{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		User:     cfg.SMTPUser,
		Pass:     cfg.SMTPPass,
		From:     cfg.SMTPFrom,
		FromName: cfg.SMTPFromName,
	})
	if cfg.SMTPHost != "" {
		log.Info("SMTP 已配置", "host", cfg.SMTPHost, "from", cfg.SMTPFrom)
	}

	accH := &account.Handler{
		St:                st,
		Cap:               capSvc,
		CaptchaEnabled:    true,
		ClientSessionTTL:  cfg.ClientSessionTTL,
		AccountSessionTTL: cfg.AccountSessionTTL,
		AdminSessionTTL:   cfg.AdminSessionTTL,
		WebDir:            cfg.WebDir,
		// 面板托管登录/注册/找回页后,客户端入口页 302 跳转到面板;
		// 留空(未接入面板)时回落到本地静态文件,保持 §0 客户端契约不破。
		PanelPublicURL: cfg.PanelPublicURL,
		LoginLimiter:   loginLimiter.Middleware,
		AuthLimiter:    authLimiter.Middleware,
		CodeLimiter:    codeLimiter.Middleware,
		SaveLimiter:    saveLimiter,
		Mailer:         mailer,
		AutoBan:        autoBan,
	}
	r.Route("/account", accH.Routes)
	r.Route("/auth", accH.AuthRoutes)

	capH := &captcha.Handler{Svc: capSvc}
	r.Group(func(g chi.Router) {
		g.Use(capLimiter.Middleware)
		g.Route("/api", capH.Routes)
	})

	adminH := &admin.Handler{
		St:           st,
		Hearts:       hearts,
		Cap:          capSvc,
		RequireSuper: middleware.RequireSuperAdmin(st),
		Pack: admin.PackerConfig{
			SourceDir: cfg.PrimaryResDir,
			DestDir:   cfg.OfflineDir,
			URLBase: func(req *http.Request) string {
				if cfg.PublicURL != "" {
					return strings.TrimRight(cfg.PublicURL, "/") + cfg.OfflineURLPath
				}
				scheme := "http"
				if req.TLS != nil {
					scheme = "https"
				}
				return scheme + "://" + req.Host + cfg.OfflineURLPath
			},
		},
		HotUpdate: admin.HotUpdateConfig{
			Dir: cfg.HotUpdateDir,
			URLBase: func(req *http.Request) string {
				if cfg.PublicURL != "" {
					return strings.TrimRight(cfg.PublicURL, "/") + cfg.HotUpdateURLPath
				}
				scheme := "http"
				if req.TLS != nil {
					scheme = "https"
				}
				return scheme + "://" + req.Host + cfg.HotUpdateURLPath
			},
		},
		Limits:      limits,
		MirrorStats: mirrorStats,
	}
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireWritableAdmin(st))
		g.Route("/admin", adminH.Routes)
	})

	userH := &user.Handler{St: st}
	r.Group(func(g chi.Router) {
		g.Use(middleware.RequireAccount(st, cfg.AccountSessionTTL))
		g.Route("/user/api", userH.Routes)
	})

	setupH := &setup.Handler{St: st}
	r.Route("/setup", setupH.Routes)

	if cfg.PrimaryResDir != "" {
		serveResources(r, cfg.PrimaryResPath, cfg.PrimaryResDir)
	}
	if cfg.OfflineDir != "" {
		serveResources(r, cfg.OfflineURLPath, cfg.OfflineDir)
	}
	// 热更新包自托管:管理员发布/上传后落盘到 HotUpdateDir,经此对外分发。
	if cfg.HotUpdateDir != "" {
		serveResources(r, cfg.HotUpdateURLPath, cfg.HotUpdateDir)
	}

	// 人类可见的前端面板(管理后台/登录/注册/用户中心)已交由面板托管,
	// 业务节点不再在根目录托管 WebUI;根目录改为只读实时状态页。
	// 仅当未接入面板(CNV_PANEL_PUBLIC_URL 留空)时,回落挂载本地静态资源,
	// 让客户端入口页(/account/register|forgot|verify-email)及其样式可用——保证 §0 不破。
	if cfg.PanelPublicURL == "" {
		serveWebAssets(r, cfg.WebDir)
	}
	mountStatus(r, cfg.NodeID, version, startTime, tel)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		respond.Fail(w, http.StatusNotFound, "not_found", "")
	})

	sch := &scheduler.Scheduler{
		St:            st,
		Hearts:        hearts,
		Log:           log,
		PackSourceDir: cfg.PrimaryResDir,
		PackDestDir:   cfg.OfflineDir,
		MirrorStats:   mirrorStats,
		OfflineURL: func() string {
			if cfg.PublicURL != "" {
				return strings.TrimRight(cfg.PublicURL, "/") + cfg.OfflineURLPath
			}
			return cfg.OfflineURLPath
		},
	}
	sch.Start(ctx)

	// ── 部署形态告警 ────────────────────────────────────────────────────
	// 这两条都不阻止启动，但必须在日志里显眼：它们描述的是「这个部署与生产
	// 应有的样子不一样」，而这种偏差恰恰是最容易被遗忘地留在生产里的东西。
	if cfg.DevMode {
		log.Warn("⚠️ 开发模式已开启（CNV_DEV_MODE）—— 会下发协议的开发期临时值，" +
			"生产环境务必关闭")
	}
	if len(dirJSON) == 0 {
		log.Warn("⚠️ 未配置签名节点目录（CNV_DIRECTORY_FILE）—— 握手不下发 directory。" +
			"这只应出现在开发部署：缺少目录意味着节点路由脱离服务端控制，" +
			"想把流量从出问题的节点挪走都做不到，而客户端那份缓存可能是任意久以前的")
	}

	log.Info("业务节点启动", "addr", cfg.Addr, "node_id", cfg.NodeID)
	startHTTP(ctx, cfg, r, log)
}

func startHTTP(ctx context.Context, cfg *config.Config, r http.Handler, log *slog.Logger) {
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go func() {
		<-ctx.Done()
		shCtx, sc := context.WithTimeout(context.Background(), 10*time.Second)
		defer sc()
		_ = srv.Shutdown(shCtx)
	}()
	var serveErr error
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		serveErr = srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
	} else {
		serveErr = srv.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		log.Error("HTTP 服务异常退出", "err", serveErr)
		os.Exit(1)
	}
}

func resolveResourceTokenSecret(ctx context.Context, st *store.Store, log *slog.Logger) ([]byte, error) {
	type wrap struct {
		Hex string `json:"hex"`
	}
	var w wrap
	ok, err := st.ConfigGet(ctx, "resource_token_secret", &w)
	if err != nil {
		return nil, err
	}
	if ok && len(w.Hex) >= 32 {
		key, decErr := hex.DecodeString(w.Hex)
		if decErr == nil {
			return key, nil
		}
		log.Warn("resource_token_secret 解析失败,重新生成", "err", decErr)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	w.Hex = hex.EncodeToString(key)
	if err := st.ConfigSet(ctx, "resource_token_secret", w); err != nil {
		return nil, err
	}
	log.Info("已自动生成 resource_token 签名密钥并持久化")
	return key, nil
}

func serveResources(r chi.Router, base, dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	fs := http.FileServer(http.Dir(abs))
	r.Get(base+"/*", http.StripPrefix(base, fs).ServeHTTP)
}

// serveWebAssets 在「未接入面板」的回落场景下挂载本地静态资源(样式/脚本/入口页),
// 让客户端打开的 /account/register 等本地页面能正常渲染。
// 不接管根目录 "/"——根目录恒为只读状态页(见 mountStatus)。
func serveWebAssets(r chi.Router, dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	if _, err := os.Stat(abs); err != nil {
		return
	}
	// .jsx 是客户端 JavaScript；Go 标准库未内置该扩展名的 MIME 映射。
	_ = mime.AddExtensionType(".jsx", "application/javascript")
	fs := http.FileServer(http.Dir(abs))
	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		for _, prefix := range []string{"/client/", "/account/", "/auth/", "/admin/", "/user/", "/api/", "/setup/"} {
			if strings.HasPrefix(p, prefix) {
				respond.Fail(w, http.StatusNotFound, "not_found", "")
				return
			}
		}
		fs.ServeHTTP(w, req)
	})
}

func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
