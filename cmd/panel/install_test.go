package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/cnv-server/internal/panelstore"
	"magirecocn-revival/cnv-server/internal/store"
)

// 用与 main.go 相同的 chi 接线搭一个最小面板路由：/install 挂安装模块、
// "/" 在未初始化时跳转向导。验证安装模块的「装好即自毁」行为。
func newTestPanel(t *testing.T) (*httptest.Server, *installMount, *panelstore.Store) {
	t.Helper()
	ps, err := panelstore.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open panelstore: %v", err)
	}
	t.Cleanup(func() { ps.Close() })

	install := &installMount{}
	if n, _ := ps.AdminCountSuper(context.Background()); n == 0 {
		newInstallModule(ps, install, nil)
	}

	r := chi.NewRouter()
	r.Mount("/install", install)
	// 镜像 main.go 在 CNV_WEB_DIR 存在(webDirOK)时托管前端的 "/*" 静态兜底：验证 /install
	// 挂载与根通配能共存，且安装路由优先级更高（移除后仍命中 mount 的 404，
	// 不会回落到静态处理器）。
	r.Get("/*", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("static-fallback"))
	})
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		if install.active() {
			http.Redirect(w, req, "/install/", http.StatusSeeOther)
			return
		}
		_, _ = w.Write([]byte("dashboard"))
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, install, ps
}

