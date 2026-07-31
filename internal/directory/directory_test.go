package directory_test

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"

	"magirecocn-revival/api-server/internal/directory"
)

func sampleDir() *directory.Directory {
	return &directory.Directory{
		Seq: 5, IssuedAt: 1000, ExpiresAt: 2000,
		Nodes: []directory.Node{
			{ID: "biz", Role: "business", API: "https://api.x",
				Caps: []string{directory.CapInit, directory.CapLogin, directory.CapAccount, directory.CapSave}, Weight: 100},
			{ID: "edge", Role: "edge", API: "https://hk.x",
				Caps: []string{directory.CapResource}, Weight: 50, Region: "hk"},
		},
	}
}

// Sign → Verify 往返；并模拟客户端：把 SignedDirectory JSON 化后再反序列化验签。
func TestDirectory_SignVerifyRoundTrip(t *testing.T) {
	pubHex, privHex, err := directory.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := directory.ParsePublicKey(pubHex)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := directory.ParsePrivateKey(privHex)
	if err != nil {
		t.Fatal(err)
	}

	d := sampleDir()
	sd, err := directory.Sign(d, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sd.Payload == "" || sd.Sig == "" {
		t.Fatal("payload 或 sig 为空")
	}
	got, err := directory.Verify(sd, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Seq != d.Seq || len(got.Nodes) != len(d.Nodes) {
		t.Fatalf("解码后字段不匹配: %+v", got)
	}

	// 模拟客户端：拿到字节 → 反序列化 SignedDirectory → 验签
	raw, _ := json.Marshal(sd)
	var sd2 directory.SignedDirectory
	if err := json.Unmarshal(raw, &sd2); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Verify(sd2, pub); err != nil {
		t.Fatalf("JSON 往返后校验失败: %v", err)
	}
}

// 篡改 payload 或用错误公钥都应导致校验失败。
func TestDirectory_TamperDetected(t *testing.T) {
	pubHex, privHex, _ := directory.GenerateKey()
	priv, _ := directory.ParsePrivateKey(privHex)
	pub, _ := directory.ParsePublicKey(pubHex)
	otherPubHex, _, _ := directory.GenerateKey()
	otherPub, _ := directory.ParsePublicKey(otherPubHex)

	d := sampleDir()
	sd, _ := directory.Sign(d, priv)

	// 1) 用错误公钥校验：必拒
	if _, err := directory.Verify(sd, otherPub); err == nil {
		t.Fatal("错误公钥不应通过")
	}

	// 2) 篡改 payload 字节后用正确公钥校验：必拒
	tampered := sd
	b := []byte(tampered.Payload)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	tampered.Payload = string(b)
	if _, err := directory.Verify(tampered, pub); err == nil {
		t.Fatal("篡改 payload 未被检出")
	}
}

func TestDirectory_CheckFresh(t *testing.T) {
	d := &directory.Directory{Seq: 10, ExpiresAt: 2000}
	if err := d.CheckFresh(5, 1500); err != nil {
		t.Fatalf("应当新鲜: %v", err)
	}
	if err := d.CheckFresh(11, 1500); err == nil {
		t.Fatal("seq 回滚应被拒")
	}
	if err := d.CheckFresh(5, 2500); err == nil {
		t.Fatal("过期应被拒")
	}
}

func TestDirectory_NodesWithCap(t *testing.T) {
	d := sampleDir()
	d.Nodes = append(d.Nodes, directory.Node{ID: "biz2", Role: "business",
		Caps: []string{directory.CapInit, directory.CapLogin}, Weight: 200})

	login := d.NodesWithCap(directory.CapLogin)
	if len(login) != 2 {
		t.Fatalf("期望 2 个 login 节点, 得到 %d", len(login))
	}
	if login[0].ID != "biz2" { // weight 200 > 100，应排在前
		t.Fatalf("应按 weight 降序, 第一个得到 %q", login[0].ID)
	}
	res := d.NodesWithCap(directory.CapResource)
	if len(res) != 1 || res[0].ID != "edge" {
		t.Fatalf("resource 能力节点应只有 edge, 得到 %+v", res)
	}
}

// TestDirectory_SignatureScalarIsCanonical 锁定「签出来的 S 必须小于群阶 L」。
//
// 客户端 Ed25519Verify 现在按 RFC 8032 §5.1.7 拒绝 S >= L 的签名（补这道检查是
// 为了消除签名可延展性——否则任何人无需私钥就能由一个合法签名构造出 S' = S + L
// 的另一个合法签名，而目录的 (payload, sig) 会被客户端落盘、下次启动重新验签）。
//
// Go 的 crypto/ed25519 本来就只产出规范 S，所以这不是修 bug，而是把这条跨端契约
// 钉成回归测试：万一以后换签名实现（自研/HSM/第三方库）产出非规范 S，会在这里
// 当场炸掉，而不是等到已发布的客户端集体拒绝目录、退回 API_HOST 才发现。
func TestDirectory_SignatureScalarIsCanonical(t *testing.T) {
	_, privHex, err := directory.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	priv, err := directory.ParsePrivateKey(privHex)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}

	// L = 2^252 + 27742317777372353535851937790883648493
	l := new(big.Int).Lsh(big.NewInt(1), 252)
	l.Add(l, mustBig(t, "27742317777372353535851937790883648493"))

	// 多签几次：S 依赖消息与私钥，单次通过不足以说明问题。
	for i := 0; i < 64; i++ {
		d := sampleDir()
		d.Seq = int64(i)
		sd, err := directory.Sign(d, priv)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		raw, err := base64.StdEncoding.DecodeString(sd.Sig)
		if err != nil {
			t.Fatalf("签名不是合法 standard base64: %v", err)
		}
		if len(raw) != 64 {
			t.Fatalf("签名长度 = %d, 期望 64", len(raw))
		}
		// S 是后 32 字节、小端序；转成大端喂给 big.Int
		le := raw[32:]
		be := make([]byte, 32)
		for j := 0; j < 32; j++ {
			be[j] = le[31-j]
		}
		s := new(big.Int).SetBytes(be)
		if s.Cmp(l) >= 0 {
			t.Fatalf("seq=%d: 签名标量 S >= L，非规范签名会被客户端拒绝\n S = %s\n L = %s",
				i, s, l)
		}
	}
}

func mustBig(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("SetString(%q) 失败", s)
	}
	return v
}

