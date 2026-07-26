package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Client 是面板侧到单个节点的管控连接。线程安全。
type Client struct {
	URL string // ws(s)://host/control/ws
	Key string

	OnTelemetry func(Telemetry)        // 收到遥测时回调
	OnStream    func(channel, line string) // 收到流式输出时回调
	Log         *slog.Logger

	mu      sync.Mutex
	conn    *websocket.Conn
	out     chan []byte
	pending map[string]chan Envelope
	node    NodeInfo
	cancel  context.CancelFunc
	closed  bool
}

// Connect 拨号、鉴权、起读写循环。成功后 Node() 可用。
func (c *Client) Connect(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.URL, &websocket.DialOptions{})
	if err != nil {
		return fmt.Errorf("拨号失败: %w", err)
	}
	conn.SetReadLimit(1 << 20)

	keyB, _ := json.Marshal(authPayload{Key: c.Key})
	if err := writeJSON(ctx, conn, Envelope{Type: TypeAuth, Payload: keyB}); err != nil {
		conn.CloseNow()
		return fmt.Errorf("发送 auth 失败: %w", err)
	}
	actx, ac := context.WithTimeout(ctx, 10*time.Second)
	_, data, err := conn.Read(actx)
	ac()
	if err != nil {
		conn.CloseNow()
		return fmt.Errorf("读取 auth 响应失败: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		conn.CloseNow()
		return fmt.Errorf("auth 响应解析失败: %w", err)
	}
	if env.Type != TypeAuthOK {
		conn.CloseNow()
		if env.Error == "" {
			env.Error = "鉴权被拒"
		}
		return errors.New(env.Error)
	}
	var node NodeInfo
	_ = json.Unmarshal(env.Payload, &node)

	cctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.conn = conn
	c.out = make(chan []byte, 64)
	c.pending = make(map[string]chan Envelope)
	c.cancel = cancel
	c.closed = false
	c.node = node
	c.mu.Unlock()

	go c.writeLoop(cctx, conn)
	go c.readLoop(cctx, conn)
	return nil
}

// Node 返回握手时节点上报的身份信息。
func (c *Client) Node() NodeInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.node
}

func (c *Client) writeLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c.out:
			wctx, wc := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, b)
			wc()
			if err != nil {
				c.cancelAll()
				return
			}
		}
	}
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) {
	defer c.cancelAll()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		switch env.Type {
		case TypeTelemetry:
			if c.OnTelemetry != nil {
				var t Telemetry
				if json.Unmarshal(env.Payload, &t) == nil {
					c.OnTelemetry(t)
				}
			}
		case TypeStream:
			if c.OnStream != nil {
				c.OnStream(env.Channel, env.Line)
			}
		case TypeResp:
			c.mu.Lock()
			ch := c.pending[env.ID]
			delete(c.pending, env.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- env
			}
		case TypePong:
			// 保活回执，忽略
		}
	}
}

// Call 下发一条指令并等待回执（受 ctx 超时/取消，或连接断开时返回错误）。
func (c *Client) Call(ctx context.Context, action string, payload any) (json.RawMessage, error) {
	var pb json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		pb = b
	}
	id := newID()
	ch := make(chan Envelope, 1)

	c.mu.Lock()
	if c.closed || c.conn == nil {
		c.mu.Unlock()
		return nil, errors.New("连接未建立")
	}
	c.pending[id] = ch
	outCh := c.out
	c.mu.Unlock()

	b, _ := json.Marshal(Envelope{Type: TypeCmd, ID: id, Action: action, Payload: pb})
	select {
	case outCh <- b:
	case <-ctx.Done():
		c.clearPending(id)
		return nil, ctx.Err()
	}

	select {
	case resp := <-ch:
		if resp.OK != nil && !*resp.OK {
			return nil, errors.New(resp.Error)
		}
		return resp.Payload, nil
	case <-ctx.Done():
		c.clearPending(id)
		return nil, ctx.Err()
	}
}

// Subscribe 请求订阅一个流通道（如 "log"）。
func (c *Client) Subscribe(ctx context.Context, channel string) error {
	c.mu.Lock()
	if c.closed || c.out == nil {
		c.mu.Unlock()
		return errors.New("连接未建立")
	}
	outCh := c.out
	c.mu.Unlock()
	b, _ := json.Marshal(Envelope{Type: TypeSubscribe, Channel: channel})
	select {
	case outCh <- b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 主动断开连接。
func (c *Client) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	c.cancelAll()
	if conn != nil {
		return conn.Close(websocket.StatusNormalClosure, "client closing")
	}
	return nil
}

func (c *Client) clearPending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// cancelAll 关闭连接上下文，并给所有在等回执的 Call 回一个"连接已断开"错误。
func (c *Client) cancelAll() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	pend := c.pending
	c.pending = map[string]chan Envelope{}
	c.mu.Unlock()
	for id, ch := range pend {
		select {
		case ch <- Envelope{Type: TypeResp, ID: id, OK: boolPtr(false), Error: "连接已断开"}:
		default:
		}
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
