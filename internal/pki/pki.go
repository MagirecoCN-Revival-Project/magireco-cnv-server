// Package pki 实现节点身份证书与证书链校验。
//
// # 为什么需要它
//
// 资源分发服务端在设计上是**分布式**的:多个 resource-server 实例,每个下面再挂
// 若干边缘节点。原先的信任模型是一张扁平表(签发方标识 → 公钥),校验方要带外配置
// 每一个签发方的公钥——节点一多就没法维护,加一台机器要去改所有校验方的配置。
//
// 证书链把这件事收敛成:**只钉一把根公钥**,其余身份靠链自证。
//
//	未连接 API 后端:resource-server 自签根证书,自己就是根 CA。
//	连接了 API 后端:API 后端是根 CA,签出 resource-server 的子 CA 证书,
//	                子 CA 再签自己底下边缘节点的证书。
//
// # 密钥姿态
//
// 这个分层顺带改善了纪律文件 §2 的密钥暴露面:**根 CA 私钥可以保持离线**
// (只在签子 CA 时用一次,频率极低),需要在线的是子 CA 私钥——它只能签出
// 能力不超过自身的下级,即使被击穿也无法提权到根。
//
// # 线格式
//
//	单张证书:cnvc1.<base64url(紧凑JSON)>.<base64url(Ed25519签名)>
//	证书链:  JSON 数组,**叶子在前、越往后越靠近根**,不含根证书本身
//	         (根由校验方钉住,不能由对端提供——否则等于让攻击者自带信任根)。
//
// 签名覆盖 "cnvc1.<payload>" 的 ASCII 字节,与 internal/clienttoken 同一约定:
// 版本前缀纳入签名,避免将来出 cnvc2 时载荷被搬到旧前缀下复用签名。
package pki

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
	"time"
)

// Prefix 是证书的版本前缀。
const Prefix = "cnvc1"

// 节点角色。这是与 caps **正交**的一根轴:caps 回答"能对客户端做什么"
// (init/login/account/save/resource),角色回答"你是哪一类节点"——互相鉴权时
// 要问的是后者。把两者混在一起的话,"只有 resource 能力的节点"和"边缘节点"
// 会被迫同义,而将来一台业务节点完全可能只被授予 resource。
const (
	RoleRoot     = "root"     // 离线根 CA
	RoleAPI      = "api"      // API 后端(账号与游戏后端)
	RoleResource = "resource" // 资源分发服务端(子 CA)
	RoleEdge     = "edge"     // 边缘节点(叶子)
)

// allowedChildRoles 规定每种角色可以签出哪些下级角色。
//
// 这是"根恒在"模型的落点:api 与 resource 是**平级**的子 CA,都由根直签。
// api 不能签 resource,所以 API 后端即使被击穿也造不出一台资源分发服务端的身份;
// 反之亦然。少了这张表,caps 子集规则拦不住它——根的 caps 是全集,api 子 CA
// 只要 caps 不越界就能签出任何角色。
var allowedChildRoles = map[string][]string{
	RoleRoot:     {RoleAPI, RoleResource},
	RoleResource: {RoleEdge},
	RoleAPI:      {},
	RoleEdge:     {},
}

func roleCanIssue(parent, child string) bool {
	for _, r := range allowedChildRoles[parent] {
		if r == child {
			return true
		}
	}
	return false
}

const (
	// maxCertLen 单张证书解析前的长度上限。
	maxCertLen = 8192
	// MaxChainLen 链的最大长度(不含钉住的根)。现实里最深是
	// 根 → 子CA(resource-server) → 叶子(边缘节点/会话),即 2 张。
	// 给到 4 已经很宽松,超过一律拒绝——链长不设上限等于给验签留了一条
	// 廉价的 CPU 消耗通道。
	MaxChainLen = 4
)

