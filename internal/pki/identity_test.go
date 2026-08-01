package pki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/directory"
)

// 搭一套落在磁盘上的真实身份文件,供加载自检用。
type fixture struct {
	dir                        string
	rootCert, rsCert, edgeCert string
	rootPath, rsCertPath, rsKeyPath,
	edgeCertPath, edgeKeyPath string
}

func newFixture(t *testing.T, now time.Time) *fixture {
	t.Helper()
	d := t.TempDir()
	f := &fixture{dir: d}

	root, rootCert, err := NewRoot("offline-root", rootSeed, allCaps, 3, now, DefaultRootTTL)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	f.rootCert = rootCert
	f.rsCert, err = root.Issue(IssueParams{
		Subject: "rs-1", Role: RoleResource, PublicKey: pubOf(t, rsSeed),
		Caps: []string{directory.CapInit, directory.CapResource},
		CA:   true, PathLen: 2, TTL: DefaultSubCATTL,
	}, now)
	if err != nil {
		t.Fatalf("签子 CA: %v", err)
	}
	rsParsed, _, _, _ := decode(f.rsCert)
	rsSigner, _ := NewSigner(rsParsed, rsSeed)
	f.edgeCert, err = rsSigner.Issue(IssueParams{
		Subject: "edge-1", Role: RoleEdge, PublicKey: pubOf(t, edgeSeed),
		Caps: []string{directory.CapResource}, TTL: DefaultEdgeTTL,
	}, now)
	if err != nil {
		t.Fatalf("签边缘: %v", err)
	}

	write := func(name, content string) string {
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	f.rootPath = write("root.cert", f.rootCert)
	f.rsCertPath = write("rs.cert", f.rsCert)
	f.rsKeyPath = write("rs.key", rsSeed)
	f.edgeCertPath = write("edge.cert", f.edgeCert)
	f.edgeKeyPath = write("edge.key", edgeSeed)
	return f
}

func TestLoadIdentityHappyPath(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)

	// 子 CA:由根直签,不需要中间证书
	id, err := Load(LoadOptions{
		CertFile: f.rsCertPath, KeyFile: f.rsKeyPath,
		AnchorFiles: []string{f.rootPath},
		WantRole:    RoleResource, Now: now,
	})
	if err != nil {
		t.Fatalf("加载子 CA 身份: %v", err)
	}
	if id.Leaf().Sub != "rs-1" || !id.Leaf().CA {
		t.Fatalf("身份不对: %+v", id.Leaf())
	}

	// 边缘节点:需要带上中间证书
	edge, err := Load(LoadOptions{
		CertFile: f.edgeCertPath, ChainFiles: []string{f.rsCertPath},
		KeyFile: f.edgeKeyPath, AnchorFiles: []string{f.rootPath},
		WantRole: RoleEdge, Now: now,
	})
	if err != nil {
		t.Fatalf("加载边缘身份: %v", err)
	}
	if len(edge.Chain) != 2 {
		t.Fatalf("边缘节点链长应为 2,得到 %d", len(edge.Chain))
	}
	if s := edge.Describe(); !strings.Contains(s, "edge-1") || strings.Contains(s, edgeSeed) {
		t.Fatalf("摘要有问题(或泄露了私钥): %s", s)
	}
}

// 私钥与证书不配对:必须在启动时就拒绝,而不是等到第一次签东西。
func TestLoadRejectsMismatchedKey(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)
	if _, err := Load(LoadOptions{
		CertFile: f.rsCertPath, KeyFile: f.edgeKeyPath, // 拿错私钥
		AnchorFiles: []string{f.rootPath}, Now: now,
	}); err == nil {
		t.Fatal("证书与私钥不配对必须拒绝启动")
	}
}

// 角色配反通常不会报错,只会安静地让边缘节点收下凭证类请求。必须查。
func TestLoadRejectsRoleMismatch(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)
	_, err := Load(LoadOptions{
		CertFile: f.edgeCertPath, ChainFiles: []string{f.rsCertPath},
		KeyFile: f.edgeKeyPath, AnchorFiles: []string{f.rootPath},
		WantRole: RoleResource, // 证书其实是 edge
		Now:      now,
	})
	if err == nil {
		t.Fatal("角色不符必须拒绝启动")
	}
	if !strings.Contains(err.Error(), "edge") || !strings.Contains(err.Error(), "resource") {
		t.Fatalf("错误信息应当同时点出实际与期望角色: %v", err)
	}
}

