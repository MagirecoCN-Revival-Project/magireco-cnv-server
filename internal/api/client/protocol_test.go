// Package client 的协议保真测试。
//
// 字段全部以 magireco-cnv-client/patch/src/main/java/io/kamihama/cnv/
// 下的 Java 源码为唯一真理:
//
//   * ClientInit.java     — /client/init 请求与响应、/online-download、
//                            /offline-package、/hot-update、authTriple()
//   * ResourceFlow.java   — /client/heartbeat 请求与响应(ban / switch_mirrors)
//   * SaveSyncHelper.java — /account/save/{put,get}(此包不覆盖)
//
// 任何时候改 handler 都需要让这套测试继续通过 —— 否则就说明真机要崩。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/cnv-server/internal/store"
)

// newTestHandler 启一份 SQLite 内存库,跑迁移,构造 Handler。
func newTestHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "proto.db")
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	return &Handler{
		St:                  st,
		ResourceTokenSecret: []byte("test-secret-32-bytes-12345678"),
		SignatureAllowed:    nil, // 空白名单 = 放行
		TokenWindowSec:      300,
		ClientSessionTTL:    time.Hour,
		Heartbeats:          NewHeartbeats(),
		PrimaryResBaseURL: func(*http.Request) string {
			return "https://primary.example.com/res"
		},
	}, st
}

func newRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/client", h.Routes)
	return r
}

// postJSON 模拟客户端 Net.postJson:发 JSON、读 JSON,断言 HTTP 200。
func postJSON(t *testing.T, srv http.Handler, path string, body any) map[string]any {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		respBody, _ := io.ReadAll(w.Result().Body)
		t.Fatalf("%s 期望 HTTP 200, 实际 %d: %s", path, w.Code, string(respBody))
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s 响应不是合法 JSON: %v\n%s", path, err, w.Body.String())
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────
// /client/init — 字段对照 ClientInit.handshake() 第 126-170 行
// ─────────────────────────────────────────────────────────────────────────

func TestInit_RequestFields(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)

	// 客户端实际发的 body(ClientInit.java 第 126-130 行):
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version":   "4.0.0",
		"device_id": "dev_abc123",
		"signature": "",
		"channel":   "internal-test",
	})
	if resp["success"] != true {
		t.Fatalf("expected success=true, got %v", resp["success"])
	}
	if _, ok := resp["access_token"].(string); !ok {
		t.Fatalf("expected access_token (string) at top level, got %v", resp["access_token"])
	}
}

// TestInit_ResponseShape 先种入完整配置,然后验证所有字段都按客户端期望的位置
// 出现。空配置场景由 TestInit_OmitsEmptyOptionalStrings 单独验证。
func TestInit_ResponseShape(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)

	// 种入所有配置,保证字段都出现在响应里。
	ctx := context.Background()
	_ = st.ConfigSet(ctx, "server", map[string]any{
		"status": "maintenance", "message": "测试维护", "end_time": int64(1_770_000_000),
	})
	_ = st.ConfigSet(ctx, "features", map[string]any{
		"online_download": true, "offline_package": true, "disabled_message": "下载暂停",
	})
	_ = st.ConfigSet(ctx, "versions", map[string]any{
		"allowed_versions":         []string{"4.0.0"},
		"fake_version":             "1.0.0",
		"fake_name":                "假名",
		"update_url_normal":        "https://u.example.com/n",
		"update_url_internal_test": "https://u.example.com/i",
		"update_apk_sha256":        "abc",
	})
	// 重新解封,因为种入了 maintenance status
	// 用全新 device_id 避免和别的测试冲突

	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_shape", "signature": "", "channel": "normal",
	})

	// 客户端读的是顶层 banned (ClientInit.java 第 141 行)
	if v, ok := resp["banned"]; !ok {
		t.Errorf("missing top-level `banned`")
	} else if v != false {
		t.Errorf("expected banned=false, got %v", v)
	}

	// server.{status, message, end_time} 平级 (第 145-149 行)
	srvObj, ok := resp["server"].(map[string]any)
	if !ok {
		t.Fatalf("expected `server` to be an object")
	}
	for _, k := range []string{"status", "message", "end_time"} {
		if _, ok := srvObj[k]; !ok {
			t.Errorf("server.%s missing", k)
		}
	}
	// **不能**有 server.maintenance 这个嵌套对象,否则字段读不到
	if _, has := srvObj["maintenance"]; has {
		t.Errorf("server.maintenance should be flat, not nested")
	}

	// client.{allowed_versions, update_url_normal, update_url_internal_test}
	clientObj, ok := resp["client"].(map[string]any)
	if !ok {
		t.Fatalf("expected `client` object")
	}
	for _, k := range []string{"allowed_versions", "update_url_normal", "update_url_internal_test"} {
		if _, ok := clientObj[k]; !ok {
			t.Errorf("client.%s missing", k)
		}
	}

	// spoof.{fake_version, fake_name} 顶层对象 (第 160-163 行)
	spoofObj, ok := resp["spoof"].(map[string]any)
	if !ok {
		t.Fatalf("expected `spoof` object at top level (not nested under client)")
	}
	for _, k := range []string{"fake_version", "fake_name"} {
		if _, ok := spoofObj[k]; !ok {
			t.Errorf("spoof.%s missing — 客户端读不到伪装信息", k)
		}
	}

	// features.{online_download, offline_package, disabled_message} (第 166-170 行)
	feat, ok := resp["features"].(map[string]any)
	if !ok {
		t.Fatalf("expected `features` object at top level")
	}
	for _, k := range []string{"online_download", "offline_package", "disabled_message"} {
		if _, ok := feat[k]; !ok {
			t.Errorf("features.%s missing — 客户端会读到默认 true,无法关闭下载", k)
		}
	}
}

