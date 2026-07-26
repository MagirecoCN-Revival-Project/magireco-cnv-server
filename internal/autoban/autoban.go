// Package autoban 实现服务端内部的**自动封禁**:从多路滥用信号累积判定,命中阈值
// 即写入设备封禁(IssuedBy=system、Auto=true)。被封设备此后在所有 authTriple 端点
// (/client/heartbeat 等)按既有契约收到 action:ban —— 不新增任何 wire 字段。
//
// 覆盖的信号(与管理后台「设备封禁」页示例一致):
//
//	客户端篡改检测命中   —— signature 中途变更(劫持/换包)即时封;init 签名/渠道不过累计
//	心跳包高频伪造       —— 单设备心跳频率远超正常节律
//	异常资源请求频率     —— 单设备 /client/online-download 高频
//	未通过 cap-worker 校验 3 次 —— 登录连续验证码失败
//	多账号异常切换       —— 单设备短时登录过多不同账号(养号/共享)
//
// 阈值存 config 表(key "autoban"),管理员可在后台「设备封禁」页运行时调整;未配置时
// 用 DefaultConfig() 的保守默认。计数全部在内存滑动窗口里做,周期清理;封禁状态以库为准。
package autoban

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"magirecocn-revival/cnv-server/internal/store"
)

// ConfigKey 是 config 表里存自动封禁阈值的键名。
const ConfigKey = "autoban"

// 封禁理由(与 web 设备封禁页示例文案一致,便于前端按文案归类)。
const (
	ReasonTamper       = "客户端篡改检测命中"
	ReasonHeartbeat    = "心跳包高频伪造"
	ReasonResourceRate = "异常资源请求频率"
	ReasonCaptcha      = "未通过 cap-worker 校验 3 次"
	ReasonMultiAccount = "多账号异常切换"
)

// Rule 一条信号的判定规则:WindowSec 窗口内累计达到 Max 次即触发,封禁 TTLSec 秒(0=永久)。
type Rule struct {
	Max       int `json:"max"`
	WindowSec int `json:"window_sec"`
	TTLSec    int `json:"ttl_sec"`
}

func (r Rule) window() time.Duration { return time.Duration(r.WindowSec) * time.Second }
func (r Rule) ttl() time.Duration    { return time.Duration(r.TTLSec) * time.Second }

// Config 各信号阈值。JSON 可序列化,既存 config 表也走 /admin/autoban。
type Config struct {
	Enabled      bool `json:"enabled"`
	Tamper       Rule `json:"tamper"`
	Heartbeat    Rule `json:"heartbeat"`
	Resource     Rule `json:"resource"`
	Captcha      Rule `json:"captcha"`
	MultiAccount Rule `json:"multi_account"`
}

// DefaultConfig 返回保守默认阈值(偏向避免误封正常用户)。
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		// 篡改:累计 3 次命中(severe 信号即时);永久封。
		Tamper: Rule{Max: 3, WindowSec: 1800, TTLSec: 0},
		// 心跳高频:60s 内 ≥ 90 次(>1.5/s,远超正常 2~5s 一跳);封 24h。
		Heartbeat: Rule{Max: 90, WindowSec: 60, TTLSec: 86400},
		// 资源请求高频:5min 内 ≥ 30 次 online-download;封 24h。
		Resource: Rule{Max: 30, WindowSec: 300, TTLSec: 86400},
		// 验证码连败:15min 内 3 次;封 1h(软封,留人工申诉空间)。
		Captcha: Rule{Max: 3, WindowSec: 900, TTLSec: 3600},
		// 多账号:30min 内 ≥ 5 个不同账号;封 24h。
		MultiAccount: Rule{Max: 5, WindowSec: 1800, TTLSec: 86400},
	}
}

// rules 便于按名遍历做校验/补默认。
func (c *Config) rules() []*Rule {
	return []*Rule{&c.Tamper, &c.Heartbeat, &c.Resource, &c.Captcha, &c.MultiAccount}
}

// normalize 用 def 补齐非法/缺失字段(Max<1 或 WindowSec<1 视为未设),TTL 允许 0(永久)。
func (c *Config) normalize(def Config) {
	cur, d := c.rules(), def.rules()
	for i := range cur {
		if cur[i].Max < 1 {
			cur[i].Max = d[i].Max
		}
		if cur[i].WindowSec < 1 {
			cur[i].WindowSec = d[i].WindowSec
		}
		if cur[i].TTLSec < 0 {
			cur[i].TTLSec = d[i].TTLSec
		}
	}
}

