package client

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/clienttoken"
	"magirecocn-revival/api-server/internal/store"
)

// 这组测试盯的是 requireClientSession 的**双路径分流**。它是安全关键:分流错了
// 要么把合法会话全拒(可见,会被立刻发现),要么把伪造令牌放行(不可见,不会有人
// 发现)。后者正是必须用测试钉死的那一类。
//
// 本服务端是身份的源头:它签出的令牌会被资源分发服务端凭公钥直接采信,那边不查库。
// 所以这里签发路径上的任何松动,影响面都不止本进程。

const (
	localSeed  = "3a1b2c3d4e5f60718293a4b5c6d7e8f900112233445566778899aabbccddeeff"
	remoteSeed = "ff00112233445566778899aabbccddeeff102030405060708090a0b0c0d0e0f0"
	evilSeed   = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
)

// withTokens 在 newTestHandler 已装好的本节点签发方之外,追加信任若干外部签发方。
func withTokens(t *testing.T, h *Handler, extra ...*clienttoken.Issuer) *clienttoken.Issuer {
	t.Helper()
	trusted := map[string]string{h.TokenIssuer.ID(): h.TokenIssuer.PublicKeyHex()}
	for _, e := range extra {
		trusted[e.ID()] = e.PublicKeyHex()
	}
	ver, err := clienttoken.NewVerifier(trusted)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	h.TokenVerifier = ver
	return h.TokenIssuer
}

// initAndGrabToken 走一次真实的 /client/init,拿回下发的 access_token。
func initAndGrabToken(t *testing.T, h *Handler, deviceID string) string {
	t.Helper()
	srv := newRouter(h)
	out := postJSON(t, srv, "/client/init", map[string]any{
		"version": "1.0.0", "device_id": deviceID, "signature": "sig-a", "channel": "normal",
	})
	tok, _ := out["access_token"].(string)
	if tok == "" {
		t.Fatalf("/client/init 没有下发 access_token: %v", out)
	}
	return tok
}

// 握手下发的是新格式令牌,且它能通过后续端点的鉴权。
func TestIssuesSelfContainedTokenAndAccepts(t *testing.T) {
	h, _ := newTestHandler(t)
	withTokens(t, h)

	tok := initAndGrabToken(t, h, "dev-1")
	if !strings.HasPrefix(tok, "cnv1.") {
		t.Fatalf("应当下发自包含令牌,得到 %q", tok)
	}

	sess, key, err := h.resolveSession(context.Background(), tok, "dev-1")
	if err != nil {
		t.Fatalf("自己签发的令牌应当能通过: %v", err)
	}
	if sess.DeviceID != "dev-1" || sess.Signature != "sig-a" || sess.ClientVersion != "1.0.0" {
		t.Fatalf("会话元数据不对: %+v", sess)
	}
	// 主键是 jti 而不是令牌本身:令牌约 400 字节,MySQL 那列是 VARCHAR(128)
	if key == tok || len(key) != 64 {
		t.Fatalf("client_sessions 主键应当是 jti,得到 %q", key)
	}
}