var (
	ErrMalformed       = errors.New("pki: 证书格式非法")
	ErrEmptyChain      = errors.New("pki: 证书链为空")
	ErrChainTooLong    = errors.New("pki: 证书链过长")
	ErrBadSignature    = errors.New("pki: 证书签名校验失败")
	ErrUnknownAnchor   = errors.New("pki: 链未锚定到任何受信任的根")
	ErrNotYetValid     = errors.New("pki: 证书尚未生效")
	ErrExpired         = errors.New("pki: 证书已过期")
	ErrNotCA           = errors.New("pki: 签发者不是 CA,无权签发下级证书")
	ErrPathLenViolated = errors.New("pki: 超出签发者允许的链深度")
	ErrCapEscalation   = errors.New("pki: 下级证书的能力超出签发者")
	ErrIssuerMismatch  = errors.New("pki: 证书的签发者标识与上级不符")
	ErrRevoked         = errors.New("pki: 证书已被撤销")
	ErrRoleNotAllowed  = errors.New("pki: 签发者无权签出该角色的证书")
	ErrRoleMismatch    = errors.New("pki: 对端角色与预期不符")
	ErrBadProof        = errors.New("pki: 持有性证明校验失败")
	ErrWeakNonce       = errors.New("pki: 挑战随机数过短")
)

// Cert 是证书的明文载荷。
type Cert struct {
	V       int      `json:"v"`              // 载荷版本,当前恒为 1
	Serial  string   `json:"serial"`         // 唯一序列号,撤销按它匹配
	Sub     string   `json:"sub"`            // 主体标识(节点 ID)
	Pub     string   `json:"pub"`            // 主体公钥,64 位小写十六进制
	Iss     string   `json:"iss"`            // 签发者的主体标识
	Role    string   `json:"role"`           // 节点角色,取值见 Role* 常量
	Caps    []string `json:"caps"`           // 被授权的能力,取值见 internal/directory
	CA      bool     `json:"ca"`             // 是否允许再签发下级
	PathLen int      `json:"path_len"`       // 还允许往下签几层(仅 CA 有意义)
	NBF     int64    `json:"nbf"`            // 生效时刻,Unix 毫秒
	Exp     int64    `json:"exp"`            // 过期时刻,Unix 毫秒
	Note    string   `json:"note,omitempty"` // 备注,不参与任何判定
}

// PublicKey 解析主体公钥。
func (c *Cert) PublicKey() (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(c.Pub))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrMalformed
	}
	return ed25519.PublicKey(b), nil
}

// HasCap 判断证书是否声明了某能力。
func (c *Cert) HasCap(cap string) bool {
	for _, x := range c.Caps {
		if x == cap {
			return true
		}
	}
	return false
}

// capsSubsetOf 判断 child 的能力是否**完全包含于** parent。
//
// 这条是整套模型里最要紧的不变量:没有它,一张只该有 resource 能力的边缘节点
// 证书可以给自己签出带 login/account/save 的下级,§2 好不容易做出来的能力隔离
// 会整个失效——而且失效得悄无声息。
func capsSubsetOf(child, parent []string) bool {
	have := make(map[string]struct{}, len(parent))
	for _, c := range parent {
		have[c] = struct{}{}
	}
	for _, c := range child {
		if _, ok := have[c]; !ok {
			return false
		}
	}
	return true
}

// ── 签发 ──────────────────────────────────────────────────────────────────

// Signer 是一张证书 + 对应私钥,可以签发下级证书。
type Signer struct {
	Cert *Cert
	priv ed25519.PrivateKey
}

// NewSigner 用主体证书与 32 字节私钥种子构造签发者。
// 会校验种子与证书里的公钥确实是一对——配错了的话签出来的证书在别处一律验不过,
// 而错误现场离真正的原因很远,不如在这里就拦住。
func NewSigner(cert *Cert, seedHex string) (*Signer, error) {
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("pki: 私钥种子必须是 32 字节十六进制")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if !strings.EqualFold(pub, strings.TrimSpace(cert.Pub)) {
		return nil, errors.New("pki: 私钥与证书里的公钥不匹配")
	}
	return &Signer{Cert: cert, priv: priv}, nil
}

