package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// smoke 用 SQLite(内存)跑一遍每个表的核心 CRUD,验证迁移 + 三方言抽象层在
// 至少一个驱动上能跑通。MySQL/Postgres 需要 docker,只在显式开启时跑。
func TestSQLite_Smoke(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if st.Dialect().Driver() != DriverSQLite {
		t.Fatalf("expected sqlite driver, got %s", st.Dialect().Driver())
	}

	// admin: insert + lookup + list
	now := time.Now().UnixMilli()
	if err := st.AdminInsert(ctx, Admin{
		ID: "adm_1", Username: "alice", Email: "a@b", PasswordHash: "h",
		Role: "super_admin", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	a, err := st.AdminByEmail(ctx, "a@b")
	if err != nil || a.Username != "alice" {
		t.Fatalf("AdminByEmail: %v %+v", err, a)
	}
	n, _ := st.AdminCountSuper(ctx)
	if n != 1 {
		t.Fatalf("super count = %d", n)
	}

	// account: insert + session join + list filter
	if err := st.AccountInsert(ctx, Account{
		ID: "acc_1", Username: "bob", Email: "b@c", PasswordHash: "h",
		Status: "active", CreatedAt: now, LoginCount: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AccountSessionInsert(ctx, AccountSession{
		Token: "tok_1", AccountID: "acc_1", DeviceName: "dev", OS: "android",
		IP: "1.1.1.1", Region: "JP", CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now + 60_000,
	}); err != nil {
		t.Fatal(err)
	}
	ses, acc, err := st.AccountSessionLookup(ctx, "tok_1")
	if err != nil || ses.AccountID != "acc_1" || acc.Email != "b@c" {
		t.Fatalf("session lookup: %v %+v %+v", err, ses, acc)
	}
	list, total, err := st.AccountList(ctx, "bo", "active", 0, 10)
	if err != nil || total != 1 || len(list) != 1 {
		t.Fatalf("AccountList: %v total=%d list=%v", err, total, list)
	}

	// config: KV roundtrip
	type kv struct{ A int `json:"a"` }
	if err := st.ConfigSet(ctx, "smoke", kv{A: 42}); err != nil {
		t.Fatal(err)
	}
	var got kv
	ok, err := st.ConfigGet(ctx, "smoke", &got)
	if err != nil || !ok || got.A != 42 {
		t.Fatalf("ConfigGet: %v ok=%v got=%+v", err, ok, got)
	}
	// 再写一次触发 upsert 路径
	if err := st.ConfigSet(ctx, "smoke", kv{A: 100}); err != nil {
		t.Fatal(err)
	}
	_, _ = st.ConfigGet(ctx, "smoke", &got)
	if got.A != 100 {
		t.Fatalf("upsert: %d", got.A)
	}

	// saves
	raw := json.RawMessage(`{"slot1":"data"}`)
	if err := st.SavePut(ctx, "acc_1", raw); err != nil {
		t.Fatal(err)
	}
	d, _, sz, err := st.SaveGet(ctx, "acc_1")
	if err != nil || string(d) != string(raw) || sz != int64(len(raw)) {
		t.Fatalf("SaveGet: %v %s %d", err, d, sz)
	}

	// mirrors: replace + flat list(走 RETURNING id 路径)
	if err := st.MirrorsReplaceFlat(ctx, []string{"https://a/", "https://b/"}); err != nil {
		t.Fatal(err)
	}
	urls, err := st.MirrorsFlatList(ctx)
	if err != nil || len(urls) != 2 || urls[0] != "https://a/" {
		t.Fatalf("MirrorsFlatList: %v %v", err, urls)
	}

	// ban: insert + active + lift
	exp := now + 3600_000
	if err := st.BanInsert(ctx, Ban{
		ID: "ban_1", DeviceID: "dev_1", Reason: "test",
		IssuedAt: now, ExpireTime: &exp, IssuedBy: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	b, err := st.BanActive(ctx, "dev_1")
	if err != nil || b == nil {
		t.Fatalf("BanActive: %v %v", err, b)
	}
	if err := st.BanLift(ctx, "ban_1", "admin"); err != nil {
		t.Fatal(err)
	}
	b, _ = st.BanActive(ctx, "dev_1")
	if b != nil {
		t.Fatalf("expected no active ban after lift")
	}

	// device touch + upsert preserving signature
	if err := st.DeviceTouch(ctx, "dev_xyz", "sig123", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeviceTouch(ctx, "dev_xyz", "", "1.0.1"); err != nil {
		t.Fatal(err)
	}

	// audit
	det := json.RawMessage(`{"ip":"127.0.0.1"}`)
	tgt := "acc_1"
	if err := st.AuditInsert(ctx, AuditEntry{
		ID: "log_1", Ts: now, Actor: "admin", Type: "smoke.test",
		Target: &tgt, Details: det,
	}); err != nil {
		t.Fatal(err)
	}
	entries, total, err := st.AuditList(ctx, "", "", 0, 0, 10)
	if err != nil || total != 1 || len(entries) != 1 || entries[0].ID != "log_1" {
		t.Fatalf("AuditList: %v %d %v", err, total, entries)
	}

	// cap-worker: token roundtrip
	if err := st.CapTokenInsert(ctx, "captok_1", now+60_000); err != nil {
		t.Fatal(err)
	}
	ok2, err := st.CapTokenConsume(ctx, "captok_1")
	if err != nil || !ok2 {
		t.Fatalf("CapTokenConsume: %v %v", err, ok2)
	}
	ok3, _ := st.CapTokenConsume(ctx, "captok_1")
	if ok3 {
		t.Fatalf("CapTokenConsume should not double-spend")
	}

	// email code
	if err := st.EmailCodeIssue(ctx, "x@y", "register", "123456", time.Minute); err != nil {
		t.Fatal(err)
	}
	ok4, err := st.EmailCodeCheck(ctx, "x@y", "123456", "register", true)
	if err != nil || !ok4 {
		t.Fatalf("EmailCodeCheck: %v %v", err, ok4)
	}
	ok5, _ := st.EmailCodeCheck(ctx, "x@y", "123456", "register", false)
	if ok5 {
		t.Fatalf("EmailCodeCheck should not pass after consume")
	}
}
