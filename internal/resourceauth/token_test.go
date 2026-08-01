package resourceauth

import (
	"strings"
	"testing"
	"time"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestSignVerifyRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := Signer{Secret: secret, WindowSec: 300}
	tok, exp := s.Sign("dev-1", now)
	if !strings.HasPrefix(tok, Prefix) {
		t.Fatalf("令牌应带版本前缀: %q", tok)
	}
	if exp <= now.Unix() {
		t.Fatalf("过期时刻应当在将来: exp=%d now=%d", exp, now.Unix())
	}

	dev, err := Verifier{Secret: secret, WindowSec: 300}.Verify(tok, now)
	if err != nil {
		t.Fatalf("刚签出的令牌应当能验过: %v", err)
	}
	// 自描述:device_id 从令牌本身取回,不需要客户端另送一个头。
	if dev != "dev-1" {
		t.Fatalf("device_id = %q, want dev-1", dev)
	}
}

// 密钥不同必须验不过——这条是整个方案的地基,单独钉住。
func TestWrongSecretRejected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, _ := Signer{Secret: secret, WindowSec: 300}.Sign("dev-1", now)

	other := []byte("ffffffffffffffffffffffffffffffff")
	if _, err := (Verifier{Secret: other, WindowSec: 300}).Verify(tok, now); err != ErrBadMAC {
		t.Fatalf("换一把密钥必须报签名失败,得到 %v", err)
	}
}

// 改载荷(冒充别的设备)必须被拒。
func TestTamperedDeviceRejected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := Signer{Secret: secret, WindowSec: 300}
	tok, _ := s.Sign("dev-1", now)
	other, _ := s.Sign("dev-2", now)

	// 拿 dev-2 的设备段拼上 dev-1 的 MAC
	a, b := strings.Split(tok, "."), strings.Split(other, ".")
	forged := strings.Join([]string{a[0], b[1], a[2], a[3]}, ".")
	if _, err := (Verifier{Secret: secret, WindowSec: 300}).Verify(forged, now); err != ErrBadMAC {
		t.Fatalf("换掉设备段必须被拒,得到 %v", err)
	}
}

// 时间桶是明文字段,把它往后改不能延长寿命——MAC 覆盖了它。
func TestTamperedBucketRejected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, _ := Signer{Secret: secret, WindowSec: 300}.Sign("dev-1", now)
	p := strings.Split(tok, ".")
	p[2] = "999999999"
	if _, err := (Verifier{Secret: secret, WindowSec: 300}).Verify(strings.Join(p, "."), now); err != ErrBadMAC {
		t.Fatalf("改时间桶必须被拒(而且应当是签名失败,不是过期),得到 %v", err)
	}
}

// 上一个桶仍然接受:签发方与校验方之间的时钟差不该表现为随机 401。
func TestPreviousBucketAccepted(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, _ := Signer{Secret: secret, WindowSec: 300}.Sign("dev-1", now)

	later := now.Add(300 * time.Second)
	if _, err := (Verifier{Secret: secret, WindowSec: 300}).Verify(tok, later); err != nil {
		t.Fatalf("上一个桶应当仍然接受: %v", err)
	}
	// 再往后一格就该过期了,否则"短时令牌"名不副实。
	tooLate := now.Add(2 * 300 * time.Second)
	if _, err := (Verifier{Secret: secret, WindowSec: 300}).Verify(tok, tooLate); err != ErrExpired {
		t.Fatalf("超过两格必须过期,得到 %v", err)
	}
}

// 密钥过短一律拒绝:短密钥的 HMAC 可离线爆破,而症状是"一切正常"。
func TestShortSecretRefuses(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	short := []byte("tooshort")
	if tok, exp := (Signer{Secret: short}).Sign("dev-1", now); tok != "" || exp != 0 {
		t.Fatalf("密钥过短时不得签出令牌,得到 %q/%d", tok, exp)
	}
	good, _ := Signer{Secret: secret}.Sign("dev-1", now)
	if _, err := (Verifier{Secret: short}).Verify(good, now); err != ErrNoSecret {
		t.Fatalf("密钥过短时校验必须明确报错,得到 %v", err)
	}
}

// 结构畸形的串一律走 ErrMalformed,不得 panic、不得当成有效。
func TestMalformedRejected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	v := Verifier{Secret: secret, WindowSec: 300}
	for _, bad := range []string{
		"", "garbage", "cnva1.", "cnva1.a.b",
		"cnva1.a.b.c.d", "cnva2." + strings.Repeat("a", 10) + ".1.xx",
		"cnva1.!!!.1.xx", "cnva1.YWJj.notanumber.xx", "cnva1.YWJj.1.!!!",
	} {
		if _, err := v.Verify(bad, now); err == nil {
			t.Fatalf("畸形令牌必须被拒: %q", bad)
		}
	}
}

// 跨仓库测试向量。
//
// 这段算法在 magirecocn-resource-server 里有**一份完全相同的拷贝**:令牌由本服务端签发,
// 由资源分发服务端的边缘节点校验,两边必须字节级一致。两个仓库不共享 Go
// module,做不到只留一份实现。
//
// 于是用一枚钉死的向量代替:两边跑同一组输入,必须算出同一个串。哪边改了算法、
// 改了拼接顺序、换了编码,这个测试当场就红——而不是等上线之后表现为"所有资产
// 请求都 401",那种故障从错误信息里完全看不出根因。
//
// **改这个向量之前先想清楚**:它红了通常说明你在单方面改一个双方约定的线格式。
func TestCrossRepoVector(t *testing.T) {
	const (
		vecSecret = "cnv-cross-repo-test-vector-key!!"
		vecDevice = "dev-vector"
		vecAt     = 1_800_000_000
		vecToken  = "cnva1.ZGV2LXZlY3Rvcg.6000000.uO1_8OVns2J6JnfyyKbPqYs3O1RjFA79ZzRamddH6hw"
		vecExpiry = 1_800_000_300
	)
	tok, exp := Signer{Secret: []byte(vecSecret), WindowSec: 300}.Sign(vecDevice, time.Unix(vecAt, 0))
	if tok != vecToken {
		t.Errorf("跨仓库向量不匹配\n得到 %s\n期望 %s", tok, vecToken)
	}
	if exp != vecExpiry {
		t.Errorf("过期时刻 = %d, want %d", exp, vecExpiry)
	}
	dev, err := Verifier{Secret: []byte(vecSecret), WindowSec: 300}.Verify(vecToken, time.Unix(vecAt, 0))
	if err != nil || dev != vecDevice {
		t.Errorf("校验固定向量失败: dev=%q err=%v", dev, err)
	}
}