// NewRoot 自签一张根证书。未连接 API 后端时,resource-server 用它把自己
// 立成根 CA。
// 根证书由**离线**的 admintool 签发并持久化,服务进程正常运行期间不应调用它。
// 「根 CA 恒在」:无论是否连接 API 后端,信任根都是同一个离线根,独立模式只是
// 少了 api-server 这个参与者,而不是换一套信任模型。
func NewRoot(subject, seedHex string, caps []string, pathLen int, now time.Time, ttl time.Duration) (*Signer, string, error) {
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, "", errors.New("pki: 私钥种子必须是 32 字节十六进制")
	}
	if subject == "" {
		return nil, "", errors.New("pki: 根证书主体不能为空")
	}
	if ttl <= 0 {
		return nil, "", errors.New("pki: ttl 必须为正")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	serial, err := newSerial()
	if err != nil {
		return nil, "", err
	}
	c := &Cert{
		V: 1, Serial: serial, Sub: subject,
		Pub:  hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
		Iss:  subject, // 自签
		Role: RoleRoot,
		Caps: append([]string(nil), caps...),
		CA:   true, PathLen: pathLen,
		NBF: now.UnixMilli(), Exp: now.Add(ttl).UnixMilli(),
	}
	encoded, err := encodeAndSign(c, priv)
	if err != nil {
		return nil, "", err
	}
	return &Signer{Cert: c, priv: priv}, encoded, nil
}

// IssueParams 是签发下级证书时由调用方决定的部分。
type IssueParams struct {
	Subject   string
	Role      string // 下级角色,见 Role* 常量
	PublicKey string // 下级的公钥,64 位十六进制
	Caps      []string
	CA        bool
	PathLen   int
	TTL       time.Duration
	Note      string
}

// Issue 签发一张下级证书,返回编码后的证书串。
//
// 这里就把不变量卡死,而不是只在校验侧卡:签发侧拦住,错误的证书根本不会存在;
// 只在校验侧拦,错误证书会一路发到生产再被拒,排查成本高得多。
func (s *Signer) Issue(p IssueParams, now time.Time) (string, error) {
	if !s.Cert.CA {
		return "", ErrNotCA
	}
	if s.Cert.PathLen <= 0 {
		return "", ErrPathLenViolated
	}
	if p.Subject == "" {
		return "", errors.New("pki: 下级主体不能为空")
	}
	// 有效期下限按默认容差校验:签发侧不知道各校验方各自配了多大容差,
	// 用默认值卡住已经能挡掉"签出一张几乎立刻就要续期的证书"这类错误。
	if err := ValidateTTL(p.TTL, time.Minute); err != nil {
		return "", err
	}
	if b, err := hex.DecodeString(strings.TrimSpace(p.PublicKey)); err != nil || len(b) != ed25519.PublicKeySize {
		return "", errors.New("pki: 下级公钥必须是 32 字节十六进制")
	}
	if !capsSubsetOf(p.Caps, s.Cert.Caps) {
		return "", ErrCapEscalation
	}
	if !roleCanIssue(s.Cert.Role, p.Role) {
		return "", ErrRoleNotAllowed
	}
	// 下级若也是 CA,它的 path_len 必须比自己小,否则链深度可以无限延展。
	childPathLen := 0
	if p.CA {
		childPathLen = p.PathLen
		if childPathLen >= s.Cert.PathLen {
			childPathLen = s.Cert.PathLen - 1
		}
		if childPathLen <= 0 {
			return "", ErrPathLenViolated
		}
	}
	// 下级不得比签发者活得久:上级过期后下级仍有效的话,吊销上级就失去意义了。
	exp := now.Add(p.TTL).UnixMilli()
	if exp > s.Cert.Exp {
		exp = s.Cert.Exp
	}
	serial, err := newSerial()
	if err != nil {
		return "", err
	}
	c := &Cert{
		V: 1, Serial: serial, Sub: p.Subject, Pub: strings.ToLower(strings.TrimSpace(p.PublicKey)),
		Iss: s.Cert.Sub, Role: p.Role, Caps: append([]string(nil), p.Caps...),
		CA: p.CA, PathLen: childPathLen,
		NBF: now.UnixMilli(), Exp: exp, Note: p.Note,
	}
	return encodeAndSign(c, s.priv)
}

