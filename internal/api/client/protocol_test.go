// Package client 的协议保真测试。
//
// **字段以架构协议文档(magirecocn-architecture-protocol-document)为唯一真理。**
// 早期版本以 Android 客户端的 Java 源码为锚点,该锚点已随 Android 端弃维而失效。
//
// 这套测试的职责有两面,缺一不可:
//
//   - **正向**:新协议要求的字段确实出现在约定的位置;
//   - **反向**:Android 专有字段确实**不再**出现——哪怕对应的 config 仍留在库里。
//     少了反向断言,删字段这件事就没有回归保护,下次重构很容易把它们捡回来。
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

	"magirecocn-revival/api-server/internal/clienttoken"
	"magirecocn-revival/api-server/internal/resourceauth"
	"magirecocn-revival/api-server/internal/store"
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

	// 会话令牌签发方/校验方是必配项:本服务端只接受自包含签名令牌,
	// 不配的话 /client/init 会直接 500(失败关闭),整套协议测试都跑不起来。
	iss, err := clienttoken.NewIssuer("test-node", localSeed)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	ver, err := clienttoken.NewVerifier(map[string]string{iss.ID(): iss.PublicKeyHex()})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	return &Handler{
		St:                  st,
		TokenIssuer:         iss,
		TokenVerifier:       ver,
		ResourceTokenSecret: []byte("test-secret-32-bytes-12345678"),
		SignatureAllowed:    nil, // 空白名单 = 放行
		TokenWindowSec:      300,
		ClientSessionTTL:    time.Hour,
		Heartbeats:          NewHeartbeats(),
	}, st
}

func newRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/client", h.Routes)
	return r
}

// postJSONRaw 发 JSON、读 JSON,返回状态码而不在非 200 时 t.Fatal,
// 供断言错误响应的用例使用。
func postJSONRaw(t *testing.T, srv http.Handler, path string, body any) (int, map[string]any) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// authTripleFor 先走一次 /client/init 拿到会话，返回后续端点所需的 authTriple。
func authTripleFor(t *testing.T, h *Handler, srv http.Handler, deviceID string) map[string]any {
	t.Helper()
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "1.0.0", "device_id": deviceID, "signature": "", "channel": "normal",
	})
	tok, _ := resp["access_token"].(string)
	if tok == "" {
		t.Fatalf("init 未返回 access_token")
	}
	return map[string]any{"device_id": deviceID, "access_token": tok, "signature": ""}
}

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
// /client/init — 握手
// ─────────────────────────────────────────────────────────────────────────