// Validate 校验管理员提交的阈值是否在合理范围内。
func (c Config) Validate() error {
	for _, r := range c.rules() {
		if r.Max < 1 || r.Max > 100000 {
			return errors.New("触发次数需在 1–100000 之间")
		}
		if r.WindowSec < 1 || r.WindowSec > 30*86400 {
			return errors.New("统计窗口需在 1 秒–30 天之间")
		}
		if r.TTLSec < 0 || r.TTLSec > 3650*86400 {
			return errors.New("封禁时长需在 0(永久)–10 年之间")
		}
	}
	return nil
}

// Load 从 config 表读取阈值;未配置或非法字段回落 DefaultConfig()。
func Load(ctx context.Context, st *store.Store) Config {
	def := DefaultConfig()
	if st == nil {
		return def
	}
	c := def
	if ok, _ := st.ConfigGet(ctx, ConfigKey, &c); !ok {
		return def
	}
	c.normalize(def)
	return c
}

// Service 是自动封禁判定器。并发安全;St 为 nil 或当前配置 Enabled=false 时所有 On* 为 no-op。
type Service struct {
	st  *store.Store
	log *slog.Logger

	mu       sync.Mutex
	events   map[string][]int64          // "device|signal" -> 命中时间戳(滑动窗口)
	accounts map[string]map[string]int64 // device -> accountID -> 最近登录时间(多账号去重)
	banned   map[string]int64            // device -> 触发自动封禁的时间(内存去重)

	// 配置缓存:避免每个请求都打一次库;5s 刷新。
	cmu      sync.Mutex
	cached   Config
	cachedAt time.Time
}

// New 构造 Service。阈值在运行时从 config 表读取(带缓存)。
func New(st *store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		st: st, log: log,
		events:   map[string][]int64{},
		accounts: map[string]map[string]int64{},
		banned:   map[string]int64{},
	}
}

// Start 起一个后台 goroutine 周期清理过期的内存计数,随 ctx 结束退出。
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.sweep()
			}
		}
	}()
}

// cfg 返回当前阈值配置(5s 缓存)。
func (s *Service) cfg(ctx context.Context) Config {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < 5*time.Second {
		return s.cached
	}
	s.cached = Load(ctx, s.st)
	s.cachedAt = time.Now()
	return s.cached
}

// ── 信号入口 ────────────────────────────────────────────────────────────────

// OnIntegrityViolation 改包/签名/渠道异常。severe=true(signature 中途变更等劫持信号)即时封。
func (s *Service) OnIntegrityViolation(ctx context.Context, deviceID string, severe bool) {
	if s == nil || s.st == nil || deviceID == "" {
		return
	}
	c := s.cfg(ctx)
	if !c.Enabled {
		return
	}
	if severe {
		s.fire(ctx, deviceID, ReasonTamper, c.Tamper.ttl())
		return
	}
	if s.bump(deviceID, "tamper", c.Tamper.window()) >= c.Tamper.Max {
		s.fire(ctx, deviceID, ReasonTamper, c.Tamper.ttl())
	}
}

// OnHeartbeat 每次心跳调用;窗口内频率过高判为伪造/刷包。
func (s *Service) OnHeartbeat(ctx context.Context, deviceID string) {
	if s == nil || s.st == nil || deviceID == "" {
		return
	}
	c := s.cfg(ctx)
	if !c.Enabled {
		return
	}
	if s.bump(deviceID, "hb", c.Heartbeat.window()) >= c.Heartbeat.Max {
		s.fire(ctx, deviceID, ReasonHeartbeat, c.Heartbeat.ttl())
	}
}

// OnResourceRequest 每次 /client/online-download 调用;窗口内过高判为异常拉取。
func (s *Service) OnResourceRequest(ctx context.Context, deviceID string) {
	if s == nil || s.st == nil || deviceID == "" {
		return
	}
	c := s.cfg(ctx)
	if !c.Enabled {
		return
	}
	if s.bump(deviceID, "res", c.Resource.window()) >= c.Resource.Max {
		s.fire(ctx, deviceID, ReasonResourceRate, c.Resource.ttl())
	}
}