func encodeAndSign(c *Cert, priv ed25519.PrivateKey) (string, error) {
	inner, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signing := Prefix + "." + base64.RawURLEncoding.EncodeToString(inner)
	sig := ed25519.Sign(priv, []byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ── 校验 ──────────────────────────────────────────────────────────────────

// decode 解出证书载荷与"被签名的字节 + 签名",此时**尚未验签**。
func decode(encoded string) (*Cert, []byte, []byte, error) {
	if len(encoded) > maxCertLen || !strings.HasPrefix(encoded, Prefix+".") {
		return nil, nil, nil, ErrMalformed
	}
	parts := strings.Split(encoded, ".")
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return nil, nil, nil, ErrMalformed
	}
	inner, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, nil, nil, ErrMalformed
	}
	var c Cert
	if err := json.Unmarshal(inner, &c); err != nil {
		return nil, nil, nil, ErrMalformed
	}
	if c.V != 1 || c.Sub == "" || c.Serial == "" || c.Role == "" || c.Exp <= c.NBF {
		return nil, nil, nil, ErrMalformed
	}
	return &c, []byte(parts[0] + "." + parts[1]), sig, nil
}

// Verifier 校验证书链。
//
// 钉住的是**整张根证书**而不只是根公钥。这不是形式上的讲究:只钉公钥的话,链尾
// 那张由根直签的证书就没有上级可比对,CA 标志、path_len、能力子集三项检查会整个
// 跳过——等于根签出来的任何东西都无条件可信,包括一张 path_len 被写成 9 的子 CA。
// 钉整张证书之后,每一级(含最顶上那一级)走的是同一套检查,没有特例。
//
// 根仍然不能由对端在链里提供——那等于让攻击者自带信任根,链再自洽也没有意义。
type Verifier struct {
	// anchors 按**公钥**去重,bySubject 按主体标识建候选索引。
	//
	// 不能只按主体标识建索引:轮换时最自然的做法就是沿用同一个名字(offline-root),
	// 那样新旧两把根会**静默互相覆盖**,只剩最后加进来的一把——"钉现用 + 下一个"
	// 的重叠期轮换根本无从谈起,而且没有任何报错。
	anchors   map[string]*Cert
	bySubject map[string][]*Cert
	// Leeway 容忍的时钟偏移,默认 60s。分布式部署里各节点时钟不可能完全一致。
	Leeway time.Duration
	// Revoked 可选撤销回调,收到的是**已验签**的证书。与 clienttoken 同理:
	// 回调需要知道是谁签的才能决定去哪查撤销名单。
	Revoked func(*Cert) bool
}

// NewVerifier 构造校验器。anchors 是若干张**自签根证书**(编码后的串)。
//
// 每张都会当场自验:必须自签(iss == sub)、签名用自己的公钥验得过、且是 CA。
// 配错根是最致命的配置错误——错了不是拒绝服务,而是信任了不该信任的东西,
// 所以在这里就拦住,不留到运行时。
func NewVerifier(anchors []string) (*Verifier, error) {
	v := &Verifier{
		anchors:   map[string]*Cert{},
		bySubject: map[string][]*Cert{},
		Leeway:    time.Minute,
	}
	for _, enc := range anchors {
		c, signed, sig, err := decode(enc)
		if err != nil {
			return nil, fmt.Errorf("pki: 信任根证书无法解析: %w", err)
		}
		if c.Iss != c.Sub {
			return nil, fmt.Errorf("pki: 信任根 %q 不是自签证书", c.Sub)
		}
		pub, err := c.PublicKey()
		if err != nil {
			return nil, fmt.Errorf("pki: 信任根 %q 的公钥非法", c.Sub)
		}
		if !ed25519.Verify(pub, signed, sig) {
			return nil, fmt.Errorf("pki: 信任根 %q 自签名校验失败", c.Sub)
		}
		if !c.CA {
			return nil, fmt.Errorf("pki: 信任根 %q 不是 CA", c.Sub)
		}
		if c.Role != RoleRoot {
			return nil, fmt.Errorf("pki: 信任根 %q 的角色是 %q,应为 %q", c.Sub, c.Role, RoleRoot)
		}
		key := strings.ToLower(strings.TrimSpace(c.Pub))
		if _, dup := v.anchors[key]; dup {
			continue // 同一把根重复配置,幂等忽略
		}
		v.anchors[key] = c
		v.bySubject[c.Sub] = append(v.bySubject[c.Sub], c)
	}
	return v, nil
}

