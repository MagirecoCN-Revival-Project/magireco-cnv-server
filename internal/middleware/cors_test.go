package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS_Disabled_Passthrough(t *testing.T) {
	called := false
	h := CORS("")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/state", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called {
		t.Fatal("禁用 CORS 时应原样直通到下游")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("禁用时不应写 ACAO,得到 %q", got)
	}
}

func TestCORS_AllowsMatchingOrigin(t *testing.T) {
	h := CORS("https://panel.example.com/")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/state", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://panel.example.com" {
		t.Fatalf("匹配来源应回显 Origin,得到 %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("应带 Vary: Origin,得到 %q", got)
	}
}

func TestCORS_RejectsOtherOrigin(t *testing.T) {
	h := CORS("https://panel.example.com")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/state", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("非白名单来源不应放行,得到 ACAO=%q", got)
	}
}

func TestCORS_PreflightShortCircuits(t *testing.T) {
	downstream := false
	h := CORS("https://panel.example.com")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downstream = true
	}))
	req := httptest.NewRequest(http.MethodOptions, "/account/login", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if downstream {
		t.Fatal("OPTIONS 预检应短路,不应进入下游")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("预检应回 204,得到 %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("预检应带 Access-Control-Allow-Methods")
	}
}
