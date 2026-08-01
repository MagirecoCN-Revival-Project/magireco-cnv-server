package pki

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/directory"
)

// renewEnv 搭一套可续期的边缘节点环境:根 → 子 CA → 边缘,文件都落在磁盘上。
type renewEnv struct {
	f        *fixture
	rsSigner *Signer
	id       *Identity
}

func newRenewEnv(t *testing.T, now time.Time) *renewEnv {
	t.Helper()
	f := newFixture(t, now)
	rsParsed, _, _, _ := decode(f.rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)
	id, err := Load(LoadOptions{
		CertFile: f.edgeCertPath, ChainFiles: []string{f.rsCertPath},
		KeyFile: f.edgeKeyPath, AnchorFiles: []string{f.rootPath}, Now: now,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return &renewEnv{f: f, rsSigner: rsSigner, id: id}
}

func (e *renewEnv) cfg(now time.Time, req RenewFunc) RenewConfig {
	return RenewConfig{
		CertFile: e.f.edgeCertPath, ChainFiles: []string{e.f.rsCertPath},
		KeyFile: e.f.edgeKeyPath, AnchorFiles: []string{e.f.rootPath},
		Request: req,
		Now:     func() time.Time { return now },
	}
}

// 正常续期:新证书落盘、有效期延长、内存里的身份也换掉。
func TestRenewOnceHappyPath(t *testing.T) {
	now := time.Unix(1700000000, 0)
	e := newRenewEnv(t, now)
	later := now.Add(DefaultEdgeTTL/2 + time.Minute)

	r, err := NewRenewer(e.id, e.cfg(later, func(_ context.Context, csr string) ([]string, error) {
		p, err := ParseCSR(csr)
		if err != nil {
			return nil, err
		}
		fresh, err := e.rsSigner.Issue(IssueParams{
			Subject: p.Sub, Role: p.Role, PublicKey: p.Pub,
			Caps: p.Caps, TTL: DefaultEdgeTTL,
		}, later)
		if err != nil {
			return nil, err
		}
		return []string{fresh, e.f.rsCert}, nil
	}))
	if err != nil {
		t.Fatalf("NewRenewer: %v", err)
	}

	oldExp := e.id.Leaf().Exp
	if err := r.RenewOnce(context.Background()); err != nil {
		t.Fatalf("RenewOnce: %v", err)
	}
	if r.Identity().Leaf().Exp <= oldExp {
		t.Fatal("续期后有效期应当延长")
	}
	// 落盘的证书要能被重新加载
	reloaded, err := Load(LoadOptions{
		CertFile: e.f.edgeCertPath, ChainFiles: []string{e.f.rsCertPath},
		KeyFile: e.f.edgeKeyPath, AnchorFiles: []string{e.f.rootPath}, Now: later,
	})
	if err != nil {
		t.Fatalf("续期后的证书应当能重新加载: %v", err)
	}
	if reloaded.Leaf().Exp != r.Identity().Leaf().Exp {
		t.Fatal("落盘的证书与内存里的不一致")
	}
}

// **最要紧的一条**:上游返回垃圾时,现有证书必须原封不动。
// 跳过校验直接落盘的话,上游一次异常响应就能把节点变砖。
func TestRenewRejectsBadUpstreamWithoutTouchingFiles(t *testing.T) {
	now := time.Unix(1700000000, 0)
	later := now.Add(DefaultEdgeTTL/2 + time.Minute)

	// 攻击者/故障上游可能返回的几类东西
	otherRoot, _, _ := NewRoot("offline-root", otherSeed, allCaps, 3, now, DefaultRootTTL)
	cases := map[string]RenewFunc{
		"空链": func(context.Context, string) ([]string, error) {
			return nil, nil
		},
		"垃圾串": func(context.Context, string) ([]string, error) {
			return []string{"cnvc1.garbage.garbage"}, nil
		},
		"锚不到我们的根": func(_ context.Context, csr string) ([]string, error) {
			p, _ := ParseCSR(csr)
			evil, err := otherRoot.Issue(IssueParams{
				Subject: "rs-evil", Role: RoleResource, PublicKey: p.Pub,
				Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: DefaultSubCATTL,
			}, later)
			if err != nil {
				return nil, err
			}
			return []string{evil}, nil
		},
	}
	for name, req := range cases {
		e := newRenewEnv(t, now)
		before, _ := os.ReadFile(e.f.edgeCertPath)
		r, err := NewRenewer(e.id, e.cfg(later, req))
		if err != nil {
			t.Fatalf("NewRenewer: %v", err)
		}
		if err := r.RenewOnce(context.Background()); err == nil {
			t.Fatalf("%s:应当拒绝", name)
		}
		after, _ := os.ReadFile(e.f.edgeCertPath)
		if string(before) != string(after) {
			t.Fatalf("%s:失败时不得改动现有证书", name)
		}
		if r.Identity().Leaf().Exp != e.id.Leaf().Exp {
			t.Fatalf("%s:失败时不得替换内存里的身份", name)
		}
	}
}

// 上游把角色改掉或扩大能力,不能靠一次自动续期就悄悄生效。
func TestRenewRejectsPrivilegeChange(t *testing.T) {
	now := time.Unix(1700000000, 0)
	later := now.Add(DefaultEdgeTTL/2 + time.Minute)

	for name, mutate := range map[string]func(p *CSRPayload){
		"改角色": func(p *CSRPayload) { p.Role = RoleResource },
		"扩能力": func(p *CSRPayload) { p.Caps = []string{directory.CapResource, directory.CapInit} },
	} {
		e := newRenewEnv(t, now)
		r, _ := NewRenewer(e.id, e.cfg(later, func(_ context.Context, csr string) ([]string, error) {
			p, _ := ParseCSR(csr)
			mutate(p)
			isCA := p.Role == RoleResource
			fresh, err := e.rsSigner.Issue(IssueParams{
				Subject: p.Sub, Role: p.Role, PublicKey: p.Pub, Caps: p.Caps,
				CA: isCA, PathLen: 1, TTL: DefaultEdgeTTL,
			}, later)
			if err != nil {
				return nil, err
			}
			return []string{fresh, e.f.rsCert}, nil
		}))
		if err := r.RenewOnce(context.Background()); err == nil {
			t.Fatalf("%s:应当拒绝自动接受", name)
		}
	}
}

// 上游返回一张不延长有效期的证书:接受它只会让续期循环空转。
func TestRenewRejectsNonExtendingCert(t *testing.T) {
	now := time.Unix(1700000000, 0)
	e := newRenewEnv(t, now)
	later := now.Add(DefaultEdgeTTL/2 + time.Minute)

	r, _ := NewRenewer(e.id, e.cfg(later, func(context.Context, string) ([]string, error) {
		return []string{e.f.edgeCert, e.f.rsCert}, nil // 原样返回旧证书
	}))
	if err := r.RenewOnce(context.Background()); err == nil {
		t.Fatal("没有延长有效期的证书应当被拒")
	}
}

// 退避上限必须由**剩余寿命**决定:退到比剩余寿命还长等于放弃后续所有重试。
func TestBackoffCapScalesWithRemainingLifetime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	e := newRenewEnv(t, now)
	r, _ := NewRenewer(e.id, e.cfg(now, func(context.Context, string) ([]string, error) {
		return nil, os.ErrDeadlineExceeded
	}))

	exp := time.UnixMilli(e.id.Leaf().Exp)
	// 刚过半时剩余约 3 小时,上限应当是它的 1/8 左右
	half := exp.Add(-DefaultEdgeTTL / 2)
	capHalf := r.backoffCap(half)
	if capHalf > DefaultEdgeTTL/2 {
		t.Fatalf("退避上限不得超过剩余寿命: cap=%s remaining=%s", capHalf, DefaultEdgeTTL/2)
	}
	// 临近过期时剩余很少,上限应当更小(退得更密)
	nearEnd := exp.Add(-10 * time.Minute)
	capNear := r.backoffCap(nearEnd)
	if capNear >= capHalf {
		t.Fatalf("越接近过期退避应当越短: near=%s half=%s", capNear, capHalf)
	}
	// 但不得低于最小退避,否则会变成忙轮询
	if capNear < r.cfg.MinBackoff {
		t.Fatalf("退避上限不得低于最小退避: %s", capNear)
	}
	// 已过期时也要有个正数,不能变成 0 导致空转
	if got := r.backoffCap(exp.Add(time.Hour)); got <= 0 {
		t.Fatalf("过期后退避仍须为正: %s", got)
	}
}

// 原子写:临时文件必须落在同一目录(跨文件系统 rename 不是原子操作),
// 且成功后不留残余。
func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/x.cert"
	if err := writeFileAtomic(p, []byte("hello"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "hello" {
		t.Fatalf("内容不对: %q %v", got, err)
	}
	// 覆盖写
	if err := writeFileAtomic(p, []byte("world"), 0o644); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	got, _ = os.ReadFile(p)
	if string(got) != "world" {
		t.Fatalf("覆盖后内容不对: %q", got)
	}
	// 目录里不该留下临时文件
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("留下了临时文件 %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("目录里应当只有一个文件,实际 %d", len(entries))
	}
}

// 续期沿用同一把私钥:换密钥属于另一件事,混进来一旦落盘失败就两头落空。
func TestRenewKeepsSameKey(t *testing.T) {
	now := time.Unix(1700000000, 0)
	e := newRenewEnv(t, now)
	later := now.Add(DefaultEdgeTTL/2 + time.Minute)

	var seenPub string
	r, _ := NewRenewer(e.id, e.cfg(later, func(_ context.Context, csr string) ([]string, error) {
		p, _ := ParseCSR(csr)
		seenPub = p.Pub
		fresh, err := e.rsSigner.Issue(IssueParams{
			Subject: p.Sub, Role: p.Role, PublicKey: p.Pub,
			Caps: p.Caps, TTL: DefaultEdgeTTL,
		}, later)
		if err != nil {
			return nil, err
		}
		return []string{fresh, e.f.rsCert}, nil
	}))
	if err := r.RenewOnce(context.Background()); err != nil {
		t.Fatalf("RenewOnce: %v", err)
	}
	if seenPub != pubOf(t, edgeSeed) {
		t.Fatal("续期用的应当是原来那把密钥")
	}
	keyAfter, _ := readTrimmed(e.f.edgeKeyPath)
	if keyAfter != edgeSeed {
		t.Fatal("续期不得改动私钥文件")
	}
}

func TestNewRenewerValidatesConfig(t *testing.T) {
	now := time.Unix(1700000000, 0)
	e := newRenewEnv(t, now)
	ok := func(_ context.Context, _ string) ([]string, error) { return nil, nil }

	if _, err := NewRenewer(nil, e.cfg(now, ok)); err == nil {
		t.Fatal("没有身份应当报错")
	}
	c := e.cfg(now, nil)
	if _, err := NewRenewer(e.id, c); err == nil {
		t.Fatal("没有 Request 回调应当报错")
	}
	c = e.cfg(now, ok)
	c.AnchorFiles = nil
	if _, err := NewRenewer(e.id, c); err == nil {
		t.Fatal("没有信任根应当报错")
	}
}
