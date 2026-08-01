package clienttoken

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	seedA = "1f8b2c3d4e5f60718293a4b5c6d7e8f900112233445566778899aabbccddeeff"
	seedB = "00112233445566778899aabbccddeeff102030405060708090a0b0c0d0e0f001"
)

func mustIssuer(t *testing.T, id, seed string) *Issuer {
	t.Helper()
	i, err := NewIssuer(id, seed)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return i
}

func mustVerifier(t *testing.T, iss ...*Issuer) *Verifier {
	t.Helper()
	m := map[string]string{}
	for _, i := range iss {
		m[i.ID()] = i.PublicKeyHex()
	}
	v, err := NewVerifier(m)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	v := mustVerifier(t, iss)
	now := time.Unix(1700000000, 0)

	tok, err := iss.Issue(Claims{
		Sub: "device-1", Sig: "abcd", CV: "1.2.3", Ch: "normal",
		Acc: "0127be0d-cc12-11e9-1234-0123456789ab",
	}, now, time.Hour, "jti-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !Looks(tok) {
		t.Fatalf("Looks 应当认出自己签发的令牌: %q", tok)
	}

	c, err := v.Verify(tok, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Sub != "device-1" || c.Acc != "0127be0d-cc12-11e9-1234-0123456789ab" ||
		c.CV != "1.2.3" || c.Ch != "normal" || c.Sig != "abcd" {
		t.Fatalf("载荷字段丢失: %+v", c)
	}
	// iss / iat / exp / jti 由 Issue 统一填写
	if c.Iss != "api" || c.JTI != "jti-1" || c.V != 1 {
		t.Fatalf("签发方元数据不对: %+v", c)
	}
	if c.Iat != now.UnixMilli() || c.Exp != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("时间戳不对: iat=%d exp=%d", c.Iat, c.Exp)
	}
}

// 调用方传进来的 v/iss/iat/exp/jti 必须被覆盖,不能借此签出永不过期或冒名的令牌。
func TestIssueOverridesCallerSuppliedMetadata(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	v := mustVerifier(t, iss)
	now := time.Unix(1700000000, 0)

	tok, err := iss.Issue(Claims{
		Sub: "d", V: 99, Iss: "冒充的签发方",
		Iat: 1, Exp: 1 << 62, JTI: "调用方指定的",
	}, now, time.Minute, "真正的jti")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	c, err := v.Verify(tok, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Iss != "api" || c.V != 1 || c.JTI != "真正的jti" ||
		c.Exp != now.Add(time.Minute).UnixMilli() {
		t.Fatalf("调用方字段没有被覆盖: %+v", c)
	}
}

// 换一个字节就必须验签失败——这是整个方案的地基。
func TestTamperedPayloadRejected(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	v := mustVerifier(t, iss)
	now := time.Unix(1700000000, 0)
	tok, _ := iss.Issue(Claims{Sub: "device-1"}, now, time.Hour, "j")

	parts := strings.Split(tok, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c Claims
	_ = json.Unmarshal(raw, &c)
	c.Sub = "device-2" // 把令牌改到别的设备上
	tampered, _ := json.Marshal(c)
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(tampered) + "." + parts[2]

	if _, err := v.Verify(forged, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("被篡改的载荷应当验签失败,得到 %v", err)
	}
}

// 另一把私钥签的令牌不能通过——校验方无法被别的签发方冒名。
func TestForeignIssuerRejected(t *testing.T) {
	good := mustIssuer(t, "api", seedA)
	evil := mustIssuer(t, "api", seedB) // 同名,不同私钥
	v := mustVerifier(t, good)
	now := time.Unix(1700000000, 0)

	tok, _ := evil.Issue(Claims{Sub: "d"}, now, time.Hour, "j")
	if _, err := v.Verify(tok, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("冒名签发方应当验签失败,得到 %v", err)
	}

	other := mustIssuer(t, "别的后端", seedB)
	tok2, _ := other.Issue(Claims{Sub: "d"}, now, time.Hour, "j")
	if _, err := v.Verify(tok2, now); !errors.Is(err, ErrUnknownIssuer) {
		t.Fatalf("未信任的签发方应当被拒,得到 %v", err)
	}
}

// 签名覆盖 "cnv1.<payload>" 而非单独 payload,所以换前缀后签名不再成立。
// 这道题的意义是:将来出 cnv2 时,cnv2 的载荷不能被搬到 cnv1 前缀下复用签名。
func TestSignatureBindsVersionPrefix(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	now := time.Unix(1700000000, 0)
	tok, _ := iss.Issue(Claims{Sub: "d"}, now, time.Hour, "j")
	parts := strings.Split(tok, ".")

	pub, _ := hex.DecodeString(iss.PublicKeyHex())
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])

	if ed25519.Verify(ed25519.PublicKey(pub), []byte(parts[1]), sig) {
		t.Fatal("签名不应当只覆盖 payload——那样换前缀即可复用")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("签名应当覆盖 cnv1.<payload>")
	}
}