// 缺中间证书 → 链锚不到根。这是部署时最容易犯的错,错误必须指向"链"而不是别处。
func TestLoadRejectsBrokenChain(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)
	_, err := Load(LoadOptions{
		CertFile: f.edgeCertPath, // 漏了 ChainFiles
		KeyFile:  f.edgeKeyPath, AnchorFiles: []string{f.rootPath},
		Now: now,
	})
	if err == nil {
		t.Fatal("链断裂必须拒绝启动")
	}
	if !strings.Contains(err.Error(), "证书链") {
		t.Fatalf("错误信息应当指向证书链: %v", err)
	}
}

// 证书过期同样拒绝启动:带着过期证书跑起来,对端一律拒绝它,等于静默失联。
func TestLoadRejectsExpiredCert(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)
	_, err := Load(LoadOptions{
		CertFile: f.edgeCertPath, ChainFiles: []string{f.rsCertPath},
		KeyFile: f.edgeKeyPath, AnchorFiles: []string{f.rootPath},
		Now: now.Add(DefaultEdgeTTL + time.Hour),
	})
	if err == nil {
		t.Fatal("证书过期必须拒绝启动")
	}
}

// 配错信任根(拿别的根去锚)必须拒绝——这是最危险的配置错误。
func TestLoadRejectsWrongAnchor(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)
	_, otherRootCert, err := NewRoot("offline-root", otherSeed, allCaps, 3, now, DefaultRootTTL)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	wrong := filepath.Join(f.dir, "wrong-root.cert")
	if err := os.WriteFile(wrong, []byte(otherRootCert+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(LoadOptions{
		CertFile: f.rsCertPath, KeyFile: f.rsKeyPath,
		AnchorFiles: []string{wrong}, Now: now,
	}); err == nil {
		t.Fatal("同名但不同公钥的根必须被拒")
	}
}

// 轮换重叠期:同时钉新旧两把根,本节点的链只要能锚到其中一把就该起得来。
func TestLoadDuringRootRotation(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)
	_, newRootCert, err := NewRoot("offline-root", otherSeed, allCaps, 3, now, DefaultRootTTL)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	newPath := filepath.Join(f.dir, "root-next.cert")
	if err := os.WriteFile(newPath, []byte(newRootCert+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	id, err := Load(LoadOptions{
		CertFile: f.rsCertPath, KeyFile: f.rsKeyPath,
		AnchorFiles: []string{f.rootPath, newPath}, Now: now,
	})
	if err != nil {
		t.Fatalf("重叠期应当能启动: %v", err)
	}
	if got := len(id.Verifier.Anchors()); got != 2 {
		t.Fatalf("应当钉住两把根,得到 %d", got)
	}
}

func TestLoadRequiresAllThreeInputs(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)
	for name, o := range map[string]LoadOptions{
		"缺证书":  {KeyFile: f.rsKeyPath, AnchorFiles: []string{f.rootPath}},
		"缺私钥":  {CertFile: f.rsCertPath, AnchorFiles: []string{f.rootPath}},
		"缺信任根": {CertFile: f.rsCertPath, KeyFile: f.rsKeyPath},
	} {
		o.Now = now
		if _, err := Load(o); err == nil {
			t.Fatalf("%s 时应当报错", name)
		}
	}
	// 空文件也要报错,而不是当成空字符串一路带下去
	empty := filepath.Join(f.dir, "empty")
	_ = os.WriteFile(empty, nil, 0o600)
	if _, err := Load(LoadOptions{
		CertFile: empty, KeyFile: f.rsKeyPath,
		AnchorFiles: []string{f.rootPath}, Now: now,
	}); err == nil {
		t.Fatal("空证书文件应当报错")
	}
}

// 续期判定:刚签发不续,过半后要续。
func TestIdentityNeedsRenewal(t *testing.T) {
	now := time.Unix(1700000000, 0)
	f := newFixture(t, now)
	id, err := Load(LoadOptions{
		CertFile: f.edgeCertPath, ChainFiles: []string{f.rsCertPath},
		KeyFile: f.edgeKeyPath, AnchorFiles: []string{f.rootPath}, Now: now,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if id.NeedsRenewal(now) {
		t.Fatal("刚签发不该需要续期")
	}
	if !id.NeedsRenewal(now.Add(DefaultEdgeTTL/2 + time.Minute)) {
		t.Fatal("过半后应当需要续期")
	}
}
