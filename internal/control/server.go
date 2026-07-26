package control

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/coder/websocket"

	"magirecocn-revival/cnv-server/internal/auth"
)

// CommandFunc 处理一条指令，返回结果（将被 JSON 编码进 resp.payload）或错误。
type CommandFunc func(ctx context.Context, payload json.RawMessage) (any, error)

// Server 是节点侧的管控 WS 服务。面板连上来后用密钥鉴权，随后承载遥测/指令/流。
type Server struct {
	Key      string                 // 期望的连接密钥（节点自持）
	Node     NodeInfo               // auth_ok 时上报给面板
	Commands map[string]CommandFunc // action -> 处理器
	// Telemetry 若非 nil：连接建立后立即推一次，之后每 TelemetryInterval 推一次。
	Telemetry         func() Telemetry
	TelemetryInterval time.Duration // 默认 5s
	AuthTimeout       time.Duration // 等待 auth 首帧的超时，默认 10s
	Log               *slog.Logger
}

func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Handler 升级 HTTP 到 WebSocket 并跑完整个会话。挂到节点的 /control/ws。
func (s *Server) Handler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 鉴权边界是连接密钥而非 Origin（面板不是浏览器，通常无 Origin 头）。
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 1) 鉴权：限时读首帧
	if !s.authenticate(ctx, c) {
		return
	}

	// 2) 单写协程 + 出站队列（WS 不允许并发写，统一在此序列化）
	out := make(chan []byte, 64)
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case b := <-out:
				wctx, wc := context.WithTimeout(ctx, 10*time.Second)
				err := c.Write(wctx, websocket.MessageText, b)
				wc()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	send := func(env Envelope) {
		b, err := json.Marshal(env)
		if err != nil {
			return
		}
		select {
		case out <- b:
		case <-ctx.Done():
		}
	}

	// 3) 遥测推送
	if s.Telemetry != nil {
		send(s.telemetryEnvelope())
		iv := s.TelemetryInterval
		if iv <= 0 {
			iv = 5 * time.Second
		}
		go func() {
			t := time.NewTicker(iv)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					send(s.telemetryEnvelope())
				}
			}
		}()
	}

	// 4) 读循环：处理 ping / cmd / subscribe
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		switch env.Type {
		case TypePing:
			send(Envelope{Type: TypePong, ID: env.ID})
		case TypeCmd:
			go s.dispatch(ctx, env, send)
		case TypeSubscribe:
			// 流通道订阅占位：后续由具体 stream 源（如日志）接管。
		}
	}
	cancel()
	writerWG.Wait()
}

func (s *Server) authenticate(ctx context.Context, c *websocket.Conn) bool {
	to := s.AuthTimeout
	if to <= 0 {
		to = 10 * time.Second
	}
	actx, ac := context.WithTimeout(ctx, to)
	defer ac()
	_, data, err := c.Read(actx)
	if err != nil {
		return false
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type != TypeAuth {
		_ = writeJSON(ctx, c, Envelope{Type: TypeAuthFail, Error: "expected auth frame"})
		return false
	}
	var ap authPayload
	_ = json.Unmarshal(env.Payload, &ap)
	if s.Key == "" || !auth.SafeStrEq(ap.Key, s.Key) {
		_ = writeJSON(ctx, c, Envelope{Type: TypeAuthFail, Error: "unauthorized"})
		s.logger().Warn("管控连接鉴权失败")
		return false
	}
	infoB, _ := json.Marshal(s.Node)
	return writeJSON(ctx, c, Envelope{Type: TypeAuthOK, Payload: infoB}) == nil
}

func (s *Server) dispatch(ctx context.Context, env Envelope, send func(Envelope)) {
	fn, ok := s.Commands[env.Action]
	if !ok {
		send(Envelope{Type: TypeResp, ID: env.ID, OK: boolPtr(false), Error: "unknown action: " + env.Action})
		return
	}
	res, err := fn(ctx, env.Payload)
	if err != nil {
		send(Envelope{Type: TypeResp, ID: env.ID, OK: boolPtr(false), Error: err.Error()})
		return
	}
	var pb json.RawMessage
	if res != nil {
		b, mErr := json.Marshal(res)
		if mErr != nil {
			send(Envelope{Type: TypeResp, ID: env.ID, OK: boolPtr(false), Error: "marshal result: " + mErr.Error()})
			return
		}
		pb = b
	}
	send(Envelope{Type: TypeResp, ID: env.ID, OK: boolPtr(true), Payload: pb})
}

func (s *Server) telemetryEnvelope() Envelope {
	t := s.Telemetry()
	if t.Goroutines == 0 {
		t.Goroutines = runtime.NumGoroutine()
	}
	if t.NodeRole == "" {
		t.NodeRole = s.Node.Role
	}
	b, _ := json.Marshal(t)
	return Envelope{Type: TypeTelemetry, Payload: b}
}

// writeJSON 一次性写一帧。仅在握手阶段（单写协程尚未启动前）使用。
func writeJSON(ctx context.Context, c *websocket.Conn, env Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	wctx, wc := context.WithTimeout(ctx, 10*time.Second)
	defer wc()
	return c.Write(wctx, websocket.MessageText, b)
}
