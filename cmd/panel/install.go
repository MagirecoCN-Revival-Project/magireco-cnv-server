package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/cnv-server/internal/auth"
	"magirecocn-revival/cnv-server/internal/panelstore"
	"magirecocn-revival/cnv-server/internal/store"
)

// ─── 安装模块（WordPress 式安装向导）────────────────────────────────────────────
//
// 设计要点：与节点的 internal/api/setup 不同，节点是「关闭入口」——路由常驻、
// 靠 setupGuard 读 DB 标志返回 404。本模块是「删除模块」：
//
//   - 安装的运行态（子路由 mux、模块实例）封装在 installModule 实例里，
//     通过 installMount 这个可热插拔的 http.Handler 挂到 /install。
//   - 安装完成的瞬间调用 installMount.remove()：把内部指针置空，installModule
//     实例失去最后一个引用 → 被 GC 回收，/install/* 之后命中默认 404。
//     这不是「flag 为真就拒绝」，而是处理器本身从运行中的路由树里消失。
//   - 「已初始化」的唯一真相是 panelstore 里是否存在 super_admin；重启后若已初始化，
//     main 根本不会再构造 installModule —— 模块永久不再出现，无需任何请求期 flag。
//
// 注：Go 是静态编译，installHTML 模板这类编译期常量与指令一样烙在二进制里、
// 运行时无法抹除；这里的「删除」指把活动处理器从运行中的服务摘除并让其被 GC 回收，
// 彻底从制品中剔除需重新编译（编译型程序的固有约束）。

// installMount 是挂在 /install 的可热插拔处理器。安装完成后 remove() 使其永久 404。
type installMount struct {
	mod atomic.Pointer[installModule]
}

func (im *installMount) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m := im.mod.Load(); m != nil {
		m.mux.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// active 报告安装模块当前是否仍挂载（供 main 决定 "/" 是否跳转安装）。
func (im *installMount) active() bool { return im.mod.Load() != nil }

// remove 从运行中的路由树摘除安装模块并释放它（最后一个引用置空 → 可被 GC）。
func (im *installMount) remove() { im.mod.Store(nil) }

// installModule 持有安装向导的全部状态与资源。实例被 installMount 引用，
// 摘除后即可回收。
type installModule struct {
	ps    *panelstore.Store
	mux   *chi.Mux
	mount *installMount
	log   interface{ Info(string, ...any) }

	// 本机装业务节点用的环境信息(由 main 注入):
	//   panelVersion = 面板自身版本字符串(--version 输出),用于硬比对节点二进制版本
	//   nodeControlAddr = 节点管控通道地址(默认 127.0.0.1:9090)。装完之后用它探测
	//                     节点是否在跑、用它等节点起来。
	panelVersion    string
	nodeControlAddr string
}

// newInstallModule 构造安装模块并把自身注册进 mount。仅在面板未初始化时调用。
func newInstallModule(ps *panelstore.Store, mount *installMount, log interface{ Info(string, ...any) }) *installModule {
	m := &installModule{
		ps: ps, mount: mount, log: log,
		panelVersion:    version,
		nodeControlAddr: defaultNodeControlAddr(),
	}
	r := chi.NewRouter()
	r.Get("/", m.page)
	r.Post("/api/db-check", m.apiDBCheck)
	r.Post("/api/gamedb-test", m.apiGameDBTest)
	r.Post("/api/complete", m.apiComplete)
	m.mux = r
	mount.mod.Store(m)
	return m
}

// alreadyInitialized 二次校验：即便模块尚未摘除，一旦已存在 super_admin 也立刻拒绝
// （防并发/重复安装）。
func (m *installModule) alreadyInitialized(ctx context.Context) bool {
	n, err := m.ps.AdminCountSuper(ctx)
	return err == nil && n > 0
}

func (m *installModule) page(w http.ResponseWriter, r *http.Request) {
	if m.alreadyInitialized(r.Context()) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(installHTML))
}