func post(t *testing.T, base, path, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestInstall_WizardThenSelfRemoves(t *testing.T) {
	srv, install, ps := newTestPanel(t)
	ctx := context.Background()

	// 1) 未初始化：向导页可访问，模块处于激活态。
	if !install.active() {
		t.Fatal("安装前模块应处于激活态")
	}
	resp, err := http.Get(srv.URL + "/install/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "面板安装向导") {
		t.Fatalf("向导页异常: code=%d body=%.80q", resp.StatusCode, body)
	}

	// 2) 数据库连通性探测应通过（真实读写探针）。
	if code, b := post(t, srv.URL, "/install/api/db-check", "{}"); code != 200 || !strings.Contains(b, "\"db_ok\":true") {
		t.Fatalf("db-check 应通过: code=%d body=%s", code, b)
	}

	// 3) 完成安装：创建超管。
	code, b := post(t, srv.URL, "/install/api/complete", `{"username":"admin","email":"a@b.com","password":"hunter2hunter2"}`)
	if code != 200 || !strings.Contains(b, "\"success\":true") {
		t.Fatalf("complete 应成功: code=%d body=%s", code, b)
	}
	if n, _ := ps.AdminCountSuper(ctx); n != 1 {
		t.Fatalf("应创建 1 个超管，得到 %d", n)
	}

	// 4) 关键：模块已从运行中的服务移除（非 flag 拒绝）。
	if install.active() {
		t.Fatal("安装完成后模块应已被移除")
	}
	for _, p := range []string{"/install/", "/install/api/db-check", "/install/api/gamedb-test", "/install/api/complete"} {
		r2, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		r2.Body.Close()
		if r2.StatusCode != http.StatusNotFound {
			t.Fatalf("%s 移除后应 404，得到 %d", p, r2.StatusCode)
		}
	}

	// 5) "/" 不再跳转安装，直达面板。
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rootResp, err := noRedirect.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(rootResp.Body)
	rootResp.Body.Close()
	if rootResp.StatusCode != 200 || !strings.Contains(string(rb), "dashboard") {
		t.Fatalf("安装后根路径应直达面板: code=%d body=%.40q", rootResp.StatusCode, rb)
	}
}

func TestInstall_WeakPasswordRejected(t *testing.T) {
	srv, _, ps := newTestPanel(t)
	if code, b := post(t, srv.URL, "/install/api/complete", `{"username":"admin","email":"a@b.com","password":"short"}`); code != http.StatusBadRequest || !strings.Contains(b, "12") {
		t.Fatalf("弱密码应被拒: code=%d body=%s", code, b)
	}
	if n, _ := ps.AdminCountSuper(context.Background()); n != 0 {
		t.Fatalf("弱密码不应创建管理员，得到 %d", n)
	}
}

// 游戏库连接测试：内存 SQLite 走通整套 DSN 识别 + Open + ping，返回引擎名。
func TestInstall_GameDBTest_SQLiteMemoryOK(t *testing.T) {
	srv, _, _ := newTestPanel(t)
	code, b := post(t, srv.URL, "/install/api/gamedb-test", `{"dsn":":memory:"}`)
	if code != 200 || !strings.Contains(b, "\"db_ok\":true") || !strings.Contains(b, "SQLite") {
		t.Fatalf("内存 SQLite 应连通: code=%d body=%s", code, b)
	}
}

// 空 DSN 直接 400，不触发任何连接尝试。
func TestInstall_GameDBTest_EmptyDSNRejected(t *testing.T) {
	srv, _, _ := newTestPanel(t)
	if code, _ := post(t, srv.URL, "/install/api/gamedb-test", `{"dsn":"   "}`); code != http.StatusBadRequest {
		t.Fatalf("空 DSN 应 400, got %d", code)
	}
}

// 无法连接的 DSN（指向不可创建的 SQLite 路径）返回 503，且错误信息不回显口令。
func TestInstall_GameDBTest_UnreachableRedactsCreds(t *testing.T) {
	srv, _, _ := newTestPanel(t)
	// postgres 指向回环上一个几乎必然无监听的端口，连接很快失败；断言口令被抹掉。
	code, b := post(t, srv.URL, "/install/api/gamedb-test",
		`{"dsn":"postgres://u:secretpw@127.0.0.1:1/db?sslmode=disable&connect_timeout=2"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("不可达应 503, got %d body=%s", code, b)
	}
	if strings.Contains(b, "secretpw") {
		t.Fatalf("错误信息不应回显数据库口令: %s", b)
	}
}


// stubNodeOps 替换 nodelocal.go 的五个测试缝(找二进制/探版本/探在跑/启动/等就绪)
// 为可控行为。t.Cleanup 在测试结束时恢复原值,跨 test 隔离。
func stubNodeOps(t *testing.T, ver string, running, spawnOK, readyOK bool) {
	t.Helper()
	oldF, oldP, oldR, oldS, oldW := nodeFindBin, nodeProbeVersion, nodeIsRunning, nodeSpawn, nodeWaitReady
	nodeFindBin = func() (string, error) { return "/fake/magireco-node", nil }
	nodeProbeVersion = func(context.Context, string) (string, error) { return ver, nil }
	nodeIsRunning = func(string) bool { return running }
	if spawnOK {
		nodeSpawn = func(spawnNodeOpts) (int, error) { return 12345, nil }
	} else {
		nodeSpawn = func(spawnNodeOpts) (int, error) {
			return 0, errSpawnSimulated
		}
	}
	if readyOK {
		nodeWaitReady = func(context.Context, string, time.Duration) error { return nil }
	} else {
		nodeWaitReady = func(context.Context, string, time.Duration) error {
			return errReadyTimeoutSimulated
		}
	}
	t.Cleanup(func() {
		nodeFindBin, nodeProbeVersion, nodeIsRunning, nodeSpawn, nodeWaitReady = oldF, oldP, oldR, oldS, oldW
	})
}

var (
	errSpawnSimulated         = simulatedErr("simulated spawn failure")
	errReadyTimeoutSimulated  = simulatedErr("simulated ready timeout")
)

type simulatedErr string

func (e simulatedErr) Error() string { return string(e) }

// ── install_local_business=true 的 8 个分支 ─────────────────────────

// 满路径:找到二进制 + 版本同 + DB 全新 + 节点没在跑 → 装库 + 启进程 + 等就绪。
func TestInstall_LocalBusiness_FullFlow(t *testing.T) {
	stubNodeOps(t, "dev" /*同面板*/, false /*not running*/, true, true)
	srv, _, ps := newTestPanel(t)
	bizDSN := "sqlite://" + t.TempDir() + "/biz.db"
	body := `{"username":"admin","email":"a@b.com","password":"hunter2hunter2",` +
		`"install_local_business":true,"game_db_dsn":"` + bizDSN + `"}`
	code, b := post(t, srv.URL, "/install/api/complete", body)
	if code != 200 || !strings.Contains(b, "\"success\":true") {
		t.Fatalf("complete 应成功: code=%d body=%s", code, b)
	}
	if !strings.Contains(b, "\"installed\":true") || !strings.Contains(b, "\"started\":true") {
		t.Fatalf("响应应当含 business.installed=true 与 started=true: %s", b)
	}
	if !strings.Contains(b, "\"pid\":12345") {
		t.Fatalf("响应应当带 fork 出来的 pid: %s", b)
	}
	if n, _ := ps.AdminCountSuper(context.Background()); n != 1 {
		t.Fatalf("面板应建 1 个超管,得到 %d", n)
	}
	// 业务侧:reopen 验证装好了
	bizSt, _ := store.Open(context.Background(), bizDSN)
	defer bizSt.Close()
	if n, _ := bizSt.AdminCountSuper(context.Background()); n != 1 {
		t.Fatalf("业务侧应建 1 个超管,得到 %d", n)
	}
	var flag struct{ Done bool `json:"done"` }
	ok, _ := bizSt.ConfigGet(context.Background(), "setup_state", &flag)
	if !ok || !flag.Done {
		t.Fatalf("setup_state.done 应为 true,ok=%v flag=%+v", ok, flag)
	}
}

// 节点已经在跑(运维提前手动起的)→ 不重复 spawn,但 DB 仍然装上、面板超管仍然建。
func TestInstall_LocalBusiness_NodeAlreadyRunning_SkipsSpawn(t *testing.T) {
	stubNodeOps(t, "dev", true /*running*/, true, true)
	// spawn 不应该被调,改成只要被叫到就 fail
	nodeSpawn = func(spawnNodeOpts) (int, error) {
		t.Errorf("节点在跑时不应再调 spawn")
		return 0, nil
	}
	srv, _, _ := newTestPanel(t)
	bizDSN := "sqlite://" + t.TempDir() + "/biz.db"
	body := `{"username":"admin","email":"a@b.com","password":"hunter2hunter2",` +
		`"install_local_business":true,"game_db_dsn":"` + bizDSN + `"}`
	code, b := post(t, srv.URL, "/install/api/complete", body)
	if code != 200 {
		t.Fatalf("complete 应成功: code=%d body=%s", code, b)
	}
	if !strings.Contains(b, "\"installed\":true") {
		t.Fatalf("DB 仍应当被装上: %s", b)
	}
	if strings.Contains(b, "\"started\":true") {
		t.Fatalf("节点已在跑,started 不应为 true: %s", b)
	}
}

// 业务 DB 已经标记 setup_state.done=true → 硬拒,不重装不启动。
func TestInstall_LocalBusiness_AlreadyInstalled_HardReject(t *testing.T) {
	stubNodeOps(t, "dev", false, true, true)
	tmp := t.TempDir()
	bizDSN := "sqlite://" + tmp + "/biz.db"

	// 预先建好库 + 写 setup_state.done=true 模拟"以前装过"
	pre, _ := store.Open(context.Background(), bizDSN)
	if err := pre.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = pre.ConfigSet(context.Background(), "setup_state", map[string]any{"done": true})
	pre.Close()

	srv, _, ps := newTestPanel(t)
	body := `{"username":"admin","email":"a@b.com","password":"hunter2hunter2",` +
		`"install_local_business":true,"game_db_dsn":"` + bizDSN + `"}`
	code, b := post(t, srv.URL, "/install/api/complete", body)
	if code != http.StatusConflict {
		t.Fatalf("已装过应当 409,得到 %d: %s", code, b)
	}
	if !strings.Contains(b, "已经安装完成过了") {
		t.Fatalf("错误信息应说\"已经安装完成过了\": %s", b)
	}
	if n, _ := ps.AdminCountSuper(context.Background()); n != 0 {
		t.Fatalf("失败时面板侧也不该建超管,得到 %d", n)
	}
}

// 节点二进制版本 ≠ 面板版本 → 硬拒。
func TestInstall_LocalBusiness_VersionMismatch_HardReject(t *testing.T) {
	stubNodeOps(t, "v999.0.0" /*与面板 "dev" 不一致*/, false, true, true)
	srv, _, ps := newTestPanel(t)
	bizDSN := "sqlite://" + t.TempDir() + "/biz.db"
	body := `{"username":"admin","email":"a@b.com","password":"hunter2hunter2",` +
		`"install_local_business":true,"game_db_dsn":"` + bizDSN + `"}`
	code, b := post(t, srv.URL, "/install/api/complete", body)
	if code != http.StatusConflict {
		t.Fatalf("版本不匹配应当 409,得到 %d: %s", code, b)
	}
	if !strings.Contains(b, "版本不匹配") {
		t.Fatalf("错误信息应说\"版本不匹配\": %s", b)
	}
	if n, _ := ps.AdminCountSuper(context.Background()); n != 0 {
		t.Fatalf("失败时面板侧也不该建超管,得到 %d", n)
	}
}

// 二进制找不到 → 400 报错。
func TestInstall_LocalBusiness_BinaryNotFound(t *testing.T) {
	oldF := nodeFindBin
	nodeFindBin = func() (string, error) {
		return "", simulatedErr("二进制不存在")
	}
	t.Cleanup(func() { nodeFindBin = oldF })

	srv, _, _ := newTestPanel(t)
	bizDSN := "sqlite://" + t.TempDir() + "/biz.db"
	body := `{"username":"admin","email":"a@b.com","password":"hunter2hunter2",` +
		`"install_local_business":true,"game_db_dsn":"` + bizDSN + `"}`
	code, b := post(t, srv.URL, "/install/api/complete", body)
	if code != http.StatusBadRequest {
		t.Fatalf("找不到二进制应当 400,得到 %d: %s", code, b)
	}
	if !strings.Contains(b, "二进制不存在") {
		t.Fatalf("错误应当透传 findBin 的原因: %s", b)
	}
}

// 勾了"本机也装"却没填 DSN → 400 + 明确提示。
func TestInstall_LocalBusiness_MissingDSN(t *testing.T) {
	srv, _, _ := newTestPanel(t)
	body := `{"username":"admin","email":"a@b.com","password":"hunter2hunter2",` +
		`"install_local_business":true}` // 故意不带 game_db_dsn
	code, b := post(t, srv.URL, "/install/api/complete", body)
	if code != http.StatusBadRequest || !strings.Contains(b, "missing_game_db_dsn") {
		t.Fatalf("缺 DSN 应当 400 + missing_game_db_dsn: code=%d body=%s", code, b)
	}
}

// install_local_business=false → 完全跳过业务节点,仅装面板。
func TestInstall_PanelOnly_NoBusinessAction(t *testing.T) {
	// 故意把所有 node ops 设成炸的,以保证没人调它们
	t.Cleanup(func() {})
	old := []any{nodeFindBin, nodeProbeVersion, nodeIsRunning, nodeSpawn, nodeWaitReady}
	nodeFindBin = func() (string, error) { t.Error("不该被调"); return "", nil }
	nodeProbeVersion = func(context.Context, string) (string, error) { t.Error("不该被调"); return "", nil }
	nodeIsRunning = func(string) bool { t.Error("不该被调"); return false }
	nodeSpawn = func(spawnNodeOpts) (int, error) { t.Error("不该被调"); return 0, nil }
	nodeWaitReady = func(context.Context, string, time.Duration) error { t.Error("不该被调"); return nil }
	t.Cleanup(func() {
		nodeFindBin = old[0].(func() (string, error))
		nodeProbeVersion = old[1].(func(context.Context, string) (string, error))
		nodeIsRunning = old[2].(func(string) bool)
		nodeSpawn = old[3].(func(spawnNodeOpts) (int, error))
		nodeWaitReady = old[4].(func(context.Context, string, time.Duration) error)
	})

	srv, _, ps := newTestPanel(t)
	body := `{"username":"admin","email":"a@b.com","password":"hunter2hunter2",` +
		`"install_local_business":false}`
	code, b := post(t, srv.URL, "/install/api/complete", body)
	if code != 200 || !strings.Contains(b, "\"success\":true") {
		t.Fatalf("纯装面板应成功: code=%d body=%s", code, b)
	}
	if strings.Contains(b, "business") {
		t.Fatalf("不应在响应里出现 business 字段: %s", b)
	}
	if n, _ := ps.AdminCountSuper(context.Background()); n != 1 {
		t.Fatalf("面板应建 1 个超管,得到 %d", n)
	}
}

// 业务 DB 不可达 → 整笔 fail + 面板侧不写。
func TestInstall_LocalBusiness_DBUnreachable_AtomicFail(t *testing.T) {
	stubNodeOps(t, "dev", false, true, true)
	srv, _, ps := newTestPanel(t)
	// SQLite 路径指向不存在的目录 → Open 报错
	body := `{"username":"admin","email":"a@b.com","password":"hunter2hunter2",` +
		`"install_local_business":true,"game_db_dsn":"sqlite:///nope/nope/biz.db"}`
	code, b := post(t, srv.URL, "/install/api/complete", body)
	if code == 200 {
		t.Fatalf("不可达应失败: body=%s", b)
	}
	if n, _ := ps.AdminCountSuper(context.Background()); n != 0 {
		t.Fatalf("失败时面板侧也不该建超管,得到 %d", n)
	}
}