// TestInit_RequestFields —— 最小握手:只给 version/device_id/signature/channel,
// 不带 protocol_versions(老客户端),应当照常签发会话。
func TestInit_RequestFields(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)

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

	ctx := context.Background()
	_ = st.ConfigSet(ctx, "server", map[string]any{
		"status": "ok", "message": "测试提示", "end_time": int64(1_770_000_000),
	})
	_ = st.ConfigSet(ctx, "features", map[string]any{
		"account_enabled": true, "disabled_message": "下载暂停",
	})
	// versions 这组 config 仍可被管理后台写入,但新协议下 /client/init 不再读它。
	// 种进去正是为了确认它**不会**泄漏成响应字段(见文末的 Android 专有字段断言)。
	_ = st.ConfigSet(ctx, "versions", map[string]any{
		"allowed_versions":  []string{"4.0.0"},
		"update_url_normal": "https://example.invalid/app.apk",
		"latest_version":    "9.9.9",
	})

	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "4.0.0", "device_id": "dev_shape", "signature": "", "channel": "normal",
		"protocol_versions": []int{1},
	})

	// ── 新协议要求存在的顶层字段 ──────────────────────────────────────
	for _, k := range []string{
		"success", "banned", "protocol_version", "protocol_versions", "access_token",
		"server_time_at", "server", "features", "asset_auth",
	} {
		if _, ok := resp[k]; !ok {
			t.Errorf("顶层缺少 %q", k)
		}
	}
	if got := resp["protocol_version"]; got != float64(ProtocolVersion) {
		t.Errorf("protocol_version = %v, want %d", got, ProtocolVersion)
	}
	if ts, ok := resp["server_time_at"].(float64); !ok || ts <= 0 {
		t.Errorf("server_time_at 应为正的 Unix 秒, got %v", resp["server_time_at"])
	}
	// protocol_versions 是服务端支持的全集,且必须包含本次协商结果——
	// 否则客户端无从判断"升到哪一版还能继续对话"。
	pvs, ok := resp["protocol_versions"].([]any)
	if !ok || len(pvs) == 0 {
		t.Fatalf("protocol_versions 应为非空数组, got %v", resp["protocol_versions"])
	}
	found := false
	for _, v := range pvs {
		if v == float64(ProtocolVersion) {
			found = true
		}
	}
	if !found {
		t.Errorf("protocol_versions %v 未包含协商结果 %d", pvs, ProtocolVersion)
	}

	// ── asset_auth 信封:必有 type 判别字段 ───────────────────────────
	aa, ok := resp["asset_auth"].(map[string]any)
	if !ok {
		t.Fatalf("asset_auth 应为对象, got %v", resp["asset_auth"])
	}
	if aa["type"] != "bearer" {
		t.Errorf("asset_auth.type = %v, want bearer", aa["type"])
	}
	for _, k := range []string{"token", "expires_at"} {
		if _, ok := aa[k]; !ok {
			t.Errorf("asset_auth.%s 缺失", k)
		}
	}
	// expires_at 是 **Unix 秒**,与 server_time_at / expire_time / end_time 一致。
	// 旧实现返回毫秒,量级断言正是为了防止那种单位回潮——毫秒值会大三个数量级。
	if exp, ok := aa["expires_at"].(float64); !ok || exp < 1.7e9 || exp > 2.0e9 {
		t.Errorf("asset_auth.expires_at 应为 Unix 秒(1.7e9~2.0e9), got %v", aa["expires_at"])
	}

	// ── server / client / features ────────────────────────────────────
	srvObj, ok := resp["server"].(map[string]any)
	if !ok {
		t.Fatalf("server 应为对象")
	}
	for _, k := range []string{"status", "message", "end_time"} {
		if _, ok := srvObj[k]; !ok {
			t.Errorf("server.%s 缺失", k)
		}
	}
	feat, ok := resp["features"].(map[string]any)
	if !ok {
		t.Fatalf("features 应为对象")
	}
	for _, k := range []string{"account_enabled", "disabled_message"} {
		if _, ok := feat[k]; !ok {
			t.Errorf("features.%s 缺失", k)
		}
	}

	// ── Android 专有字段必须已经消失 ──────────────────────────────────
	// 留在响应里会误导 Web 客户端,也会让协议文档与实现脱节。
	// 注意上面已把 versions 配置种进库:这些断言证明的是"配置存在也不下发",
	// 而不是"恰好没配所以看不见"。
	for _, k := range []string{"spoof", "offline_pack", "client", "force_update"} {
		if _, ok := resp[k]; ok {
			t.Errorf("顶层不应再出现 %q(Android 专有)", k)
		}
	}
	for _, k := range []string{"online_download", "offline_package"} {
		if _, ok := feat[k]; ok {
			t.Errorf("features.%s 不应再下发(整包资源准备)", k)
		}
	}
}

// TestInit_UnlistedVersionStillHandshakes —— APK 版本闸门已移除:
// 即便 allowed_versions 里没有客户端上报的版本号,握手也照常完成,
// 不再返回 force_update / update_url_*。浏览器自行更新,无 APK 可推。
func TestInit_UnlistedVersionStillHandshakes(t *testing.T) {
	h, st := newTestHandler(t)
	_ = st.ConfigSet(context.Background(), "versions", map[string]any{
		"allowed_versions":  []string{"4.0.0"},
		"update_url_normal": "https://example.invalid/app.apk",
	})
	resp := postJSON(t, newRouter(h), "/client/init", map[string]any{
		"device_id": "dev_oldver", "version": "1.0.0-不在白名单",
	})
	if resp["success"] != true {
		t.Fatalf("版本不在白名单也应握手成功, got %v", resp)
	}
	if _, ok := resp["access_token"].(string); !ok {
		t.Errorf("应照常签发 access_token, got %v", resp["access_token"])
	}
	for _, k := range []string{"force_update", "update_url_normal", "current_version"} {
		if _, has := resp[k]; has {
			t.Errorf("不应再下发 %q(APK 更新流程已移除)", k)
		}
	}
}
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