// Anchors 返回当前钉住的根证书,按主体标识排序后输出,便于启动时打日志核对。
// 轮换期会有同名的多把,这正是预期。
func (v *Verifier) Anchors() []*Cert {
	out := make([]*Cert, 0, len(v.anchors))
	for _, c := range v.anchors {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sub != out[j].Sub {
			return out[i].Sub < out[j].Sub
		}
		return out[i].Pub < out[j].Pub
	})
	return out
}

// Anchored 返回是否至少钉了一个信任根。
func (v *Verifier) Anchored() bool { return v != nil && len(v.anchors) > 0 }

// Verified 是校验通过的结果。
type Verified struct {
	// Leaf 是叶子证书(链的第一张)。
	Leaf *Cert
	// Caps 是叶子**实际生效**的能力。由于每一级都强制"下级 ⊆ 上级",
	// 它等于叶子自己声明的 caps;单独给出来是为了让调用方不必再自行推导。
	Caps []string
	// Path 是从叶子到根的完整证书序列(含钉住的根)。
	Path []*Cert
}

// VerifyChain 校验一条证书链,chain **叶子在前**,不含根证书。
//
// 校验顺序是从叶子往根走,每一步都用**上一级的公钥**验下一级的签名,最后要求
// 走到一个被钉住的根。任何一步不满足即整条链失败,不做部分接受。
func (v *Verifier) VerifyChain(chain []string, now time.Time) (*Verified, error) {
	if v == nil || len(v.anchors) == 0 {
		return nil, ErrUnknownAnchor
	}
	if len(chain) == 0 {
		return nil, ErrEmptyChain
	}
	if len(chain) > MaxChainLen {
		return nil, ErrChainTooLong
	}

	certs := make([]*Cert, len(chain))
	signed := make([][]byte, len(chain))
	sigs := make([][]byte, len(chain))
	for i, enc := range chain {
		c, sb, sg, err := decode(enc)
		if err != nil {
			return nil, err
		}
		certs[i], signed[i], sigs[i] = c, sb, sg
	}

	// 链内部逐级向上:certs[i] 由 certs[i+1] 签发。
	for i := 0; i+1 < len(certs); i++ {
		if err := v.checkLink(certs[i], certs[i+1], signed[i], sigs[i], now); err != nil {
			return nil, err
		}
	}

	// 链尾由钉住的根签发。轮换重叠期会有同名的多把根,所以要**逐个候选试**:
	// 只按名字取一把的话,重叠期里必然有一半的链验不过,而且报的是"签名错误"
	// 这种完全指错方向的错。
	top := certs[len(certs)-1]
	candidates := v.bySubject[top.Iss]
	if len(candidates) == 0 {
		return nil, ErrUnknownAnchor
	}
	var lastErr error = ErrUnknownAnchor
	matched := false
	for _, anchor := range candidates {
		if err := v.checkLink(top, anchor, signed[len(certs)-1], sigs[len(certs)-1], now); err != nil {
			lastErr = err
			continue
		}
		matched = true
		break
	}
	if !matched {
		return nil, lastErr
	}

	return &Verified{
		Leaf: certs[0],
		Caps: append([]string(nil), certs[0].Caps...),
		Path: append([]*Cert(nil), certs...),
	}, nil
}

// checkLink 校验"child 由 parent 签发"这一条边:先验签,再谈语义。
//
// 根也走这个函数,不开特例——第一版把这些检查写在"有上级时才做"的分支里,
// 而根不在链中,于是根直签的证书完全不受 CA / path_len / 能力子集约束。
func (v *Verifier) checkLink(child, parent *Cert, signed, sig []byte, now time.Time) error {
	// 签发者标识必须对得上。只验签不验标识的话,一张由 A 签发但 iss 写着 B
	// 的证书能混过去,后续按 iss 做的判断就全错位了。
	if child.Iss != parent.Sub {
		return ErrIssuerMismatch
	}
	parentPub, err := parent.PublicKey()
	if err != nil {
		return err
	}
	if !ed25519.Verify(parentPub, signed, sig) {
		return ErrBadSignature
	}

	// ── 验签之后才谈语义 ──
	if err := v.checkValidity(child, now); err != nil {
		return err
	}
	if v.Revoked != nil && v.Revoked(child) {
		return ErrRevoked
	}
	// 签发者必须是 CA。**漏掉这一条是证书链最经典的漏洞**:任何持有叶子
	// 证书的人都能给自己签出下级,冒充任意主体。
	if !parent.CA {
		return ErrNotCA
	}
	if parent.PathLen <= 0 {
		return ErrPathLenViolated
	}
	// 下级若也是 CA,深度必须严格递减。
	if child.CA && child.PathLen >= parent.PathLen {
		return ErrPathLenViolated
	}
	if !capsSubsetOf(child.Caps, parent.Caps) {
		return ErrCapEscalation
	}
	if !roleCanIssue(parent.Role, child.Role) {
		return ErrRoleNotAllowed
	}
	return nil
}

