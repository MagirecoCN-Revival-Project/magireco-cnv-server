// Package store 是主节点的数据库访问层。同时支持 PostgreSQL、MySQL、SQLite。
// 一切 SQL 都被收敛到这里,Handler 不直接拼 SQL。副节点不调用本包(它没有数据库)。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// 三种驱动:按 DSN 自动选用
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Store 持有 *sql.DB 与方言。所有方法都接受 context。
type Store struct {
	db *sql.DB
	d  Dialect
}

// Open 按 DSN 推断方言并打开连接;ping 一次确认可用。
//
// DSN 支持:
//   - postgres://user:pwd@host:port/db?sslmode=disable
//   - mysql://user:pwd@host:port/db?parseTime=true  (内部转换成 user:pwd@tcp(host:port)/db)
//   - sqlite:///path/to/file.db  或  file:/path/to/file.db  或  /path/to/file.db
//   - :memory:                   (内存 SQLite,仅做测试用)
func Open(ctx context.Context, dsn string) (*Store, error) {
	d := DetectDialect(dsn)
	driver := string(d.Driver())
	conn := NormalizeDSN(d, dsn)

	db, err := sql.Open(driver, conn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败 (%s): %w", driver, err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	// SQLite 需要 WAL + 外键约束。
	if d.Driver() == DriverSQLite {
		for _, pragma := range []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA synchronous=NORMAL",
			"PRAGMA foreign_keys=ON",
			"PRAGMA busy_timeout=5000",
		} {
			if _, err := db.ExecContext(ctx, pragma); err != nil {
				db.Close()
				return nil, fmt.Errorf("设置 SQLite PRAGMA 失败: %w", err)
			}
		}
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库 ping 失败: %w", err)
	}
	return &Store{db: db, d: d}, nil
}

// Close 关闭连接池。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层 sql.DB,供少数需要事务/直接执行的高级调用方使用。
func (s *Store) DB() *sql.DB { return s.db }

// Dialect 暴露方言。
func (s *Store) Dialect() Dialect { return s.d }

// ErrNotFound 业务层用以区分"行不存在"与其它错误。
var ErrNotFound = errors.New("记录不存在")

// mapErr 把 sql.ErrNoRows 统一映射为 ErrNotFound,其它原样返回。
func mapErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// nowMs 返回当前 Unix 毫秒。
func nowMs() int64 { return time.Now().UnixMilli() }

// rebind 是 d.Rebind 的便捷方法。
func (s *Store) rebind(q string) string { return s.d.Rebind(q) }

// exec/query/queryRow 三个薄封装:把方言重绑放在一处。
func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.rebind(q), args...)
}
func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.rebind(q), args...)
}
func (s *Store) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.rebind(q), args...)
}