// TestInit_LatestVersion_DownsendsWhenSet —— 配置了 latest_version 时,
// /client/init 响应的 client 对象中须携带该字段供客户端做软更新提示判断。
func TestInit_LatestVersion_DownsendsWhenSet(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "versions", map[string]any{
		"allowed_versions": []string{"4.0.0"},
		"latest_version":   "4.1.0",
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_lv_set", "signature": "", "channel": "normal",
	})
	clientObj, ok := resp["client"].(map[string]any)
	if !ok {
		t.Fatalf("expected client object")
	}
	if clientObj["latest_version"] != "4.1.0" {
		t.Errorf("client.latest_version: want 4.1.0, got %v", clientObj["latest_version"])
	}
}

// TestInit_LatestVersion_OmittedWhenUnset —— 未配置 latest_version 时,
// client 对象不得携带该字段(空字段省略规则)。
func TestInit_LatestVersion_OmittedWhenUnset(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_lv_unset", "signature": "", "channel": "normal",
	})
	clientObj := resp["client"].(map[string]any)
	if _, has := clientObj["latest_version"]; has {
		t.Errorf("client.latest_version should be omitted when not configured, got %v", clientObj["latest_version"])
	}
}

// TestInit_UpdateAPKSHA256_PerChannel —— 内测渠道单独配置了 sha256 时,
// internal-test 请求应拿到内测 sha256;normal 请求应拿到通用 sha256。
func TestInit_UpdateAPKSHA256_PerChannel(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "versions", map[string]any{
		"allowed_versions":                []string{"4.0.0"},
		"update_apk_sha256":               "sha256-normal",
		"update_apk_sha256_internal_test": "sha256-internal",
	})

	// 内测渠道 → 内测 sha256
	respIT := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_sha_it", "signature": "", "channel": "internal-test",
	})
	clientIT := respIT["client"].(map[string]any)
	if clientIT["update_apk_sha256"] != "sha256-internal" {
		t.Errorf("internal-test channel: want sha256-internal, got %v", clientIT["update_apk_sha256"])
	}

	// 正式渠道 → 通用 sha256
	respN := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_sha_n", "signature": "", "channel": "normal",
	})
	clientN := respN["client"].(map[string]any)
	if clientN["update_apk_sha256"] != "sha256-normal" {
		t.Errorf("normal channel: want sha256-normal, got %v", clientN["update_apk_sha256"])
	}
}

// TestInit_UpdateAPKSHA256_FallbackToCommon —— 内测渠道未单独配置 sha256 时,
// 回退到通用 update_apk_sha256。
func TestInit_UpdateAPKSHA256_FallbackToCommon(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "versions", map[string]any{
		"allowed_versions":  []string{"4.0.0"},
		"update_apk_sha256": "sha256-common",
		// 不设 update_apk_sha256_internal_test
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_sha_fb", "signature": "", "channel": "internal-test",
	})
	clientObj := resp["client"].(map[string]any)
	if clientObj["update_apk_sha256"] != "sha256-common" {
		t.Errorf("fallback: want sha256-common, got %v", clientObj["update_apk_sha256"])
	}
}

// TestInit_FeatureAccountEnabled_DefaultTrue —— features.account_enabled 未配置时
// 客户端应收到 true(向后兼容:无此字段等同于已启用)。
func TestInit_FeatureAccountEnabled_DefaultTrue(t *testing.T) {
	h, _ := newTestHandler(t)
	resp := postJSON(t, newRouter(h), "/client/init", map[string]any{
		"device_id": "dev-ae-1", "version": "4.0.0",
	})
	features, _ := resp["features"].(map[string]any)
	if v, ok := features["account_enabled"]; !ok || v != true {
		t.Fatalf("features.account_enabled 期望 true(默认),实际 %v (ok=%v)", v, ok)
	}
}

// TestInit_FeatureAccountEnabled_FalseWhenConfigured —— 管理员将 account_enabled 设为
// false 时,客户端收到 false,触发跳过登录/存档同步/悬浮窗逻辑。
func TestInit_FeatureAccountEnabled_FalseWhenConfigured(t *testing.T) {
	h, st := newTestHandler(t)
	_ = st.ConfigSet(context.Background(), "features", map[string]any{
		"online_download": true, "offline_package": true, "account_enabled": false,
	})
	resp := postJSON(t, newRouter(h), "/client/init", map[string]any{
		"device_id": "dev-ae-2", "version": "4.0.0",
	})
	features, _ := resp["features"].(map[string]any)
	if v, ok := features["account_enabled"]; !ok || v != false {
		t.Fatalf("features.account_enabled 期望 false,实际 %v (ok=%v)", v, ok)
	}
}

