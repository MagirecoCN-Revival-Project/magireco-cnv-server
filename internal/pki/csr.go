package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// 证书签名请求(CSR)。
//
// # 它解决什么
//
// 「离线手工签」要求节点的私钥**从不离开节点**——否则把私钥拿去离线机器上签,
// 私钥就在两台机器上各存了一份,离线根的意义少了一半。所以流程是反过来的:
// 节点自己生成密钥对,只把**公钥**连同申请信息打包成 CSR 交出去,运维把 CSR 拿到
// 离线根机器上签,再把签好的证书拷回节点。
//
// # CSR 自签名
//
// CSR 由申请者用自己的私钥签名。这不是给 CA 建立信任用的——CA 的信任来自运维
// 的人工确认,不来自这个签名。它证明的是"提交这份 CSR 的人确实持有这个公钥对应的
// 私钥",挡住"拿别人的公钥去申请一张证书"这种替换:那样签出来的证书主体是攻击者
// 指定的,而私钥在受害者手里,能造成什么后果取决于上层怎么用,不如直接堵死。
//
// # 申请的字段只是"申请"
//
// role 与 caps 在 CSR 里是**请求**,不是决定。签发侧必须由人明确指定,不能直接
// 采信 CSR——否则一台被攻陷的边缘节点只要在 CSR 里写 role=root 就能申请到根权限。
// admintool 因此强制要求显式传 -role/-caps,并把 CSR 里申请的值一并打出来供比对。

// CSRPrefix 是 CSR 的版本前缀。
const CSRPrefix = "cnvr1"

var (
	ErrBadCSR           = errors.New("pki: CSR 格式非法")
	ErrCSRSelfSignature = errors.New("pki: CSR 自签名校验失败")
)

// CSRPayload 是 CSR 的明文内容。
type CSRPayload struct {
	V    int      `json:"v"`
	Sub  string   `json:"sub"`            // 申请的主体标识
	Pub  string   `json:"pub"`            // 申请者的公钥,64 位小写十六进制
	Role string   `json:"role"`           // **申请**的角色,签发侧不得直接采信
	Caps []string `json:"caps"`           // **申请**的能力,签发侧不得直接采信
	At   int64    `json:"at"`             // 生成时刻,Unix 毫秒,仅供人工核对
	Note string   `json:"note,omitempty"` // 备注,例如机房/用途
}

// NewCSR 生成一份 CSR,用申请者自己的私钥自签。
func NewCSR(subject, role string, caps []string, seedHex string, now time.Time, note string) (string, error) {
	if subject == "" {
		return "", errors.New("pki: CSR 主体不能为空")
	}
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", errors.New("pki: 私钥种子必须是 32 字节十六进制")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	p := CSRPayload{
		V: 1, Sub: subject,
		Pub:  hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
		Role: role, Caps: append([]string(nil), caps...),
		At: now.UnixMilli(), Note: note,
	}
	inner, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	signing := CSRPrefix + "." + base64.RawURLEncoding.EncodeToString(inner)
	sig := ed25519.Sign(priv, []byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ParseCSR 解析并校验 CSR 的自签名。
func ParseCSR(encoded string) (*CSRPayload, error) {
	encoded = strings.TrimSpace(encoded)
	if len(encoded) > maxCertLen || !strings.HasPrefix(encoded, CSRPrefix+".") {
		return nil, ErrBadCSR
	}
	parts := strings.Split(encoded, ".")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return nil, ErrBadCSR
	}
	inner, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrBadCSR
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrBadCSR
	}
	var p CSRPayload
	if err := json.Unmarshal(inner, &p); err != nil {
		return nil, ErrBadCSR
	}
	if p.V != 1 || p.Sub == "" {
		return nil, ErrBadCSR
	}
	pubBytes, err := hex.DecodeString(strings.TrimSpace(p.Pub))
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return nil, ErrBadCSR
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), []byte(parts[0]+"."+parts[1]), sig) {
		return nil, ErrCSRSelfSignature
	}
	return &p, nil
}

// NewSeed 生成一把 32 字节私钥种子的十六进制表示,以及对应公钥。
// 节点首次启动用它建立自己的身份密钥;种子落盘后**绝不外传**。
func NewSeed() (seedHex, pubHex string, err error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err = rand.Read(seed); err != nil {
		return "", "", err
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return hex.EncodeToString(seed),
		hex.EncodeToString(priv.Public().(ed25519.PublicKey)), nil
}

// ParseCert 解析一张证书并**只**校验它的自身结构,不做链校验、不验签。
//
// 用途限于展示与工具链(admintool ca show / 加载本节点自己的证书)。任何信任
// 判断都必须走 Verifier.VerifyChain——所以这个函数刻意不叫 VerifyCert,
// 免得调用方以为解析成功就等于可信。
func ParseCert(encoded string) (*Cert, error) {
	c, _, _, err := decode(strings.TrimSpace(encoded))
	return c, err
}