// apiDBCheck 对面板自身的 SQLite 存储做真实连通性 + 可写性探测。
func (m *installModule) apiDBCheck(w http.ResponseWriter, r *http.Request) {
	if m.alreadyInitialized(r.Context()) {
		writeErr(w, http.StatusForbidden, "already_initialized", "面板已初始化")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := m.ps.Ping(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "db_error", "数据库不可用："+err.Error())
		return
	}
	writeOK(w, map[string]any{"db_ok": true, "engine": "sqlite"})
}

// gameDBTestReq 是游戏库连接测试请求：直接传一个完整的 CNV_DB_URL（DSN）。
// 由浏览器从结构化字段拼好（可在「生成的 DSN」框里手改），保证「测的就是要设的」。
type gameDBTestReq struct {
	DSN string `json:"dsn"`
}

// apiGameDBTest 对业务节点将要使用的游戏库（CNV_DB_URL）做真实连接测试。
// 复用 internal/store 的 DSN 识别与 Open（含 ping），确保「此处测通 == 节点能连」。
// 面板自身不使用该库，也不持久化该 DSN（避免在面板侧留存数据库口令）。
//
// 测试是尽力而为：面板与游戏库不在同机时可能连不通（防火墙/网络），以业务节点
// 实际连接为准——故前端「下一步」不依赖此测试结果。
func (m *installModule) apiGameDBTest(w http.ResponseWriter, r *http.Request) {
	if m.alreadyInitialized(r.Context()) {
		writeErr(w, http.StatusForbidden, "already_initialized", "面板已初始化")
		return
	}
	var req gameDBTestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式错误")
		return
	}
	dsn := strings.TrimSpace(req.DSN)
	if dsn == "" {
		writeErr(w, http.StatusBadRequest, "validation", "请填写数据库连接信息")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	// 注意：dsn 含数据库口令，绝不写入日志。
	st, err := store.Open(ctx, dsn)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "db_error", "连接失败："+redactCreds(err.Error()))
		return
	}
	_ = st.Close()
	writeOK(w, map[string]any{"db_ok": true, "engine": dbEngineName(store.DetectDialect(dsn))})
}

// dbEngineName 把方言驱动名映射成展示用引擎名。
func dbEngineName(d store.Dialect) string {
	switch d.Driver() {
	case store.DriverPostgres:
		return "PostgreSQL"
	case store.DriverMySQL:
		return "MySQL"
	case store.DriverSQLite:
		return "SQLite"
	default:
		return string(d.Driver())
	}
}

