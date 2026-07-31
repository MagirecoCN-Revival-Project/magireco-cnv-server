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
	"fmt"
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

// 节点角色。
const (
	RoleBusiness = "business"
	RoleEdge     = "edge"
)

// isCredentialCap 判断该能力是否会让客户端把**凭证**发到这个节点。
func isCredentialCap(c string) bool {
	return c == CapLogin || c == CapAccount || c == CapSave
}

func isKnownCap(c string) bool {
	switch c {
	case CapInit, CapLogin, CapAccount, CapSave, CapResource:
		return true
	}
	return false
}

// Validate 校验目录是否满足纪律文件 §2 的能力分配约定。
//
// 为什么必须在**服务端签发时**卡住:客户端拿到目录后只验签名,不会、也无法
// 质疑能力分配是否合理——一份把 save 能力发给边缘节点的目录,只要签名有效,
// 客户端就会老老实实把云存档凭证发到那台边缘机上。也就是说,这条不变量
// 客户端侧根本兜不住,只能在签发这一侧强制。
//
// 后台误勾一个复选框就足以造成这种情况,而事故现象(凭证泄漏到边缘节点)
// 既不报错也不易察觉,所以宁可让签发当场失败。
func (d *Directory) Validate() error {
	if d == nil {
		return errors.New("目录为空")
	}
	if len(d.Nodes) == 0 {
		return errors.New("目录不含任何节点")
	}
	if d.ExpiresAt <= 0 {
		return errors.New("expires_at 必须为正的 unix 秒(为 0 会被客户端立即判为过期)")
	}
	seen := make(map[string]bool, len(d.Nodes))
	for i, n := range d.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return fmt.Errorf("节点 #%d 缺少 id", i)
		}
		if seen[n.ID] {
			return fmt.Errorf("节点 id 重复: %s", n.ID)
		}
		seen[n.ID] = true
		if strings.TrimSpace(n.API) == "" {
			return fmt.Errorf("节点 %s 缺少 api", n.ID)
		}
		if len(n.Caps) == 0 {
			return fmt.Errorf("节点 %s 未声明任何 caps(客户端永远不会路由到它)", n.ID)
		}
		for _, c := range n.Caps {
			if !isKnownCap(c) {
				return fmt.Errorf("节点 %s 声明了未知能力 %q", n.ID, c)
			}
		}
		// §2 能力隔离:边缘节点只能有 resource,绝不能碰凭证类。
		if n.Role == RoleEdge {
			for _, c := range n.Caps {
				if isCredentialCap(c) {
					return fmt.Errorf(
						"边缘节点 %s 不得持有凭证类能力 %q(§2 能力隔离:"+
							"凭证类请求绝不能指向仅做资源分发的节点)", n.ID, c)
				}
			}
		}
	}
	return nil
}

// Sign 将 d 序列化为紧凑 JSON，base64url 编码（无填充），
// 再对该编码字符串的 UTF-8 字节用 priv 签名，返回线上格式 SignedDirectory。
//
// 签发前先跑 Validate:签名一旦签出就是客户端眼中的"真理",非法的能力分配
// 必须在这里挡住,而不是等它生效。
func Sign(d *Directory, priv ed25519.PrivateKey) (SignedDirectory, error) {
	if err := d.Validate(); err != nil {
		return SignedDirectory{}, fmt.Errorf("拒绝签发非法目录: %w", err)
	}
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

// DecodeUnverified 只做「解析」不做「验签」:把线上格式的 payload base64url 解码并
// 反序列化为 Directory。
//
// ⚠️ 名字里的 Unverified 是认真的——它**不校验签名**,返回的内容不可信,
// 绝不能拿它替代 Verify 去做任何信任决策。
//
// 唯一用途:业务节点自检。节点只分发离线签好的字节、手里没有根公钥(公钥钉在
// 客户端 APK 里),因此没法验签;但它仍然可以把 payload 解出来看一眼结构是否
// 合理——比如能力分配有没有违反 §2、是不是早就过期了。这类问题若不在启动时
// 喊出来,客户端只会静默丢弃目录并回退 API_HOST,运维完全无感。
func DecodeUnverified(sd SignedDirectory) (*Directory, error) {
	if sd.Payload == "" || sd.Sig == "" {
		return nil, errors.New("目录缺少 payload 或 sig")
	}
	inner, err := base64.RawURLEncoding.DecodeString(sd.Payload)
	if err != nil {
		return nil, errors.New("payload 不是合法 base64url")
	}
	var d Directory
	if err := json.Unmarshal(inner, &d); err != nil {
		return nil, fmt.Errorf("payload 不是合法目录 JSON: %w", err)
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
