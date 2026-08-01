package pki

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

// 证书自动续期。
//
// 边缘节点叶子只有小时级有效期(见 DefaultEdgeTTL),没有自动续期就得每几小时
// 人工重签一次。续签方是本节点的上级子 CA,它本来就在线,所以这条路径不触碰
// 离线根。
//
// # 三件容易做错的事
//
//  1. **先验证再落盘**。上游返回的证书必须先完整校验(能锚到根、与本机私钥配对、
//     角色不变)才允许覆盖现有证书。跳过这步的话,上游一次异常响应就能把节点
//     变砖:旧证书没了,新证书用不了,而且要到下次重启自检才被发现。
//  2. **退避上限必须由剩余寿命决定**,不能是个拍脑袋的常数。续期从生命周期过半
//     开始,剩余时间是固定的;退避退到比剩余寿命还长,等于放弃了后面所有重试机会。
//  3. **写盘要原子**。直接覆盖写,中途崩溃会留下半张证书,下次启动自检直接失败。
//     先写同目录临时文件再 rename。

// RenewFunc 向上级请求一张新证书,返回新的证书链(叶子在前,不含根)。
//
// 做成注入的函数而不是内建 HTTP 调用:传输方式(管控通道 / HTTP / 手工)会变,
// 而"何时续、失败怎么退、怎么安全换证"这套逻辑不该跟着变。
type RenewFunc func(ctx context.Context, csr string) ([]string, error)

// RenewConfig 配置续期器。
type RenewConfig struct {
	// CertFile / ChainFiles / KeyFile / AnchorFiles 与 LoadOptions 同义,
	// 续期成功后会把新证书写回 CertFile。
	CertFile    string
	ChainFiles  []string
	KeyFile     string
	AnchorFiles []string

	// Request 执行实际的续期请求。
	Request RenewFunc

	// MinBackoff 首次失败后的等待,默认 30s。
	MinBackoff time.Duration
	// MaxBackoffRatio 退避上限相对**剩余寿命**的比例,默认 1/8。
	// 用比例而不是常数,是因为不同角色的有效期差几个数量级:
	// 边缘 6 小时与子 CA 90 天用同一个常数上限,必然有一边是错的。
	MaxBackoffRatio float64

	// Now 可注入的时钟,测试用。零值取 time.Now。
	Now func() time.Time
	// Sleep 可注入的等待,测试用。nil 时用 time.After + ctx。
	Sleep func(ctx context.Context, d time.Duration) error

	Log *slog.Logger
}

// Renewer 持续维持本节点证书的有效性。
type Renewer struct {
	cfg RenewConfig
	id  *Identity
}

