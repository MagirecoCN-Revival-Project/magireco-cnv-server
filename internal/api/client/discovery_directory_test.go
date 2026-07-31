package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"magirecocn-revival/api-server/internal/directory"
	"magirecocn-revival/api-server/internal/totentanz"
)

// 能力隔离必须在**签发时**挡住违规目录：客户端只验签名，不会质疑能力分配是否合理，
// 一份把 save 发给边缘节点的目录只要签名有效，客户端就会把云存档凭证发过去。
func TestPorted_DirectoryValidateBlocksEdgeCredentialCap(t *testing.T) {
	d := &directory.Directory{
		Seq: 1, IssuedAt: 1, ExpiresAt: 1 << 40,
		Nodes: []directory.Node{
			{ID: "edge-1", Role: directory.RoleEdge, API: "https://e", Caps: []string{"resource", "save"}},
		},
	}
	if err := d.Validate(); err == nil {
		t.Fatal("边缘节点持有 save 能力却通过了校验——能力隔离没生效")
	} else if !strings.Contains(err.Error(), "凭证类能力") {
		t.Fatalf("错误信息没点明凭证类能力: %v", err)
	}
	d.Nodes[0].Caps = []string{"resource"}
	if err := d.Validate(); err != nil {
		t.Fatalf("合法目录被误拒: %v", err)
	}
}

// 发现结果必须真的出现在 services 里。这条测试是有来历的：toResponseMap 的空判
// 曾经卡在 game_server_host 之后，只配端点发现时整个 services 被吞掉且无任何报错。
func TestPorted_DiscoveryFeedsServices(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"snaa","status":200,
			"response":{"endpoint":"https://ttz.example/en","max_threads":9,"version":128}}`))
	}))
	defer up.Close()

	c := totentanz.New(up.URL, 128, nil)
	if !c.Enabled() {
		t.Fatal("配置了 URL 却报未启用")
	}
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("拉取失败: %v", err)
	}
	cfg := servicesCfg{}
	if ep := c.Get(); ep != nil {
		cfg.ResourceBase = ep.Base
		cfg.GameMaxThreads = ep.MaxThreads
	}
	out := cfg.toResponseMap()
	b, _ := json.Marshal(out)
	if out["resource_base"] != "https://ttz.example/en" {
		t.Fatalf("resource_base 没进 services: %s", b)
	}
	if out["game_max_threads"] != 9 {
		t.Fatalf("game_max_threads 没进 services: %s", b)
	}
	// 严格区分：发现结果绝不能污染 API 语义的字段
	if _, has := out["game_server_host"]; has {
		t.Fatalf("发现结果污染了 game_server_host: %s", b)
	}
}
