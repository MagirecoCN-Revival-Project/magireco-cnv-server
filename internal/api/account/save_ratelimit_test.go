package account

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"magirecocn-revival/api-server/internal/auth"
	"magirecocn-revival/api-server/internal/middleware"
	"magirecocn-revival/api-server/internal/store"
)

// 启一份真实 Handler:内存 SQLite,跑迁移,插一个账号 + 会话拿到 token。
// 返回 (router, account_token)。
func newSaveTestHandler(t *testing.T, saveLimit int) (http.Handler, string) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "save.db")
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, _ := auth.HashPassword("dummy")
	now := time.Now().UnixMilli()
	if err := st.AccountInsert(ctx, store.Account{
		ID: "acc_save", Username: "saver", Email: "save@x", PasswordHash: hash,
		Status: "active", EmailVerified: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.NewToken()
	if err := st.AccountSessionInsert(ctx, store.AccountSession{
		Token: token, AccountID: "acc_save",
		CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now + (30 * 24 * time.Hour).Milliseconds(),
	}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		St:                st,
		AccountSessionTTL: 30 * 24 * time.Hour,
		SaveLimiter: middleware.NewLimiter("save-put", saveLimit, time.Minute,
			"云存档上传过于频繁,请稍后再试", nil),
	}
	r := chi.NewRouter()
	r.Route("/account", h.Routes)
	return r, token
}

func putSave(t *testing.T, srv http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"token": token,
		"data":  map[string]any{"slot": "x"},
	})
	req := httptest.NewRequest(http.MethodPost, "/account/save/put", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// TestSavePut_RateLimit_PerSession 锁定要求:单会话 1 分钟最多 2 次。
// 第 3 次必须 HTTP 429 + rate_limited。
func TestSavePut_RateLimit_PerSession(t *testing.T) {
	srv, tok := newSaveTestHandler(t, 2)

	for i := 1; i <= 2; i++ {
		w := putSave(t, srv, tok)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次上传应当成功,得到 %d: %s", i, w.Code, w.Body.String())
		}
	}
	// 第 3 次:超限
	w := putSave(t, srv, tok)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("第 3 次应当 429,得到 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "rate_limited") {
		t.Errorf("响应应当含 rate_limited,得到 %s", w.Body.String())
	}
}

// TestSavePut_NoLimiter_PassesThrough SaveLimiter=nil 时应当不限速。
// 保证可配置性:开发环境关掉限流仍能高频压测。
func TestSavePut_NoLimiter_PassesThrough(t *testing.T) {
	srv, tok := newSaveTestHandler(t, 0)
	// saveLimit=0 仍然会创建限流器但配额为 0 → 所有请求都被拒
	// 这里换成显式 nil:重新搭一份 handler
	dsn := filepath.Join(t.TempDir(), "no-limit.db")
	st, _ := store.Open(context.Background(), dsn)
	_ = st.Migrate(context.Background())
	defer st.Close()
	hash, _ := auth.HashPassword("x")
	now := time.Now().UnixMilli()
	_ = st.AccountInsert(context.Background(), store.Account{
		ID: "acc_nl", Username: "nl", Email: "nl@x", PasswordHash: hash,
		Status: "active", EmailVerified: true, CreatedAt: now,
	})
	tok, _ = auth.NewToken()
	_ = st.AccountSessionInsert(context.Background(), store.AccountSession{
		Token: tok, AccountID: "acc_nl",
		CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now + (24 * time.Hour).Milliseconds(),
	})
	h := &Handler{St: st, AccountSessionTTL: 24 * time.Hour, SaveLimiter: nil}
	r := chi.NewRouter()
	r.Route("/account", h.Routes)
	srv = r

	// 连续 10 次都应该通过(SaveLimiter=nil 退化为不限速)
	for i := 0; i < 10; i++ {
		w := putSave(t, srv, tok)
		if w.Code != http.StatusOK {
			t.Fatalf("第 %d 次应当通过(nil limiter),得到 %d", i+1, w.Code)
		}
	}
}