// TestInit_OmitsEmptyOptionalStrings —— 客户端 API 变动说明 §1.1 & §4 强制规定:
// 所有可选字符串字段未设置时必须省略 key,绝不发送 JSON null。Android org.json
// 的 optString 对显式 null 会返回字符串 "null"。
func TestInit_OmitsEmptyOptionalStrings(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_omit", "signature": "", "channel": "normal",
	})

	// server.message 未配置 → 不应该出现
	srvObj := resp["server"].(map[string]any)
	if _, has := srvObj["message"]; has {
		t.Errorf("server.message should be omitted when empty, got %v", srvObj["message"])
	}
	if _, has := srvObj["end_time"]; has {
		t.Errorf("server.end_time should be omitted when 0, got %v", srvObj["end_time"])
	}

	// spoof 全空时,fake_version/fake_name 不应出现(spoof 对象本身可以是空 {})
	spoofObj := resp["spoof"].(map[string]any)
	if _, has := spoofObj["fake_version"]; has {
		t.Errorf("spoof.fake_version should be omitted when empty")
	}
	if _, has := spoofObj["fake_name"]; has {
		t.Errorf("spoof.fake_name should be omitted when empty")
	}

	// client.update_url_* 未配置时不应出现
	clientObj := resp["client"].(map[string]any)
	for _, k := range []string{"update_url_normal", "update_url_internal_test", "update_apk_sha256"} {
		if _, has := clientObj[k]; has {
			t.Errorf("client.%s should be omitted when empty", k)
		}
	}

	// features.disabled_message 未配置时不应出现(online_download/offline_package 是 bool,允许 false)
	featObj := resp["features"].(map[string]any)
	if _, has := featObj["disabled_message"]; has {
		t.Errorf("features.disabled_message should be omitted when empty")
	}
}

// TestInit_ServicesOmittedWhenAllEmpty —— services 三字段全部未配置时,
// 整个 services 对象应当被省略,不发空对象。
func TestInit_ServicesOmittedWhenAllEmpty(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_no_svc", "signature": "", "channel": "normal",
	})
	if _, has := resp["services"]; has {
		t.Errorf("services should be omitted when all three keys are unset, got %v", resp["services"])
	}
}

// TestInit_ServicesPartial —— 只配置 cap_worker_url 时,services 对象只
// 含该一个键;proxy_backends 与 game_server_host 缺席而非空数组/字符串。
func TestInit_ServicesPartial(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "services", map[string]any{
		"cap_worker_url": "https://captcha.example.com",
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_partial", "signature": "", "channel": "normal",
	})
	svc, ok := resp["services"].(map[string]any)
	if !ok {
		t.Fatalf("expected services object, got %T", resp["services"])
	}
	if svc["cap_worker_url"] != "https://captcha.example.com" {
		t.Errorf("cap_worker_url mismatch: %v", svc["cap_worker_url"])
	}
	if _, has := svc["proxy_backends"]; has {
		t.Errorf("proxy_backends should be omitted (empty list), got %v", svc["proxy_backends"])
	}
	if _, has := svc["game_server_host"]; has {
		t.Errorf("game_server_host should be omitted (empty), got %v", svc["game_server_host"])
	}
}

// TestInit_ServicesFull —— 三字段全部配置时,services 对象按文档结构下发。
// 注意 game_server_host 是**纯 host**(2026-05-29 客户端代理回退修复说明),
// 客户端会自动拼 https:// 前缀。
func TestInit_ServicesFull(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "services", map[string]any{
		"cap_worker_url":   "https://captcha.example.com/", // 故意带尾斜杠测试归一化
		"proxy_backends":   []string{"https://proxy1.example.com", "https://proxy2.example.com/"},
		"game_server_host": "totentanz-9b.magica-us.com", // 纯 host
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_full", "signature": "", "channel": "normal",
	})
	svc, ok := resp["services"].(map[string]any)
	if !ok {
		t.Fatalf("expected services object")
	}
	// cap_worker_url:含 scheme,尾斜杠应被去掉
	if svc["cap_worker_url"] != "https://captcha.example.com" {
		t.Errorf("cap_worker_url tail slash not stripped: %v", svc["cap_worker_url"])
	}
	// game_server_host:**不含 scheme**,客户端自己拼
	if svc["game_server_host"] != "totentanz-9b.magica-us.com" {
		t.Errorf("game_server_host should be plain host, got: %v", svc["game_server_host"])
	}
	proxies, ok := svc["proxy_backends"].([]any)
	if !ok {
		t.Fatalf("proxy_backends should be array, got %T", svc["proxy_backends"])
	}
	if len(proxies) != 2 {
		t.Fatalf("expected 2 proxy backends, got %d", len(proxies))
	}
	if proxies[1] != "https://proxy2.example.com" {
		t.Errorf("proxy_backends[1] tail slash not stripped: %v", proxies[1])
	}
}

// TestInit_GameServerHost_BackwardCompat —— 老配置里 game_server_host 误存了
// 完整 URL,服务端应当向后兼容地剥离 scheme + 路径,只把纯 host 下发给客户端
// (避免客户端拼出 https://https://...)。
func TestInit_GameServerHost_BackwardCompat(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	// 模拟老配置:绕过 admin 校验直接落库
	_ = st.ConfigSet(context.Background(), "services", map[string]any{
		"game_server_host": "https://old-style.example.com/path",
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_compat", "signature": "", "channel": "normal",
	})
	svc := resp["services"].(map[string]any)
	if svc["game_server_host"] != "old-style.example.com" {
		t.Errorf("game_server_host should be normalized to plain host, got %v", svc["game_server_host"])
	}
}

