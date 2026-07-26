// Package panelstore 是面板专属的本地 SQLite 存储。
// 与游戏 DB（internal/store）完全独立：面板只管自己的管理员账号和节点注册表。
package panelstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound 记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Store 持有底层 SQLite 连接。
type Store struct {
	db *sql.DB
}

// Node 节点注册条目。Key 为连接密钥，不对浏览器暴露（JSON "-"）。
type Node struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Role      string `json:"role"`     // business | edge
	APIURL    string `json:"api_url"`  // 节点 HTTP API 基址，如 http://127.0.0.1:8080
	CtrlURL   string `json:"ctrl_url"` // 管控 WS 地址，如 ws://127.0.0.1:9090/ctrl
	Key       string `json:"-"`        // 连接密钥（节点自持，面板持有副本）
	Region    string `json:"region"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Admin 面板级管理员（与游戏 admin 完全分离）。
type Admin struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // super_admin | admin
	CreatedAt    int64  `json:"created_at"`
}

// Session 面板管理员会话。
type Session struct {
	Token     string
	AdminID   string
	ExpiresAt int64
}

// Open 打开（必要时创建）SQLite 数据库，运行初始迁移，返回 Store。
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单写
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, err
		}
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭连接。
func (s *Store) Close() error { return s.db.Close() }

// Ping 校验数据库连通且可写——安装向导「数据库」步骤用它做真实连通性探测：
// 先 PingContext 验证可读，再在探针表上写入/删除一行验证可写（只读挂载会在此失败）。
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS panel_health_probe (k INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO panel_health_probe(k) VALUES (1)`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM panel_health_probe WHERE k=1`)
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS panel_nodes (
    id         TEXT PRIMARY KEY,
    label      TEXT NOT NULL DEFAULT '',
    role       TEXT NOT NULL DEFAULT 'business',
    api_url    TEXT NOT NULL,
    ctrl_url   TEXT NOT NULL,
    key        TEXT NOT NULL,
    region     TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS panel_admins (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL,
    email         TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'admin',
    created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS panel_sessions (
    token      TEXT PRIMARY KEY,
    admin_id   TEXT NOT NULL REFERENCES panel_admins(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_panel_sessions_admin ON panel_sessions(admin_id);
CREATE INDEX IF NOT EXISTS idx_panel_sessions_exp   ON panel_sessions(expires_at);
`)
	return err
}

// ─── 节点注册表 ──────────────────────────────────────────────────────────────

func (s *Store) NodeList(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,label,role,api_url,ctrl_url,key,region,created_at,updated_at FROM panel_nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Label, &n.Role, &n.APIURL, &n.CtrlURL, &n.Key, &n.Region, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) NodeByID(ctx context.Context, id string) (*Node, error) {
	var n Node
	err := s.db.QueryRowContext(ctx,
		`SELECT id,label,role,api_url,ctrl_url,key,region,created_at,updated_at FROM panel_nodes WHERE id=?`, id).
		Scan(&n.ID, &n.Label, &n.Role, &n.APIURL, &n.CtrlURL, &n.Key, &n.Region, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

func (s *Store) NodeInsert(ctx context.Context, n Node) error {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO panel_nodes(id,label,role,api_url,ctrl_url,key,region,created_at,updated_at)
         VALUES(?,?,?,?,?,?,?,?,?)`,
		n.ID, n.Label, n.Role, n.APIURL, n.CtrlURL, n.Key, n.Region, now, now)
	return err
}

func (s *Store) NodeUpdate(ctx context.Context, n Node) error {
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`UPDATE panel_nodes SET label=?,role=?,api_url=?,ctrl_url=?,key=?,region=?,updated_at=? WHERE id=?`,
		n.Label, n.Role, n.APIURL, n.CtrlURL, n.Key, n.Region, now, n.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) NodeDelete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM panel_nodes WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ─── 面板管理员 ──────────────────────────────────────────────────────────────

func (s *Store) AdminInsert(ctx context.Context, a Admin) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO panel_admins(id,username,email,password_hash,role,created_at) VALUES(?,?,?,?,?,?)`,
		a.ID, a.Username, a.Email, a.PasswordHash, a.Role, a.CreatedAt)
	return err
}

func (s *Store) AdminByEmail(ctx context.Context, email string) (*Admin, error) {
	var a Admin
	err := s.db.QueryRowContext(ctx,
		`SELECT id,username,email,password_hash,role,created_at FROM panel_admins WHERE email=?`, email).
		Scan(&a.ID, &a.Username, &a.Email, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (s *Store) AdminByID(ctx context.Context, id string) (*Admin, error) {
	var a Admin
	err := s.db.QueryRowContext(ctx,
		`SELECT id,username,email,password_hash,role,created_at FROM panel_admins WHERE id=?`, id).
		Scan(&a.ID, &a.Username, &a.Email, &a.PasswordHash, &a.Role, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (s *Store) AdminCountSuper(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM panel_admins WHERE role='super_admin'`).Scan(&n)
	return n, err
}

func (s *Store) AdminUpdatePassword(ctx context.Context, id, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE panel_admins SET password_hash=? WHERE id=?`, hash, id)
	return err
}

// ─── 面板会话 ────────────────────────────────────────────────────────────────

func (s *Store) SessionInsert(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO panel_sessions(token,admin_id,expires_at) VALUES(?,?,?)`,
		sess.Token, sess.AdminID, sess.ExpiresAt)
	return err
}

func (s *Store) SessionByToken(ctx context.Context, token string) (*Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx,
		`SELECT token,admin_id,expires_at FROM panel_sessions WHERE token=?`, token).
		Scan(&sess.Token, &sess.AdminID, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &sess, err
}

func (s *Store) SessionDeleteByAdmin(ctx context.Context, adminID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM panel_sessions WHERE admin_id=?`, adminID)
	return err
}

func (s *Store) SessionDeleteExpired(ctx context.Context, now int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM panel_sessions WHERE expires_at<?`, now)
	return err
}
