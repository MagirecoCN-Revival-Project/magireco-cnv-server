package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestAccountSessionTouch_SlidingRenewal 锁定"记住登录"的滑动续期语义:
//   * ttl=0      → 只更 last_seen,expires_at 不动
//   * 剩余 > ttl/2 → 不续期(防止每次请求都写库)
//   * 剩余 < ttl/2 → expires_at 推到 now+ttl
//
// 客户端 token 寿命说明文档 §3 依赖于此。
func TestAccountSessionTouch_SlidingRenewal(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "slide.db")
	st, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := st.AccountInsert(ctx, Account{
		ID: "acc_slide", Username: "u", Email: "u@x", PasswordHash: "h",
		Status: "active", CreatedAt: nowMs(),
	}); err != nil {
		t.Fatal(err)
	}

	const ttl = 30 * 24 * time.Hour
	ttlMs := ttl.Milliseconds()

	// 场景 A:剩余 < ttl/2 → 应当续期
	mustReset := func(remaining time.Duration) {
		t.Helper()
		_ = st.AccountSessionDelete(ctx, "tok_a")
		now := nowMs()
		expires := now + remaining.Milliseconds()
		if err := st.AccountSessionInsert(ctx, AccountSession{
			Token: "tok_a", AccountID: "acc_slide",
			CreatedAt: now, LastSeenAt: now, ExpiresAt: expires,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A. 剩余 5 天 < 15 天(ttl/2)→ 应续期
	mustReset(5 * 24 * time.Hour)
	before := nowMs()
	st.AccountSessionTouch(ctx, "tok_a", ttl)
	ses, _, err := st.AccountSessionLookup(ctx, "tok_a")
	if err != nil {
		t.Fatal(err)
	}
	if ses.ExpiresAt < before+ttlMs-1000 {
		t.Errorf("剩余<半 TTL 时应当续期到 now+ttl;ExpiresAt=%d before=%d ttlMs=%d",
			ses.ExpiresAt, before, ttlMs)
	}

	// B. 剩余 20 天 > 15 天 → 不动
	mustReset(20 * 24 * time.Hour)
	sesBefore, _, _ := st.AccountSessionLookup(ctx, "tok_a")
	time.Sleep(3 * time.Millisecond) // 保证 nowMs() 走到下一毫秒,便于断言 last_seen
	st.AccountSessionTouch(ctx, "tok_a", ttl)
	sesAfter, _, _ := st.AccountSessionLookup(ctx, "tok_a")
	if sesBefore.ExpiresAt != sesAfter.ExpiresAt {
		t.Errorf("剩余>半 TTL 时不应续期;before=%d after=%d",
			sesBefore.ExpiresAt, sesAfter.ExpiresAt)
	}
	// last_seen 仍然应该被更新
	if sesAfter.LastSeenAt <= sesBefore.LastSeenAt {
		t.Errorf("last_seen_at 应当被更新;before=%d after=%d",
			sesBefore.LastSeenAt, sesAfter.LastSeenAt)
	}

	// C. ttl=0(不续期模式)→ expires_at 不动
	mustReset(1 * time.Hour) // 故意剩很少
	sesBefore, _, _ = st.AccountSessionLookup(ctx, "tok_a")
	st.AccountSessionTouch(ctx, "tok_a", 0)
	sesAfter, _, _ = st.AccountSessionLookup(ctx, "tok_a")
	if sesBefore.ExpiresAt != sesAfter.ExpiresAt {
		t.Errorf("ttl=0 应当完全不动 expires_at;before=%d after=%d",
			sesBefore.ExpiresAt, sesAfter.ExpiresAt)
	}
}