// Android 专有旧版 API 废弃后,旧的 64 位 hex 令牌必须一律被拒——
// 它曾经是合法凭证,所以这条要显式钉住,防止哪天"为了兼容"又被放回来。
func TestLegacyHexTokenRejected(t *testing.T) {
	h, st := newTestHandler(t)
	withTokens(t, h)

	// 直接往库里塞一行旧形态会话,模拟废弃前签发的令牌
	legacy := strings.Repeat("ab", 32)
	now := time.Now().UnixMilli()
	if err := st.ClientSessionInsert(context.Background(), store.ClientSession{
		AccessToken: legacy, DeviceID: "dev-1", Signature: "sig-a",
		ClientVersion: "1.0.0", Channel: "normal",
		CreatedAt: now, ExpiresAt: now + 3600_000, LastSeenAt: now,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, _, err := h.resolveSession(context.Background(), legacy, "dev-1"); err == nil {
		t.Fatal("旧的 hex 令牌必须被拒,即使 client_sessions 里还有对应的行")
	}
}

// 没装签发方时 /client/init 必须失败关闭,而不是签出一个无签名的凭证。
func TestInitFailsClosedWithoutIssuer(t *testing.T) {
	h, _ := newTestHandler(t)
	h.TokenIssuer = nil
	srv := newRouter(h)

	w := postRaw(t, srv, "/client/init", map[string]any{
		"version": "1.0.0", "device_id": "dev-1", "signature": "sig-a", "channel": "normal",
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("没有签发方时应当 500 失败关闭,得到 %d: %s", w.Code, w.Body.String())
	}
}

// 撤销:后台"踢下线"删掉 client_sessions 那一行后,本节点签的令牌必须立刻失效。
// 自包含令牌最大的代价就是撤销,这条必须钉死。
func TestLocallyIssuedTokenRevokedByDeletingRow(t *testing.T) {
	h, st := newTestHandler(t)
	withTokens(t, h)

	tok := initAndGrabToken(t, h, "dev-1")
	_, key, err := h.resolveSession(context.Background(), tok, "dev-1")
	if err != nil {
		t.Fatalf("撤销前应当有效: %v", err)
	}
	if err := st.ClientSessionDelete(context.Background(), key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := h.resolveSession(context.Background(), tok, "dev-1"); err == nil {
		t.Fatal("删掉会话行之后,本节点签发的令牌必须立刻失效")
	}
}

// 外部签发方签的令牌本地没有任何行,必须**照样接受**——否则一配上外部签发方,
// 它签的令牌会被"查不到行"全部拒掉。这就是撤销判定必须先看 iss 的原因。
func TestExternalIssuerTokenAcceptedWithoutLocalRow(t *testing.T) {
	remote, err := clienttoken.NewIssuer("other-issuer", remoteSeed)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	h, _ := newTestHandler(t)
	withTokens(t, h, remote)

	tok, err := remote.Issue(clienttoken.Claims{
		Sub: "dev-9", Sig: "sig-a", CV: "1.0.0", Ch: "normal",
		Acc: "acct-uuid",
	}, time.Now(), time.Hour, strings.Repeat("b", 32))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	sess, _, err := h.resolveSession(context.Background(), tok, "dev-9")
	if err != nil {
		t.Fatalf("受信任的外部签发方所签令牌应当被接受: %v", err)
	}
	if sess.DeviceID != "dev-9" || sess.Signature != "sig-a" {
		t.Fatalf("会话元数据应当来自已验签的载荷: %+v", sess)
	}
}

// 未被信任的签发方,即使令牌本身结构完好、签名自洽,也必须拒。
func TestUntrustedIssuerRejected(t *testing.T) {
	evil, err := clienttoken.NewIssuer("evil", evilSeed)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	h, _ := newTestHandler(t)
	withTokens(t, h) // 不信任 evil

	tok, _ := evil.Issue(clienttoken.Claims{Sub: "dev-1"}, time.Now(), time.Hour, strings.Repeat("c", 32))
	if _, _, err := h.resolveSession(context.Background(), tok, "dev-1"); err == nil {
		t.Fatal("未信任签发方的令牌必须被拒")
	}
}

// 分流不得留降级通道:长得像新令牌但无效的串,必须直接拒,
// **不能**因为验签失败就退回去当旧令牌查库。
func TestNoDowngradeFromNewFormatToLegacyLookup(t *testing.T) {
	h, _ := newTestHandler(t)
	withTokens(t, h)

	for _, bad := range []string{
		"cnv1.garbage.garbage",
		"cnv1..",
		"cnv1." + strings.Repeat("A", 200) + ".x",
	} {
		if _, _, err := h.resolveSession(context.Background(), bad, "dev-1"); err == nil {
			t.Fatalf("伪造的新格式令牌必须被拒: %q", bad)
		}
	}

	// 没配校验方时,新格式令牌一律拒(而不是掉进旧路径)
	h2, _ := newTestHandler(t)
	h2.TokenVerifier = nil
	if _, _, err := h2.resolveSession(context.Background(), "cnv1.abc.def", "dev-1"); err == nil {
		t.Fatal("未配校验方时新格式令牌必须被拒")
	}
}

// 令牌不得被搬到另一台设备上复用。
func TestTokenBoundToDevice(t *testing.T) {
	h, _ := newTestHandler(t)
	withTokens(t, h)
	tok := initAndGrabToken(t, h, "dev-1")

	if _, _, err := h.resolveSession(context.Background(), tok, "dev-2"); err == nil {
		t.Fatal("令牌被搬到别的设备上必须被拒")
	}
}

// 端到端:新令牌走完整的 HTTP 中间件链,后续端点应当正常放行。
func TestNewTokenWorksThroughMiddleware(t *testing.T) {
	h, _ := newTestHandler(t)
	withTokens(t, h)
	srv := newRouter(h)

	tok := initAndGrabToken(t, h, "dev-1")
	out := postJSON(t, srv, "/client/heartbeat", map[string]any{
		"device_id": "dev-1", "access_token": tok, "signature": "sig-a",
	})
	if act, _ := out["action"].(string); act == "" {
		t.Fatalf("心跳应当正常返回 action,得到 %v", out)
	}
}

// 中间件层面的拒绝:签名中途变了要 403 并作废会话(防换包),
// 这条原本就有,改分流之后不能失效——删的那一行必须是 jti 对应的行。
func TestSignatureChangeInvalidatesNewToken(t *testing.T) {
	h, _ := newTestHandler(t)
	withTokens(t, h)
	srv := newRouter(h)

	tok := initAndGrabToken(t, h, "dev-1")
	w := postRaw(t, srv, "/client/heartbeat", map[string]any{
		"device_id": "dev-1", "access_token": tok, "signature": "被换过的签名",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("签名中途变化应当 403,得到 %d: %s", w.Code, w.Body.String())
	}
	// 会话应当已被作废
	if _, _, err := h.resolveSession(context.Background(), tok, "dev-1"); err == nil {
		t.Fatal("签名异常后会话应当已作废")
	}
}
