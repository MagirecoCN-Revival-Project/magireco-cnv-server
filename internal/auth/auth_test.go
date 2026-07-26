package auth

import (
	"encoding/hex"
	"testing"

	"golang.org/x/crypto/scrypt"
)

func TestHashRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery staple", h) {
		t.Errorf("正确口令应当校验通过")
	}
	if VerifyPassword("wrong", h) {
		t.Errorf("错误口令不应通过")
	}
}

func TestHashFormatIsVersioned(t *testing.T) {
	h, _ := HashPassword("x")
	// 新格式 scrypt$N$r$p$salt$hash,N 为当前默认
	if got := h[:7]; got != "scrypt$" {
		t.Fatalf("期望版本化前缀 scrypt$,得到 %q", got)
	}
}

// 旧库里的 "saltHex:hashHex"(N=16384)必须仍能校验,保证不破坏既有用户。
func TestLegacyHashStillVerifies(t *testing.T) {
	// 用历史参数手工造一个 legacy 哈希
	legacy := legacyHash(t, "legacy-pass")
	if !VerifyPassword("legacy-pass", legacy) {
		t.Errorf("旧格式哈希应当仍可校验")
	}
	if VerifyPassword("nope", legacy) {
		t.Errorf("旧格式哈希错误口令不应通过")
	}
}

func TestMalformedHashFailsClosed(t *testing.T) {
	for _, bad := range []string{
		"", "garbage", "scrypt$only", "scrypt$a$b$c$d$e",
		"deadbeef:", ":deadbeef", "scrypt$0$8$1$aa$bb",
	} {
		if VerifyPassword("whatever", bad) {
			t.Errorf("畸形哈希 %q 不应通过校验", bad)
		}
	}
}

func TestSafeStrEq(t *testing.T) {
	if !SafeStrEq("shared-key-123", "shared-key-123") {
		t.Errorf("相同串应当相等")
	}
	if SafeStrEq("a", "b") {
		t.Errorf("不同串不应相等")
	}
	// 长度不同也不应 panic / 误判
	if SafeStrEq("short", "a-much-longer-string-here") {
		t.Errorf("不同长度串不应相等")
	}
}

func TestIsWellFormedToken(t *testing.T) {
	tok, _ := NewToken()
	if !IsWellFormedToken(tok) {
		t.Errorf("NewToken 产物应当合法")
	}
	for _, bad := range []string{"", "short", "G" + tok[1:], tok + "0"} {
		if IsWellFormedToken(bad) {
			t.Errorf("%q 不应判定合法", bad)
		}
	}
}

// legacyHash 用历史固定参数 N=16384 造一个旧格式 "saltHex:hashHex",仅供测试。
func legacyHash(t *testing.T, plain string) string {
	t.Helper()
	salt := []byte("0123456789abcdef")
	d, err := scrypt.Key([]byte(plain), salt, 16384, 8, 1, 32)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(d)
}
