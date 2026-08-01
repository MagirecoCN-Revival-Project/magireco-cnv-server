package pki

import (
	"sync"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/directory"
)

// 紧急吊销必须**立即**生效——这正是小时级有效期给不了的那半秒。
func TestRevocationTakesEffectImmediately(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rootCert, _ := NewRoot("offline-root", rootSeed, allCaps, 3, now, DefaultRootTTL)
	rootEncoded[root] = rootCert
	rsCert, _ := root.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: DefaultSubCATTL,
	}, now)
	rsParsed, _, _, _ := decode(rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)
	edgeCert, _ := rsSigner.Issue(IssueParams{
		Subject: "edge-被入侵的", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource}, TTL: DefaultEdgeTTL,
	}, now)
	edgeParsed, _, _, _ := decode(edgeCert)

	v, _ := NewVerifier([]string{rootCert})
	revs := NewRevocations()
	revs.Now = func() time.Time { return now }
	v.Revoked = revs.Hook()

	chain := []string{edgeCert, rsCert}
	if _, err := v.VerifyChain(chain, now); err != nil {
		t.Fatalf("吊销前应当有效: %v", err)
	}

	if err := revs.Add(Revocation{
		Serial: edgeParsed.Serial, Subject: edgeParsed.Sub,
		ExpiresAt: edgeParsed.Exp, Reason: "私钥疑似泄漏",
	}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := v.VerifyChain(chain, now); err == nil {
		t.Fatal("吊销后必须立即失效,不能等到自然过期")
	}
}

// 吊销一张子 CA,它签出的**所有**下级随之失效——这是紧急止损要的效果。
func TestRevokingIntermediateKillsWholeSubtree(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rootCert, _ := NewRoot("offline-root", rootSeed, allCaps, 3, now, DefaultRootTTL)
	rootEncoded[root] = rootCert
	rsCert, _ := root.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: DefaultSubCATTL,
	}, now)
	rsParsed, _, _, _ := decode(rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)

	var chains [][]string
	for _, seed := range []string{edgeSeed, otherSeed} {
		c, err := rsSigner.Issue(IssueParams{
			Subject: "edge-" + seed[:4], Role: RoleEdge, PublicKey: pubOf(t, seed),
			Caps: []string{directory.CapResource}, TTL: DefaultEdgeTTL,
		}, now)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		chains = append(chains, []string{c, rsCert})
	}

	v, _ := NewVerifier([]string{rootCert})
	revs := NewRevocations()
	revs.Now = func() time.Time { return now }
	v.Revoked = revs.Hook()
	for _, ch := range chains {
		if _, err := v.VerifyChain(ch, now); err != nil {
			t.Fatalf("吊销前应当有效: %v", err)
		}
	}

	_ = revs.Add(Revocation{
		Serial: rsParsed.Serial, Subject: rsParsed.Sub,
		ExpiresAt: rsParsed.Exp, Reason: "子 CA 私钥泄漏",
	}, now)
	for i, ch := range chains {
		if _, err := v.VerifyChain(ch, now); err == nil {
			t.Fatalf("第 %d 条链应当随子 CA 一起失效", i)
		}
	}
}

// 条目自清理:撑到原过期时刻即可,之后证书自己就失效了,再留着没有意义。
// 这让吊销集大小由"有效期内被吊销的证书数"决定,而不是随时间无界增长。
func TestRevocationSelfExpires(t *testing.T) {
	now := time.Unix(1700000000, 0)
	revs := NewRevocations()
	exp := now.Add(time.Hour)
	_ = revs.Add(Revocation{Serial: "s1", ExpiresAt: exp.UnixMilli()}, now)

	if got := len(revs.List(now)); got != 1 {
		t.Fatalf("应当有 1 条生效记录,得到 %d", got)
	}
	if got := len(revs.List(exp.Add(time.Minute))); got != 0 {
		t.Fatalf("过了原过期时刻应当不再列出,得到 %d", got)
	}
	if n := revs.Prune(exp.Add(time.Minute)); n != 1 {
		t.Fatalf("应当清掉 1 条,得到 %d", n)
	}
	if revs.Len() != 0 {
		t.Fatal("清理后应当为空")
	}

	// 加一条已经过期的:直接忽略,不该让集合变大
	if err := revs.Add(Revocation{Serial: "s2", ExpiresAt: now.Add(-time.Hour).UnixMilli()}, now); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if revs.Len() != 0 {
		t.Fatal("已过期的吊销记录不应入集")
	}
}

// IsRevoked 顺手清理:不必额外起清理协程。
func TestIsRevokedPrunesLazily(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	revs := NewRevocations()
	// 绕过 Add 的过期检查,直接塞一条已过期的进去(这里刻意用真实墙钟)
	revs.m["s1"] = Revocation{Serial: "s1", ExpiresAt: past.UnixMilli()}
	if revs.IsRevoked(&Cert{Serial: "s1"}) {
		t.Fatal("原证书已过期,不该再报吊销")
	}
	if revs.Len() != 0 {
		t.Fatal("查询时应当顺手清掉无意义的条目")
	}
}

func TestRevocationsInputValidation(t *testing.T) {
	now := time.Unix(1700000000, 0)
	revs := NewRevocations()
	if err := revs.Add(Revocation{ExpiresAt: now.Add(time.Hour).UnixMilli()}, now); err == nil {
		t.Fatal("缺序列号应当报错")
	}
	if err := revs.Add(Revocation{Serial: "s"}, now); err == nil {
		t.Fatal("缺原过期时刻应当报错")
	}
	if revs.IsRevoked(nil) {
		t.Fatal("nil 证书不该被判为吊销")
	}
}

// 吊销集会被并发读(每次验链)与偶发写(推送)同时访问。
func TestRevocationsConcurrentAccess(t *testing.T) {
	now := time.Now()
	revs := NewRevocations()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = revs.Add(Revocation{
				Serial:    string(rune('a' + i)),
				ExpiresAt: now.Add(time.Hour).UnixMilli(),
			}, now)
		}(i)
		go func(i int) {
			defer wg.Done()
			revs.IsRevoked(&Cert{Serial: string(rune('a' + i))})
			revs.List(now)
			revs.Len()
		}(i)
	}
	wg.Wait()
}
