// Package capworker 实现 cap-worker 兼容的 PoW 验证码。
//
// 协议:
//   POST /api/challenge → { token, challenge:{c,s,d}, expires }
//   PoW: 对每个 i ∈ [0,c) 求最小 nonce 使
//        SHA-256(token + "." + i + "." + nonce) 前 d 个二进制位为 0
//   POST /api/redeem    → { token, solutions:[...] } → { success, token, expires }
package capworker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/bits"
	"strconv"
	"time"

	"magirecocn-revival/api-server/internal/auth"
	"magirecocn-revival/api-server/internal/store"
)

// Config 验证码挑战参数。
type Config struct {
	C               int           // 子挑战数
	D               int           // 难度(前 d 位为 0)
	ChallengeTTL    time.Duration // 挑战有效期
	TokenTTL        time.Duration // 兑换后 cap_token 有效期
}

// DefaultConfig 默认参数(c=3, d=12 约 2 万次哈希)。
func DefaultConfig() Config {
	return Config{C: 3, D: 12, ChallengeTTL: 2 * time.Minute, TokenTTL: 5 * time.Minute}
}

// Service 封装 store 与配置。
type Service struct {
	st  *store.Store
	cfg Config
}

func New(st *store.Store, cfg Config) *Service {
	return &Service{st: st, cfg: cfg}
}

// Challenge 是 /api/challenge 的响应体。
type Challenge struct {
	Token     string `json:"token"`
	Challenge struct {
		C int    `json:"c"`
		S string `json:"s"`
		D int    `json:"d"`
	} `json:"challenge"`
	Expires int64 `json:"expires"`
}

// Issue 生成新挑战并落库。
func (s *Service) Issue(ctx context.Context) (*Challenge, error) {
	token, err := auth.NewToken()
	if err != nil {
		return nil, err
	}
	saltBytes := make([]byte, 8)
	_, _ = rand.Read(saltBytes)
	saltHex := hex.EncodeToString(saltBytes)
	exp := time.Now().Add(s.cfg.ChallengeTTL).UnixMilli()
	if err := s.st.CapChallengeInsert(ctx, store.CapChallenge{
		Token:     token,
		C:         s.cfg.C,
		D:         s.cfg.D,
		S:         saltHex,
		ExpiresAt: exp,
	}); err != nil {
		return nil, err
	}
	out := &Challenge{Token: token, Expires: exp}
	out.Challenge.C = s.cfg.C
	out.Challenge.D = s.cfg.D
	out.Challenge.S = saltHex
	return out, nil
}

// RedeemResult /api/redeem 的响应体。
type RedeemResult struct {
	Success bool   `json:"success"`
	Token   string `json:"token,omitempty"`
	Expires int64  `json:"expires,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Redeem 校验 PoW solutions,通过后签发一次性 cap_token。
func (s *Service) Redeem(ctx context.Context, token string, solutions []int64) *RedeemResult {
	ch, err := s.st.CapChallengeGet(ctx, token)
	if err != nil {
		return &RedeemResult{Success: false, Error: "challenge not found"}
	}
	if ch.Solved {
		return &RedeemResult{Success: false, Error: "challenge already used"}
	}
	if ch.ExpiresAt < time.Now().UnixMilli() {
		return &RedeemResult{Success: false, Error: "challenge expired"}
	}
	if len(solutions) != ch.C {
		return &RedeemResult{Success: false, Error: "solution count mismatch"}
	}
	for i := 0; i < ch.C; i++ {
		h := sha256.Sum256([]byte(ch.Token + "." + strconv.Itoa(i) + "." + strconv.FormatInt(solutions[i], 10)))
		if leadingZeroBits(h[:]) < ch.D {
			return &RedeemResult{Success: false, Error: "invalid solution at index " + strconv.Itoa(i)}
		}
	}
	if err := s.st.CapChallengeMarkSolved(ctx, token); err != nil {
		return &RedeemResult{Success: false, Error: "internal error"}
	}
	capToken, _ := auth.NewToken()
	exp := time.Now().Add(s.cfg.TokenTTL).UnixMilli()
	if err := s.st.CapTokenInsert(ctx, capToken, exp); err != nil {
		return &RedeemResult{Success: false, Error: "internal error"}
	}
	return &RedeemResult{Success: true, Token: capToken, Expires: exp}
}

// Consume 一次性消费 cap_token。captcha 关闭时直接放行。
func (s *Service) Consume(ctx context.Context, token string, enabled bool) (bool, error) {
	if !enabled {
		return true, nil
	}
	if token == "" {
		return false, nil
	}
	return s.st.CapTokenConsume(ctx, token)
}

func leadingZeroBits(b []byte) int {
	n := 0
	for _, c := range b {
		if c == 0 {
			n += 8
			continue
		}
		n += bits.LeadingZeros8(c)
		break
	}
	return n
}

// ErrChallengeNotFound 暴露给上层,但 Redeem 会用 result.error 字符串包装。
var ErrChallengeNotFound = errors.New("challenge not found")
