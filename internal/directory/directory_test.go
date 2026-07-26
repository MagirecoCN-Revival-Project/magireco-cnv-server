package directory_test

import (
	"encoding/json"
	"testing"

	"magirecocn-revival/cnv-server/internal/directory"
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
