// Package clienttoken 实现**自包含**的客户端会话令牌。
//
// # 为什么需要它
//
// 原先的 access_token 是 32 字节随机 hex,校验方式是拿它去 client_sessions 表
// 查一行。这意味着**签发方与校验方必须共用同一个数据库**——于是账号系统被钉死
// 在资源分发服务端里。
//
// 但账号在架构上属于 API 后端(magirecocn-api-server),资源分发服务端应该只
// 持有 API 后端下发的身份,而不是自己就是身份的源头。要拆开这两者,令牌就必须
// 能被**没有账号库的一方**独立验证。
//
// 所以本包把令牌做成自包含的:载荷明文写在令牌里,用 Ed25519 私钥签名;校验方
// 只需要签发方的**公钥**,不查库、不共享数据库,也无法伪造签发方的令牌
// (这正是不用 HMAC 共享密钥的原因——共享密钥等于校验方也能签发)。
//
// # 线格式
//
//	cnv1.<payload>.<sig>
//
//	payload = base64url(紧凑 JSON),无 = 填充
//	sig     = base64url(Ed25519 签名),无 = 填充
//
// **签名覆盖的是 "cnv1.<payload>" 这个字符串的 ASCII 字节**,即把版本前缀也
// 纳入签名。这一点与纪律文件 §2 的签名节点目录不同(那边只签 payload):目录的
// 版本信息在 payload 内部,而本令牌的版本在前缀上,不签进去的话,将来出 cnv2
// 时攻击者可以把 cnv2 令牌的 payload 原样搬到 cnv1 前缀下复用签名。
//
// # 这是唯一的会话令牌格式
//
// 旧的 64 位 hex 令牌 + 查库校验已随 Android 专有 API 一并废弃,校验侧没有任何
// 降级分支。Looks() 保留下来只做一件事:把"不是本格式"和"本格式但无效"分开,
// 好让错误信息说得准——它**不是**用来分流到别的校验方式的。
package clienttoken

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Prefix 是新格式令牌的版本前缀。旧格式(64 位 hex)不含 '.',不会误判。
const Prefix = "cnv1"

// maxTokenLen 限制解析前的输入长度。正常令牌约 400 字节;给到 4 KiB 已经非常
// 宽松,超过一律直接拒绝,避免拿超长串喂 base64/json 解码器空耗 CPU。
const maxTokenLen = 4096

var (
	ErrMalformed      = errors.New("clienttoken: 令牌格式非法")
	ErrUnknownIssuer  = errors.New("clienttoken: 签发方未被信任")
	ErrBadSignature   = errors.New("clienttoken: 签名校验失败")
	ErrExpired        = errors.New("clienttoken: 令牌已过期")
	ErrNotYetValid    = errors.New("clienttoken: 令牌尚未生效")
	ErrRevoked        = errors.New("clienttoken: 令牌已被撤销")
	ErrDeviceMismatch = errors.New("clienttoken: 令牌与 device_id 不匹配")
)

// Claims 是令牌载荷。字段名刻意取短——令牌会随每个 /client/* 请求上送,
// 省下的字节是实打实的。
//
// 注意:这些字段是**签发方的断言**,校验通过后才可信;任何一个字段在
// Verify 返回 nil error 之前都不得用于业务判断。
type Claims struct {
	V   int    `json:"v"`             // 载荷版本,当前恒为 1
	Iss string `json:"iss"`           // 签发方标识,用于选公钥
	Sub string `json:"sub"`           // device_id
	Sig string `json:"sig,omitempty"` // APK 签名证书 sha256(防中途换包)
	CV  string `json:"cv,omitempty"`  // client_version
	Ch  string `json:"ch,omitempty"`  // 渠道 normal / internal-test
	Acc string `json:"acc,omitempty"` // 账号 UUID:联邦模式下由 API 后端下发
	Iat int64  `json:"iat"`           // 签发时刻,Unix 毫秒
	Exp int64  `json:"exp"`           // 过期时刻,Unix 毫秒
	JTI string `json:"jti"`           // 令牌唯一标识,撤销名单按它匹配
}

// Looks 判断一个字符串**看起来**是不是新格式令牌。它只看前缀,不做任何
// 密码学校验——用途是在校验侧区分"新令牌"与"旧的 64 位 hex 令牌",
// 好把两者分别送去对应的校验路径。
//
// 返回 true **不代表令牌有效**,必须再走 Verify。
func Looks(s string) bool {
	return len(s) <= maxTokenLen && strings.HasPrefix(s, Prefix+".")
}

// ── 签发 ──────────────────────────────────────────────────────────────────

// Issuer 持有签名私钥。
//
// 安全:这把私钥必须**在线**(每次握手都要签名),因此它与纪律文件 §2 那把
// 离线的目录私钥是**两把不同的钥匙**,绝不可复用。目录私钥一旦上线,签名节点
// 目录的信任根就退化到和业务进程同等的暴露面上了。
type Issuer struct {
	id   string
	priv ed25519.PrivateKey
}

