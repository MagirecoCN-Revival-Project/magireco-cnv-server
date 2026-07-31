package totentanz_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"magirecocn-revival/api-server/internal/totentanz"
)

// okBody 复刻上游真实响应的信封形状。
func okBody(endpoint string, maxThreads, version int) string {
	b, _ := json.Marshal(map[string]any{
		"message": "snaa",
		"status":  200,
		"response": map[string]any{
			"endpoint":    endpoint,
			"max_threads": maxThreads,
			"version":     version,
		},
	})
	return string(b)
}

func TestDiscovery_FetchAndParse(t *testing.T) {
	var gotBody atomic.Value
	var gotCT atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("必须是 POST，实际 %s", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		gotBody.Store(string(raw))
		gotCT.Store(r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okBody("https://ttz.example/en", 19, 128))
	}))
	defer srv.Close()

	c := totentanz.New(srv.URL, 128, nil)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// 请求形状必须与还原出来的协议一致
	if b := gotBody.Load().(string); b != `{"version":128}` {
		t.Errorf("请求体 = %s, 期望 {\"version\":128}", b)
	}
	if ct := gotCT.Load().(string); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	ep := c.Get()
	if ep == nil {
		t.Fatal("Get() 返回 nil")
	}
	if ep.Base != "https://ttz.example/en" {
		t.Errorf("Base = %q", ep.Base)
	}
	// Host 要能从含路径的 base 里正确剥出——这正是老字段 game_server_host 需要的形态
	if ep.Host != "ttz.example" {
		t.Errorf("Host = %q, 期望 ttz.example", ep.Host)
	}
	if ep.MaxThreads != 19 || ep.Version != 128 {
		t.Errorf("MaxThreads=%d Version=%d", ep.MaxThreads, ep.Version)
	}
}

// TestDiscovery_StaleWhileError 锁定「上游挂掉不能连累我们」这条可用性原则:
// 拉取失败必须保留上一次成功的结果,而不是清空。
func TestDiscovery_StaleWhileError(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, okBody("https://ttz.example/en", 4, 128))
	}))
	defer srv.Close()

	c := totentanz.New(srv.URL, 128, nil)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("首次 Refresh: %v", err)
	}
	first := c.Get()

	fail.Store(true)
	if err := c.Refresh(context.Background()); err == nil {
		t.Error("上游 502 时 Refresh 应返回错误")
	}
	got := c.Get()
	if got == nil {
		t.Fatal("拉取失败后结果被清空了——上游一挂我们就没地址可下发了")
	}
	if got.Base != first.Base {
		t.Errorf("失败后结果被改写: %q → %q", first.Base, got.Base)
	}
	if c.LastError() == nil {
		t.Error("LastError 应记录本次失败")
	}
}

func TestDiscovery_RejectsBadResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
	}{
		{"HTTP 非 200", `{}`, http.StatusInternalServerError},
		{"非 JSON", `not json`, http.StatusOK},
		{"信封 status 非 200", `{"message":"x","status":503,"response":{}}`, http.StatusOK},
		{"缺 endpoint", `{"message":"snaa","status":200,"response":{"max_threads":4}}`, http.StatusOK},
		{"endpoint 无协议", `{"message":"snaa","status":200,"response":{"endpoint":"ttz.example/en"}}`, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := totentanz.New(srv.URL, 128, nil)
			if err := c.Refresh(context.Background()); err == nil {
				t.Errorf("%s 应被拒绝", tc.name)
			}
			if c.Get() != nil {
				t.Errorf("%s：从未成功过时 Get() 必须是 nil", tc.name)
			}
		})
	}
}

func TestDiscovery_DisabledWhenNoURL(t *testing.T) {
	c := totentanz.New("  ", 128, nil)
	if c.Enabled() {
		t.Error("空 URL 应视为未启用")
	}
	if c.Get() != nil {
		t.Error("未启用时 Get() 应为 nil")
	}
	if err := c.Refresh(context.Background()); err == nil {
		t.Error("未启用时 Refresh 应报错")
	}
	// Run 必须直接返回，不能空转
	totentanz.New("", 128, nil).Run(context.Background(), 0)
}
