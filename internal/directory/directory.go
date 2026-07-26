// Package directory 实现「签名节点目录」——客户端多节点发现的信任根。
//
// 安全模型（详见架构文档「客户端多节点发现」）：
//   - 客户端硬编码一把 Ed25519 根公钥：钉「公钥」而非「地址」。地址是数据、
//     可动态下发；信任建立在离线私钥的签名上。攻击者返回任意字节都行，没有
//     私钥就签不出合法目录 → 客户端拒绝。
//   - 目录由离线私钥签名（私钥在 admintool / CI Secret，绝不上线）。节点与面板
//     只分发签好的字节，自身不持私钥，被击穿也签不出新目录。
//   - seq 单调防回滚；expires_at 强制刷新；caps 能力声明锁定凭证只发授权节点。
//
// 签名格式（JWS 风格）：
//
//	sig = Ed25519_sign( 私钥, UTF8( base64url(compact_JSON) ) )
//
// 签名覆盖 base64url 字符串本身，而非解码后的 JSON 字节，彻底消除跨语言序列化对齐问题。
package directory

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// 能力常量：声明某节点被离线密钥授权承担的客户端动作。
// 客户端据此决定「把什么请求发给哪个节点」——尤其凭证类只发给声明了
// 对应能力的业务节点。
const (
	CapInit     = "init"     // /client/init
	CapLogin    = "login"    // 账号登录（凭证）
	CapAccount  = "account"  // 账号相关（凭证）
	CapSave     = "save"     // 云存档（凭证）
	CapResource = "resource" // 资源分发
)

// Node 是目录中的一个节点条目。
type Node struct {
	ID      string   `json:"id"`
	Role    string   `json:"role"`             // business | edge
	API     string   `json:"api"`              // https:// 基址
	Caps    []string `json:"caps"`             // 被授权的能力
	Region  string   `json:"region,omitempty"`
	Weight  int      `json:"weight,omitempty"`    // 选择权重，越大越优先
	TLSSPKI string   `json:"tls_spki,omitempty"` // 可选：证书公钥指纹（每节点钉扎，当前客户端仅解析保留）
}

// Directory 是签名目录的内层 payload（明文部分）。
// 签名字段不在此处，而在外层 SignedDirectory.Sig。
type Directory struct {
	Seq       int64  `json:"seq"`        // 单调递增序号，防回滚
	IssuedAt  int64  `json:"issued_at"`  // 签发时间（unix 秒）
	ExpiresAt int64  `json:"expires_at"` // 过期时间（unix 秒），强制刷新
	Nodes     []Node `json:"nodes"`
}

// SignedDirectory 是下发给客户端的线上格式，对应 /client/init 响应中的 "directory" 字段：
//
//	{ "payload": "<base64url(UTF-8(紧凑JSON))，无填充>",
//	  "sig":     "<standard_base64(Ed25519签名)>" }
//
// 签名覆盖的是 Payload 字符串的 UTF-8 字节（而非解码后的 JSON），
// 客户端不需要重序列化即可验签，消除跨语言字段顺序 / 转义 / 数字格式对齐问题。
type SignedDirectory struct {
	Payload string `json:"payload"` // base64url(inner JSON)，无 = 填充
	Sig     string `json:"sig"`     // standard base64(Ed25519 签名)
}

// Sign 将 d 序列化为紧凑 JSON，base64url 编码（无填充），
// 再对该编码字符串的 UTF-8 字节用 priv 签名，返回线上格式 SignedDirectory。
func Sign(d *Directory, priv ed25519.PrivateKey) (SignedDirectory, error) {
	inner, err := json.Marshal(d)
	if err != nil {
		return SignedDirectory{}, err
	}
	payload := base64.RawURLEncoding.EncodeToString(inner) // base64url，无填充
	sig := ed25519.Sign(priv, []byte(payload))             // 签 payload 字符串字节
	return SignedDirectory{
		Payload: payload,
		Sig:     base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// Verify 用钉扎的根公钥校验 sd.Sig 覆盖 sd.Payload 字符串字节的签名，
// 验证通过后 base64url 解码 payload 并反序列化，返回 *Directory。
func Verify(sd SignedDirectory, pub ed25519.PublicKey) (*Directory, error) {
	if sd.Payload == "" || sd.Sig == "" {
		return nil, errors.New("目录缺少 payload 或 sig")
	}
	sig, err := base64.StdEncoding.DecodeString(sd.Sig)
	if err != nil {
		return nil, errors.New("签名格式错误")
	}
	if !ed25519.Verify(pub, []byte(sd.Payload), sig) {
		return nil, errors.New("签名校验失败")
	}
	inner, err := base64.RawURLEncoding.DecodeString(sd.Payload)
	if err != nil {
		return nil, errors.New("payload 解码失败")
	}
	var d Directory
	if err := json.Unmarshal(inner, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// CheckFresh 校验未过期且 seq 不低于本地已知的 lastSeq（防回滚）。now 为 unix 秒。
func (d *Directory) CheckFresh(lastSeq, now int64) error {
	if d.ExpiresAt > 0 && now > d.ExpiresAt {
		return errors.New("目录已过期")
	}
	if d.Seq < lastSeq {
		return errors.New("目录 seq 回滚")
	}
	return nil
}

// NodesWithCap 返回声明了某能力的节点，按 weight 降序（稳定）。
func (d *Directory) NodesWithCap(cap string) []Node {
	var out []Node
	for _, n := range d.Nodes {
		for _, c := range n.Caps {
			if c == cap {
				out = append(out, n)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Weight > out[j].Weight })
	return out
}

// GenerateKey 生成 Ed25519 根密钥对，返回十六进制的 (公钥, 私钥)。
// 公钥钉扎进客户端；私钥离线保管。
func GenerateKey() (pubHex, privHex string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(pub), hex.EncodeToString(priv), nil
}

// ParsePublicKey 解析十六进制根公钥。
func ParsePublicKey(h string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(h))
	if err != nil {
		return nil, errors.New("公钥不是合法十六进制")
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, errors.New("公钥长度错误")
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKey 解析十六进制根私钥。
func ParsePrivateKey(h string) (ed25519.PrivateKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(h))
	if err != nil {
		return nil, errors.New("私钥不是合法十六进制")
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("私钥长度错误")
	}
	return ed25519.PrivateKey(b), nil
}