// TestInit_GameServerHost_WithPort —— 带端口的 host 必须能正常透传。
func TestInit_GameServerHost_WithPort(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "services", map[string]any{
		"game_server_host": "gameserver.example.com:8443",
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_port", "signature": "", "channel": "normal",
	})
	svc := resp["services"].(map[string]any)
	if svc["game_server_host"] != "gameserver.example.com:8443" {
		t.Errorf("game_server_host with port not preserved: %v", svc["game_server_host"])
	}
}

// TestOfflinePackage_OmitsEmpty —— 未配置离线包时,download_url/package_version/
// sha256 应当全部省略(客户端 optString 拿到 null,会跳过提示离线包入口)。
func TestOfflinePackage_OmitsEmpty(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	tok := initAndGetToken(t, srv, "dev_no_off")
	resp := postJSON(t, srv, "/client/offline-package", map[string]any{
		"device_id": "dev_no_off", "access_token": tok, "signature": "",
	})
	for _, k := range []string{"download_url", "package_version", "sha256"} {
		if _, has := resp[k]; has {
			t.Errorf("offline.%s should be omitted when not configured, got %v", k, resp[k])
		}
	}
}

func TestInit_BanReturns200WithBanFields(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)

	// 先发一条封禁
	expire := time.Now().Add(time.Hour).UnixMilli()
	if err := st.BanInsert(context.Background(), store.Ban{
		ID: "ban_1", DeviceID: "banned_dev", Reason: "测试封禁",
		IssuedAt: time.Now().UnixMilli(), ExpireTime: &expire,
		IssuedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}

	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "banned_dev", "signature": "", "channel": "normal",
	})
	// 客户端 Net.postJson 对 HTTP ≥ 400 会抛 IOException —— 所以封禁必须 HTTP 200
	if resp["banned"] != true {
		t.Errorf("expected banned=true, got %v", resp["banned"])
	}
	if resp["ban_reason"] != "测试封禁" {
		t.Errorf("expected ban_reason='测试封禁', got %v", resp["ban_reason"])
	}
	// expire_time 必须是 Unix **秒**(客户端 BanInfo.java 第 72 行 optLong + 第 575 行 *1000L)
	if ts, ok := resp["expire_time"].(float64); !ok {
		t.Errorf("expire_time should be number, got %T", resp["expire_time"])
	} else if ts < 1_700_000_000 || ts > 2_000_000_000 {
		t.Errorf("expire_time looks like ms not seconds: %v", ts)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// 防改包:APK 签名校验 — cnv-anti-tamper-server-side.md §3
// ─────────────────────────────────────────────────────────────────────────

// postRaw 直接发请求拿原始 ResponseRecorder,用于断言非 200 状态码 / 错误码。
func postRaw(t *testing.T, srv http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// ─────────────────────────────────────────────────────────────────────────
// 版本闸门 force_update — version-not-allowed-followup.md 方案 A
// (客户端 2026-05-30 已确认实现)
// ─────────────────────────────────────────────────────────────────────────

// TestInit_VersionNotAllowed_ReturnsForceUpdate —— 客户端版本不在 allowed_versions
// 列表里时,必须 HTTP 200 + force_update:true,且带上 update_url。**不能** HTTP 4xx
// (会被客户端 Net.postJson 直接抛 IOException,update_url 永远读不到)。
func TestInit_VersionNotAllowed_ReturnsForceUpdate(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "versions", map[string]any{
		"allowed_versions":         []string{"4.0.0", "4.1.0"},
		"update_url_normal":        "https://dl.example.com/normal.apk",
		"update_url_internal_test": "https://dl.example.com/internal.apk",
	})

	// 关键:必须用 postRaw 拿原始状态码,确认是 200 而不是 403。
	w := postRaw(t, srv, "/client/init", map[string]any{
		"version": "3.9.0", "device_id": "dev_old", "signature": "", "channel": "normal",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("version_not_allowed 必须 HTTP 200(否则客户端 Net.postJson 抛 IOException 读不到 body),得到 %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["success"] != false {
		t.Errorf("强制更新分支 success 应当为 false,得到 %v", resp["success"])
	}
	if resp["force_update"] != true {
		t.Errorf("缺少 force_update:true,客户端 ClientInit.parse 不会触发更新流程")
	}
	if resp["current_version"] != "3.9.0" {
		t.Errorf("current_version 应当回显请求的版本,得到 %v", resp["current_version"])
	}
	if resp["update_url_normal"] != "https://dl.example.com/normal.apk" {
		t.Errorf("update_url_normal 缺失或错误: %v", resp["update_url_normal"])
	}
	if resp["update_url_internal_test"] != "https://dl.example.com/internal.apk" {
		t.Errorf("update_url_internal_test 缺失或错误: %v", resp["update_url_internal_test"])
	}
	// **不应**返回 access_token / server / client / spoof / features
	// (客户端 doc 明确:force_update=true 时不要继续读这些字段)
	for _, k := range []string{"access_token", "server", "client", "spoof", "features"} {
		if _, has := resp[k]; has {
			t.Errorf("force_update 响应不应该携带 %s(客户端不会读)", k)
		}
	}
}

// TestInit_VersionNotAllowed_OmitsMissingURLs —— 没配 update_url 时,
// 那些键省略不发(避免客户端 optString 拿到 "" 误以为有值)。
func TestInit_VersionNotAllowed_OmitsMissingURLs(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "versions", map[string]any{
		"allowed_versions": []string{"4.0.0"},
		// 故意不设 update_url_*
	})
	w := postRaw(t, srv, "/client/init", map[string]any{
		"version": "3.9.0", "device_id": "dev_old2", "signature": "", "channel": "normal",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["force_update"] != true {
		t.Fatalf("force_update missing: %v", resp)
	}
	for _, k := range []string{"update_url_normal", "update_url_internal_test"} {
		if _, has := resp[k]; has {
			t.Errorf("%s 未配置时应当省略,得到 %v", k, resp[k])
		}
	}
}

// TestInit_VersionAllowed_NoForceUpdate —— 在白名单内的版本走正常流程,
// force_update 不应出现,access_token 应该签发。
func TestInit_VersionAllowed_NoForceUpdate(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "versions", map[string]any{
		"allowed_versions": []string{"4.0.0", "4.1.0"},
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.1.0", "device_id": "dev_ok_ver", "signature": "", "channel": "normal",
	})
	if _, has := resp["force_update"]; has {
		t.Errorf("合法版本响应不应该带 force_update,得到 %v", resp["force_update"])
	}
	if _, ok := resp["access_token"].(string); !ok {
		t.Errorf("合法版本应当签发 access_token,响应=%v", resp)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// 离线包版本策略 offline_pack.min_version
// (server-offline-pack-validation.md §3.1)
// ─────────────────────────────────────────────────────────────────────────

// TestInit_OfflinePack_OmittedWhenUnset —— 未配置 min_version 时整个
// offline_pack 对象不下发。客户端会跳过版本检查,不弹窗、不阻断启动。
func TestInit_OfflinePack_OmittedWhenUnset(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_op_unset", "signature": "", "channel": "normal",
	})
	if _, has := resp["offline_pack"]; has {
		t.Errorf("offline_pack 未配置时应当省略整个对象,得到 %v", resp["offline_pack"])
	}
}

// TestInit_OfflinePack_DownsendsMinVersion —— 配置了 min_version 后,
// /client/init 响应顶层带 offline_pack:{min_version}。
func TestInit_OfflinePack_DownsendsMinVersion(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "offline_pack", map[string]any{
		"min_version": "20250501",
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_op_set", "signature": "", "channel": "normal",
	})
	op, ok := resp["offline_pack"].(map[string]any)
	if !ok {
		t.Fatalf("offline_pack 应当是对象,得到 %T: %v", resp["offline_pack"], resp["offline_pack"])
	}
	if op["min_version"] != "20250501" {
		t.Errorf("min_version 应当为 20250501,得到 %v", op["min_version"])
	}
}

// TestInit_OfflinePack_EmptyMinVersion_StillOmits —— 显式存了空串
// min_version 时(运维曾设过策略后又清空)也应当当作"不下发"处理。
func TestInit_OfflinePack_EmptyMinVersion_StillOmits(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	_ = st.ConfigSet(context.Background(), "offline_pack", map[string]any{
		"min_version": "  ", // 全空白
	})
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_op_empty", "signature": "", "channel": "normal",
	})
	if _, has := resp["offline_pack"]; has {
		t.Errorf("min_version 为空时 offline_pack 应当省略,得到 %v", resp["offline_pack"])
	}
}