// TestDirectory_ValidateCapIsolation 覆盖 §2 能力隔离:边缘节点绝不能持有凭证类能力。
//
// 这条不变量客户端**兜不住**——它只验签名,不质疑能力分配是否合理。一份把
// save 发给边缘节点的目录只要签名有效,客户端就会把云存档凭证发过去。所以
// 只能在签发这一侧强制,且必须让 Sign 直接失败而不是仅仅告警。
func TestDirectory_ValidateCapIsolation(t *testing.T) {
	_, privHex, _ := directory.GenerateKey()
	priv, _ := directory.ParsePrivateKey(privHex)

	edgeWith := func(caps ...string) *directory.Directory {
		return &directory.Directory{
			Seq: 1, IssuedAt: 1000, ExpiresAt: 2000,
			Nodes: []directory.Node{
				{ID: "biz", Role: directory.RoleBusiness, API: "https://api.x",
					Caps: []string{directory.CapInit, directory.CapLogin,
						directory.CapAccount, directory.CapSave}},
				{ID: "edge", Role: directory.RoleEdge, API: "https://cdn.x", Caps: caps},
			},
		}
	}

	// 边缘节点持有任一凭证类能力都必须被拒
	for _, bad := range []string{directory.CapLogin, directory.CapAccount, directory.CapSave} {
		d := edgeWith(directory.CapResource, bad)
		if err := d.Validate(); err == nil {
			t.Errorf("边缘节点带 %q 应被拒绝,却通过了", bad)
		}
		if _, err := directory.Sign(d, priv); err == nil {
			t.Errorf("Sign 应拒绝签发带 %q 的边缘节点目录", bad)
		}
	}

	// 合法配置必须放行
	if err := edgeWith(directory.CapResource).Validate(); err != nil {
		t.Errorf("合法目录被误拒: %v", err)
	}
	if _, err := directory.Sign(edgeWith(directory.CapResource), priv); err != nil {
		t.Errorf("合法目录签发失败: %v", err)
	}
	// business 节点持有凭证类能力是正常的,不能误拦
	if err := sampleDir().Validate(); err != nil {
		t.Errorf("business 节点持凭证能力被误拒: %v", err)
	}
}