// TestInit_OmitsEmptyOptionalStrings —— 可选字符串字段未设置时必须省略 key,
// 不发送 JSON null:"字段缺席"与"字段为空"在客户端是两种不同的判断。
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

	// spoof 已随 Android 弃维移除:无论是否配置都不应出现。
	if _, has := resp["spoof"]; has {
		t.Errorf("spoof 不应再下发(Android 原生引擎版本伪造,Web 端无此概念)")
	}

	// client 子对象整体已移除(它只承载 APK 版本闸门与更新地址)。
	if _, has := resp["client"]; has {
		t.Errorf("client 子对象不应再下发(APK 版本闸门/更新地址)")
	}

	// features.disabled_message 未配置时不应出现(account_enabled 是 bool,允许 false)
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

// TestInit_BanReturns200WithBanFields —— 封禁是业务结果而非传输错误:
// 必须 HTTP 200,且 ban_reason / expire_time 平铺在顶层。
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
	// 封禁必须 HTTP 200:客户端要读到 body 里的理由与到期时间才能提示玩家
	if resp["banned"] != true {
		t.Errorf("expected banned=true, got %v", resp["banned"])
	}
	if resp["ban_reason"] != "测试封禁" {
		t.Errorf("expected ban_reason='测试封禁', got %v", resp["ban_reason"])
	}
	// expire_time 必须是 Unix **秒**,不是毫秒
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
// 客户端签名白名单
// ─────────────────────────────────────────────────────────────────────────

// TestInit_RejectsEmptySignatureWhenWhitelistSet —— 配了白名单却收到空 signature
// 时必须拒绝:空串必然不在白名单,放行等于让白名单形同虚设。
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
	w := postRaw(t, srv, "/client/heartbeat", map[string]any{
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
	w2 := postRaw(t, srv, "/client/heartbeat", map[string]any{
		"device_id": "dev_mid_change", "access_token": tok,
		"signature": good, "method": "online",
	})
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("作废后再用应当 401,得到 %d", w2.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// authTriple 鉴权 — {device_id, access_token, signature}
// ─────────────────────────────────────────────────────────────────────────

func TestAuthTriple_Required(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	// 受保护端点不带 access_token 应当 401
	buf, _ := json.Marshal(map[string]any{
		"device_id": "dev_x", "signature": "",
	})
	req := httptest.NewRequest(http.MethodPost, "/client/heartbeat", bytes.NewReader(buf))
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
	req := httptest.NewRequest(http.MethodPost, "/client/heartbeat", bytes.NewReader(buf))
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
// /client/heartbeat — 封禁 / 维护状态推送
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
	// reason / expire_time(秒)平铺在顶层,不嵌套在 ban 对象里
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
// 新协议:版本协商 / asset_auth 信封 / 场景清单
// ─────────────────────────────────────────────────────────────────────────

// 老客户端不上报 protocol_versions,应按当前版本放行(向后兼容)。
func TestInit_ProtocolNegotiation_OmittedDefaultsToCurrent(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "1.0.0", "device_id": "dev_pv_omit", "signature": "", "channel": "normal",
	})
	if got := resp["protocol_version"]; got != float64(ProtocolVersion) {
		t.Errorf("protocol_version = %v, want %d", got, ProtocolVersion)
	}
}

// 客户端支持多版本时,应在交集中选出服务端实现的那个。
func TestInit_ProtocolNegotiation_PicksIntersection(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "1.0.0", "device_id": "dev_pv_multi", "signature": "", "channel": "normal",
		"protocol_versions": []int{99, ProtocolVersion, 100},
	})
	if got := resp["protocol_version"]; got != float64(ProtocolVersion) {
		t.Errorf("protocol_version = %v, want %d", got, ProtocolVersion)
	}
}