// TestOfflinePackage_StillReturnsSha256 —— 离线包元数据接口已有返回
// {download_url, package_version, sha256, size},新设计客户端会用这些做实时
// 校验。重申字段名以防回归。
func TestOfflinePackage_StillReturnsRequiredFields(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	uploaded := time.Now().UnixMilli()
	_ = st.OfflinePackageSet(context.Background(), store.OfflinePackage{
		DownloadURL:    "https://cdn.example.com/pack/cn_offline_20250501.zip",
		PackageVersion: "20250501",
		SHA256:         "a3f1c2d4e5b67890123456789012345678901234567890123456789012345678",
		Size:           4096,
		UploadedAt:     &uploaded,
	})
	tok := initAndGetToken(t, srv, "dev_pack_meta")
	resp := postJSON(t, srv, "/client/offline-package", map[string]any{
		"device_id": "dev_pack_meta", "access_token": tok, "signature": "",
	})
	want := map[string]any{
		"download_url":    "https://cdn.example.com/pack/cn_offline_20250501.zip",
		"package_version": "20250501",
		"sha256":          "a3f1c2d4e5b67890123456789012345678901234567890123456789012345678",
	}
	for k, v := range want {
		if resp[k] != v {
			t.Errorf("offline-package.%s: want %v, got %v", k, v, resp[k])
		}
	}
}

// TestInit_RejectsEmptySignatureWhenWhitelistSet —— 配了白名单后,空 signature
// 必须拒发会话(防改包客户端拿不到签名时发空串就直接放行)。
func TestInit_RejectsEmptySignatureWhenWhitelistSet(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SignatureAllowed = []string{"abcdef0123456789"}
	srv := newRouter(h)
	w := postRaw(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_empty_sig", "signature": "", "channel": "normal",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("空 signature 应当 403,得到 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "signature_rejected") {
		t.Errorf("响应应当含 signature_rejected,得到 %s", w.Body.String())
	}
}

