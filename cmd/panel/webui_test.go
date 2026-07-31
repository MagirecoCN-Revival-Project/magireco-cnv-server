package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/panelstore"
)

func TestConnectOrigin(t *testing.T) {
	cases := map[string]string{
		"":                            "",
		"http://127.0.0.1:8080":       "http://127.0.0.1:8080",
		"https://node.example.com/":   "https://node.example.com",
		"https://node.example.com/x/": "https://node.example.com",
		"not-a-url":                   "",
	}
	for in, want := range cases {
		if got := connectOrigin(strings.TrimRight(in, "/")); got != want {
			t.Errorf("connectOrigin(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestWebUI_APIBaseAndConfig(t *testing.T) {
	ctx := context.Background()
	ps, err := panelstore.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer ps.Close()

	ui := newWebUI(ps, ".")

	// 无节点时应回落空基址(同源)。
	if got := ui.apiBase(ctx); got != "" {
		t.Fatalf("无节点时 apiBase 应为空,得到 %q", got)
	}

	// 登记一个业务节点后,apiBase 应指向它(去尾斜杠)。
	if err := ps.NodeInsert(ctx, panelstore.Node{
		ID:      "node_biz",
		Role:    "business",
		APIURL:  "https://api.example.com/",
		CtrlURL: "ws://127.0.0.1:9090/ctrl",
		Key:     "k",
	}); err != nil {
		t.Fatalf("NodeInsert: %v", err)
	}
	ui.cachedAt = ui.cachedAt.Add(-time.Hour) // 让 3s 缓存失效
	if got := ui.apiBase(ctx); got != "https://api.example.com" {
		t.Fatalf("apiBase 应指向业务节点,得到 %q", got)
	}

	// /app-config.js 应注入该基址,且 CSP connect-src 放行其来源。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/app-config.js", nil)
	ui.appConfig(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `window.MR_API_BASE = "https://api.example.com"`) {
		t.Fatalf("app-config.js 未注入正确基址: %q", body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self' https://api.example.com;") {
		t.Fatalf("CSP connect-src 未放行节点来源: %q", csp)
	}
}