// credInURL 匹配 "scheme://user:pwd@" 里的口令段。
var credInURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^:/@\s]+):[^@/\s]+@`)

// redactCreds 把错误信息里可能出现的连接串口令抹成 ***，避免把数据库口令回显或下传。
func redactCreds(s string) string { return credInURL.ReplaceAllString(s, `$1:***@`) }

type installCompleteReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// InstallLocalBusiness 是 UI 上"本机也装业务节点"那个勾。
	//   true: 找平级 magireco-node 二进制 → 版本必须与面板严格相等 →
	//         打开业务 DB,setup_state.done=true 时硬拒("已经装过了")→
	//         否则跑迁移 + 建同凭证超管 + 写 setup_state.done → 若节点没起
	//         自动 setsid fork 启动 + 轮询管控端口确认就绪。
	//   false: 跳过所有业务节点动作,只装面板自己(老的两阶段流程,业务
	//          节点自己的 /setup/* 作后路)。
	InstallLocalBusiness bool `json:"install_local_business"`
	// GameDBDSN:业务节点 DB 连接串。仅 InstallLocalBusiness=true 时使用,
	// 直接拿步骤 3 测试通过的那串 DSN(前端 g-dsn 字段)。
	GameDBDSN string `json:"game_db_dsn"`
}

// apiComplete 创建第一个 super_admin，成功后立即从运行中的服务删除安装模块。
func (m *installModule) apiComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if m.alreadyInitialized(ctx) {
		writeErr(w, http.StatusForbidden, "already_initialized", "面板已初始化")
		return
	}
	var req installCompleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式错误")
		return
	}
	if req.Username == "" || !isValidEmail(req.Email) {
		writeErr(w, http.StatusBadRequest, "validation", "用户名不能为空，邮箱格式需正确")
		return
	}
	if len(req.Password) < 12 {
		writeErr(w, http.StatusBadRequest, "weak_password", "超级管理员密码至少 12 位")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	// ── "本机也装业务节点"分支 ────────────────────────────────────────
	// 顺序:业务节点先,面板后。前者任何失败都中断,面板侧不写,
	// 这样运维可以原样重试,不会留下半装状态。
	bizResult := struct {
		Installed bool   `json:"installed"`
		Started   bool   `json:"started"`
		PID       int    `json:"pid,omitempty"`
		Version   string `json:"version,omitempty"`
	}{}
	if req.InstallLocalBusiness {
		bizDSN := strings.TrimSpace(req.GameDBDSN)
		if bizDSN == "" {
			writeErr(w, http.StatusBadRequest, "missing_game_db_dsn",
				"选了'本机也装业务节点'就必须先在第 3 步配并测通游戏数据库")
			return
		}
		out, msg, code := m.installLocalBusinessNode(ctx, bizDSN, req.Username, req.Email, hash)
		if msg != "" {
			writeErr(w, code, "business_setup_failed", msg)
			return
		}
		bizResult.Installed = true
		bizResult.Started = out.Started
		bizResult.PID = out.PID
		bizResult.Version = out.Version
	}

	a := panelstore.Admin{
		ID:           "padm_" + randHex(8),
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: hash,
		Role:         "super_admin",
		CreatedAt:    time.Now().UnixMilli(),
	}
	if err := m.ps.AdminInsert(ctx, a); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", "创建管理员失败")
		return
	}

	// 先回响应,再删除模块——确保客户端拿到成功结果。
	body := map[string]any{
		"success": true,
		"message": "安装完成,安装模块已从服务中移除。请使用管理员账号登录。",
	}
	if bizResult.Installed {
		body["business"] = bizResult
	}
	writeOK(w, body)

	// 从运行中的路由树摘除并释放本模块：此后 /install/* 永久 404（非 flag 拒绝）。
	m.mount.remove()
	if m.log != nil {
		m.log.Info("面板安装完成，安装模块已移除", "admin_id", a.ID, "email", a.Email)
	}
}

// installLocalBusinessResult installLocalBusinessNode 成功时的产出。
type installLocalBusinessResult struct {
	Started bool   // 本次是不是把节点 fork 起来了(false = 装完发现已经在跑)
	PID     int    // fork 出来的 pid,Started=false 时为 0
	Version string // 节点二进制 --version 输出,展示用
}

// installLocalBusinessNode 在本机装业务节点。流程:
//
//  1. 定位节点二进制(平级默认 / CNV_NODE_BIN 覆盖)
//  2. 跑 node --version,与面板版本硬比对
//  3. Open 业务 DB,读 setup_state.done —— true 时硬拒("已经装过了")
//  4. 否则跑迁移(已建表的库自动跳过)、建超管、写 setup_state.done=true
//  5. dial 管控端口探测节点是否在跑;没在跑就 setsid fork 启动 + 轮询确认
//
// 任何一步失败都不会留下半装的业务节点状态(配置写入是最后一步);面板侧
// 也未写超管,运维可重试。
func (m *installModule) installLocalBusinessNode(
	ctx context.Context, dsn, username, email, passwordHash string,
) (installLocalBusinessResult, string, int) {
	var zero installLocalBusinessResult

	// ① 找二进制
	binPath, err := nodeFindBin()
	if err != nil {
		return zero, err.Error(), http.StatusBadRequest
	}

	// ② 版本硬比对
	nodeVer, err := nodeProbeVersion(ctx, binPath)
	if err != nil {
		return zero, "无法获取节点二进制版本: " + err.Error(), http.StatusBadRequest
	}
	if !versionsMatch(m.panelVersion, nodeVer) {
		return zero, fmt.Sprintf(
			"二进制版本不匹配:面板 %s,节点 %s — 请同步两边到同一版本再装",
			m.panelVersion, nodeVer), http.StatusConflict
	}

	// ③ 打开业务 DB
	bizSt, err := store.Open(ctx, dsn)
	if err != nil {
		return zero, "无法连接业务节点 DB(请回到第 3 步确认连接信息): " +
			redactDSNErr(err.Error(), dsn), http.StatusBadRequest
	}
	defer bizSt.Close()

	// ④ 已经装过?硬拒
	var flag struct {
		Done bool `json:"done"`
	}
	if ok, _ := bizSt.ConfigGet(ctx, "setup_state", &flag); ok && flag.Done {
		return zero,
			"业务节点已经安装完成过了 —— 如需重装请先清理业务节点的 setup_state " +
				"标志或删库重来",
			http.StatusConflict
	}

	// ⑤ 装:schema 探测(AdminCountSuper 在 admins 表不存在时报错 = 全新 DB)
	n, probeErr := bizSt.AdminCountSuper(ctx)
	if probeErr != nil {
		if err := bizSt.Migrate(ctx); err != nil {
			return zero, "业务节点迁移失败: " + redactDSNErr(err.Error(), dsn),
				http.StatusInternalServerError
		}
		n = 0
	}
	if n == 0 {
		idBytes := make([]byte, 8)
		_, _ = rand.Read(idBytes)
		if err := bizSt.AdminInsert(ctx, store.Admin{
			ID: "adm-" + hex.EncodeToString(idBytes), Username: username, Email: email,
			PasswordHash: passwordHash, Role: "super_admin", CreatedAt: time.Now().UnixMilli(),
		}); err != nil {
			return zero,
				"业务节点超管创建失败(可能用户名/邮箱已被占用): " + err.Error(),
				http.StatusConflict
		}
	}
	if err := bizSt.ConfigSet(ctx, "setup_state",
		map[string]any{"done": true}); err != nil {
		return zero, "业务节点配置写入失败: " + err.Error(),
			http.StatusInternalServerError
	}

	out := installLocalBusinessResult{Version: nodeVer}

	// ⑥ 节点进程在跑吗?在跑就别动它
	if nodeIsRunning(m.nodeControlAddr) {
		return out, "", 0
	}

	// ⑦ fork 启动 —— 把 DSN 注入子进程环境
	pid, err := nodeSpawn(spawnNodeOpts{
		BinPath:  binPath,
		EnvExtra: map[string]string{"CNV_DB_URL": dsn},
	})
	if err != nil {
		return zero, "启动节点进程失败: " + err.Error(),
			http.StatusInternalServerError
	}
	out.Started = true
	out.PID = pid

	// ⑧ 等节点起来。15s 没起来认为失败,但业务 DB 装好的状态保持
	// —— 运维可以手动启,/setup/* 也已经被锁住了。
	if err := nodeWaitReady(ctx, m.nodeControlAddr, 15*time.Second); err != nil {
		return zero,
			fmt.Sprintf("节点进程已 fork(pid=%d)但 15s 内未在 %s 上响应 —— 请查 logs/node.log",
				pid, m.nodeControlAddr),
			http.StatusInternalServerError
	}
	return out, "", 0
}

// redactDSNErr 把可能出现在 err 里的完整 DSN 替换掉(防口令泄漏)。
// 简单粗暴:把 dsn 当字面量替换为占位符。
func redactDSNErr(s, dsn string) string {
	if dsn == "" {
		return s
	}
	return strings.ReplaceAll(s, dsn, "<REDACTED-DSN>")
}

func isValidEmail(s string) bool {
	at := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '@' {
			if at >= 0 { // 多个 @
				return false
			}
			at = i
		}
	}
	if at <= 0 || at == len(s)-1 {
		return false
	}
	dot := false
	for i := at + 1; i < len(s); i++ {
		if s[i] == '.' && i < len(s)-1 {
			dot = true
		}
	}
	return dot
}

// installHTML 是安装向导页面（资源随实例存在，安装完成后随模块一并释放）。
const installHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1"/>
<title>面板安装向导 · 魔法纪录复兴计划</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:'IBM Plex Sans SC','PingFang SC',sans-serif;background:linear-gradient(135deg,#fbe4f1,#ede0fb 55%,#f4e9fc);min-height:100vh;display:flex;align-items:center;justify-content:center;padding:20px;color:#1a1a2e}
.card{background:#fff;border-radius:16px;box-shadow:0 8px 40px rgba(0,0,0,.12);padding:40px;width:100%;max-width:540px}
h1{font-size:22px;font-weight:700;margin-bottom:4px}
.sub{font-size:13px;color:#666;margin-bottom:28px}
.step{display:none}.step.active{display:block}
.field{margin-bottom:16px}
label{display:block;font-size:13px;font-weight:500;color:#333;margin-bottom:6px}
input{width:100%;padding:9px 12px;border:1.5px solid #dde1e7;border-radius:8px;font-size:14px;outline:none;transition:border-color .2s}
input:focus{border-color:#d63384}
select{width:100%;padding:9px 12px;border:1.5px solid #dde1e7;border-radius:8px;font-size:14px;outline:none;background:#fff;color:#1a1a2e}
select:focus{border-color:#d63384}
.two{display:flex;gap:10px}.two>div{flex:1}
.hint{font-size:11.5px;color:#888;margin-top:4px}
.btn{display:inline-flex;align-items:center;justify-content:center;gap:6px;padding:10px 20px;border:none;border-radius:8px;font-size:14px;font-weight:600;cursor:pointer;transition:background .2s}
.btn-primary{background:#d63384;color:#fff}.btn-primary:hover{background:#b52f6e}
.btn-sec{background:#f0f0f5;color:#333}.btn-sec:hover{background:#e2e2eb}
.btn:disabled{opacity:.5;cursor:not-allowed}
.row{display:flex;gap:10px;justify-content:flex-end;margin-top:24px}
.err{background:#fff0f3;border:1px solid rgba(220,38,38,.3);color:#c0392b;padding:10px 14px;border-radius:8px;font-size:13px;margin-bottom:16px}
.ok{background:#f0fdf4;border:1px solid rgba(34,197,94,.3);color:#166534;padding:10px 14px;border-radius:8px;font-size:13px;margin-bottom:16px}
.warn{background:#fffbeb;border:1px solid rgba(234,179,8,.4);color:#854d0e;padding:10px 14px;border-radius:8px;font-size:13px;margin-bottom:16px}
.kv{font-family:monospace;background:#f3f4f6;border-radius:6px;padding:2px 8px;font-size:12.5px}
.progress{display:flex;margin-bottom:28px}
.progress-step{flex:1;padding:8px 0;text-align:center;font-size:12px;font-weight:500;color:#aaa;border-bottom:3px solid #eee}
.progress-step.done{color:#d63384;border-color:#d63384}
</style>
</head>
<body>
<div class="card">
  <h1>🔧 面板安装向导</h1>
  <div class="sub">魔法纪录复兴计划 · 管理面板首次配置</div>

  <div class="progress">
    <div class="progress-step done" id="ps1">① 欢迎</div>
    <div class="progress-step" id="ps2">② 面板存储</div>
    <div class="progress-step" id="ps3">③ 游戏数据库</div>
    <div class="progress-step" id="ps4">④ 管理员</div>
    <div class="progress-step" id="ps5">⑤ 完成</div>
  </div>

  <div class="step active" id="step1">
    <p style="font-size:14px;line-height:1.8;color:#444;margin-bottom:16px">
      欢迎使用面板安装向导。本向导将引导您：
    </p>
    <ul style="font-size:13px;color:#555;line-height:2;padding-left:20px;margin-bottom:20px">
      <li>检查面板本地存储（SQLite —— 节点注册表 + 管理员）连通性与可写性</li>
      <li>配置并测试业务节点的游戏数据库（SQLite / MySQL / PostgreSQL）</li>
      <li>创建面板的超级管理员账号</li>
    </ul>
    <div class="warn">
      ⚠️ <strong>安全设计：</strong>安装完成后，本安装模块会<strong>立即从运行中的服务里移除</strong>
      （不是简单关闭入口），<code>/install/*</code> 之后永久返回 404。请妥善保管管理员凭证。
    </div>
    <div class="row">
      <button class="btn btn-primary" onclick="goStep(2)">开始 →</button>
    </div>
  </div>

  <div class="step" id="step2">
    <p style="font-size:14px;color:#444;margin-bottom:18px">
      面板使用本地 <strong>SQLite</strong> 存储节点注册表与管理员账号。
      点击「测试连接」对数据库做真实的读写探测。
    </p>
    <div id="db-result"></div>
    <div class="row">
      <button class="btn btn-sec" onclick="goStep(1)">← 返回</button>
      <button class="btn btn-primary" id="btn-db" onclick="doDBCheck()">测试连接</button>
    </div>
  </div>

  <div class="step" id="step3">
    <p style="font-size:13.5px;color:#444;line-height:1.7;margin-bottom:16px">
      业务节点（全系统<strong>唯一</strong>）通过 <span class="kv">CNV_DB_URL</span> 连接游戏数据库——
      玩家账号与云存档都存这里；边缘节点只分发资源、不连库。在此选择引擎并测试连接，
      向导会给出对应的 <span class="kv">CNV_DB_URL</span> 供你设置到业务节点。
    </p>
    <div class="field">
      <label>数据库引擎</label>
      <select id="g-engine" onchange="onEngineChange()">
        <option value="sqlite">SQLite（单机 / 入门）</option>
        <option value="mysql">MySQL / MariaDB</option>
        <option value="postgres">PostgreSQL</option>
      </select>
    </div>
    <div id="grp-sqlite">
      <div class="field">
        <label>数据库文件路径</label>
        <input type="text" id="g-sqlite-path" value="/var/lib/magireco/data.db" oninput="buildGameDSN()"/>
        <div class="hint">节点首次启动会自动建库建表</div>
      </div>
    </div>
    <div id="grp-net" style="display:none">
      <div class="two">
        <div class="field"><label>主机</label><input type="text" id="g-host" value="127.0.0.1" oninput="buildGameDSN()"/></div>
        <div class="field" style="max-width:130px"><label>端口</label><input type="text" id="g-port" oninput="buildGameDSN()"/></div>
      </div>
      <div class="two">
        <div class="field"><label>用户名</label><input type="text" id="g-user" placeholder="magireco" oninput="buildGameDSN()"/></div>
        <div class="field"><label>密码</label><input type="password" id="g-pass" oninput="buildGameDSN()"/></div>
      </div>
      <div class="field">
        <label>数据库名</label>
        <input type="text" id="g-db" placeholder="magireco" oninput="buildGameDSN()"/>
        <div class="hint">需<strong>预先创建</strong>该数据库；节点只建表不建库</div>
      </div>
      <div class="field" id="grp-myparams">
        <label>连接参数</label>
        <input type="text" id="g-myparams" value="parseTime=true" oninput="buildGameDSN()"/>
      </div>
      <div class="field" id="grp-sslmode" style="display:none">
        <label>sslmode</label>
        <select id="g-sslmode" onchange="buildGameDSN()">
          <option value="disable">disable</option>
          <option value="require">require</option>
          <option value="verify-ca">verify-ca</option>
          <option value="verify-full">verify-full</option>
        </select>
      </div>
    </div>
    <div class="field">
      <label>生成的 CNV_DB_URL（可手改）</label>
      <input type="text" id="g-dsn" spellcheck="false"/>
      <div class="hint">这串就是要设到业务节点环境变量里的值</div>
    </div>
    <div id="gdb-result"></div>
    <div class="warn" style="font-size:12px">
      测试为<strong>尽力而为</strong>：面板与游戏库不在同机时此处可能连不通（防火墙 / 网络），以业务节点实际连接为准。面板不会保存这串 DSN。
    </div>
    <div class="row">
      <button class="btn btn-sec" onclick="goStep(2)">← 返回</button>
      <button class="btn btn-sec" id="btn-gdb" onclick="doGameDBTest()">测试连接</button>
      <button class="btn btn-primary" onclick="goStep(4)">下一步 →</button>
    </div>
  </div>

  <div class="step" id="step4">
    <div class="err" id="admin-err" style="display:none"></div>
    <div class="field">
      <label>用户名 <span style="color:#e03">*</span></label>
      <input type="text" id="admin-username" placeholder="admin"/>
    </div>
    <div class="field">
      <label>管理员邮箱 <span style="color:#e03">*</span></label>
      <input type="email" id="admin-email" placeholder="admin@example.com"/>
    </div>
    <div class="field">
      <label>密码 <span style="color:#e03">*</span>（至少 12 位）</label>
      <input type="password" id="admin-pass" placeholder="强密码，建议 16 位以上"/>
      <div class="hint">建议用密码管理器生成随机密码并妥善保管</div>
    </div>
    <div class="field">
      <label>确认密码 <span style="color:#e03">*</span></label>
      <input type="password" id="admin-pass2"/>
    </div>
    <div class="field" style="margin-top:14px;padding:12px 14px;background:#f7f7f9;border:1px solid #e6e6ea;border-radius:6px">
      <label style="display:flex;align-items:flex-start;gap:8px;cursor:pointer">
        <input type="checkbox" id="install-local-biz" checked style="margin-top:3px"/>
        <span>
          <strong>本机也装业务节点（推荐）</strong>
          <div class="hint" style="margin-top:4px">
            勾选时面板会找平级的 <code>magireco-node</code> 二进制、版本严格匹配、
            把业务节点 DB 也装上、必要时 setsid 启动节点进程。
            <strong>不勾</strong>则只装面板,业务节点需要你手动跑
            <code>magireco-node</code> 并用它自己的 <code>/setup/*</code> 单独装。
          </div>
        </span>
      </label>
    </div>
    <div class="row">
      <button class="btn btn-sec" onclick="goStep(3)">← 返回</button>
      <button class="btn btn-primary" id="btn-complete" onclick="doComplete()">完成安装</button>
    </div>
  </div>

  <div class="step" id="step5">
    <div class="ok">✅ 安装完成！超级管理员账号已创建。</div>
    <p style="font-size:14px;color:#444;line-height:1.8;margin-bottom:16px">
      安装模块已<strong>从服务中移除</strong>，后续访问 <code>/install/</code> 将返回 404。<br/>
      请使用管理员账号登录面板。
    </p>
    <div class="row">
      <a href="/"><button class="btn btn-primary">前往面板 →</button></a>
    </div>
  </div>
</div>

<script>
let currentStep = 1;
function goStep(n){
  document.getElementById('step'+currentStep).classList.remove('active');
  currentStep = n;
  document.getElementById('step'+n).classList.add('active');
  for(let i=1;i<=5;i++){
    const el=document.getElementById('ps'+i);
    if(i<=n) el.classList.add('done'); else el.classList.remove('done');
  }
}
function esc(s){return String(s).replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));}
function gv(id){return document.getElementById(id).value.trim();}
function onEngineChange(){
  const e=document.getElementById('g-engine').value;
  document.getElementById('grp-sqlite').style.display = e==='sqlite'?'':'none';
  document.getElementById('grp-net').style.display    = e==='sqlite'?'none':'';
  document.getElementById('grp-myparams').style.display= e==='mysql'?'':'none';
  document.getElementById('grp-sslmode').style.display = e==='postgres'?'':'none';
  document.getElementById('g-port').value = e==='mysql'?'3306':(e==='postgres'?'5432':'');
  buildGameDSN();
}
function buildGameDSN(){
  const e=document.getElementById('g-engine').value; let dsn='';
  if(e==='sqlite'){
    dsn='sqlite://'+(gv('g-sqlite-path')||'/var/lib/magireco/data.db');
  }else{
    const host=gv('g-host')||'127.0.0.1';
    const port=gv('g-port')||(e==='mysql'?'3306':'5432');
    const user=gv('g-user'), pass=document.getElementById('g-pass').value;
    const db=gv('g-db'), cred=user+(pass?(':'+pass):'');
    if(e==='mysql'){ const pr=gv('g-myparams'); dsn='mysql://'+cred+'@'+host+':'+port+'/'+db+(pr?('?'+pr):''); }
    else{ const ssl=document.getElementById('g-sslmode').value||'disable'; dsn='postgres://'+cred+'@'+host+':'+port+'/'+db+'?sslmode='+ssl; }
  }
  document.getElementById('g-dsn').value=dsn; return dsn;
}
async function doGameDBTest(){
  const dsn=document.getElementById('g-dsn').value.trim(), res=document.getElementById('gdb-result');
  if(!dsn){ res.innerHTML='<div class="err">请先填写连接信息</div>'; return; }
  const btn=document.getElementById('btn-gdb'); btn.disabled=true;
  res.innerHTML='<div style="font-size:13px;color:#666">连接测试中…</div>';
  try{
    const r=await fetch('/install/api/gamedb-test',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({dsn:dsn})});
    const d=await r.json();
    if(d.ok && d.data && d.data.db_ok){
      res.innerHTML='<div class="ok">✅ 连接成功（引擎：<span class="kv">'+esc(d.data.engine)+'</span>）。在业务节点设置：'
        +'<div class="kv" style="margin-top:6px;word-break:break-all">CNV_DB_URL='+esc(dsn)+'</div></div>';
    }else{ res.innerHTML='<div class="err">❌ '+esc(d.message||'连接失败')+'</div>'; }
  }catch(e){ res.innerHTML='<div class="err">❌ 测试失败：'+esc(e.message)+'</div>'; }
  btn.disabled=false;
}
async function doDBCheck(){
  const btn=document.getElementById('btn-db'), res=document.getElementById('db-result');
  btn.disabled=true; res.innerHTML='<div style="font-size:13px;color:#666">探测中…</div>';
  try{
    const r=await fetch('install/api/db-check',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});
    const d=await r.json();
    if(d.ok && d.data && d.data.db_ok){
      res.innerHTML='<div class="ok">✅ 数据库连接正常（引擎：<span class="kv">'+d.data.engine+'</span>，可读可写）。</div>';
      btn.textContent='下一步 →'; btn.onclick=()=>goStep(3);
    }else{
      res.innerHTML='<div class="err">❌ '+((d.message)||'数据库异常')+'</div>';
    }
  }catch(e){ res.innerHTML='<div class="err">❌ 探测失败：'+e.message+'</div>'; }
  btn.disabled=false;
}
async function doComplete(){
  const user=document.getElementById('admin-username').value.trim();
  const email=document.getElementById('admin-email').value.trim();
  const pass=document.getElementById('admin-pass').value;
  const pass2=document.getElementById('admin-pass2').value;
  const installLocalBiz=document.getElementById('install-local-biz').checked;
  // 第 3 步收集的业务节点 DSN。本机装业务节点时必填,纯装面板时可空。
  const gameDBDSN=(document.getElementById('g-dsn').value||'').trim();
  const errEl=document.getElementById('admin-err'); errEl.style.display='none';
  if(!user){ errEl.textContent='请输入用户名'; errEl.style.display=''; return; }
  if(!email||!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)){ errEl.textContent='请输入有效的邮箱地址'; errEl.style.display=''; return; }
  if(pass.length<12){ errEl.textContent='密码至少 12 位'; errEl.style.display=''; return; }
  if(pass!==pass2){ errEl.textContent='两次输入的密码不一致'; errEl.style.display=''; return; }
  if(installLocalBiz && !gameDBDSN){
    errEl.textContent='勾了"本机也装业务节点"就必须先回第 3 步配并测通游戏数据库'; errEl.style.display=''; return;
  }
  const btn=document.getElementById('btn-complete'); btn.disabled=true;
  btn.textContent=installLocalBiz?'安装中（含启动节点）…':'安装中…';
  try{
    const r=await fetch('install/api/complete',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({username:user,email:email,password:pass,
        install_local_business:installLocalBiz,game_db_dsn:gameDBDSN})});
    const d=await r.json();
    if(d.ok && d.data && d.data.success){ goStep(5); }
    else{ errEl.textContent=(d.message)||'安装失败'; errEl.style.display=''; btn.disabled=false; btn.textContent='完成安装'; }
  }catch(e){ errEl.textContent='请求失败：'+e.message; errEl.style.display=''; btn.disabled=false; btn.textContent='完成安装'; }
}
onEngineChange();
</script>
</body>
</html>`