// TestInit_RejectsWrongSignature —— 不在白名单的 signature 必须拒。
func TestInit_RejectsWrongSignature(t *testing.T) {
	h, _ := newTestHandler(t)
	h.SignatureAllowed = []string{"abcdef0123456789"}
	srv := newRouter(h)
	w := postRaw(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_bad_sig",
		"signature": "0000000000000000", "channel": "normal",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("不在白名单的 signature 应当 403,得到 %d", w.Code)
	}
}

// TestInit_AcceptsWhitelistedSignature —— 在白名单的 signature 应当签发 token。
func TestInit_AcceptsWhitelistedSignature(t *testing.T) {
	h, _ := newTestHandler(t)
	const good = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	h.SignatureAllowed = []string{good}
	srv := newRouter(h)
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_ok_sig",
		"signature": good, "channel": "normal",
	})
	if _, ok := resp["access_token"].(string); !ok {
		t.Errorf("白名单签名应当签发 access_token,响应=%v", resp)
	}
}

// TestInit_RequireSignatureFlagBlocksEmptyEvenWithoutWhitelist —— 没有白名单时,
// RequireSignature=true 也能堵住空 signature 的改包客户端。
func TestInit_RequireSignatureFlagBlocksEmptyEvenWithoutWhitelist(t *testing.T) {
	h, _ := newTestHandler(t)
	h.RequireSignature = true
	srv := newRouter(h)
	w := postRaw(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_strict_empty", "signature": "", "channel": "normal",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("RequireSignature=true 时空 signature 应当 403,得到 %d", w.Code)
	}
}

// TestInit_ChannelWhitelist —— 不在白名单的 channel 拒。
func TestInit_ChannelWhitelist(t *testing.T) {
	h, _ := newTestHandler(t)
	h.ChannelAllowed = []string{"normal", "internal-test"}
	srv := newRouter(h)
	w := postRaw(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_bad_ch", "signature": "", "channel": "thirdparty",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("非法 channel 应当 403,得到 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "channel_rejected") {
		t.Errorf("响应应当含 channel_rejected,得到 %s", w.Body.String())
	}
}