// TestDirectory_ValidateStructural 覆盖结构性校验:缺字段 / 重复 id / 未知能力 / 无效过期时间。
func TestDirectory_ValidateStructural(t *testing.T) {
	base := func() *directory.Directory {
		return &directory.Directory{
			Seq: 1, IssuedAt: 1000, ExpiresAt: 2000,
			Nodes: []directory.Node{
				{ID: "biz", Role: directory.RoleBusiness, API: "https://api.x",
					Caps: []string{directory.CapInit}},
			},
		}
	}
	cases := []struct {
		name   string
		mutate func(*directory.Directory)
	}{
		{"缺 id", func(d *directory.Directory) { d.Nodes[0].ID = "  " }},
		{"缺 api", func(d *directory.Directory) { d.Nodes[0].API = "" }},
		{"caps 为空", func(d *directory.Directory) { d.Nodes[0].Caps = nil }},
		{"未知能力", func(d *directory.Directory) { d.Nodes[0].Caps = []string{"root"} }},
		{"expires_at 为 0", func(d *directory.Directory) { d.ExpiresAt = 0 }},
		{"没有任何节点", func(d *directory.Directory) { d.Nodes = nil }},
		{"id 重复", func(d *directory.Directory) {
			d.Nodes = append(d.Nodes, d.Nodes[0])
		}},
	}
	for _, tc := range cases {
		d := base()
		tc.mutate(d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: 应被 Validate 拒绝,却通过了", tc.name)
		}
	}
	if err := base().Validate(); err != nil {
		t.Errorf("基准用例不该失败: %v", err)
	}
}

// TestDirectory_DecodeUnverified 明确 DecodeUnverified 的语义:只解析、不验签。
//
// 它存在的意义是给「手里没有根公钥」的业务节点做启动自检——公钥钉在客户端 APK
// 里,节点验不了签,但仍应能看出目录结构是否合理(能力分配、过期时间),否则
// 一份坏目录会被照单下发,客户端静默丢弃回退 API_HOST,而服务端日志一片安静。
func TestDirectory_DecodeUnverified(t *testing.T) {
	_, privHex, _ := directory.GenerateKey()
	priv, _ := directory.ParsePrivateKey(privHex)
	sd, err := directory.Sign(sampleDir(), priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	got, err := directory.DecodeUnverified(sd)
	if err != nil {
		t.Fatalf("DecodeUnverified: %v", err)
	}
	if got.Seq != 5 || len(got.Nodes) != 2 {
		t.Errorf("解析结果不对: seq=%d nodes=%d", got.Seq, len(got.Nodes))
	}

	// 关键语义:签名被篡改照样能解析成功——它本就不验签。
	// 这条断言是为了防止有人误把 DecodeUnverified 当成 Verify 用。
	tampered := sd
	tampered.Sig = base64.StdEncoding.EncodeToString(make([]byte, 64))
	if _, err := directory.DecodeUnverified(tampered); err != nil {
		t.Errorf("DecodeUnverified 不应校验签名,却因签名无效而失败: %v", err)
	}
	// 同一份数据交给 Verify 必须失败,二者语义不可混淆。
	pub, _, _ := directory.GenerateKey()
	pk, _ := directory.ParsePublicKey(pub)
	if _, err := directory.Verify(tampered, pk); err == nil {
		t.Error("Verify 必须拒绝被篡改的签名")
	}

	// 垃圾输入要报错而不是返回零值目录
	for _, bad := range []directory.SignedDirectory{
		{Payload: "", Sig: "x"},
		{Payload: "x", Sig: ""},
		{Payload: "!!!not-base64url!!!", Sig: "x"},
		{Payload: base64.RawURLEncoding.EncodeToString([]byte("not json")), Sig: "x"},
	} {
		if _, err := directory.DecodeUnverified(bad); err == nil {
			t.Errorf("垃圾输入 %+v 应报错", bad)
		}
	}
}
