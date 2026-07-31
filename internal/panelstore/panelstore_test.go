package panelstore_test

import (
	"context"
	"testing"
	"time"

	"magirecocn-revival/api-server/internal/panelstore"
)

func openTest(t *testing.T) *panelstore.Store {
	t.Helper()
	ps, err := panelstore.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { ps.Close() })
	return ps
}

func TestPanelstore_NodeCRUD(t *testing.T) {
	ps := openTest(t)
	ctx := context.Background()

	n := panelstore.Node{
		ID:      "node_test01",
		Label:   "本地测试节点",
		Role:    "business",
		APIURL:  "http://127.0.0.1:8080",
		CtrlURL: "ws://127.0.0.1:9090/ctrl",
		Key:     "abc123def456abc123def456abc123de",
		Region:  "local",
	}
	if err := ps.NodeInsert(ctx, n); err != nil {
		t.Fatalf("NodeInsert: %v", err)
	}

	nodes, err := ps.NodeList(ctx)
	if err != nil {
		t.Fatalf("NodeList: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != n.ID {
		t.Fatalf("期望 1 个节点，得到 %+v", nodes)
	}

	n.Label = "已更新"
	if err := ps.NodeUpdate(ctx, n); err != nil {
		t.Fatalf("NodeUpdate: %v", err)
	}
	got, err := ps.NodeByID(ctx, n.ID)
	if err != nil {
		t.Fatalf("NodeByID: %v", err)
	}
	if got.Label != "已更新" {
		t.Fatalf("标签未更新: %q", got.Label)
	}

	if err := ps.NodeDelete(ctx, n.ID); err != nil {
		t.Fatalf("NodeDelete: %v", err)
	}
	if _, err := ps.NodeByID(ctx, n.ID); err != panelstore.ErrNotFound {
		t.Fatalf("删除后应返回 ErrNotFound，得到 %v", err)
	}
}

func TestPanelstore_AdminSession(t *testing.T) {
	ps := openTest(t)
	ctx := context.Background()

	a := panelstore.Admin{
		ID:           "padm_test01",
		Username:     "testadmin",
		Email:        "test@example.com",
		PasswordHash: "scrypt$32768$8$1$deadbeef$deadbeef",
		Role:         "super_admin",
		CreatedAt:    time.Now().UnixMilli(),
	}
	if err := ps.AdminInsert(ctx, a); err != nil {
		t.Fatalf("AdminInsert: %v", err)
	}

	count, err := ps.AdminCountSuper(ctx)
	if err != nil || count != 1 {
		t.Fatalf("AdminCountSuper: count=%d err=%v", count, err)
	}

	got, err := ps.AdminByEmail(ctx, a.Email)
	if err != nil || got.ID != a.ID {
		t.Fatalf("AdminByEmail: %v %+v", err, got)
	}

	sess := panelstore.Session{
		Token:     "tok_test_abc",
		AdminID:   a.ID,
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := ps.SessionInsert(ctx, sess); err != nil {
		t.Fatalf("SessionInsert: %v", err)
	}

	s, err := ps.SessionByToken(ctx, sess.Token)
	if err != nil || s.AdminID != a.ID {
		t.Fatalf("SessionByToken: %v %+v", err, s)
	}

	if err := ps.SessionDeleteByAdmin(ctx, a.ID); err != nil {
		t.Fatalf("SessionDeleteByAdmin: %v", err)
	}
	if _, err := ps.SessionByToken(ctx, sess.Token); err != panelstore.ErrNotFound {
		t.Fatalf("会话删除后应返回 ErrNotFound，得到 %v", err)
	}
}
