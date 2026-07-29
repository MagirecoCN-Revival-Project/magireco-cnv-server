package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func snaaRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/magica/api", h.MagicaRoutes)
	return r
}

func postSnaa(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/magica/api/snaa", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	snaaRouter(h).ServeHTTP(w, req)
	return w
}

// 引导端点的线格式由已分发的底包写死,这里锁住它:字段名、嵌套层级、
// status 的取值都不允许再变(铁律四)。
func TestSnaaWireFormat(t *testing.T) {
	h := &Handler{
		BootstrapEndpoint:   "https://example.invalid/en",
		BootstrapMaxThreads: 4,
		BootstrapVersion:    128,
	}
	w := postSnaa(t, h, `{"version":128}`)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", w.Code)
	}

	var got snaaResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if got.Message != "snaa" {
		t.Errorf("message = %q, 期望 \"snaa\"", got.Message)
	}
	if got.Status != 200 {
		t.Errorf("status = %d, 期望 200", got.Status)
	}
	if got.Response.Endpoint != "https://example.invalid/en" {
		t.Errorf("endpoint = %q", got.Response.Endpoint)
	}
	if got.Response.MaxThreads != 4 {
		t.Errorf("max_threads = %d, 期望 4", got.Response.MaxThreads)
	}
	if got.Response.Version != 128 {
		t.Errorf("version = %d, 期望 128", got.Response.Version)
	}

	// 底包按字面量找这三个键,改名即等于让所有已发布客户端解析失败。
	for _, key := range []string{`"message"`, `"response"`, `"endpoint"`, `"max_threads"`, `"status"`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("响应缺少键 %s: %s", key, w.Body.String())
		}
	}
}

// 未配置端点时必须返回 503 且响应体非空。
// 返回 200+空 endpoint 会让客户端弹 "Empty endpoint URL",
// 把配置缺失误报成客户端故障;返回空体则触发 "Response length: 0"。
func TestSnaaUnconfiguredIsNotEmptyBody(t *testing.T) {
	h := &Handler{BootstrapEndpoint: "   "} // 只有空白 = 视为未配置
	w := postSnaa(t, h, `{"version":128}`)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, 期望 503", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Fatal("响应体为空——客户端会显示 (Response length: 0) 这种无信息报错")
	}
}

// 底包将来追加字段不得把老服务端打成 400。
func TestSnaaAcceptsUnknownFields(t *testing.T) {
	h := &Handler{BootstrapEndpoint: "https://example.invalid/en", BootstrapVersion: 128}
	w := postSnaa(t, h, `{"version":128,"future_field":"x"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200(未知字段应被忽略)", w.Code)
	}
}

// 异常 version 不得导致 5xx 或空体:客户端只会把它显示成"连不上服务器"。
func TestSnaaOutOfRangeVersionStillAnswers(t *testing.T) {
	h := &Handler{BootstrapEndpoint: "https://example.invalid/en", BootstrapVersion: 128}
	for _, body := range []string{`{"version":-1}`, `{"version":999999999}`, `{}`} {
		w := postSnaa(t, h, body)
		if w.Code != http.StatusOK {
			t.Errorf("body=%s 状态码 = %d, 期望 200", body, w.Code)
		}
		var got snaaResp
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Response.Endpoint == "" {
			t.Errorf("body=%s 未返回可用 endpoint: %s", body, w.Body.String())
		}
	}
}
