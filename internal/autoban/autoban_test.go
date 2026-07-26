package autoban

import (
	"context"
	"path/filepath"
	"testing"

	"magirecocn-revival/cnv-server/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ab.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func activeCount(t *testing.T, st *store.Store) int {
	t.Helper()
	list, err := st.BanListActive(context.Background())
	if err != nil {
		t.Fatalf("BanListActive: %v", err)
	}
	return len(list)
}

func TestAutoBan_CaptchaThreshold(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	s := New(st, nil) // 未写 config 表 → 用 DefaultConfig 阈值
	dev := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	// 前两次失败不封。
	s.OnCaptchaFail(ctx, dev)
	s.OnCaptchaFail(ctx, dev)
	if got := activeCount(t, st); got != 0 {
		t.Fatalf("2 次失败后不应封禁,活跃封禁=%d", got)
	}
	// 第三次触发。
	s.OnCaptchaFail(ctx, dev)
	ban, _ := st.BanActive(ctx, dev)
	if ban == nil {
		t.Fatal("3 次验证码失败后应自动封禁")
	}
	if ban.Reason != ReasonCaptcha || !ban.Auto || ban.IssuedBy != "system" {
		t.Fatalf("封禁记录不符: reason=%q auto=%v by=%q", ban.Reason, ban.Auto, ban.IssuedBy)
	}
	// 去重:继续失败不应再多封一条。
	s.OnCaptchaFail(ctx, dev)
	if got := activeCount(t, st); got != 1 {
		t.Fatalf("重复触发应去重,活跃封禁=%d", got)
	}
}

func TestAutoBan_TamperSevereImmediate(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	s := New(st, nil)
	dev := "ffffffffffffffffffffffffffffffffffffffff"

	s.OnIntegrityViolation(ctx, dev, true) // severe → 即时
	ban, _ := st.BanActive(ctx, dev)
	if ban == nil || ban.Reason != ReasonTamper {
		t.Fatalf("severe 篡改应即时封禁,得到 %+v", ban)
	}
	if ban.ExpireTime != nil {
		t.Fatalf("篡改应为永久封禁(ExpireTime=nil),得到 %v", *ban.ExpireTime)
	}
}

func TestAutoBan_TamperAccumulate(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	s := New(st, nil)
	dev := "0000000000000000000000000000000000000001"

	s.OnIntegrityViolation(ctx, dev, false)
	s.OnIntegrityViolation(ctx, dev, false)
	if c := activeCount(t, st); c != 0 {
		t.Fatalf("非 severe 2 次不应封,活跃=%d", c)
	}
	s.OnIntegrityViolation(ctx, dev, false) // 第 3 次到阈值
	if c := activeCount(t, st); c != 1 {
		t.Fatalf("非 severe 累计 3 次应封,活跃=%d", c)
	}
}

func TestAutoBan_MultiAccount(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	s := New(st, nil)
	dev := "0000000000000000000000000000000000000002"

	for i := 0; i < 4; i++ {
		s.OnAccountLogin(ctx, dev, string(rune('A'+i)))
	}
	if c := activeCount(t, st); c != 0 {
		t.Fatalf("4 个账号不应封,活跃=%d", c)
	}
	s.OnAccountLogin(ctx, dev, "E") // 第 5 个不同账号
	ban, _ := st.BanActive(ctx, dev)
	if ban == nil || ban.Reason != ReasonMultiAccount {
		t.Fatalf("5 个不同账号应封禁多账号切换,得到 %+v", ban)
	}
	// 同一账号重复登录不增加去重计数,不应误封新设备。
	dev2 := "0000000000000000000000000000000000000003"
	for i := 0; i < 10; i++ {
		s.OnAccountLogin(ctx, dev2, "same")
	}
	if ban2, _ := st.BanActive(ctx, dev2); ban2 != nil {
		t.Fatal("同一账号反复登录不应触发多账号封禁")
	}
}

func TestAutoBan_HeartbeatRate(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	s := New(st, nil)
	dev := "0000000000000000000000000000000000000004"

	for i := 0; i < DefaultConfig().Heartbeat.Max-1; i++ {
		s.OnHeartbeat(ctx, dev)
	}
	if c := activeCount(t, st); c != 0 {
		t.Fatalf("低于阈值不应封,活跃=%d", c)
	}
	s.OnHeartbeat(ctx, dev) // 达到阈值
	if ban, _ := st.BanActive(ctx, dev); ban == nil || ban.Reason != ReasonHeartbeat {
		t.Fatal("心跳高频应触发封禁")
	}
}

func TestAutoBan_Disabled_NoOp(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	cfg := DefaultConfig()
	cfg.Enabled = false
	if err := st.ConfigSet(ctx, ConfigKey, cfg); err != nil { // 运行时配置:关闭
		t.Fatalf("ConfigSet: %v", err)
	}
	s := New(st, nil)
	dev := "0000000000000000000000000000000000000005"

	for i := 0; i < 10; i++ {
		s.OnCaptchaFail(ctx, dev)
		s.OnIntegrityViolation(ctx, dev, true)
		s.OnHeartbeat(ctx, dev)
	}
	if c := activeCount(t, st); c != 0 {
		t.Fatalf("禁用时不应有任何自动封禁,活跃=%d", c)
	}
}

func TestAutoBan_RuntimeConfigOverride(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	// 运行时把验证码阈值调到 2 次(默认 3)。
	cfg := DefaultConfig()
	cfg.Captcha.Max = 2
	if err := st.ConfigSet(ctx, ConfigKey, cfg); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
	s := New(st, nil)
	dev := "0000000000000000000000000000000000000006"

	s.OnCaptchaFail(ctx, dev)
	if c := activeCount(t, st); c != 0 {
		t.Fatalf("1 次失败不应封,活跃=%d", c)
	}
	s.OnCaptchaFail(ctx, dev) // 第 2 次到自定义阈值
	if ban, _ := st.BanActive(ctx, dev); ban == nil || ban.Reason != ReasonCaptcha {
		t.Fatal("自定义阈值=2 时,2 次验证码失败应封禁")
	}
}

func TestAutoBan_ValidateRejectsBad(t *testing.T) {
	c := DefaultConfig()
	c.Captcha.Max = 0
	if c.Validate() == nil {
		t.Fatal("Max=0 应校验失败")
	}
	c = DefaultConfig()
	c.Heartbeat.WindowSec = 0
	if c.Validate() == nil {
		t.Fatal("WindowSec=0 应校验失败")
	}
	if DefaultConfig().Validate() != nil {
		t.Fatal("默认配置应通过校验")
	}
}

func TestAutoBan_NilSafe(t *testing.T) {
	var s *Service // nil 接收者
	// 不应 panic。
	s.OnCaptchaFail(context.Background(), "dev")
	s.OnHeartbeat(context.Background(), "dev")
	s.OnIntegrityViolation(context.Background(), "dev", true)
	s.OnResourceRequest(context.Background(), "dev")
	s.OnAccountLogin(context.Background(), "dev", "acc")
}