func (v *Verifier) checkValidity(c *Cert, now time.Time) error {
	leeway := v.Leeway
	if leeway < 0 {
		leeway = 0
	}
	skew := leeway.Milliseconds()
	nowMs := now.UnixMilli()
	if nowMs < c.NBF-skew {
		return ErrNotYetValid
	}
	if nowMs > c.Exp+skew {
		return ErrExpired
	}
	return nil
}

func (v *Verifier) VerifyChainForCap(chain []string, want string, now time.Time) (*Verified, error) {
	res, err := v.VerifyChain(chain, now)
	if err != nil {
		return nil, err
	}
	if !res.Leaf.HasCap(want) {
		return nil, fmt.Errorf("pki: 叶子证书未被授权能力 %q", want)
	}
	return res, nil
}

// newSerial 生成 16 字节随机序列号。
func newSerial() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ── 互相鉴权 ──────────────────────────────────────────────────────────────
//
// api-server 与 resource-server 建立连接时**双向**验证:各自出示证书链,并证明
// 自己确实持有叶子证书对应的私钥。证书关系对不上就拒绝连接。
//
// # 为什么光出示证书链不够
//
// 证书链是**公开**的——它本来就要发给对端看。任何抓到过一次握手的人都能把
// 那条链原样重放,冒充对端。所以必须有持有性证明:验证方给一个随机挑战,
// 对端用私钥签它。私钥没有离开过机器,重放者签不出来。
//
// # 为什么挑战里要写双方标识
//
// 只签随机数会有**反射攻击**:攻击者拿到 A 发给它的挑战,转手去问 B 要签名,
// 再把 B 的签名交回给 A 冒充 B——甚至可以把 A 自己的挑战反射给 A。
// 把"谁在证明、向谁证明"绑进被签名的消息里,一份签名就只在它被制造出来的那个
// 方向上有效,换个方向立刻失配。

// proofPrefix 是持有性证明的域分隔前缀,确保这类签名不会与证书签名混用。
const proofPrefix = "cnvp1"

// MinNonceLen 挑战随机数的最短长度。太短的挑战可被预生成签名表打掉。
const MinNonceLen = 16

// proofMessage 构造待签名的挑战消息:域分隔前缀 + 挑战 + 证明方 + 验证方。
// 后两者是防反射的关键,顺序固定不可调换。
func proofMessage(nonce []byte, proverSub, verifierSub string) []byte {
	return []byte(proofPrefix + "." +
		base64.RawURLEncoding.EncodeToString(nonce) + "." +
		proverSub + "." + verifierSub)
}

