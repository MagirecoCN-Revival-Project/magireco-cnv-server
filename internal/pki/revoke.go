package pki

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// 证书吊销。
//
// # 为什么小时级有效期之外还需要它
//
// 边缘叶子只有几小时有效期,正常情况下"下线一台机器"靠自然过期就够了,这也是
// 我们不做吊销列表的原因。但**紧急情况下"最多几小时"不够快**:机器被入侵、
// 私钥疑似泄漏、运维误签,这些场景要的是"立刻"。
//
// # 为什么这里可以是推送而不是拉取
//
// 传统吊销列表的麻烦在于校验方要主动去拉,拉不到时 fail-open 还是 fail-closed
// 两难。这套架构里校验方(各节点)本来就挂在管控通道上,上级可以**主动推**——
// 延迟是一次 WS 往返,而且推不到的节点在管控通道上是可见的离线状态,
// 不存在"以为拉到了其实没拉到"的静默失败。
//
// # 为什么条目可以自清理
//
// 吊销只需要撑到证书本来的过期时刻:那之后证书自己就失效了,再留着条目没有意义。
// 所以每条记 (序列号, 原过期时刻),过了就删。这让吊销集的大小由**在有效期内被
// 吊销的证书数**决定,而不是随时间无界增长——传统 CRL 最麻烦的运维问题在这里
// 因为有效期短而自然消失了。

// Revocation 是一条吊销记录。
type Revocation struct {
	// Serial 被吊销证书的序列号。
	Serial string `json:"serial"`
	// Subject 便于人读的主体标识,不参与匹配。
	Subject string `json:"subject,omitempty"`
	// ExpiresAt 被吊销证书**原本**的过期时刻(Unix 毫秒)。
	// 过了这个点条目自动清理:证书那时已经自己失效了。
	ExpiresAt int64 `json:"expires_at"`
	// Reason 人工填写的原因,只用于审计与日志。
	Reason string `json:"reason,omitempty"`
	// At 吊销时刻,Unix 毫秒。
	At int64 `json:"at"`
}

// Revocations 是一份线程安全的吊销集,可直接挂给 Verifier.Revoked。
type Revocations struct {
	// Now 可注入的时钟。存在的理由不只是测试:Verifier.Revoked 的签名不带时间,
	// 若这里直接读墙钟,同一次链校验里就会出现**两个时间源**——链的有效期按调用方
	// 传入的 now 判定,吊销却按墙钟判定。生产环境下两者通常一致,所以这种不一致
	// 平时看不出来,但它是错的,也让"按某个时刻回放校验"变得不可能。
	Now func() time.Time

	mu sync.RWMutex
	m  map[string]Revocation
}

// NewRevocations 构造空的吊销集。
func NewRevocations() *Revocations {
	return &Revocations{m: map[string]Revocation{}, Now: time.Now}
}

func (r *Revocations) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Add 加入一条吊销。已过原过期时刻的条目直接忽略——加了也没有任何效果,
// 留着只会让集合变大、让人误以为它还在起作用。
func (r *Revocations) Add(rev Revocation, now time.Time) error {
	if rev.Serial == "" {
		return errors.New("pki: 吊销记录缺少序列号")
	}
	if rev.ExpiresAt <= 0 {
		return errors.New("pki: 吊销记录缺少原过期时刻")
	}
	if rev.At == 0 {
		rev.At = now.UnixMilli()
	}
	if now.UnixMilli() > rev.ExpiresAt {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[rev.Serial] = rev
	return nil
}

// IsRevoked 判断一张证书是否已被吊销。签名与 Verifier.Revoked 一致。
//
// 顺手做过期清理:吊销集只在被查时才需要正确,不必额外起一个清理协程。
func (r *Revocations) IsRevoked(c *Cert) bool {
	if c == nil {
		return false
	}
	nowMs := r.now().UnixMilli()
	r.mu.RLock()
	rev, ok := r.m[c.Serial]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	if nowMs > rev.ExpiresAt {
		// 原证书已自然过期,条目没有意义了,顺手删掉。
		r.mu.Lock()
		if cur, still := r.m[c.Serial]; still && cur.ExpiresAt == rev.ExpiresAt {
			delete(r.m, c.Serial)
		}
		r.mu.Unlock()
		return false
	}
	return true
}

// Hook 返回可直接赋给 Verifier.Revoked 的回调。
func (r *Revocations) Hook() func(*Cert) bool { return r.IsRevoked }

// List 返回当前仍然生效的吊销记录,按吊销时刻倒序。
func (r *Revocations) List(now time.Time) []Revocation {
	nowMs := now.UnixMilli()
	r.mu.RLock()
	out := make([]Revocation, 0, len(r.m))
	for _, rev := range r.m {
		if nowMs <= rev.ExpiresAt {
			out = append(out, rev)
		}
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// Prune 主动清掉已经没有意义的条目,返回清掉的数量。
func (r *Revocations) Prune(now time.Time) int {
	nowMs := now.UnixMilli()
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, rev := range r.m {
		if nowMs > rev.ExpiresAt {
			delete(r.m, k)
			n++
		}
	}
	return n
}

// Len 返回当前条目数(含已过期但尚未清理的)。
func (r *Revocations) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.m)
}
