package pki

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Identity 是本节点在信任树里的身份:自己的证书链 + 私钥 + 钉住的信任根。
//
// # 为什么要有启动自检
//
// PKI 配错的症状**几乎总是延迟且指错方向的**:证书与私钥不配对,要到第一次签发
// 或第一次被对端挑战时才炸;链锚不到根,要到第一次互鉴时才炸;角色配反了,可能
// 一直不炸,只是安静地把凭证类请求收进了一台本该只发资源的机器。
//
// 所以这里在**进程启动时**把能查的全查掉,查不过就拒绝启动。宁可起不来,
// 也不要带着一个错误的身份跑起来——后者是那种上线三个月才被发现的问题。
type Identity struct {
	// Chain 是本节点的证书链,叶子(自己)在前,不含根。
	Chain []string
	// Signer 用本节点私钥签名:签下级证书(子 CA)或签持有性证明。
	Signer *Signer
	// Verifier 钉住信任根,用来验证对端。
	Verifier *Verifier
}

// LoadOptions 是加载身份所需的文件路径与期望值。
type LoadOptions struct {
	// CertFile 本节点证书。链上更高层的证书用 ChainFiles 补。
	CertFile string
	// ChainFiles 中间证书(自底向上),不含根。本节点由根直签时留空。
	ChainFiles []string
	// KeyFile 本节点私钥种子文件。
	KeyFile string
	// AnchorFiles 钉住的根证书,可多把(轮换重叠期)。
	AnchorFiles []string
	// WantRole 期望的角色;非空时与证书里的角色比对,不符拒绝启动。
	WantRole string
	// Now 用于校验有效期,零值取 time.Now()。
	Now time.Time
}

// Load 读入并自检本节点身份。任何一项不满足都返回错误,调用方应当据此拒绝启动。
func Load(o LoadOptions) (*Identity, error) {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if o.CertFile == "" || o.KeyFile == "" || len(o.AnchorFiles) == 0 {
		return nil, errors.New("pki: 证书、私钥与信任根三者都必须配置")
	}

	anchors := make([]string, 0, len(o.AnchorFiles))
	for _, p := range o.AnchorFiles {
		raw, err := readTrimmed(p)
		if err != nil {
			return nil, fmt.Errorf("读取信任根 %s: %w", p, err)
		}
		anchors = append(anchors, raw)
	}
	verifier, err := NewVerifier(anchors)
	if err != nil {
		return nil, err
	}

	certRaw, err := readTrimmed(o.CertFile)
	if err != nil {
		return nil, fmt.Errorf("读取证书 %s: %w", o.CertFile, err)
	}
	chain := []string{certRaw}
	for _, p := range o.ChainFiles {
		raw, err := readTrimmed(p)
		if err != nil {
			return nil, fmt.Errorf("读取中间证书 %s: %w", p, err)
		}
		chain = append(chain, raw)
	}

	// ① 整条链必须锚定到钉住的根。这一步顺带查了有效期、CA 标志、path_len、
	//    能力子集、角色可签性——全套规则,和运行时验对端走的是同一段代码。
	res, err := verifier.VerifyChain(chain, now)
	if err != nil {
		return nil, fmt.Errorf("本节点证书链未通过校验: %w", err)
	}

	// ② 私钥必须与证书里的公钥配对。不查的话,进程能正常起来、能接受请求,
	//    直到第一次要签东西才失败,而那时错误现场离真正的原因已经很远了。
	keyRaw, err := readTrimmed(o.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("读取私钥 %s: %w", o.KeyFile, err)
	}
	signer, err := NewSigner(res.Leaf, keyRaw)
	if err != nil {
		return nil, err
	}

	// ③ 角色必须与进程实际承担的职责一致。
	//    配反了通常**不会报错**——只是安静地让一台本该只发资源的机器收下了
	//    凭证类请求。这是三项检查里唯一可能长期无症状的,所以必须查。
	if o.WantRole != "" && res.Leaf.Role != o.WantRole {
		return nil, fmt.Errorf("pki: 证书角色是 %q,但本节点按 %q 运行;"+
			"配反会让本该只发资源的节点收下凭证类请求,拒绝启动",
			res.Leaf.Role, o.WantRole)
	}

	return &Identity{Chain: chain, Signer: signer, Verifier: verifier}, nil
}

// Leaf 返回本节点自己的证书。
func (id *Identity) Leaf() *Cert { return id.Signer.Cert }

// Describe 返回一行可读摘要,供启动日志使用。不含任何私钥材料。
func (id *Identity) Describe() string {
	c := id.Leaf()
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s role=%s caps=%v ca=%v 有效期至 %s",
		c.Sub, c.Role, c.Caps, c.CA,
		time.UnixMilli(c.Exp).Format(time.RFC3339))
	if len(id.Chain) > 1 {
		fmt.Fprintf(&sb, " 链长 %d", len(id.Chain))
	}
	anchors := id.Verifier.Anchors()
	fmt.Fprintf(&sb, " 信任根 %d 把", len(anchors))
	return sb.String()
}

// NeedsRenewal 判断本节点证书是否该续期了。
func (id *Identity) NeedsRenewal(now time.Time) bool {
	return id.Leaf().NeedsRenewal(now)
}

func readTrimmed(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "", errors.New("文件为空")
	}
	return s, nil
}