// TestRequireClientSession_SignatureChangedMidSession —— 后续 /client/* 请求
// 的 signature 必须与 init 时校验通过的那个一致;变了就视为会话被劫持或客户端
// 被换包,作废 session。
func TestRequireClientSession_SignatureChangedMidSession(t *testing.T) {
	h, _ := newTestHandler(t)
	const good = "feedface00000000feedface00000000feedface00000000feedface00000000"
	h.SignatureAllowed = []string{good}
	srv := newRouter(h)
	// 1) 用合法签名握手
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_mid_change",
		"signature": good, "channel": "normal",
	})
	tok := resp["access_token"].(string)

	// 2) 后续请求换成不同签名(模拟换包客户端 / 中间人篡改)
	w := postRaw(t, srv, "/client/method-select", map[string]any{
		"device_id": "dev_mid_change", "access_token": tok,
		"signature": "deadbeef00000000deadbeef00000000deadbeef00000000deadbeef00000000",
		"method":    "online",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("中途换 signature 应当 403,得到 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "signature_rejected") {
		t.Errorf("响应应当含 signature_rejected,得到 %s", w.Body.String())
	}

	// 3) session 已作废:即便回到正确签名也用不了(必须重新握手)
	w2 := postRaw(t, srv, "/client/method-select", map[string]any{
		"device_id": "dev_mid_change", "access_token": tok,
		"signature": good, "method": "online",
	})
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("作废后再用应当 401,得到 %d", w2.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// authTriple 鉴权 — ClientInit.authTriple() 第 316-321 行
// ─────────────────────────────────────────────────────────────────────────

func TestAuthTriple_Required(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	// /method-select 不带 access_token 应当 401
	buf, _ := json.Marshal(map[string]any{
		"device_id": "dev_x", "signature": "", "method": "online",
	})
	req := httptest.NewRequest(http.MethodPost, "/client/method-select", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when access_token missing, got %d", w.Code)
	}
}

func TestAuthTriple_WrongDevice(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	// 先 init 拿一个 token,然后用别的 device_id 发请求应当 401
	initResp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_a", "signature": "", "channel": "normal",
	})
	tok := initResp["access_token"].(string)
	buf, _ := json.Marshal(map[string]any{
		"device_id": "dev_b", "access_token": tok, "signature": "", "method": "online",
	})
	req := httptest.NewRequest(http.MethodPost, "/client/method-select", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when token bound to different device, got %d", w.Code)
	}
}

// initAndGetToken 走一次握手拿 access_token,供后续测试复用。
func initAndGetToken(t *testing.T, srv http.Handler, deviceID string) string {
	t.Helper()
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": deviceID, "signature": "", "channel": "normal",
	})
	tok, _ := resp["access_token"].(string)
	if tok == "" {
		t.Fatalf("/client/init 没有签发 access_token")
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────
// /client/online-download — ClientInit.fetchOnlineDownload 第 198-265 行
// ─────────────────────────────────────────────────────────────────────────

func TestOnlineDownload_GroupsShape(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	tok := initAndGetToken(t, srv, "dev_dl")
	resp := postJSON(t, srv, "/client/online-download", map[string]any{
		"device_id": "dev_dl", "access_token": tok, "signature": "",
	})
	if _, ok := resp["resource_token"].(string); !ok {
		t.Errorf("resource_token missing or wrong type")
	}
	// 客户端 ClientInit.java 第 205 行优先读 groups,fallback 才看 mirrors
	groups, ok := resp["groups"].([]any)
	if !ok {
		t.Fatalf("groups should be array; got %T", resp["groups"])
	}
	// 至少要有主节点本地这一组(PrimaryResBaseURL 在测试 Handler 里注入了)
	if len(groups) == 0 {
		t.Fatalf("expected at least 1 group (主节点本地), got 0")
	}
	for _, g := range groups {
		gObj := g.(map[string]any)
		if _, ok := gObj["name"].(string); !ok {
			t.Errorf("group.name should be string")
		}
		if _, ok := gObj["mirrors"].([]any); !ok {
			t.Errorf("group.mirrors should be array")
		}
	}
}

// TestOnlineDownload_InlineFiles 验证管理员配的内联 files 清单能正确出现在
// 响应里:mirrors 数组的对象项 + files 字段(可以是 [key_string] 或
// [{key,size}],客户端两种都支持)。
func TestOnlineDownload_InlineFiles(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)

	// 准备:两个组,每组一个 mirror,各带内联 files
	err := st.MirrorsReplaceAll(context.Background(), []store.MirrorWithGroup{
		{
			GroupName: "线路A",
			Mirror: store.Mirror{
				Kind: "http", URL: "https://a.example.com/res/",
				Files: jsonRaw(`[{"key":"data/main.bin","size":1024},{"key":"data/aux.bin","size":2048}]`),
			},
		},
		{
			GroupName: "线路B",
			Mirror: store.Mirror{
				Kind: "s3", URL: "https://s3.example.com",
				Bucket: strPtr("res-bucket"), Region: strPtr("ap-east-1"),
				// 这条没有 files → 客户端走 S3 XML 自发现
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tok := initAndGetToken(t, srv, "dev_inline")
	resp := postJSON(t, srv, "/client/online-download", map[string]any{
		"device_id": "dev_inline", "access_token": tok, "signature": "",
	})

	groups := resp["groups"].([]any)
	// 至少 2 组(管理员配的)+ 1 组(主节点本地)
	if len(groups) < 2 {
		t.Fatalf("expected at least 2 admin groups, got %d", len(groups))
	}

	// 找"线路A"组,验证其内联 files
	var aGroup map[string]any
	for _, g := range groups {
		obj := g.(map[string]any)
		if obj["name"] == "线路A" {
			aGroup = obj
			break
		}
	}
	if aGroup == nil {
		t.Fatalf("线路A group not found in response, groups=%v", groups)
	}
	mirrors := aGroup["mirrors"].([]any)
	if len(mirrors) != 1 {
		t.Fatalf("线路A should have 1 mirror, got %d", len(mirrors))
	}
	// 应该是对象格式(因为带 files),不是字符串
	mObj, ok := mirrors[0].(map[string]any)
	if !ok {
		t.Fatalf("mirror with files should be JSON object, got %T", mirrors[0])
	}
	if mObj["url"] != "https://a.example.com/res/" {
		t.Errorf("mirror url mismatch: %v", mObj["url"])
	}
	files, ok := mObj["files"].([]any)
	if !ok {
		t.Fatalf("mirror.files should be array, got %T", mObj["files"])
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
	// 第一个文件应该是 {key, size} 对象
	f0 := files[0].(map[string]any)
	if f0["key"] != "data/main.bin" {
		t.Errorf("file[0].key: want data/main.bin, got %v", f0["key"])
	}
	if int64(f0["size"].(float64)) != 1024 {
		t.Errorf("file[0].size: want 1024, got %v", f0["size"])
	}

	// "线路B"组:mirror 没 files,所以应该是字符串
	var bGroup map[string]any
	for _, g := range groups {
		obj := g.(map[string]any)
		if obj["name"] == "线路B" {
			bGroup = obj
			break
		}
	}
	if bGroup == nil {
		t.Fatalf("线路B group not found")
	}
	bMirrors := bGroup["mirrors"].([]any)
	if _, isString := bMirrors[0].(string); !isString {
		t.Errorf("线路B 没有 files 的 mirror 应当返回为字符串(让客户端走 S3 自发现),got %T", bMirrors[0])
	}
}

// helpers for the inline-files test
func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }
func strPtr(s string) *string           { return &s }

// ─────────────────────────────────────────────────────────────────────────
// /client/offline-package — ClientInit.fetchOfflinePackage 第 273-279 行
// ─────────────────────────────────────────────────────────────────────────

func TestOfflinePackage_FieldNames(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	uploaded := time.Now().UnixMilli()
	_ = st.OfflinePackageSet(context.Background(), store.OfflinePackage{
		DownloadURL:    "https://offline.example.com/full.zip",
		PackageVersion: "2.4.1",
		SHA256:         "abc123",
		Size:           12345,
		UploadedAt:     &uploaded,
	})
	tok := initAndGetToken(t, srv, "dev_off")
	resp := postJSON(t, srv, "/client/offline-package", map[string]any{
		"device_id": "dev_off", "access_token": tok, "signature": "",
	})
	// 客户端 optString 的字段名(第 274-276 行)
	cases := map[string]any{
		"download_url":    "https://offline.example.com/full.zip",
		"package_version": "2.4.1",
		"sha256":          "abc123",
	}
	for k, want := range cases {
		if resp[k] != want {
			t.Errorf("offline.%s: want %v, got %v", k, want, resp[k])
		}
	}
	// 旧字段名不应该再返回
	for _, oldK := range []string{"url", "version"} {
		if _, has := resp[oldK]; has {
			t.Errorf("offline.%s should not be present (旧字段名)", oldK)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// /client/heartbeat — ResourceFlow.HeartbeatSender 第 528-577 行
// ─────────────────────────────────────────────────────────────────────────

func TestHeartbeat_Ok(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	tok := initAndGetToken(t, srv, "dev_hb")
	resp := postJSON(t, srv, "/client/heartbeat", map[string]any{
		"device_id": "dev_hb", "access_token": tok, "signature": "",
		"files": []map[string]any{{
			"name": "a.bin", "status": "downloading", "percent": 42.0, "speed_bps": 1024 * 1024,
		}},
	})
	if resp["action"] != "ok" {
		t.Errorf("expected action=ok, got %v", resp["action"])
	}
}

func TestHeartbeat_SwitchMirrors_AssignmentsShape(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	tok := initAndGetToken(t, srv, "dev_sw")
	// 入队一条换线指令
	h.Heartbeats.QueueSwitch("dev_sw", &SwitchAssignment{
		MirrorAssignments: map[string]string{
			"a.bin": "https://m1/",
			"b.bin": "https://m1/",
			"c.bin": "https://m2/",
		},
		Message: "切到 m1/m2",
	})
	resp := postJSON(t, srv, "/client/heartbeat", map[string]any{
		"device_id": "dev_sw", "access_token": tok, "signature": "",
		"files": []map[string]any{{"name": "a.bin", "status": "downloading"}},
	})
	if resp["action"] != "switch_mirrors" {
		t.Fatalf("expected switch_mirrors, got %v", resp["action"])
	}
	// 客户端期望 `assignments: [{mirror, files:[name]}]`,不是 `mirror_assignments` map
	if _, badShape := resp["mirror_assignments"]; badShape {
		t.Errorf("response should NOT contain mirror_assignments (老字段名),客户端读不到")
	}
	arr, ok := resp["assignments"].([]any)
	if !ok {
		t.Fatalf("assignments should be array, got %T", resp["assignments"])
	}
	if len(arr) != 2 {
		t.Errorf("expected 2 mirror groups, got %d", len(arr))
	}
	for _, e := range arr {
		obj := e.(map[string]any)
		if _, ok := obj["mirror"].(string); !ok {
			t.Errorf("each assignment needs `mirror` string field, got %v", obj)
		}
		if _, ok := obj["files"].([]any); !ok {
			t.Errorf("each assignment needs `files` array, got %v", obj["files"])
		}
	}
}

func TestHeartbeat_Ban_FieldsAtTopLevel(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	tok := initAndGetToken(t, srv, "dev_ban")
	// 现在补一条封禁(init 完成后才发,模拟运行中被踢)
	expire := time.Now().Add(2 * time.Hour).UnixMilli()
	_ = st.BanInsert(context.Background(), store.Ban{
		ID: "ban_hb", DeviceID: "dev_ban", Reason: "脚本检测",
		IssuedAt: time.Now().UnixMilli(), ExpireTime: &expire,
		IssuedBy: "system",
	})
	resp := postJSON(t, srv, "/client/heartbeat", map[string]any{
		"device_id": "dev_ban", "access_token": tok, "signature": "",
	})
	if resp["action"] != "ban" {
		t.Fatalf("expected action=ban, got %v", resp["action"])
	}
	// 客户端读顶层 reason / expire_time(秒)—— ResourceFlow.java 第 566-575 行
	if resp["reason"] != "脚本检测" {
		t.Errorf("expected reason='脚本检测' at top level, got %v", resp["reason"])
	}
	ts, ok := resp["expire_time"].(float64)
	if !ok {
		t.Fatalf("expire_time at top level required, got %T", resp["expire_time"])
	}
	if ts < 1_700_000_000 || ts > 2_000_000_000 {
		t.Errorf("expire_time must be Unix seconds (got %v looks like ms)", ts)
	}
	// 不应该再有嵌套的 ban 对象
	if _, has := resp["ban"]; has {
		t.Errorf("response should NOT nest ban fields under `ban` (老格式),客户端只读顶层")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// /client/hot-update — ClientInit.fetchHotUpdate 第 293-310 行
// ─────────────────────────────────────────────────────────────────────────

func TestHotUpdate_BundleFields(t *testing.T) {
	h, st := newTestHandler(t)
	srv := newRouter(h)
	tok := initAndGetToken(t, srv, "dev_hot")
	now := time.Now().UnixMilli()
	_ = st.HotBundleSet(context.Background(), store.HotBundle{
		Kind: "js", Version: 42, SHA256: "deadbeef",
		DownloadURL: "https://hot.example.com/js.zip", Size: 999, PublishedAt: &now,
	})
	resp := postJSON(t, srv, "/client/hot-update", map[string]any{
		"device_id": "dev_hot", "access_token": tok, "signature": "",
	})
	js, ok := resp["js"].(map[string]any)
	if !ok {
		t.Fatalf("js section missing")
	}
	// 客户端字段名(第 297-300 行)
	want := map[string]any{
		"version":      float64(42),
		"sha256":       "deadbeef",
		"download_url": "https://hot.example.com/js.zip",
		"size":         float64(999),
	}
	for k, v := range want {
		if js[k] != v {
			t.Errorf("hot.js.%s: want %v, got %v", k, v, js[k])
		}
	}
}