// NewRenewer 构造续期器。identity 是当前已加载并自检过的身份。
func NewRenewer(id *Identity, cfg RenewConfig) (*Renewer, error) {
	if id == nil {
		return nil, errors.New("pki: 续期器需要一个已加载的身份")
	}
	if cfg.Request == nil {
		return nil, errors.New("pki: 续期器需要 Request 回调")
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" || len(cfg.AnchorFiles) == 0 {
		return nil, errors.New("pki: 续期器需要证书、私钥与信任根的路径")
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = 30 * time.Second
	}
	if cfg.MaxBackoffRatio <= 0 {
		cfg.MaxBackoffRatio = 0.125
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Renewer{cfg: cfg, id: id}, nil
}

// Identity 返回当前生效的身份。续期成功后会指向新证书。
func (r *Renewer) Identity() *Identity { return r.id }

// backoffCap 返回退避上限:剩余寿命的固定比例。
//
// 剩余寿命越短退得越密——这正是想要的行为:临近过期时更频繁地重试,
// 而不是按同一个常数不紧不慢地退。
func (r *Renewer) backoffCap(now time.Time) time.Duration {
	remaining := time.UnixMilli(r.id.Leaf().Exp).Sub(now)
	if remaining <= 0 {
		return r.cfg.MinBackoff
	}
	cap := time.Duration(float64(remaining) * r.cfg.MaxBackoffRatio)
	if cap < r.cfg.MinBackoff {
		return r.cfg.MinBackoff
	}
	return cap
}

// CSR 生成一份用于续期的证书签名请求。
//
// 续期沿用**同一把密钥**:换密钥属于另一件事(emit-csr -rotate),混在续期里做的话,
// 一旦新证书没能落盘,本机私钥已经换了,旧证书也跟着作废,两头落空。
func (r *Renewer) CSR() (string, error) {
	leaf := r.id.Leaf()
	keyHex, err := readTrimmed(r.cfg.KeyFile)
	if err != nil {
		return "", fmt.Errorf("读取私钥: %w", err)
	}
	return NewCSR(leaf.Sub, leaf.Role, leaf.Caps, keyHex, r.cfg.Now(), leaf.Note)
}

// Install 校验上游给的证书链并原子换证。失败时**不改动任何文件**。
//
// 这是续期里最要紧的一段:少了这里的校验,上游一次异常响应就能把节点变砖——
// 旧证书没了、新证书用不了,而且要到下次重启自检才被发现。
//
// 它与 RenewOnce 拆开是因为管控通道上续期是**上级驱动**的:上级先问节点要 CSR,
// 自己签完再把链送回来装上,两步各自独立可用(手工送证书也走 Install)。
func (r *Renewer) Install(chain []string) error {
	leaf := r.id.Leaf()
	if len(chain) == 0 {
		return errors.New("上游返回了空证书链")
	}
	keyHex, err := readTrimmed(r.cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("读取私钥: %w", err)
	}

	// ── 先验证,再落盘 ──
	verified, err := r.id.Verifier.VerifyChain(chain, r.cfg.Now())
	if err != nil {
		return fmt.Errorf("上游返回的证书链未通过校验,拒绝替换: %w", err)
	}
	if _, err := NewSigner(verified.Leaf, keyHex); err != nil {
		return fmt.Errorf("上游返回的证书与本机私钥不配对,拒绝替换: %w", err)
	}
	if verified.Leaf.Sub != leaf.Sub {
		return fmt.Errorf("上游返回的证书主体是 %q,本节点是 %q,拒绝替换",
			verified.Leaf.Sub, leaf.Sub)
	}
	// 角色或能力被悄悄改动,必须由人确认——自动续期是维持现状的机制,
	// 不该成为提权通道。
	if verified.Leaf.Role != leaf.Role {
		return fmt.Errorf("上游把角色从 %q 改成了 %q,拒绝自动接受",
			leaf.Role, verified.Leaf.Role)
	}
	if !capsSubsetOf(verified.Leaf.Caps, leaf.Caps) {
		return fmt.Errorf("上游扩大了本节点的能力(%v → %v),拒绝自动接受",
			leaf.Caps, verified.Leaf.Caps)
	}
	// 新证书必须真的更晚过期,否则这次续期毫无意义,还会让循环空转。
	if verified.Leaf.Exp <= leaf.Exp {
		return fmt.Errorf("上游返回的证书没有延长有效期(%d → %d),拒绝替换",
			leaf.Exp, verified.Leaf.Exp)
	}

	if err := writeFileAtomic(r.cfg.CertFile, []byte(chain[0]+"\n"), 0o644); err != nil {
		return fmt.Errorf("写入新证书: %w", err)
	}
	// 中间证书也可能换(上级自己续期过),一并落盘。数量对不上时只换叶子——
	// 链形状变化属于结构调整,应当由人介入,不该由自动续期悄悄完成。
	if len(chain)-1 == len(r.cfg.ChainFiles) {
		for i, p := range r.cfg.ChainFiles {
			if err := writeFileAtomic(p, []byte(chain[i+1]+"\n"), 0o644); err != nil {
				return fmt.Errorf("写入中间证书 %s: %w", p, err)
			}
		}
	} else if len(chain) > 1 {
		r.cfg.Log.Warn("上游返回的链长与本地配置不符,只更新了叶子证书",
			"上游链长", len(chain), "本地中间证书数", len(r.cfg.ChainFiles))
	}

	r.id = &Identity{Chain: chain, Signer: mustSigner(verified.Leaf, keyHex), Verifier: r.id.Verifier}
	r.cfg.Log.Info("证书已续期",
		"subject", verified.Leaf.Sub,
		"expires_at", time.UnixMilli(verified.Leaf.Exp).Format(time.RFC3339),
		"next_renew_at", verified.Leaf.RenewAt().Format(time.RFC3339))
	return nil
}

// RenewOnce 执行一次节点驱动的续期:生成 CSR、请求、校验、原子换证。
func (r *Renewer) RenewOnce(ctx context.Context) error {
	csr, err := r.CSR()
	if err != nil {
		return err
	}
	chain, err := r.cfg.Request(ctx, csr)
	if err != nil {
		return fmt.Errorf("请求续期: %w", err)
	}
	return r.Install(chain)
}

// Run 持续维持证书有效,直到 ctx 结束。
func (r *Renewer) Run(ctx context.Context) {
	backoff := time.Duration(0)
	for {
		now := r.cfg.Now()
		if !r.id.NeedsRenewal(now) {
			// 睡到续期时点。加一点抖动:同一批边缘节点往往同时部署、同时到期,
			// 不抖的话它们会在同一秒一起冲上级,把续期打成一次自制的雪崩。
			wait := r.id.Leaf().RenewAt().Sub(now)
			wait += time.Duration(rand.Int63n(int64(jitterOf(wait)) + 1))
			if err := r.cfg.Sleep(ctx, wait); err != nil {
				return
			}
			backoff = 0
			continue
		}

		if err := r.RenewOnce(ctx); err != nil {
			cap := r.backoffCap(now)
			if backoff == 0 {
				backoff = r.cfg.MinBackoff
			} else {
				backoff *= 2
			}
			if backoff > cap {
				backoff = cap
			}
			remaining := time.UnixMilli(r.id.Leaf().Exp).Sub(now)
			lvl := slog.LevelWarn
			if remaining <= 0 {
				lvl = slog.LevelError
			}
			r.cfg.Log.Log(ctx, lvl, "证书续期失败,稍后重试",
				"err", err, "retry_in", backoff, "证书剩余", remaining)
			if err := r.cfg.Sleep(ctx, backoff); err != nil {
				return
			}
			continue
		}
		backoff = 0
	}
}

// jitterOf 返回一段等待应当叠加的抖动幅度上限(等待时长的 1/16,至少 1s)。
func jitterOf(d time.Duration) time.Duration {
	j := d / 16
	if j < time.Second {
		j = time.Second
	}
	return j
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func mustSigner(c *Cert, keyHex string) *Signer {
	// 调用点已经验证过配对,这里不会失败。
	s, _ := NewSigner(c, keyHex)
	return s
}

// writeFileAtomic 先写同目录临时文件、fsync、再 rename。
//
// 直接覆盖写的话,中途崩溃会留下半张证书,下次启动自检直接失败——而那时
// 现场只剩一个"证书格式非法",看不出是被写坏的。同目录是必须的:跨文件系统
// rename 不是原子操作。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // rename 成功后这里是 no-op

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