// 无交集必须**握手失败**,不得降级——降级会让双方以不同的协议理解同一份数据。
func TestInit_ProtocolNegotiation_NoIntersectionFails(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	body, _ := json.Marshal(map[string]any{
		"version": "1.0.0", "device_id": "dev_pv_bad", "signature": "", "channel": "normal",
		"protocol_versions": []int{98, 99},
	})
	req := httptest.NewRequest(http.MethodPost, "/client/init", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("期望 HTTP 400, 实际 %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["error"] != "protocol_version_unsupported" {
		t.Errorf("error = %v, want protocol_version_unsupported", out["error"])
	}
}

// asset_auth.token 必须与 resource_token 的签名机制一致(可被服务端自行验证)。
func TestInit_AssetAuthTokenIsVerifiable(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	resp := postJSON(t, srv, "/client/init", map[string]any{
		"version": "1.0.0", "device_id": "dev_aa", "signature": "", "channel": "normal",
	})
	aa, ok := resp["asset_auth"].(map[string]any)
	if !ok {
		t.Fatalf("asset_auth 缺失")
	}
	tok, _ := aa["token"].(string)
	want, _ := h.signResourceToken("dev_aa")
	if tok == "" || tok != want {
		t.Errorf("asset_auth.token 与 signResourceToken 不一致\n got=%q\nwant=%q", tok, want)
	}

	// 上面那条只证明"init 下发的就是签发方算出来的",两边一起错也照样通过。
	// 真正的契约是**边缘节点验得过**——那边只有密钥,没有本进程的任何状态。
	// 所以照着校验方的样子独立验一次,并核对令牌里带回的设备。
	dev, err := resourceauth.Verifier{
		Secret:    h.ResourceTokenSecret,
		WindowSec: h.TokenWindowSec,
	}.Verify(tok, time.Now())
	if err != nil {
		t.Fatalf("asset_auth.token 在校验方(资源分发服务端边缘节点)验不过: %v", err)
	}
	if dev != "dev_aa" {
		t.Errorf("令牌里的设备 = %q, want dev_aa", dev)
	}
}

// 场景清单未接入构建管线时必须**明确报错**,而不是返回空清单——
// 空清单会被客户端理解为"该场景无需任何资产",从而静默进入残缺场景。
func TestSceneManifest_UnavailableWhenNotWired(t *testing.T) {
	h, _ := newTestHandler(t)
	h.DevMode = true // 开着开发模式,才能测到"没接清单"而不是被生产守卫拦下
	srv := newRouter(h)
	triple := authTripleFor(t, h, srv, "dev_sm_off")
	body := map[string]any{"scene_id": "quest_101101"}
	for k, v := range triple {
		body[k] = v
	}
	code, out := postJSONRaw(t, srv, "/client/scene-manifest", body)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("期望 HTTP 503, 实际 %d: %v", code, out)
	}
	if out["error"] != "manifest_unavailable" {
		t.Errorf("error = %v, want manifest_unavailable", out["error"])
	}
}

func TestSceneManifest_RequiresSceneID(t *testing.T) {
	h, _ := newTestHandler(t)
	h.DevMode = true
	h.SceneAssets = func(context.Context, string) ([]string, error) { return nil, nil }
	srv := newRouter(h)
	triple := authTripleFor(t, h, srv, "dev_sm_noid")
	code, out := postJSONRaw(t, srv, "/client/scene-manifest", triple)
	if code != http.StatusBadRequest {
		t.Fatalf("期望 HTTP 400, 实际 %d: %v", code, out)
	}
	if out["error"] != "missing_scene_id" {
		t.Errorf("error = %v, want missing_scene_id", out["error"])
	}
}

