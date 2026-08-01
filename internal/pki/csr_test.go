package pki

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/directory"
)

func TestCSRRoundTrip(t *testing.T) {
	now := time.Unix(1700000000, 0)
	csr, err := NewCSR("edge-tokyo-1", RoleEdge,
		[]string{directory.CapResource}, edgeSeed, now, "东京机房")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	p, err := ParseCSR(csr)
	if err != nil {
		t.Fatalf("ParseCSR: %v", err)
	}
	if p.Sub != "edge-tokyo-1" || p.Role != RoleEdge || p.Note != "东京机房" {
		t.Fatalf("字段丢失: %+v", p)
	}
	if p.Pub != pubOf(t, edgeSeed) {
		t.Fatalf("公钥不对: %s", p.Pub)
	}
}

// CSR 自签名证明"提交者持有该公钥对应的私钥",挡住拿别人公钥去申请证书的替换。
func TestCSRSelfSignatureRequired(t *testing.T) {
	now := time.Unix(1700000000, 0)
	csr, _ := NewCSR("edge-1", RoleEdge, []string{directory.CapResource}, edgeSeed, now, "")
	parts := strings.Split(csr, ".")

	// 把公钥换成别人的,签名随即对不上
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var p CSRPayload
	_ = json.Unmarshal(raw, &p)
	p.Pub = pubOf(t, evilSeed)
	swapped, _ := json.Marshal(p)
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(swapped) + "." + parts[2]

	if _, err := ParseCSR(forged); !errors.Is(err, ErrCSRSelfSignature) {
		t.Fatalf("换公钥后自签名必须失配,得到 %v", err)
	}
}

func TestCSRMalformed(t *testing.T) {
	now := time.Unix(1700000000, 0)
	good, _ := NewCSR("edge-1", RoleEdge, []string{directory.CapResource}, edgeSeed, now, "")
	parts := strings.Split(good, ".")
	for name, bad := range map[string]string{
		"空串":         "",
		"错前缀":        "cnvc1." + parts[1] + "." + parts[2],
		"缺段":         parts[0] + "." + parts[1],
		"载荷非 base64": "cnvr1.@@@.@@@",
		"载荷非 JSON":   "cnvr1." + base64.RawURLEncoding.EncodeToString([]byte("x")) + ".AAAA",
		"证书当 CSR":    "",
	} {
		if bad == "" && name != "空串" {
			continue
		}
		if _, err := ParseCSR(bad); err == nil {
			t.Fatalf("%s 应当被拒", name)
		}
	}
	// 一张真证书不能被当成 CSR 解析
	root, _, _ := NewRoot("r", rootSeed, allCaps, 2, now, DefaultRootTTL)
	rsCert, _ := root.Issue(IssueParams{
		Subject: "rs", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 1, TTL: DefaultSubCATTL,
	}, now)
	if _, err := ParseCSR(rsCert); err == nil {
		t.Fatal("证书不应能被当作 CSR 解析")
	}
}

// CSR 里的 role/caps 只是**申请**。这条测试把"签发侧不采信 CSR"钉成断言:
// 一台被攻陷的边缘节点在 CSR 里写 role=root,签发侧按运维指定的值签,
// 签出来的仍然只是边缘节点。
func TestCSRRequestedRoleIsNotAuthoritative(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// 攻击者申请 root 角色 + 全部能力
	csr, err := NewCSR("edge-恶意", RoleRoot, allCaps, edgeSeed, now, "")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	p, err := ParseCSR(csr)
	if err != nil {
		t.Fatalf("ParseCSR: %v", err)
	}
	if p.Role != RoleRoot {
		t.Fatalf("CSR 应当如实记录申请值,便于运维比对: %q", p.Role)
	}

	// 签发侧只用运维指定的 role/caps,不碰 CSR 里的
	root, rootCert, _ := NewRoot("offline-root", rootSeed, allCaps, 3, now, DefaultRootTTL)
	rsCert, _ := root.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapResource}, CA: true, PathLen: 2, TTL: DefaultSubCATTL,
	}, now)
	rsParsed, _, _, _ := decode(rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)

	issued, err := rsSigner.Issue(IssueParams{
		Subject: p.Sub, Role: RoleEdge, PublicKey: p.Pub, // ← 运维指定 edge
		Caps: []string{directory.CapResource}, TTL: DefaultEdgeTTL,
	}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, _, _, _ := decode(issued)
	if got.Role != RoleEdge {
		t.Fatalf("签出的角色应由运维决定,得到 %q", got.Role)
	}
	v, _ := NewVerifier([]string{rootCert})
	res, err := v.VerifyChain([]string{issued, rsCert}, now)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if len(res.Caps) != 1 || res.Caps[0] != directory.CapResource {
		t.Fatalf("能力应当只有 resource,得到 %v", res.Caps)
	}
}

// 私钥从不离开节点:CSR 里只有公钥,不含任何私钥材料。
func TestCSRCarriesNoPrivateMaterial(t *testing.T) {
	now := time.Unix(1700000000, 0)
	csr, _ := NewCSR("edge-1", RoleEdge, []string{directory.CapResource}, edgeSeed, now, "")
	parts := strings.Split(csr, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(edgeSeed)) {
		t.Fatal("CSR 里出现了私钥种子")
	}
	if strings.Contains(strings.ToLower(csr), strings.ToLower(edgeSeed)) {
		t.Fatal("CSR 编码串里出现了私钥种子")
	}
}

func TestNewSeedProducesUsableKeypair(t *testing.T) {
	seed, pub, err := NewSeed()
	if err != nil {
		t.Fatalf("NewSeed: %v", err)
	}
	if b, err := hex.DecodeString(seed); err != nil || len(b) != 32 {
		t.Fatalf("种子应为 32 字节十六进制: %q", seed)
	}
	csr, err := NewCSR("n1", RoleEdge, []string{directory.CapResource}, seed, time.Now(), "")
	if err != nil {
		t.Fatalf("NewCSR: %v", err)
	}
	p, err := ParseCSR(csr)
	if err != nil {
		t.Fatalf("ParseCSR: %v", err)
	}
	if p.Pub != pub {
		t.Fatalf("NewSeed 返回的公钥与 CSR 里的不一致")
	}
	// 两次生成必须不同
	seed2, _, _ := NewSeed()
	if seed == seed2 {
		t.Fatal("两次 NewSeed 生成了相同的种子")
	}
}
