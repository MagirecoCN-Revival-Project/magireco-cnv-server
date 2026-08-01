package pki

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/directory"
)

// 这组测试的重心是**攻击路径**而不是 happy path。证书链的历史教训是:正常流程
// 很容易写对,出事的永远是"少检查了一个字段",而且少检查之后一切照常工作、
// 没有任何症状——直到有人发现能签出任意身份。

const (
	rootSeed  = "11111111111111111111111111111111111111111111111111111111111111ff"
	rsSeed    = "2222222222222222222222222222222222222222222222222222222222222200"
	edgeSeed  = "3333333333333333333333333333333333333333333333333333333333333300"
	evilSeed  = "4444444444444444444444444444444444444444444444444444444444444400"
	otherSeed = "5555555555555555555555555555555555555555555555555555555555555500"
)

var allCaps = []string{
	directory.CapInit, directory.CapLogin, directory.CapAccount,
	directory.CapSave, directory.CapResource,
}

func pubOf(t *testing.T, seedHex string) string {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return hex.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// buildHierarchy 造出题目里描述的那棵树:
//
//	api-server(根 CA) → resource-server(子 CA) → edge(叶子,仅 resource)
func buildHierarchy(t *testing.T, now time.Time) (root *Signer, rsSigner *Signer, rsCert string, edgeCert string) {
	t.Helper()
	root, rootCert, err := NewRoot("api-server", rootSeed, allCaps, 3, now, 365*24*time.Hour)
	rootEncoded[root] = rootCert
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rsCert, err = root.Issue(IssueParams{
		Subject:   "resource-server-1",
		Role:      RoleResource,
		PublicKey: pubOf(t, rsSeed),
		Caps:      []string{directory.CapInit, directory.CapResource},
		CA:        true, PathLen: 2,
		TTL: 90 * 24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("签子 CA: %v", err)
	}
	rsParsed, _, _, err := decode(rsCert)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	rsSigner, err = NewSigner(rsParsed, rsSeed)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	edgeCert, err = rsSigner.Issue(IssueParams{
		Subject:   "edge-tokyo-1",
		Role:      RoleEdge,
		PublicKey: pubOf(t, edgeSeed),
		Caps:      []string{directory.CapResource},
		TTL:       30 * 24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("签边缘节点: %v", err)
	}
	return root, rsSigner, rsCert, edgeCert
}

// rootEncoded 记住每个自签根对应的编码证书串——校验器要钉整张证书,不只是公钥。
var rootEncoded = map[*Signer]string{}

func anchoredOn(t *testing.T, s *Signer) *Verifier {
	t.Helper()
	enc, ok := rootEncoded[s]
	if !ok {
		t.Fatal("测试用例没有记录该根的编码证书")
	}
	v, err := NewVerifier([]string{enc})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func TestFullChainVerifies(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, _, rsCert, edgeCert := buildHierarchy(t, now)
	v := anchoredOn(t, root)

	res, err := v.VerifyChain([]string{edgeCert, rsCert}, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("完整链应当通过: %v", err)
	}
	if res.Leaf.Sub != "edge-tokyo-1" {
		t.Fatalf("叶子不对: %+v", res.Leaf)
	}
	if len(res.Caps) != 1 || res.Caps[0] != directory.CapResource {
		t.Fatalf("生效能力应当只有 resource,得到 %v", res.Caps)
	}
	if len(res.Path) != 2 {
		t.Fatalf("路径长度不对: %d", len(res.Path))
	}
}

// 独立模式(没有 api-server)走的是**同一套**信任模型,只是少了 api-server 这个
// 参与者:根仍然是那个离线根,resource-server 仍然是它签出来的子 CA。
//
// 根**不能**直签边缘节点——这是"根恒在"模型的直接后果,也正是它的价值所在:
// 部署形态变化不会改变链的形状,不存在"我现在是哪种模式"的分支。
func TestStandaloneStillUsesSameChainShape(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rootCert, err := NewRoot("offline-root", rootSeed, allCaps, 3, now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rootEncoded[root] = rootCert

	// 根不能越过子 CA 直签边缘节点
	if _, err := root.Issue(IssueParams{
		Subject: "edge-1", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource}, TTL: time.Hour,
	}, now); !errors.Is(err, ErrRoleNotAllowed) {
		t.Fatalf("根不应能直签边缘节点,得到 %v", err)
	}

	rsCert, err := root.Issue(IssueParams{
		Subject: "rs-standalone", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapInit, directory.CapResource},
		CA:   true, PathLen: 2, TTL: 90 * 24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("签子 CA: %v", err)
	}
	rsParsed, _, _, _ := decode(rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)
	edge, err := rsSigner.Issue(IssueParams{
		Subject: "edge-1", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource}, TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("签边缘节点: %v", err)
	}
	v := anchoredOn(t, root)
	if _, err := v.VerifyChain([]string{edge, rsCert}, now); err != nil {
		t.Fatalf("根 → 子 CA → 边缘应当通过: %v", err)
	}
}

// ── 以下是攻击路径 ──

// 最经典的证书链漏洞:签发者不是 CA 却签出了下级。
// 漏掉 CA 标志检查的话,任何持有叶子证书的边缘节点都能冒充任意主体。
func TestLeafCannotSignSubordinate(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, _, rsCert, edgeCert := buildHierarchy(t, now)

	// 边缘节点(非 CA)拿自己的私钥去签一张"下级"
	edgeParsed, _, _, _ := decode(edgeCert)
	edgeSigner, err := NewSigner(edgeParsed, edgeSeed)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	// 签发侧就该拦住
	if _, err := edgeSigner.Issue(IssueParams{
		Subject: "冒充的业务节点", PublicKey: pubOf(t, evilSeed),
		Caps: []string{directory.CapResource}, TTL: time.Hour,
	}, now); !errors.Is(err, ErrNotCA) {
		t.Fatalf("非 CA 签发应当被拒,得到 %v", err)
	}

	// 就算绕过签发侧手工造一张,校验侧也必须拒
	forged := handCraft(t, &Cert{
		V: 1, Serial: "aa", Sub: "冒充的业务节点", Pub: pubOf(t, evilSeed),
		Iss: "edge-tokyo-1", Role: RoleEdge, Caps: []string{directory.CapResource},
		NBF: now.UnixMilli(), Exp: now.Add(time.Hour).UnixMilli(),
	}, edgeSeed)

	v := anchoredOn(t, root)
	if _, err := v.VerifyChain([]string{forged, edgeCert, rsCert}, now); !errors.Is(err, ErrNotCA) {
		t.Fatalf("由非 CA 签出的证书必须被拒,得到 %v", err)
	}
}

// 能力提权:边缘节点只有 resource,不能给自己或下级弄出 save/login。
func TestCapabilityEscalationRejected(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rsSigner, rsCert, _ := buildHierarchy(t, now)

	// 签发侧:子 CA 只有 init+resource,不能签出带 save 的下级
	if _, err := rsSigner.Issue(IssueParams{
		Subject: "edge-2", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource, directory.CapSave}, TTL: time.Hour,
	}, now); !errors.Is(err, ErrCapEscalation) {
		t.Fatalf("超出上级的能力应当被拒,得到 %v", err)
	}

	// 校验侧:手工造一张越权证书也必须拒
	forged := handCraft(t, &Cert{
		V: 1, Serial: "bb", Sub: "edge-2", Pub: pubOf(t, edgeSeed),
		Iss: "resource-server-1", Role: RoleEdge,
		// save 不在子 CA 的能力集里
		Caps: []string{directory.CapResource, directory.CapSave},
		NBF:  now.UnixMilli(), Exp: now.Add(time.Hour).UnixMilli(),
	}, rsSeed)

	v := anchoredOn(t, root)
	if _, err := v.VerifyChain([]string{forged, rsCert}, now); !errors.Is(err, ErrCapEscalation) {
		t.Fatalf("越权证书必须被拒,得到 %v", err)
	}
}

// 凭证类能力绝不能落到只有 resource 的节点上——§2 点名要防的事。
func TestVerifyChainForCapGuardsCredentialCaps(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, _, rsCert, edgeCert := buildHierarchy(t, now)
	v := anchoredOn(t, root)

	chain := []string{edgeCert, rsCert}
	if _, err := v.VerifyChainForCap(chain, directory.CapResource, now); err != nil {
		t.Fatalf("边缘节点应当有 resource 能力: %v", err)
	}
	for _, cap := range []string{directory.CapSave, directory.CapLogin, directory.CapAccount} {
		if _, err := v.VerifyChainForCap(chain, cap, now); err == nil {
			t.Fatalf("边缘节点不该被授予 %q", cap)
		}
	}
}

// 攻击者自带信任根:链再自洽,只要锚不在钉住的表里就必须拒。
func TestAttackerSuppliedRootRejected(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, _, _, _ := buildHierarchy(t, now)

	evilRoot, _, err := NewRoot("api-server", evilSeed, allCaps, 3, now, time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	// 注意:主体标识和真根**同名**,只有公钥不同
	evilLeaf, err := evilRoot.Issue(IssueParams{
		Subject: "rs-evil", Role: RoleResource, PublicKey: pubOf(t, otherSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	v := anchoredOn(t, root)
	if _, err := v.VerifyChain([]string{evilLeaf}, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("同名但不同公钥的伪造根必须被拒,得到 %v", err)
	}
}

// 签发者标识与上级主体对不上:只验签不验标识的话,后续按 iss 做的判断会错位。
func TestIssuerSubjectMismatchRejected(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, _, rsCert, _ := buildHierarchy(t, now)

	forged := handCraft(t, &Cert{
		V: 1, Serial: "cc", Sub: "edge-3", Pub: pubOf(t, edgeSeed),
		Iss: "某个不存在的签发者", Role: RoleEdge, // 但确实是用子 CA 的私钥签的
		Caps: []string{directory.CapResource},
		NBF:  now.UnixMilli(), Exp: now.Add(time.Hour).UnixMilli(),
	}, rsSeed)

	v := anchoredOn(t, root)
	if _, err := v.VerifyChain([]string{forged, rsCert}, now); !errors.Is(err, ErrIssuerMismatch) {
		t.Fatalf("iss 与上级 sub 不符必须被拒,得到 %v", err)
	}
}

// 链深度:path_len 必须严格递减,不能无限延展。
func TestPathLenEnforced(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rootCert, err := NewRoot("root", rootSeed, allCaps, 1, now, time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rootEncoded[root] = rootCert
	// path_len=1 的根只能签叶子,不能再签一层 CA
	if _, err := root.Issue(IssueParams{
		Subject: "sub-ca", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 5,
		TTL: time.Hour,
	}, now); !errors.Is(err, ErrPathLenViolated) {
		t.Fatalf("path_len 耗尽后不应能再签 CA,得到 %v", err)
	}

	// 手工造一张 path_len 不递减的子 CA,校验侧也要拒
	forged := handCraft(t, &Cert{
		V: 1, Serial: "dd", Sub: "sub-ca", Pub: pubOf(t, rsSeed),
		Iss: "root", Role: RoleResource, Caps: []string{directory.CapResource},
		CA: true, PathLen: 9,
		NBF: now.UnixMilli(), Exp: now.Add(time.Hour).UnixMilli(),
	}, rootSeed)
	v := anchoredOn(t, root)
	if _, err := v.VerifyChain([]string{forged}, now); !errors.Is(err, ErrPathLenViolated) {
		t.Fatalf("path_len 不递减必须被拒,得到 %v", err)
	}
}

// 中间证书过期:整条链失效,不能只看叶子。
func TestExpiredIntermediateFailsChain(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rootCert, err := NewRoot("root", rootSeed, allCaps, 3, now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rootEncoded[root] = rootCert
	// 子 CA 只活 1 小时
	rsCert, err := root.Issue(IssueParams{
		Subject: "rs", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2,
		TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	rsParsed, _, _, _ := decode(rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)

	// 叶子想要 30 天,但签发侧会把 exp 压到不超过上级
	edge, err := rsSigner.Issue(IssueParams{
		Subject: "edge", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource}, TTL: 30 * 24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	edgeParsed, _, _, _ := decode(edge)
	if edgeParsed.Exp > rsParsed.Exp {
		t.Fatalf("叶子不得比签发者活得久: leaf=%d parent=%d", edgeParsed.Exp, rsParsed.Exp)
	}

	v := anchoredOn(t, root)
	if _, err := v.VerifyChain([]string{edge, rsCert}, now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("中间证书过期后整条链应当失效,得到 %v", err)
	}
}

func TestRevocationHook(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, _, rsCert, edgeCert := buildHierarchy(t, now)
	v := anchoredOn(t, root)

	rsParsed, _, _, _ := decode(rsCert)
	// 吊销中间证书,整条链都要倒
	v.Revoked = func(c *Cert) bool { return c.Serial == rsParsed.Serial }
	if _, err := v.VerifyChain([]string{edgeCert, rsCert}, now); !errors.Is(err, ErrRevoked) {
		t.Fatalf("吊销子 CA 后整条链应当失效,得到 %v", err)
	}
	// 撤销回调必须拿得到完整证书(才知道去哪查名单)
	var sawIssuers []string
	v.Revoked = func(c *Cert) bool { sawIssuers = append(sawIssuers, c.Iss); return false }
	if _, err := v.VerifyChain([]string{edgeCert, rsCert}, now); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if len(sawIssuers) != 2 || sawIssuers[0] != "resource-server-1" || sawIssuers[1] != "api-server" {
		t.Fatalf("撤销回调应当遍历整条链并带上签发者: %v", sawIssuers)
	}
}

func TestMalformedAndBoundaryInputs(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, _, rsCert, edgeCert := buildHierarchy(t, now)
	v := anchoredOn(t, root)

	if _, err := v.VerifyChain(nil, now); !errors.Is(err, ErrEmptyChain) {
		t.Fatalf("空链应当被拒,得到 %v", err)
	}
	long := make([]string, MaxChainLen+1)
	for i := range long {
		long[i] = edgeCert
	}
	if _, err := v.VerifyChain(long, now); !errors.Is(err, ErrChainTooLong) {
		t.Fatalf("超长链应当被拒,得到 %v", err)
	}
	for name, bad := range map[string]string{
		"空串":          "",
		"只有前缀":        "cnvc1.",
		"缺签名段":        "cnvc1.abc",
		"载荷不是 base64": "cnvc1.@@@.@@@",
		"载荷不是 JSON":   "cnvc1." + base64.RawURLEncoding.EncodeToString([]byte("x")) + ".AAAA",
		"其它前缀":        "cnv1.abc.def",
	} {
		if _, err := v.VerifyChain([]string{bad}, now); err == nil {
			t.Fatalf("%s 应当被拒", name)
		}
	}
	// 少给一级(漏掉中间证书):叶子的 iss 不在钉住的根表里
	if _, err := v.VerifyChain([]string{edgeCert}, now); !errors.Is(err, ErrUnknownAnchor) {
		t.Fatalf("链条断裂应当被拒,得到 %v", err)
	}
	// 顺序颠倒(根在前)同样不成立
	if _, err := v.VerifyChain([]string{rsCert, edgeCert}, now); err == nil {
		t.Fatal("链顺序颠倒应当被拒")
	}
}

func TestNewSignerRejectsMismatchedKey(t *testing.T) {
	now := time.Unix(1700000000, 0)
	_, _, rsCert, _ := buildHierarchy(t, now)
	rsParsed, _, _, _ := decode(rsCert)
	if _, err := NewSigner(rsParsed, evilSeed); err == nil {
		t.Fatal("私钥与证书公钥不匹配时应当报错")
	}
}

func TestUnanchoredVerifierRejectsEverything(t *testing.T) {
	now := time.Unix(1700000000, 0)
	_, _, rsCert, edgeCert := buildHierarchy(t, now)
	v, err := NewVerifier(nil)
	if err != nil {
		t.Fatalf("空信任根表本身不该报错: %v", err)
	}
	if v.Anchored() {
		t.Fatal("空表 Anchored() 应为 false")
	}
	if _, err := v.VerifyChain([]string{edgeCert, rsCert}, now); !errors.Is(err, ErrUnknownAnchor) {
		t.Fatalf("没钉根时应当拒绝一切链,得到 %v", err)
	}
}

// handCraft 用指定私钥手工签一张证书,绕过 Issue 的所有前置检查。
// 专用于验证**校验侧**是否独立成立——签发侧的检查挡不住别人自己造证书。
func handCraft(t *testing.T, c *Cert, seedHex string) string {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	inner, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	signing := Prefix + "." + base64.RawURLEncoding.EncodeToString(inner)
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// 签名覆盖版本前缀,与 clienttoken 同一约定。
func TestSignatureBindsVersionPrefix(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rootCert, err := NewRoot("root", rootSeed, allCaps, 2, now, time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rootEncoded[root] = rootCert
	sub, err := root.Issue(IssueParams{
		Subject: "sub", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 1, TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	parts := strings.Split(sub, ".")
	pub, _ := hex.DecodeString(root.Cert.Pub)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])

	if ed25519.Verify(ed25519.PublicKey(pub), []byte(parts[1]), sig) {
		t.Fatal("签名不应当只覆盖 payload")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("签名应当覆盖 cnvc1.<payload>")
	}
}

// 回归测试:链尾那张由根**直签**的证书,必须和中间层走同一套检查。
//
// 第一版实现把 CA / path_len / 能力子集三项检查写在 "有上级时才做" 的分支里,
// 而根不在链中,于是根直签的证书完全不受约束——一张 path_len 写成 9、能力写成
// 全集的子 CA 可以直接通过。改成钉整张根证书之后这个特例消失了。
func TestAnchorSignedCertStillFullyChecked(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// 根的能力只有 resource,path_len 只有 1
	root, rootCert, err := NewRoot("narrow-root", rootSeed,
		[]string{directory.CapResource}, 1, now, time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	_ = root
	v, err := NewVerifier([]string{rootCert})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// 手工造一张根直签的证书,能力越权 + path_len 不递减
	forged := handCraft(t, &Cert{
		V: 1, Serial: "ee", Sub: "越权子CA", Pub: pubOf(t, rsSeed),
		Iss: "narrow-root", Role: RoleResource,
		Caps: []string{directory.CapResource, directory.CapSave, directory.CapLogin},
		CA:   true, PathLen: 9,
		NBF: now.UnixMilli(), Exp: now.Add(time.Minute).UnixMilli(),
	}, rootSeed)

	if _, err := v.VerifyChain([]string{forged}, now); err == nil {
		t.Fatal("根直签的证书也必须受 CA/path_len/能力子集检查,不能有特例")
	}

	// 只把 path_len 改合规、能力仍越权 —— 仍须被拒
	forged2 := handCraft(t, &Cert{
		V: 1, Serial: "ff", Sub: "越权叶子", Pub: pubOf(t, rsSeed),
		Iss: "narrow-root", Role: RoleEdge,
		Caps: []string{directory.CapSave},
		NBF:  now.UnixMilli(), Exp: now.Add(time.Minute).UnixMilli(),
	}, rootSeed)
	if _, err := v.VerifyChain([]string{forged2}, now); !errors.Is(err, ErrCapEscalation) {
		t.Fatalf("根直签也不得越权,得到 %v", err)
	}
}

// 信任根本身配错是最致命的配置错误:错了不是拒绝服务,而是信任了不该信任的东西。
func TestNewVerifierRejectsBadAnchors(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, _, err := NewRoot("root", rootSeed, allCaps, 2, now, time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	// 非自签的证书不能当根
	child, err := root.Issue(IssueParams{
		Subject: "child", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := NewVerifier([]string{child}); err == nil {
		t.Fatal("非自签证书不应被接受为信任根")
	}
	// 自签但签名对不上(公钥换成别人的)
	bogus := handCraft(t, &Cert{
		V: 1, Serial: "11", Sub: "fake", Pub: pubOf(t, edgeSeed),
		Iss: "fake", Role: RoleRoot, Caps: allCaps, CA: true, PathLen: 2,
		NBF: now.UnixMilli(), Exp: now.Add(time.Hour).UnixMilli(),
	}, rootSeed) // 用 root 的私钥签,但证书里写的是 edge 的公钥
	if _, err := NewVerifier([]string{bogus}); err == nil {
		t.Fatal("自签名校验不过的证书不应被接受为信任根")
	}
	// 自签且签名正确,但不是 CA
	notCA := handCraft(t, &Cert{
		V: 1, Serial: "22", Sub: "notca", Pub: pubOf(t, rootSeed),
		Iss: "notca", Role: RoleRoot, Caps: allCaps, CA: false,
		NBF: now.UnixMilli(), Exp: now.Add(time.Hour).UnixMilli(),
	}, rootSeed)
	if _, err := NewVerifier([]string{notCA}); err == nil {
		t.Fatal("非 CA 的证书不应被接受为信任根")
	}
}

// ── 互相鉴权 ──

// buildPeers 造出「根恒在」模型下的两个平级子 CA。
func buildPeers(t *testing.T, now time.Time) (v *Verifier, apiSigner, rsSigner *Signer, apiCert, rsCert string) {
	t.Helper()
	root, rootCert, err := NewRoot("offline-root", rootSeed, allCaps, 3, now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	apiCert, err = root.Issue(IssueParams{
		Subject: "api-1", Role: RoleAPI, PublicKey: pubOf(t, otherSeed),
		Caps: []string{directory.CapLogin, directory.CapAccount, directory.CapSave},
		CA:   true, PathLen: 2, TTL: 90 * 24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("签 api 子 CA: %v", err)
	}
	rsCert, err = root.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapInit, directory.CapResource},
		CA:   true, PathLen: 2, TTL: 90 * 24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("签 resource 子 CA: %v", err)
	}
	apiParsed, _, _, _ := decode(apiCert)
	rsParsed, _, _, _ := decode(rsCert)
	if apiSigner, err = NewSigner(apiParsed, otherSeed); err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	if rsSigner, err = NewSigner(rsParsed, rsSeed); err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	if v, err = NewVerifier([]string{rootCert}); err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return
}

// 双向鉴权的正常流程:各出示链 + 各自签对方的挑战。
func TestMutualAuthHappyPath(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, apiSigner, rsSigner, apiCert, rsCert := buildPeers(t, now)

	// resource-server 验 api-server
	nonceFromRS, _ := NewNonce()
	proofByAPI, err := apiSigner.ProveTo(nonceFromRS, "rs-1")
	if err != nil {
		t.Fatalf("ProveTo: %v", err)
	}
	if _, err := v.AuthenticatePeer([]string{apiCert}, RoleAPI, nonceFromRS, proofByAPI, "rs-1", now); err != nil {
		t.Fatalf("resource-server 验 api-server 应当通过: %v", err)
	}

	// api-server 验 resource-server
	nonceFromAPI, _ := NewNonce()
	proofByRS, err := rsSigner.ProveTo(nonceFromAPI, "api-1")
	if err != nil {
		t.Fatalf("ProveTo: %v", err)
	}
	if _, err := v.AuthenticatePeer([]string{rsCert}, RoleResource, nonceFromAPI, proofByRS, "api-1", now); err != nil {
		t.Fatalf("api-server 验 resource-server 应当通过: %v", err)
	}
}

// **核心**:证书链是公开的,单出示链不能证明任何事。
func TestChainAloneProvesNothing(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, _, _, apiCert, _ := buildPeers(t, now)

	nonce, _ := NewNonce()
	// 攻击者抓到了 api-server 的证书链(它本来就公开),但没有私钥
	if _, err := v.AuthenticatePeer([]string{apiCert}, RoleAPI, nonce, "", "rs-1", now); !errors.Is(err, ErrBadProof) {
		t.Fatalf("没有持有性证明必须被拒,得到 %v", err)
	}
	// 用别人的私钥签也不行
	_, _, rsSigner2, _, _ := buildPeers(t, now)
	bogus, _ := rsSigner2.ProveTo(nonce, "rs-1")
	if _, err := v.AuthenticatePeer([]string{apiCert}, RoleAPI, nonce, bogus, "rs-1", now); !errors.Is(err, ErrBadProof) {
		t.Fatalf("用别的私钥签的证明必须被拒,得到 %v", err)
	}
}

// 重放:换一个挑战,旧签名立刻失效。
func TestProofNotReplayableAcrossNonces(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, apiSigner, _, apiCert, _ := buildPeers(t, now)

	nonce1, _ := NewNonce()
	proof, _ := apiSigner.ProveTo(nonce1, "rs-1")
	if _, err := v.AuthenticatePeer([]string{apiCert}, RoleAPI, nonce1, proof, "rs-1", now); err != nil {
		t.Fatalf("原挑战应当通过: %v", err)
	}
	nonce2, _ := NewNonce()
	if _, err := v.AuthenticatePeer([]string{apiCert}, RoleAPI, nonce2, proof, "rs-1", now); !errors.Is(err, ErrBadProof) {
		t.Fatalf("换挑战后旧签名必须失效,得到 %v", err)
	}
}

// 反射:A 向 B 证明用的签名,不能被拿去在别的方向上冒充。
func TestProofNotReflectable(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, apiSigner, _, apiCert, _ := buildPeers(t, now)

	nonce, _ := NewNonce()
	// api-server 向 rs-1 证明
	proof, _ := apiSigner.ProveTo(nonce, "rs-1")

	// 攻击者把这份签名拿去冒充"api-server 向另一个验证方证明"
	if _, err := v.AuthenticatePeer([]string{apiCert}, RoleAPI, nonce, proof, "别的验证方", now); !errors.Is(err, ErrBadProof) {
		t.Fatalf("换验证方后签名必须失配,得到 %v", err)
	}
	// 也不能把挑战反射回签名者自己
	if _, err := v.AuthenticatePeer([]string{apiCert}, RoleAPI, nonce, proof, "api-1", now); !errors.Is(err, ErrBadProof) {
		t.Fatalf("反射回自己必须失配,得到 %v", err)
	}
}

// 角色不符即拒:resource-server 连上来的若声称是 api-server,必须拒。
func TestPeerRoleMismatchRejected(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, _, rsSigner, _, rsCert := buildPeers(t, now)

	nonce, _ := NewNonce()
	proof, _ := rsSigner.ProveTo(nonce, "api-1")
	if _, err := v.AuthenticatePeer([]string{rsCert}, RoleAPI, nonce, proof, "api-1", now); !errors.Is(err, ErrRoleMismatch) {
		t.Fatalf("角色不符必须被拒,得到 %v", err)
	}
	if _, err := v.AuthenticatePeer([]string{rsCert}, RoleResource, nonce, proof, "api-1", now); err != nil {
		t.Fatalf("角色相符应当通过: %v", err)
	}
}

// api 与 resource 是平级子 CA:api 被击穿也造不出一台 resource-server 的身份。
func TestApiCannotMintResourceIdentity(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, apiSigner, _, apiCert, _ := buildPeers(t, now)

	if _, err := apiSigner.Issue(IssueParams{
		Subject: "冒充的资源服务端", Role: RoleResource, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapAccount}, CA: true, PathLen: 1, TTL: time.Hour,
	}, now); !errors.Is(err, ErrRoleNotAllowed) {
		t.Fatalf("api 子 CA 不应能签出 resource 角色,得到 %v", err)
	}
	// 手工造一张,校验侧同样要拒
	forged := handCraft(t, &Cert{
		V: 1, Serial: "99", Sub: "冒充的资源服务端", Pub: pubOf(t, edgeSeed),
		Iss: "api-1", Role: RoleResource, Caps: []string{directory.CapAccount},
		CA: true, PathLen: 1,
		NBF: now.UnixMilli(), Exp: now.Add(time.Minute).UnixMilli(),
	}, otherSeed)
	if _, err := v.VerifyChain([]string{forged, apiCert}, now); !errors.Is(err, ErrRoleNotAllowed) {
		t.Fatalf("校验侧也必须拒绝跨角色签发,得到 %v", err)
	}
}

// 弱挑战要拒:太短的随机数可被预生成签名表打掉。
func TestWeakNonceRejected(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, apiSigner, _, apiCert, _ := buildPeers(t, now)

	short := []byte("tooshort")
	if _, err := apiSigner.ProveTo(short, "rs-1"); !errors.Is(err, ErrWeakNonce) {
		t.Fatalf("签名侧应当拒绝弱挑战,得到 %v", err)
	}
	if _, err := v.AuthenticatePeer([]string{apiCert}, RoleAPI, short, "AAAA", "rs-1", now); !errors.Is(err, ErrWeakNonce) {
		t.Fatalf("校验侧应当拒绝弱挑战,得到 %v", err)
	}
}

// ── 客户端验服务端:DNS 劫持下的主动止损 ──

// 客户端**不需要**自己的密钥,也不需要被根 CA 签发。它只需要钉住根公钥,
// 就能验证"对面这台机器确实是根授权过的 resource-server"。
//
// 这是整套模型里最省的一环:验签是公钥操作,不需要私钥、不需要注册、不需要
// 任何密钥保管。客户端只是又一个 Verifier,selfSub 填自己的 device_id。
func TestClientVerifiesServerWithoutOwningAnyKey(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, _, rsSigner, _, rsCert := buildPeers(t, now)

	// 客户端拿自己的 device_id 当验证方标识,生成挑战
	const deviceID = "device-abc123"
	nonce, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	// 真服务端用私钥应答
	proof, err := rsSigner.ProveTo(nonce, deviceID)
	if err != nil {
		t.Fatalf("ProveTo: %v", err)
	}
	if _, err := v.AuthenticatePeer([]string{rsCert}, RoleResource, nonce, proof, deviceID, now); err != nil {
		t.Fatalf("客户端应当能验证真服务端: %v", err)
	}
}

// DNS 劫持:攻击者把域名指到自己的机器上。
//
// 他能做到的极限是**原样重放**他抓到的真服务端证书链——链是公开的,拦不住他拿到。
// 但他没有对应私钥,签不出客户端这次新生成的挑战,于是客户端拒绝连接。
func TestDNSHijackDefeatedByProofOfPossession(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, _, rsSigner, _, rsCert := buildPeers(t, now)
	const deviceID = "device-abc123"

	// 攻击者此前观测到的一次合法握手(挑战 + 证明)
	observedNonce, _ := NewNonce()
	observedProof, _ := rsSigner.ProveTo(observedNonce, deviceID)

	// 客户端这次连接生成了**新**挑战,劫持者只能重放旧的证明
	freshNonce, _ := NewNonce()
	if _, err := v.AuthenticatePeer([]string{rsCert}, RoleResource,
		freshNonce, observedProof, deviceID, now); !errors.Is(err, ErrBadProof) {
		t.Fatalf("重放旧证明必须被拒,得到 %v", err)
	}

	// 劫持者也可以自建一整套 CA 并签出漂亮的链——但它锚不到客户端钉住的根
	evilRoot, evilRootCert, _ := NewRoot("offline-root", evilSeed, allCaps, 3, now, time.Hour)
	evilRS, _ := evilRoot.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapInit, directory.CapResource},
		CA:   true, PathLen: 2, TTL: time.Hour,
	}, now)
	evilParsed, _, _, _ := decode(evilRS)
	evilSigner, _ := NewSigner(evilParsed, edgeSeed)
	evilProof, _ := evilSigner.ProveTo(freshNonce, deviceID)

	// 注意:攻击者的根**同名**、子 CA 也**同名**,证明也是真的用自己私钥签的——
	// 整条链自洽,唯一对不上的是根公钥。
	if _, err := v.AuthenticatePeer([]string{evilRS}, RoleResource,
		freshNonce, evilProof, deviceID, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("攻击者自建的同名 CA 必须被拒,得到 %v", err)
	}
	_ = evilRootCert
}

// 一份给 A 的证明不能被拿去糊弄 B:证明绑了验证方标识,而客户端用的是 device_id。
// 所以劫持者即使从别的设备骗到过一次合法应答,也不能拿来对付这台设备。
func TestProofBoundToRequestingDevice(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, _, rsSigner, _, rsCert := buildPeers(t, now)

	nonce, _ := NewNonce()
	proofForA, _ := rsSigner.ProveTo(nonce, "device-A")

	if _, err := v.AuthenticatePeer([]string{rsCert}, RoleResource,
		nonce, proofForA, "device-B", now); !errors.Is(err, ErrBadProof) {
		t.Fatalf("给别的设备的证明必须失配,得到 %v", err)
	}
}

// 边缘节点也要能被客户端单独验证,且它**只**有 resource 能力——
// 客户端据此拒绝把凭证类请求发过去,这正是 §2 要防的事。
func TestClientRefusesCredentialsToEdgeNode(t *testing.T) {
	now := time.Unix(1700000000, 0)
	v, _, rsSigner, _, rsCert := buildPeers(t, now)

	edgeCert, err := rsSigner.Issue(IssueParams{
		Subject: "edge-1", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource}, TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	chain := []string{edgeCert, rsCert}
	if _, err := v.VerifyChainForCap(chain, directory.CapResource, now); err != nil {
		t.Fatalf("边缘节点应当可以收资源请求: %v", err)
	}
	for _, cap := range []string{directory.CapLogin, directory.CapAccount, directory.CapSave} {
		if _, err := v.VerifyChainForCap(chain, cap, now); err == nil {
			t.Fatalf("客户端不应把 %q 类请求发给边缘节点", cap)
		}
	}
}

// ── 根轮换 ──

// 重叠期轮换:新旧两把根**同名**同时钉着,两边签出来的链都要能验过。
//
// 同名是轮换时最自然的做法(主体一直叫 offline-root),而第一版实现按主体建索引,
// 新根加进来会把老根**静默挤掉**,老根签的所有子 CA 当场全废,且报的是
// "签名错误"这种完全指错方向的错。这条测试就是钉住这件事。
func TestRotationOverlapBothRootsAccepted(t *testing.T) {
	now := time.Unix(1700000000, 0)

	oldRoot, oldRootCert, err := NewRoot("offline-root", rootSeed, allCaps, 3, now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	// 新根:**同名**,不同密钥
	newRoot, newRootCert, err := NewRoot("offline-root", otherSeed, allCaps, 3, now, 365*24*time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}

	rsOld, err := oldRoot.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("老根签子 CA: %v", err)
	}
	rsNew, err := newRoot.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("新根签子 CA: %v", err)
	}

	// 重叠期:两把根同时钉着
	v, err := NewVerifier([]string{oldRootCert, newRootCert})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if got := len(v.Anchors()); got != 2 {
		t.Fatalf("同名的两把根都应当保留,实际只有 %d 把", got)
	}
	if _, err := v.VerifyChain([]string{rsOld}, now); err != nil {
		t.Fatalf("老根签的链在重叠期应当仍然有效: %v", err)
	}
	if _, err := v.VerifyChain([]string{rsNew}, now); err != nil {
		t.Fatalf("新根签的链应当有效: %v", err)
	}

	// 轮换完成:移除老根,老链随即失效、新链照常
	vAfter, err := NewVerifier([]string{newRootCert})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := vAfter.VerifyChain([]string{rsOld}, now); err == nil {
		t.Fatal("移除老根后,老根签的链必须失效")
	}
	if _, err := vAfter.VerifyChain([]string{rsNew}, now); err != nil {
		t.Fatalf("轮换后新链应当有效: %v", err)
	}
}

// 同一把根重复配置应当幂等,不该把候选表撑成两条走两遍验签。
func TestDuplicateAnchorIsIdempotent(t *testing.T) {
	now := time.Unix(1700000000, 0)
	_, rootCert, err := NewRoot("offline-root", rootSeed, allCaps, 2, now, time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	v, err := NewVerifier([]string{rootCert, rootCert, rootCert})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if got := len(v.Anchors()); got != 1 {
		t.Fatalf("重复配置同一把根应当去重,得到 %d", got)
	}
}

// 多把根之间不能互相"借"信任:老根签的子 CA 不能因为新根也在信任集里就通过。
// 逐个候选试的写法容易写成"任一候选的任一检查通过即放行",这条盯住它。
func TestAnchorsDoNotCrossValidate(t *testing.T) {
	now := time.Unix(1700000000, 0)
	oldRoot, _, err := NewRoot("offline-root", rootSeed, allCaps, 3, now, time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	_, newRootCert, err := NewRoot("offline-root", otherSeed, allCaps, 3, now, time.Hour)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rsOld, err := oldRoot.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// 只钉新根:老根签的链必须被拒(而不是因为同名就放行)
	v, err := NewVerifier([]string{newRootCert})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := v.VerifyChain([]string{rsOld}, now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("同名但非该根签发的链必须被拒,得到 %v", err)
	}
}

// ── 有效期策略 ──

// 三层有效期的量级关系必须成立,否则整个策略的前提就塌了。
func TestLifetimeDefaults(t *testing.T) {
	if DefaultEdgeTTL >= 24*time.Hour {
		t.Fatalf("边缘叶子应当是小时级,得到 %s", DefaultEdgeTTL)
	}
	if DefaultSubCATTL <= DefaultEdgeTTL || DefaultRootTTL <= DefaultSubCATTL {
		t.Fatal("有效期必须是 根 > 子CA > 边缘")
	}
	// 小时级叶子必须仍然远大于时钟容差,否则容差会吃掉可观比例的生命周期
	if err := ValidateTTL(DefaultEdgeTTL, time.Minute); err != nil {
		t.Fatalf("默认边缘有效期不该被自己的下限规则拒绝: %v", err)
	}
}

// 有效期短到与时钟容差同量级时必须拒绝签发。
func TestTTLFloorRelativeToLeeway(t *testing.T) {
	if err := ValidateTTL(30*time.Second, time.Minute); err == nil {
		t.Fatal("有效期短于时钟容差必须被拒")
	}
	if err := ValidateTTL(19*time.Minute, time.Minute); err == nil {
		t.Fatal("低于 20 倍容差应当被拒")
	}
	if err := ValidateTTL(21*time.Minute, time.Minute); err != nil {
		t.Fatalf("高于下限不该被拒: %v", err)
	}
	// 签发侧也要挡住
	now := time.Unix(1700000000, 0)
	root, rootCert, err := NewRoot("offline-root", rootSeed, allCaps, 3, now, DefaultRootTTL)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rootEncoded[root] = rootCert
	if _, err := root.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2,
		TTL: 10 * time.Second,
	}, now); err == nil {
		t.Fatal("签发侧应当拒绝过短的有效期")
	}
}

// 续期时点定在生命周期过半:续期要走网络,一次失败后还有半个生命周期可以重试。
func TestRenewAtIsHalfLife(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rootCert, err := NewRoot("offline-root", rootSeed, allCaps, 3, now, DefaultRootTTL)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rootEncoded[root] = rootCert
	rsCert, err := root.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2,
		TTL: DefaultSubCATTL,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	rsParsed, _, _, _ := decode(rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)

	edge, err := rsSigner.Issue(IssueParams{
		Subject: "edge-1", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource}, TTL: DefaultEdgeTTL,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	c, _, _, _ := decode(edge)

	if got := c.Lifetime(); got != DefaultEdgeTTL {
		t.Fatalf("生命周期应为 %s,得到 %s", DefaultEdgeTTL, got)
	}
	if c.NeedsRenewal(now) {
		t.Fatal("刚签发不该需要续期")
	}
	if c.NeedsRenewal(now.Add(DefaultEdgeTTL/2 - time.Minute)) {
		t.Fatal("未过半不该需要续期")
	}
	if !c.NeedsRenewal(now.Add(DefaultEdgeTTL/2 + time.Minute)) {
		t.Fatal("过半之后应当开始续期")
	}
	// 过半时点距过期仍有半个生命周期的重试余量
	remaining := time.UnixMilli(c.Exp).Sub(c.RenewAt())
	if remaining < DefaultEdgeTTL/2-time.Second {
		t.Fatalf("续期后应当还剩约半个生命周期可重试,实际 %s", remaining)
	}
}

// 边缘节点下线后,不需要吊销列表——有效期自己会把它清掉。
// 这条把"小时级叶子替代吊销"这个决策钉成可执行的断言。
func TestEdgeCertSelfExpiresWithoutRevocationList(t *testing.T) {
	now := time.Unix(1700000000, 0)
	root, rootCert, err := NewRoot("offline-root", rootSeed, allCaps, 3, now, DefaultRootTTL)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	rootEncoded[root] = rootCert
	rsCert, _ := root.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: DefaultSubCATTL,
	}, now)
	rsParsed, _, _, _ := decode(rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)
	edge, _ := rsSigner.Issue(IssueParams{
		Subject: "edge-下线的机器", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource}, TTL: DefaultEdgeTTL,
	}, now)

	v, err := NewVerifier([]string{rootCert})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	// 撤销回调**故意留空**:证明不依赖吊销列表也能失效
	if v.Revoked != nil {
		t.Fatal("本用例不应配置撤销回调")
	}
	if _, err := v.VerifyChain([]string{edge, rsCert}, now); err != nil {
		t.Fatalf("下线前应当有效: %v", err)
	}
	after := now.Add(DefaultEdgeTTL + 2*time.Minute) // 越过有效期与时钟容差
	if _, err := v.VerifyChain([]string{edge, rsCert}, after); !errors.Is(err, ErrExpired) {
		t.Fatalf("有效期一到必须自行失效,得到 %v", err)
	}
}
