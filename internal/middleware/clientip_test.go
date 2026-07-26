package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func req(remote, xff, xreal string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	if xreal != "" {
		r.Header.Set("X-Real-IP", xreal)
	}
	return r
}

// 默认(未配置 trust proxy):一律忽略转发头,用 TCP 对端,防伪造。
func TestClientIP_DefaultIgnoresForwardedHeaders(t *testing.T) {
	ConfigureTrustProxy("")
	r := req("203.0.113.7:54321", "1.2.3.4", "5.6.7.8")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("默认应当用 RemoteAddr,得到 %q", got)
	}
}

// trust=all:信任转发头(确有可信前置网关时)。
func TestClientIP_TrustAllHonorsXFF(t *testing.T) {
	ConfigureTrustProxy("all")
	r := req("10.0.0.1:9999", "1.2.3.4, 10.0.0.1", "")
	if got := clientIP(r); got != "1.2.3.4" {
		t.Errorf("trust=all 应取 XFF 首段 1.2.3.4,得到 %q", got)
	}
	ConfigureTrustProxy("") // 复位,避免影响其它测试
}

// CIDR 白名单:仅当直连对端命中白名单才解析转发头。
func TestClientIP_CIDROnlyFromTrustedPeer(t *testing.T) {
	ConfigureTrustProxy("10.0.0.0/8")
	defer ConfigureTrustProxy("")

	// 对端在白名单内 → 解析 XFF
	r1 := req("10.1.2.3:1234", "8.8.8.8", "")
	if got := clientIP(r1); got != "8.8.8.8" {
		t.Errorf("可信对端应取 XFF,得到 %q", got)
	}
	// 对端不在白名单内 → 忽略 XFF,用对端 IP(防伪造)
	r2 := req("198.51.100.9:1234", "8.8.8.8", "")
	if got := clientIP(r2); got != "198.51.100.9" {
		t.Errorf("不可信对端应忽略 XFF,得到 %q", got)
	}
}

// loopback 关键字。
func TestClientIP_Loopback(t *testing.T) {
	ConfigureTrustProxy("loopback")
	defer ConfigureTrustProxy("")
	r := req("127.0.0.1:5555", "9.9.9.9", "")
	if got := clientIP(r); got != "9.9.9.9" {
		t.Errorf("loopback 对端应取 XFF,得到 %q", got)
	}
}

// 伪造的非法 XFF 不应导致返回垃圾值;回退到对端。
func TestClientIP_BogusXFFFallsBack(t *testing.T) {
	ConfigureTrustProxy("all")
	defer ConfigureTrustProxy("")
	r := req("10.0.0.1:1", "not-an-ip", "")
	if got := clientIP(r); got != "10.0.0.1" {
		t.Errorf("非法 XFF 应回退对端,得到 %q", got)
	}
}
