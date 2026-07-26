package admin

import "testing"

// TestIsValidHostname 核对 game_server_host 校验的边界。
// 2026-05-29 客户端代理回退修复说明 要求该字段是**纯 host**(不含 scheme)。
func TestIsValidHostname(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// 合法
		{"totentanz-9b.magica-us.com", true},
		{"gameserver.example.com", true},
		{"a.b.c", true},
		{"single-label", true},
		{"with-port.example.com:8443", true},
		{"x.y:1", true},
		{"x.y:65535", true},

		// 非法:空 / 太短的 label
		{"", false},
		{".", false},
		{"foo..bar", false},
		{".leading.dot", false},
		{"trailing.dot.", false},

		// 非法:label 以 - 开头或结尾
		{"-bad.example.com", false},
		{"bad-.example.com", false},
		{"good.bad-.com", false},

		// 非法:含 scheme / 路径(由 servicesPut 先剥离 scheme 再校验,此处直接拒绝)
		{"https://foo.com", false},
		{"foo.com/path", false},

		// 非法:label 过长(>63)
		{string(make([]byte, 64)) + ".com", false},

		// 非法:port 越界 / 非数字
		{"foo.com:0a", false},
		{"foo.com:", false},
		{"foo.com:123456", false}, // >5 位
	}
	for _, c := range cases {
		got := isValidHostname(c.in)
		if got != c.want {
			t.Errorf("isValidHostname(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
