package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"magirecocn-revival/api-server/internal/control"
	"magirecocn-revival/api-server/internal/panelstore"
)

// nodeState 缓存单个节点的最新遥测与连接状态。
type nodeState struct {
	info   panelstore.Node
	online bool
	telmu  sync.Mutex
	last   control.Telemetry
	lastAt time.Time
	client *control.Client
}

func (ns *nodeState) telemetry() (control.Telemetry, time.Time) {
	ns.telmu.Lock()
	defer ns.telmu.Unlock()
	return ns.last, ns.lastAt
}

// Manager 负责与所有注册节点维持 WS 管控连接，并向面板 API 暴露聚合状态。
type Manager struct {
	ps  *panelstore.Store
	log *slog.Logger

	mu    sync.RWMutex
	nodes map[string]*nodeState // key = node ID
}

func newManager(ps *panelstore.Store, log *slog.Logger) *Manager {
	return &Manager{
		ps:    ps,
		log:   log,
		nodes: map[string]*nodeState{},
	}
}

// run 是面板连接管理器的主循环。每 30s 重新同步节点注册表并维护连接。
func (m *Manager) run(ctx context.Context) {
	m.sync(ctx)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.closeAll()
			return
		case <-t.C:
			m.sync(ctx)
		}
	}
}

func (m *Manager) sync(ctx context.Context) {
	nodes, err := m.ps.NodeList(ctx)
	if err != nil {
		m.log.Warn("同步节点列表失败", "err", err)
		return
	}

	// 建立 ID 集合
	want := map[string]panelstore.Node{}
	for _, n := range nodes {
		want[n.ID] = n
	}

	m.mu.Lock()
	// 移除已删除的节点
	for id, ns := range m.nodes {
		if _, ok := want[id]; !ok {
			if ns.client != nil {
				_ = ns.client.Close()
			}
			delete(m.nodes, id)
		}
	}
	// 确保新节点有占位
	for id, n := range want {
		if _, exists := m.nodes[id]; !exists {
			m.nodes[id] = &nodeState{info: n}
		} else {
			m.nodes[id].info = n
		}
	}
	toConnect := make([]*nodeState, 0)
	for _, ns := range m.nodes {
		if !ns.online {
			toConnect = append(toConnect, ns)
		}
	}
	m.mu.Unlock()

	// 非阻塞地尝试连接离线节点
	for _, ns := range toConnect {
		go m.dial(ctx, ns)
	}
}

func (m *Manager) dial(ctx context.Context, ns *nodeState) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	ns.telmu.Lock()
	info := ns.info
	ns.telmu.Unlock()

	cli := &control.Client{
		URL: info.CtrlURL,
		Key: info.Key,
		OnTelemetry: func(t control.Telemetry) {
			ns.telmu.Lock()
			ns.last = t
			ns.lastAt = time.Now()
			ns.telmu.Unlock()
		},
		Log: m.log,
	}
	if err := cli.Connect(dialCtx); err != nil {
		m.log.Debug("节点连接失败", "node", info.ID, "err", err)
		return
	}

	m.mu.Lock()
	cur, exists := m.nodes[info.ID]
	if !exists {
		m.mu.Unlock()
		_ = cli.Close()
		return
	}
	if cur.client != nil {
		_ = cur.client.Close()
	}
	cur.client = cli
	cur.online = true
	m.mu.Unlock()

	m.log.Info("节点已连接", "node", info.ID, "role", cli.Node().Role)

	// 等待连接断开后标记离线
	go func() {
		// control.Client 没有直接暴露"等待断开"的方法，
		// 用轮询 telemetry 超时作为离线检测。
		for {
			time.Sleep(15 * time.Second)
			_, lastAt := ns.telemetry()
			if !lastAt.IsZero() && time.Since(lastAt) > 30*time.Second {
				m.log.Warn("节点遥测超时，标记离线", "node", info.ID)
				m.mu.Lock()
				if n := m.nodes[info.ID]; n != nil {
					n.online = false
				}
				m.mu.Unlock()
				return
			}
		}
	}()
}

func (m *Manager) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ns := range m.nodes {
		if ns.client != nil {
			_ = ns.client.Close()
		}
	}
}

// NodeStatuses 返回所有节点的摘要状态，用于面板 API 响应。
func (m *Manager) NodeStatuses() []nodeStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]nodeStatus, 0, len(m.nodes))
	for _, ns := range m.nodes {
		tel, lastAt := ns.telemetry()
		out = append(out, nodeStatus{
			ID:         ns.info.ID,
			Label:      ns.info.Label,
			Role:       ns.info.Role,
			Region:     ns.info.Region,
			APIURL:     ns.info.APIURL,
			Online:     ns.online,
			Telemetry:  tel,
			LastSeenAt: lastAt.UnixMilli(),
		})
	}
	return out
}

// Call 向指定节点下发管控指令（JSON payload）。
func (m *Manager) Call(ctx context.Context, nodeID, action string, payload any) ([]byte, error) {
	m.mu.RLock()
	ns := m.nodes[nodeID]
	m.mu.RUnlock()
	if ns == nil || !ns.online || ns.client == nil {
		return nil, errNodeOffline
	}
	raw, err := ns.client.Call(ctx, action, payload)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

type nodeStatus struct {
	ID         string            `json:"id"`
	Label      string            `json:"label"`
	Role       string            `json:"role"`
	Region     string            `json:"region"`
	APIURL     string            `json:"api_url"`
	Online     bool              `json:"online"`
	Telemetry  control.Telemetry `json:"telemetry"`
	LastSeenAt int64             `json:"last_seen_at"`
}

var errNodeOffline = &errOffline{}

type errOffline struct{}

func (*errOffline) Error() string { return "节点离线" }