// NewIssuer 用 32 字节种子的十六进制(64 个字符)构造签发方。
// 种子来自环境变量 / CI Secret,不入库、不进日志(纪律文件 §5)。
func NewIssuer(id, seedHex string) (*Issuer, error) {
	if id == "" {
		return nil, errors.New("clienttoken: 签发方标识为空")
	}
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil {
		return nil, errors.New("clienttoken: 签名种子不是合法十六进制")
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("clienttoken: 签名种子必须是 32 字节(64 个十六进制字符)")
	}
	return &Issuer{id: id, priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// ID 返回签发方标识。
func (i *Issuer) ID() string { return i.id }

// PublicKeyHex 返回可以公开分发的验证公钥(64 位小写十六进制),
// 与纪律文件 §2 对公钥的表示方式一致。
func (i *Issuer) PublicKeyHex() string {
	return hex.EncodeToString(i.priv.Public().(ed25519.PublicKey))
}

// Issue 签发令牌。调用方只需填业务字段(Sub/Sig/CV/Ch/Acc);
// v / iss / iat / exp 由本方法按 now 与 ttl 统一填写,不接受调用方覆盖——
// 让调用方自己算过期时间迟早会出现某处忘了设而签出永不过期的令牌。
func (i *Issuer) Issue(c Claims, now time.Time, ttl time.Duration, jti string) (string, error) {
	if c.Sub == "" {
		return "", errors.New("clienttoken: sub(device_id)不能为空")
	}
	if ttl <= 0 {
		return "", errors.New("clienttoken: ttl 必须为正")
	}
	if jti == "" {
		return "", errors.New("clienttoken: jti 不能为空")
	}
	c.V = 1
	c.Iss = i.id
	c.Iat = now.UnixMilli()
	c.Exp = now.Add(ttl).UnixMilli()
	c.JTI = jti

	inner, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signing := Prefix + "." + base64.RawURLEncoding.EncodeToString(inner)
	sig := ed25519.Sign(i.priv, []byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ── 校验 ──────────────────────────────────────────────────────────────────

// Verifier 校验令牌。它**不需要数据库**,只需要一组受信任签发方的公钥。
//
// Revoked 是可选的撤销回调:返回 true 表示该令牌已被撤销。留空则不做撤销检查。
//
// 回调收到的是**已验签**的完整载荷而不是单独的 jti,因为撤销策略必须能按签发方
// 分流:令牌是本节点自己签的,撤销名单就在本地库里,查不到即视为已撤销;令牌来自
// 联邦模式下受信任的 API 后端,本地根本没有它的记录,"查不到"必须视为**有效**,
// 否则一开启联邦就会把所有远端令牌全拒掉。只给 jti 的话回调分不清这两种情况。
type Verifier struct {
	keys    map[string]ed25519.PublicKey
	Leeway  time.Duration // 容忍的时钟偏移,默认 60s
	Revoked func(c *Claims) bool
}

// NewVerifier 构造校验器。keysHex 是 签发方标识 → 64 位十六进制公钥。
func NewVerifier(keysHex map[string]string) (*Verifier, error) {
	v := &Verifier{keys: make(map[string]ed25519.PublicKey, len(keysHex)), Leeway: time.Minute}
	for id, kh := range keysHex {
		if id == "" {
			return nil, errors.New("clienttoken: 信任表里有空的签发方标识")
		}
		b, err := hex.DecodeString(strings.TrimSpace(kh))
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, errors.New("clienttoken: 签发方 " + id + " 的公钥不是 32 字节十六进制")
		}
		v.keys[id] = ed25519.PublicKey(b)
	}
	return v, nil
}

// Trusted 返回是否至少配了一个受信任签发方。没配的话校验器只会拒绝一切令牌,
// 调用方据此决定是否要退回旧的查库路径。
func (v *Verifier) Trusted() bool { return v != nil && len(v.keys) > 0 }

// Verify 校验令牌并返回载荷。
//
// 顺序很重要:**先验签,再看任何业务字段**。iss 是唯一在验签前被读取的字段,
// 且只用于挑公钥——挑不到就直接拒,不会因此信任载荷里的任何内容。
func (v *Verifier) Verify(token string, now time.Time) (*Claims, error) {
	if v == nil || len(v.keys) == 0 {
		return nil, ErrUnknownIssuer
	}
	if len(token) > maxTokenLen || !strings.HasPrefix(token, Prefix+".") {
		return nil, ErrMalformed
	}
	// 期望恰好三段:cnv1 . payload . sig
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return nil, ErrMalformed
	}
	inner, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrMalformed
	}

	var c Claims
	if err := json.Unmarshal(inner, &c); err != nil {
		return nil, ErrMalformed
	}
	if c.V != 1 {
		return nil, ErrMalformed
	}
	pub, ok := v.keys[c.Iss]
	if !ok {
		return nil, ErrUnknownIssuer
	}
	// 注意签名覆盖 "cnv1.<payload>",不是单独的 payload,见包注释。
	if !ed25519.Verify(pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, ErrBadSignature
	}

	// ── 以下字段到这里才可信 ──
	leeway := v.Leeway
	if leeway < 0 {
		leeway = 0
	}
	nowMs := now.UnixMilli()
	skew := leeway.Milliseconds()
	if c.Exp <= c.Iat {
		return nil, ErrMalformed
	}
	if nowMs > c.Exp+skew {
		return nil, ErrExpired
	}
	if nowMs < c.Iat-skew {
		return nil, ErrNotYetValid
	}
	if c.JTI == "" {
		return nil, ErrMalformed
	}
	if v.Revoked != nil && v.Revoked(&c) {
		return nil, ErrRevoked
	}
	return &c, nil
}

// VerifyForDevice 在 Verify 之上追加设备绑定检查:令牌不得被搬到另一台设备上
// 复用。deviceID 为空时跳过该检查(与旧的 ClientSessionLookup 语义保持一致)。
func (v *Verifier) VerifyForDevice(token, deviceID string, now time.Time) (*Claims, error) {
	c, err := v.Verify(token, now)
	if err != nil {
		return nil, err
	}
	if deviceID != "" && c.Sub != deviceID {
		return nil, ErrDeviceMismatch
	}
	return c, nil
}