func TestExpiryAndClockSkew(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	v := mustVerifier(t, iss)
	now := time.Unix(1700000000, 0)
	tok, _ := iss.Issue(Claims{Sub: "d"}, now, time.Hour, "j")

	if _, err := v.Verify(tok, now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期令牌应当被拒,得到 %v", err)
	}
	// 默认 60s 容差:刚过期一点点仍然接受,避免节点间时钟微差把用户踢下线
	if _, err := v.Verify(tok, now.Add(time.Hour+30*time.Second)); err != nil {
		t.Fatalf("容差内不应当被拒: %v", err)
	}
	if _, err := v.Verify(tok, now.Add(time.Hour+2*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("超出容差应当被拒,得到 %v", err)
	}
	// 签发方时钟快了很多:容差外的未来令牌也要拒
	if _, err := v.Verify(tok, now.Add(-2*time.Minute)); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("尚未生效的令牌应当被拒,得到 %v", err)
	}
}

func TestRevocationHook(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	v := mustVerifier(t, iss)
	now := time.Unix(1700000000, 0)
	tok, _ := iss.Issue(Claims{Sub: "d"}, now, time.Hour, "被撤销的")

	if _, err := v.Verify(tok, now); err != nil {
		t.Fatalf("撤销回调未设置时应当通过: %v", err)
	}
	v.Revoked = func(c *Claims) bool { return c.JTI == "被撤销的" }
	if _, err := v.Verify(tok, now); !errors.Is(err, ErrRevoked) {
		t.Fatalf("已撤销令牌应当被拒,得到 %v", err)
	}
	// 回调必须拿得到签发方,否则联邦模式下无法把"本地签的"与"远端签的"分开处理
	var sawIssuer string
	v.Revoked = func(c *Claims) bool { sawIssuer = c.Iss; return false }
	if _, err := v.Verify(tok, now); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if sawIssuer != "api" {
		t.Fatalf("撤销回调应当拿到签发方,得到 %q", sawIssuer)
	}

	// 撤销名单按 jti 精确匹配,不应误伤其它令牌
	v.Revoked = func(c *Claims) bool { return c.JTI == "被撤销的" }
	tok2, _ := iss.Issue(Claims{Sub: "d"}, now, time.Hour, "没被撤销的")
	if _, err := v.Verify(tok2, now); err != nil {
		t.Fatalf("未撤销令牌被误伤: %v", err)
	}
}

func TestDeviceBinding(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	v := mustVerifier(t, iss)
	now := time.Unix(1700000000, 0)
	tok, _ := iss.Issue(Claims{Sub: "device-1"}, now, time.Hour, "j")

	if _, err := v.VerifyForDevice(tok, "device-2", now); !errors.Is(err, ErrDeviceMismatch) {
		t.Fatalf("跨设备复用应当被拒,得到 %v", err)
	}
	if _, err := v.VerifyForDevice(tok, "device-1", now); err != nil {
		t.Fatalf("同设备应当通过: %v", err)
	}
	// 空 device_id 跳过绑定检查,与旧 ClientSessionLookup 语义一致
	if _, err := v.VerifyForDevice(tok, "", now); err != nil {
		t.Fatalf("空 device_id 应当跳过绑定检查: %v", err)
	}
}

