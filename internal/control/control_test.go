package control_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/control"
)

func wsURL(ts *httptest.Server) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http")
}

// 端到端：鉴权握手 + 指令往返 + 遥测推送。
func TestControl_AuthCommandTelemetry(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	srv := &control.Server{
		Key:  key,
		Node: control.NodeInfo{ID: "n1", Role: "business", Version: "test"},
		Commands: map[string]control.CommandFunc{
			"echo": func(_ context.Context, payload json.RawMessage) (any, error) {
				var in map[string]string
				_ = json.Unmarshal(payload, &in)
				return map[string]string{"echo": in["msg"]}, nil
			},
		},
		Telemetry:         func() control.Telemetry { return control.Telemetry{UptimeSec: 7} },
		TelemetryInterval: 50 * time.Millisecond,
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.Handler))
	defer ts.Close()

	telc := make(chan control.Telemetry, 8)
	cli := &control.Client{
		URL: wsURL(ts), Key: key,
		OnTelemetry: func(tl control.Telemetry) {
			select {
			case telc <- tl:
			default:
			}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cli.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cli.Close()

	if n := cli.Node(); n.ID != "n1" || n.Role != "business" {
		t.Fatalf("意外的节点信息: %+v", n)
	}

	resp, err := cli.Call(ctx, "echo", map[string]string{"msg": "hi"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var out map[string]string
	_ = json.Unmarshal(resp, &out)
	if out["echo"] != "hi" {
		t.Fatalf("echo 期望 hi, 得到 %q", out["echo"])
	}

	if _, err := cli.Call(ctx, "nope", nil); err == nil {
		t.Fatal("未知指令应当返回错误")
	}

	select {
	case tl := <-telc:
		if tl.UptimeSec != 7 {
			t.Fatalf("遥测 uptime 期望 7, 得到 %d", tl.UptimeSec)
		}
		if tl.NodeRole != "business" {
			t.Fatalf("遥测 node_role 期望 business, 得到 %q", tl.NodeRole)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到遥测")
	}
}

// 错误密钥必须被拒。
func TestControl_AuthReject(t *testing.T) {
	srv := &control.Server{
		Key:  "correct-key-correct-key-correct!",
		Node: control.NodeInfo{ID: "n", Role: "edge"},
	}
	ts := httptest.NewServer(http.HandlerFunc(srv.Handler))
	defer ts.Close()

	cli := &control.Client{URL: wsURL(ts), Key: "wrong-key"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Connect(ctx); err == nil {
		cli.Close()
		t.Fatal("错误密钥应当被拒绝")
	}
}

func TestNodeKey_LoadOrCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "node.key")
	k1, created, err := control.LoadOrCreateKey(path)
	if err != nil || !created || len(k1) < 32 {
		t.Fatalf("首次创建失败: k=%q created=%v err=%v", k1, created, err)
	}
	if fi, _ := os.Stat(path); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限应为 0600")
	}
	k2, created2, err := control.LoadOrCreateKey(path)
	if err != nil || created2 || k2 != k1 {
		t.Fatalf("二次读取应返回同一密钥: k2=%q created=%v err=%v", k2, created2, err)
	}
}