// ProveTo 用叶子证书的私钥对验证方给出的挑战签名,证明自己持有该私钥。
// verifierSub 是**对端**的主体标识,必须如实填写:填错了对端会验不过。
func (s *Signer) ProveTo(nonce []byte, verifierSub string) (string, error) {
	if len(nonce) < MinNonceLen {
		return "", ErrWeakNonce
	}
	if verifierSub == "" {
		return "", errors.New("pki: 验证方标识不能为空")
	}
	sig := ed25519.Sign(s.priv, proofMessage(nonce, s.Cert.Sub, verifierSub))
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// NewNonce 生成一个挑战随机数。
func NewNonce() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// AuthenticatePeer 完成对端鉴权:验证证书链锚定到钉住的根、对端角色符合预期、
// 且对端确实持有叶子私钥。
//
// selfSub 是**本方**的主体标识,会参与挑战消息的构造——它必须与对端调用
// ProveTo 时填的 verifierSub 一致,这正是防反射的那一环。
func (v *Verifier) AuthenticatePeer(chain []string, wantRole string, nonce []byte, proofB64, selfSub string, now time.Time) (*Verified, error) {
	if len(nonce) < MinNonceLen {
		return nil, ErrWeakNonce
	}
	if selfSub == "" {
		return nil, errors.New("pki: 本方标识不能为空")
	}
	res, err := v.VerifyChain(chain, now)
	if err != nil {
		return nil, err
	}
	// 角色先于持有性证明检查:角色不对就没必要再算一次签名。
	if wantRole != "" && res.Leaf.Role != wantRole {
		return nil, ErrRoleMismatch
	}
	sig, err := base64.RawURLEncoding.DecodeString(proofB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrBadProof
	}
	pub, err := res.Leaf.PublicKey()
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(pub, proofMessage(nonce, res.Leaf.Sub, selfSub), sig) {
		return nil, ErrBadProof
	}
	return res, nil
}

// ── 有效期策略 ────────────────────────────────────────────────────────────
//
// 三层的有效期差了好几个数量级,这是刻意的,各有各的理由:
//
//	根    10 年 —— 离线,轮换靠多锚重叠期而不是靠过期。让它频繁过期只会逼着人
//	              把根私钥拿出来用,反而削弱"离线"这件事本身。
//	子 CA 90 天 —— 手工签发的那一层。人能承受的频率上限大概就在这里。
//	边缘  小时级 —— 变动频繁,且这一层的**吊销问题被有效期消掉了**:机器下线后
//	              最多几小时证书自己失效,不需要吊销列表、不需要客户端去拉,
//	              也就没有"拉不到吊销列表时 fail-open 还是 fail-closed"的两难。
//
// 小时级只有在**自动续期**的前提下才成立。这不破坏"根离线":续签边缘叶子的是
// resource-server 子 CA,它本来就在线;离线的根只参与 90 天一次的子 CA 签发。
const (
	// DefaultRootTTL 根证书默认有效期。
	DefaultRootTTL = 10 * 365 * 24 * time.Hour
	// DefaultSubCATTL 子 CA(api / resource)默认有效期。
	DefaultSubCATTL = 90 * 24 * time.Hour
	// DefaultEdgeTTL 边缘节点叶子证书默认有效期。
	DefaultEdgeTTL = 6 * time.Hour
)

// MinTTLOverLeeway 是有效期相对时钟容差的最小倍数。
//
// 容差存在是因为分布式部署里各节点时钟不可能完全一致,它把有效窗口在两端各撑开
// 一点。当有效期短到与容差同量级时,这个"撑开"就不再是可忽略的修正,而会吃掉
// 可观比例的生命周期——极端情况下一张刚签发的证书,其实际可用时间与标称值差出
// 几倍,续期节奏也跟着失真。20 倍是个保守下限:默认容差 60s 时,有效期不得短于
// 20 分钟。
const MinTTLOverLeeway = 20

// ValidateTTL 检查有效期相对时钟容差是否合理。
func ValidateTTL(ttl, leeway time.Duration) error {
	if ttl <= 0 {
		return errors.New("pki: ttl 必须为正")
	}
	if leeway <= 0 {
		return nil
	}
	if min := leeway * MinTTLOverLeeway; ttl < min {
		return fmt.Errorf("pki: 有效期 %s 相对时钟容差 %s 过短,至少需要 %s",
			ttl, leeway, min)
	}
	return nil
}

// Lifetime 返回证书的标称生命周期。
func (c *Cert) Lifetime() time.Duration {
	return time.Duration(c.Exp-c.NBF) * time.Millisecond
}

// RenewAt 返回应当开始续期的时刻:生命周期过半。
//
// 过半而不是"临到期前一点"是为了给失败留出重试余量——续期本身要走网络,
// 一次失败之后还有半个生命周期可以反复重试,不至于一次网络抖动就让节点掉线。
func (c *Cert) RenewAt() time.Time {
	return time.UnixMilli(c.NBF + (c.Exp-c.NBF)/2)
}

// NeedsRenewal 判断此刻是否该续期了。
func (c *Cert) NeedsRenewal(now time.Time) bool {
	return !now.Before(c.RenewAt())
}
