// Package auth 集中口令哈希(scrypt)与会话 token 生成 / 比较。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const (
	// 当前默认 scrypt 参数。N 从 16384(2^14)提升到 32768(2^15),贴近
	// OWASP/RFC 7914 对交互式登录的推荐下限。参数随哈希一并存储,旧哈希
	// 仍可用各自记录的参数校验,无需强制迁移。
	scryptN = 32768
	scryptR = 8
	scryptP = 1
	scryptL = 32
	saltLen = 16
)

// 用作"账号不存在"分支的等价时间消耗 salt;固定无意义。
var dummySalt = []byte("cnv-anti-enum-dummy-salt-32bytes")

// HashPassword 把明文口令哈希为带参数的版本化串:
//
//	scrypt$<N>$<r>$<p>$<saltHex>$<derivedHex>
//
// 这样将来调参不会让旧哈希失效。
func HashPassword(plain string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived, err := scrypt.Key([]byte(plain), salt, scryptN, scryptR, scryptP, scryptL)
	if err != nil {
		return "", err
	}
	return "scrypt$" + strconv.Itoa(scryptN) + "$" + strconv.Itoa(scryptR) + "$" +
		strconv.Itoa(scryptP) + "$" + hex.EncodeToString(salt) + "$" +
		hex.EncodeToString(derived), nil
}

// VerifyPassword 等时常量比较;格式异常仍跑等量 scrypt 消耗 CPU。
// 同时支持新版 "scrypt$N$r$p$salt$hash" 与旧版 "salt:hash"(N 取历史默认)。
func VerifyPassword(plain, stored string) bool {
	n, r, p, salt, expected, ok := parseHash(stored)
	if !ok {
		// 格式异常:跑一次等量 scrypt 再返回 false,避免时序泄露
		_, _ = scrypt.Key([]byte(plain), dummySalt, scryptN, scryptR, scryptP, scryptL)
		return false
	}
	derived, err := scrypt.Key([]byte(plain), salt, n, r, p, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(expected, derived) == 1
}

// parseHash 解析两种存储格式,返回 scrypt 参数与盐/期望哈希。
func parseHash(stored string) (n, r, p int, salt, expected []byte, ok bool) {
	if strings.HasPrefix(stored, "scrypt$") {
		parts := strings.Split(stored, "$")
		if len(parts) != 6 {
			return 0, 0, 0, nil, nil, false
		}
		var e1, e2, e3, e4, e5 error
		n, e1 = strconv.Atoi(parts[1])
		r, e2 = strconv.Atoi(parts[2])
		p, e3 = strconv.Atoi(parts[3])
		salt, e4 = hex.DecodeString(parts[4])
		expected, e5 = hex.DecodeString(parts[5])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil ||
			n <= 1 || r <= 0 || p <= 0 || len(expected) == 0 {
			return 0, 0, 0, nil, nil, false
		}
		return n, r, p, salt, expected, true
	}
	// 旧版 salt:hash,参数取历史默认 N=16384
	if !strings.Contains(stored, ":") {
		return 0, 0, 0, nil, nil, false
	}
	parts := strings.SplitN(stored, ":", 2)
	var e1, e2 error
	salt, e1 = hex.DecodeString(parts[0])
	expected, e2 = hex.DecodeString(parts[1])
	if e1 != nil || e2 != nil || len(expected) == 0 {
		return 0, 0, 0, nil, nil, false
	}
	return 16384, 8, 1, salt, expected, true
}

// DummyVerifyTiming "账号不存在"分支跑一次,等量 CPU。
func DummyVerifyTiming(plain string) {
	_, _ = scrypt.Key([]byte(plain), dummySalt, scryptN, scryptR, scryptP, scryptL)
}

// NewToken 返回 32 字节十六进制(64 字符)。
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IsWellFormedToken 判断长度+字符集。
func IsWellFormedToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// SafeStrEq 等时常量比较两个字符串(对超长输入先 SHA-256 收敛)。
// 用于共享密钥比较 / Bearer Token 比较等敏感场景。
func SafeStrEq(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// SignatureAllowed 客户端 signature 白名单校验。
// 空白名单时放行所有(节流的 WARN 由调用方处理)。
func SignatureAllowed(signature string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}
	signature = strings.ToLower(signature)
	for _, w := range whitelist {
		if w == signature {
			return true
		}
	}
	return false
}

// ErrInvalidToken token 不合法或不存在。
var ErrInvalidToken = errors.New("token 无效")