// OnCaptchaFail 登录验证码失败一次。
func (s *Service) OnCaptchaFail(ctx context.Context, deviceID string) {
	if s == nil || s.st == nil || deviceID == "" {
		return
	}
	c := s.cfg(ctx)
	if !c.Enabled {
		return
	}
	if s.bump(deviceID, "cap", c.Captcha.window()) >= c.Captcha.Max {
		s.fire(ctx, deviceID, ReasonCaptcha, c.Captcha.ttl())
	}
}

// OnAccountLogin 登录成功一次;统计窗口内单设备登录过的不同账号数。
func (s *Service) OnAccountLogin(ctx context.Context, deviceID, accountID string) {
	if s == nil || s.st == nil || deviceID == "" || accountID == "" {
		return
	}
	c := s.cfg(ctx)
	if !c.Enabled {
		return
	}
	now := time.Now().UnixMilli()
	cutoff := now - c.MultiAccount.window().Milliseconds()
	s.mu.Lock()
	m := s.accounts[deviceID]
	if m == nil {
		m = map[string]int64{}
		s.accounts[deviceID] = m
	}
	m[accountID] = now
	distinct := 0
	for id, ts := range m {
		if ts < cutoff {
			delete(m, id)
			continue
		}
		distinct++
	}
	s.mu.Unlock()
	if distinct >= c.MultiAccount.Max {
		s.fire(ctx, deviceID, ReasonMultiAccount, c.MultiAccount.ttl())
	}
}

// ── 内部 ────────────────────────────────────────────────────────────────────

// bump 记一次命中并返回窗口内累计次数(同时惰性裁掉过期项)。
func (s *Service) bump(deviceID, signal string, window time.Duration) int {
	now := time.Now().UnixMilli()
	cutoff := now - window.Milliseconds()
	key := deviceID + "|" + signal
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := append(s.events[key], now)
	keep := ts[:0]
	for _, t := range ts {
		if t >= cutoff {
			keep = append(keep, t)
		}
	}
	s.events[key] = keep
	return len(keep)
}

// fire 落一条自动封禁(去重):内存近期已触发或库里已有活跃封禁则跳过。
func (s *Service) fire(ctx context.Context, deviceID, reason string, ttl time.Duration) {
	s.mu.Lock()
	if _, ok := s.banned[deviceID]; ok {
		s.mu.Unlock()
		return
	}
	s.banned[deviceID] = time.Now().UnixMilli()
	s.mu.Unlock()

	if ban, _ := s.st.BanActive(ctx, deviceID); ban != nil {
		return // 已被(手工或先前自动)封禁
	}

	now := time.Now().UnixMilli()
	var expire *int64
	if ttl > 0 {
		e := now + ttl.Milliseconds()
		expire = &e
	}
	if err := s.st.BanInsert(ctx, store.Ban{
		ID: "ban-" + randHex(6), DeviceID: deviceID, Reason: reason,
		IssuedAt: now, ExpireTime: expire, IssuedBy: "system", Auto: true,
	}); err != nil {
		s.log.Warn("自动封禁写库失败", "device_id", deviceID, "reason", reason, "err", err)
		return
	}
	target := deviceID
	if len(target) > 16 {
		target = target[:16]
	}
	_ = s.st.AuditInsert(ctx, store.AuditEntry{
		ID: "log-" + randHex(12), Ts: now, Actor: "system", Type: "device.ban",
		Target:  &target,
		Details: jsonMust(map[string]any{"reason": reason, "source": "auto"}),
	})
	ttlStr := "永久"
	if ttl > 0 {
		ttlStr = ttl.String()
	}
	s.log.Warn("自动封禁触发", "device_id", deviceID, "reason", reason, "ttl", ttlStr)
}

// sweep 清掉空的/过期的内存计数,防止 map 无限增长。
func (s *Service) sweep() {
	now := time.Now().UnixMilli()
	// 内存去重标记保留 1 天即可(封禁状态以库为准)。
	const banMark = int64(24 * 60 * 60 * 1000)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, ts := range s.events {
		if len(ts) == 0 {
			delete(s.events, k)
		}
	}
	for dev, m := range s.accounts {
		if len(m) == 0 {
			delete(s.accounts, dev)
		}
	}
	for dev, at := range s.banned {
		if now-at > banMark {
			delete(s.banned, dev)
		}
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func jsonMust(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
