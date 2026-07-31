// Package totentanz 消费上游 Totentanz 的「端点发现」接口。
//
// # 这是什么
//
// Totentanz 客户端并不把游戏后端地址写死，而是启动时先向一个固定的引导端点
// POST 一个版本号，换回真正的后端地址：
//
//	POST https://<引导端点>/magica/api/snaa
//	Content-Type: application/json; charset=utf-8
//	{"version": 128}
//
//	→ {"message":"snaa","status":200,
//	   "response":{"endpoint":"https://xxx.example/en","max_threads":19,"version":128}}
//
// 原客户端由 libuwasa + io.kamihama.magianative.RestClient 完成这件事。本项目
// 已移除那一层（与自有的代理/节点目录方案冲突），改由服务端代劳——服务端拉一次，
// 通过 /client/init 的 services 下发给所有客户端。
//
// # 为什么值得服务端做
//
// 上游换后端地址时，如果每个客户端各自去发现，我们既看不见也管不着；由服务端
// 统一消费，则地址变更、版本闸门都能在我们这一侧观测和干预，客户端一行代码都
// 不用改。response.version 还是上游给出的**兼容性信号**——它涨了就意味着上游
// 客户端该更新了，我们可以据此提前拦住玩家，而不是等他们进游戏崩掉。
//
// # 可用性原则
//
// 上游不是我们能控制的：它可能超时、返回垃圾、或者干脆下线。因此
//   - 拉取只在后台进行，**绝不**放在 /client/init 的请求路径上（否则上游一慢，
//     我们的握手跟着一起慢，上游一挂我们跟着挂）；
//   - 拉取失败时**保留上一次成功的结果**（stale-while-error），不清空、不报错；
//   - 从未成功过时返回 nil，调用方回退到本地配置值。
package totentanz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Endpoint 是一次成功发现的结果。
type Endpoint struct {
	// Base 是上游给出的完整 base URL，**含路径**（如 https://xxx.example/en）。
	Base string
	// Host 是从 Base 剥出的纯 host，用于兼容只认 host 的老客户端。
	Host string
	// MaxThreads 上游建议的 HTTP/2 并发连接数；实测该值会动态变化。
	MaxThreads int
	// Version 上游当前的客户端版本号，用作兼容性信号。
	Version int
	// FetchedAt 本次结果的获取时间。
	FetchedAt time.Time
}

// wire 是发现接口的响应信封。
type wire struct {
	Message  string `json:"message"`
	Status   int    `json:"status"`
	Response struct {
		Endpoint   string `json:"endpoint"`
		MaxThreads int    `json:"max_threads"`
		Version    int    `json:"version"`
	} `json:"response"`
}

// Client 负责周期性拉取并缓存发现结果。零值不可用，请用 New。
type Client struct {
	url     string // 引导端点完整 URL
	version int    // 请求体里上报的版本号
	http    *http.Client
	log     *slog.Logger

	mu      sync.RWMutex
	cur     *Endpoint // 最近一次**成功**的结果；nil = 从未成功
	lastErr error
}

// New 构造一个发现客户端。discoveryURL 为空表示不启用（Get 恒返回 nil）。
func New(discoveryURL string, clientVersion int, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		url:     strings.TrimSpace(discoveryURL),
		version: clientVersion,
		log:     log,
		// 上游不可控，超时必须短：这条链路只是锦上添花，绝不能拖住我们自己。
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled 是否配置了引导端点。
func (c *Client) Enabled() bool { return c != nil && c.url != "" }

// Get 返回最近一次成功的发现结果；从未成功过返回 nil。
// 只读缓存，不会触发网络请求——调用方可以放心在请求路径上调用。
func (c *Client) Get() *Endpoint {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cur
}

// LastError 返回最近一次拉取的错误（成功时为 nil），供状态页/诊断使用。
func (c *Client) LastError() error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// Refresh 主动拉取一次并更新缓存。失败时**保留**上一次成功的结果。
func (c *Client) Refresh(ctx context.Context) error {
	if !c.Enabled() {
		return errors.New("未配置 Totentanz 引导端点")
	}
	ep, err := c.fetch(ctx)

	c.mu.Lock()
	c.lastErr = err
	if err == nil {
		prev := c.cur
		c.cur = ep
		c.mu.Unlock()
		// 地址变更是需要被看见的事件：上游换后端时这行日志就是唯一的预警。
		if prev == nil {
			c.log.Info("Totentanz 端点发现成功",
				"base", ep.Base, "max_threads", ep.MaxThreads, "version", ep.Version)
		} else if prev.Base != ep.Base || prev.Version != ep.Version {
			c.log.Warn("Totentanz 上游端点/版本已变更",
				"base_old", prev.Base, "base_new", ep.Base,
				"version_old", prev.Version, "version_new", ep.Version)
		}
		return nil
	}
	stale := c.cur != nil
	c.mu.Unlock()

	// 上游挂掉不是我们的故障：有旧值就继续用，只告警。
	if stale {
		c.log.Warn("Totentanz 端点发现失败，沿用上次成功的结果", "err", err)
	} else {
		c.log.Warn("Totentanz 端点发现失败，且尚无可用的历史结果", "err", err)
	}
	return err
}

// Run 后台周期刷新，直到 ctx 取消。启动时先立即拉一次。
func (c *Client) Run(ctx context.Context, interval time.Duration) {
	if !c.Enabled() {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	_ = c.Refresh(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = c.Refresh(ctx)
		}
	}
}

func (c *Client) fetch(ctx context.Context) (*Endpoint, error) {
	body, err := json.Marshal(map[string]int{"version": c.version})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("引导端点返回 HTTP %d", resp.StatusCode)
	}
	// 上游返回什么长度不受我们控制,限流读取避免被超大响应拖垮内存。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("响应不是合法 JSON: %w", err)
	}
	if w.Status != http.StatusOK {
		return nil, fmt.Errorf("响应信封 status=%d message=%q", w.Status, w.Message)
	}
	base := strings.TrimRight(strings.TrimSpace(w.Response.Endpoint), "/")
	if base == "" {
		return nil, errors.New("响应中没有 endpoint")
	}
	host := hostOf(base)
	if host == "" {
		return nil, fmt.Errorf("endpoint 不是合法 URL: %q", base)
	}
	return &Endpoint{
		Base:       base,
		Host:       host,
		MaxThreads: w.Response.MaxThreads,
		Version:    w.Response.Version,
		FetchedAt:  time.Now(),
	}, nil
}

// hostOf 从完整 URL 里剥出纯 host（无协议/端口以外的路径）。非法输入返回空串。
func hostOf(u string) string {
	s := u
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	} else {
		return "" // 必须带协议，否则无从判断这是 host 还是路径
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 { // 去掉 userinfo
		s = s[i+1:]
	}
	if s == "" || strings.ContainsAny(s, " \t") {
		return ""
	}
	return s
}