func TestSceneManifest_UnknownSceneIs404(t *testing.T) {
	h, _ := newTestHandler(t)
	h.DevMode = true
	h.SceneAssets = func(context.Context, string) ([]string, error) { return nil, nil }
	srv := newRouter(h)
	triple := authTripleFor(t, h, srv, "dev_sm_404")
	body := map[string]any{"scene_id": "nope"}
	for k, v := range triple {
		body[k] = v
	}
	code, out := postJSONRaw(t, srv, "/client/scene-manifest", body)
	if code != http.StatusNotFound {
		t.Fatalf("期望 HTTP 404, 实际 %d: %v", code, out)
	}
}

// 开发期最小形状:{scene_id, assets:[{path}]}。
// R2 定稿后按扩展性规则**新增**字段(hash/size),本断言不应因此失败。
func TestSceneManifest_MinimalShape(t *testing.T) {
	h, _ := newTestHandler(t)
	h.DevMode = true // 最小形状是开发期临时值,生产守卫下不可用
	h.SceneAssets = func(_ context.Context, id string) ([]string, error) {
		if id != "quest_101101" {
			return nil, nil
		}
		return []string{"resource/image_native/a.png", "resource/sound_native/b.hca"}, nil
	}
	srv := newRouter(h)
	triple := authTripleFor(t, h, srv, "dev_sm_ok")
	body := map[string]any{"scene_id": "quest_101101"}
	for k, v := range triple {
		body[k] = v
	}
	resp := postJSON(t, srv, "/client/scene-manifest", body)
	if resp["scene_id"] != "quest_101101" {
		t.Errorf("scene_id = %v", resp["scene_id"])
	}
	assets, ok := resp["assets"].([]any)
	if !ok || len(assets) != 2 {
		t.Fatalf("assets 应为 2 项数组, got %v", resp["assets"])
	}
	first, _ := assets[0].(map[string]any)
	if first["path"] != "resource/image_native/a.png" {
		t.Errorf("assets[0].path = %v", first["path"])
	}
}

// TestSceneManifest_ProductionGuard —— 清单的最小形状是**开发期临时值**,
// 生产环境(DevMode=false)一律拒绝,**哪怕 SceneAssets 已经接好**。
//
// 这条守卫的价值在于:临时值的危险不是它们存在,而是它们可能不被发现地留在生产里。
// 一个只含 path 的清单在生产里跑得好好的,直到某天需要靠 hash 做缓存失效,
// 才发现它从来没有过。
func TestSceneManifest_ProductionGuard(t *testing.T) {
	h, _ := newTestHandler(t)
	// 注意:不设 DevMode(默认 false = 生产),但把清单接好。
	h.SceneAssets = func(context.Context, string) ([]string, error) {
		return []string{"resource/image_native/a.png"}, nil
	}
	srv := newRouter(h)
	triple := authTripleFor(t, h, srv, "dev_sm_guard")
	body := map[string]any{"scene_id": "quest_101101"}
	for k, v := range triple {
		body[k] = v
	}
	code, out := postJSONRaw(t, srv, "/client/scene-manifest", body)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("生产模式下应当 503,实际 %d: %v", code, out)
	}
	if out["error"] != "manifest_unavailable" {
		t.Errorf("error = %v, want manifest_unavailable", out["error"])
	}
	// 不能因为拒绝就顺手回一个空清单——那正是守卫要防的静默降级。
	if _, has := out["assets"]; has {
		t.Errorf("被守卫拦下时不应返回 assets 字段, got %v", out["assets"])
	}
}

// 精简版心跳不再承载下载进度与换线指令,正常情况只回 action=ok。
func TestHeartbeat_SlimOk(t *testing.T) {
	h, _ := newTestHandler(t)
	srv := newRouter(h)
	triple := authTripleFor(t, h, srv, "dev_hb_slim")
	resp := postJSON(t, srv, "/client/heartbeat", triple)
	if resp["action"] != "ok" {
		t.Errorf("action = %v, want ok", resp["action"])
	}
	if _, ok := resp["assignments"]; ok {
		t.Errorf("精简版心跳不应再下发 assignments(换线指令)")
	}
}