func TestMalformedInputs(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	v := mustVerifier(t, iss)
	now := time.Unix(1700000000, 0)
	good, _ := iss.Issue(Claims{Sub: "d"}, now, time.Hour, "j")
	parts := strings.Split(good, ".")

	cases := map[string]string{
		"空串":                "",
		"旧格式 hex 令牌":        strings.Repeat("ab", 32),
		"只有前缀":              "cnv1.",
		"缺签名段":              parts[0] + "." + parts[1],
		"多一段":               good + ".extra",
		"payload 不是 base64": "cnv1.@@@@." + parts[2],
		"payload 不是 JSON":   "cnv1." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + "." + parts[2],
		"签名段不是 base64":      parts[0] + "." + parts[1] + ".@@@",
		"签名长度不对":            parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("short")),
		"超长输入":              "cnv1." + strings.Repeat("A", maxTokenLen),
	}
	for name, tok := range cases {
		if _, err := v.Verify(tok, now); err == nil {
			t.Fatalf("%s:应当被拒但通过了", name)
		}
	}
}

// exp <= iat 的令牌即使签名有效也必须拒:它要么是签发方有 bug,要么是被构造出来
// 探测校验逻辑的。用真私钥手工签一个,确保拦截发生在签名之后的语义检查里。
func TestExpBeforeIatRejected(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	v := mustVerifier(t, iss)
	now := time.Unix(1700000000, 0)

	inner, _ := json.Marshal(Claims{
		V: 1, Iss: "api", Sub: "d", JTI: "j",
		Iat: now.UnixMilli(), Exp: now.UnixMilli() - 1,
	})
	signing := Prefix + "." + base64.RawURLEncoding.EncodeToString(inner)
	seed, _ := hex.DecodeString(seedA)
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(signing))
	tok := signing + "." + base64.RawURLEncoding.EncodeToString(sig)

	if _, err := v.Verify(tok, now); !errors.Is(err, ErrMalformed) {
		t.Fatalf("exp<=iat 应当被拒,得到 %v", err)
	}
}

func TestLooksDoesNotValidate(t *testing.T) {
	// Looks 只看前缀:必须对"长得像但完全无效"的串返回 true,
	// 否则校验侧会把伪造令牌误分流到旧的查库路径上去。
	if !Looks("cnv1.garbage.garbage") {
		t.Fatal("Looks 应当只看前缀")
	}
	if Looks(strings.Repeat("ab", 32)) {
		t.Fatal("旧格式 hex 令牌不应被认成新格式")
	}
	if Looks("cnv1" + strings.Repeat("A", maxTokenLen)) {
		t.Fatal("超长输入不应通过 Looks")
	}
}

func TestNewIssuerAndVerifierInputValidation(t *testing.T) {
	if _, err := NewIssuer("", seedA); err == nil {
		t.Fatal("空签发方标识应当报错")
	}
	if _, err := NewIssuer("api", "不是十六进制"); err == nil {
		t.Fatal("非十六进制种子应当报错")
	}
	if _, err := NewIssuer("api", "aabb"); err == nil {
		t.Fatal("长度不足的种子应当报错")
	}
	if _, err := NewVerifier(map[string]string{"api": "zz"}); err == nil {
		t.Fatal("非法公钥应当报错")
	}
	if _, err := NewVerifier(map[string]string{"": seedA}); err == nil {
		t.Fatal("空签发方标识应当报错")
	}
	// 没配任何信任公钥时,校验器必须拒绝一切令牌而不是放行
	v, err := NewVerifier(nil)
	if err != nil {
		t.Fatalf("空信任表本身不该报错: %v", err)
	}
	if v.Trusted() {
		t.Fatal("空信任表 Trusted() 应为 false")
	}
	iss := mustIssuer(t, "api", seedA)
	tok, _ := iss.Issue(Claims{Sub: "d"}, time.Unix(1700000000, 0), time.Hour, "j")
	if _, err := v.Verify(tok, time.Unix(1700000000, 0)); !errors.Is(err, ErrUnknownIssuer) {
		t.Fatalf("未配信任公钥时应当拒绝一切令牌,得到 %v", err)
	}
}

func TestIssueInputValidation(t *testing.T) {
	iss := mustIssuer(t, "api", seedA)
	now := time.Unix(1700000000, 0)
	if _, err := iss.Issue(Claims{}, now, time.Hour, "j"); err == nil {
		t.Fatal("空 sub 应当报错")
	}
	if _, err := iss.Issue(Claims{Sub: "d"}, now, 0, "j"); err == nil {
		t.Fatal("ttl<=0 应当报错")
	}
	if _, err := iss.Issue(Claims{Sub: "d"}, now, time.Hour, ""); err == nil {
		t.Fatal("空 jti 应当报错")
	}
}
