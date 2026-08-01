// 数据库方言抽象。每个 store 方法用 ? 占位符,Rebind() 把它们换成方言专属
// 写法($N for postgres,? for mysql/sqlite)。需要差异化语法的场景(如
// upsert)通过 Dialect 暴露的辅助方法生成。
//
// 三种方言:
//   - postgres:历史上的主目标,JSONB + ON CONFLICT + RETURNING
//   - mysql:JSON 列,ON DUPLICATE KEY UPDATE,无 RETURNING
//   - sqlite:TEXT 模拟 JSON,3.35+ 支持 ON CONFLICT + RETURNING
package store

import (
	"strconv"
	"strings"
)

// Driver 用来选择实际打开数据库时使用的 sql.Open 驱动名。
type Driver string

const (
	DriverPostgres Driver = "pgx"    // pgx/v5/stdlib
	DriverMySQL    Driver = "mysql"  // go-sql-driver/mysql
	DriverSQLite   Driver = "sqlite" // modernc.org/sqlite
)

// Dialect 把方言差异收敛到几个方法里。
type Dialect interface {
	// Driver 返回 sql.Open 的驱动名。
	Driver() Driver
	// Rebind 把 ? 占位符按方言改写。postgres 改为 $1/$2/...;mysql/sqlite 原样。
	Rebind(query string) string
	// UpsertReturningID 给 INSERT...ON CONFLICT...RETURNING id 生成对应方言的 SQL 与
	// 取 id 策略。supportsReturning=true 时调用方可直接 QueryRow 拿 id;否则
	// 调用方应当用 Exec + LastInsertId。
	Upsert(spec UpsertSpec) (sql string, supportsReturning bool)
	// LastInsertIDSupported 表示 sql.Result.LastInsertId() 在该方言可用。
	LastInsertIDSupported() bool
}

// UpsertSpec 描述一次 upsert:列、主键冲突时要 UPDATE 的列,以及是否需要回写自增 id。
type UpsertSpec struct {
	Table        string
	Columns      []string // 全部待插入列
	ConflictCols []string // ON CONFLICT 检测列(通常是 PK / UNIQUE)
	UpdateCols   []string // 冲突时 UPDATE 的列(必须是 Columns 的子集)
	ReturningID  bool     // 是否要回写自增 id
}

// DetectDialect 从 DSN 推断方言。
//   - postgres://... 或 postgresql://...                  → postgres
//   - mysql://... 或 user:pwd@tcp(...)/db                  → mysql
//   - sqlite://... 或 file:... 或 *.db / *.sqlite{,3} 路径 → sqlite
func DetectDialect(dsn string) Dialect {
	s := strings.ToLower(strings.TrimSpace(dsn))
	switch {
	case strings.HasPrefix(s, "postgres://"), strings.HasPrefix(s, "postgresql://"):
		return postgresDialect{}
	case strings.HasPrefix(s, "mysql://"), strings.Contains(s, "@tcp(") && strings.Contains(s, ")/"):
		return mysqlDialect{}
	case strings.HasPrefix(s, "sqlite://"), strings.HasPrefix(s, "file:"),
		strings.HasSuffix(s, ".db"), strings.HasSuffix(s, ".sqlite"), strings.HasSuffix(s, ".sqlite3"),
		s == ":memory:":
		return sqliteDialect{}
	}
	// 默认回到 postgres 保持向后兼容。
	return postgresDialect{}
}

// NormalizeDSN 把面向用户友好的前缀(mysql://、sqlite://)转换为实际驱动期望的
// 连接字符串。pgx/mysql/sqlite 三种驱动接受的格式各不相同。
func NormalizeDSN(d Dialect, dsn string) string {
	switch d.Driver() {
	case DriverMySQL:
		// go-sql-driver/mysql 不接受 mysql:// 前缀;形如 user:pwd@tcp(host:port)/db。
		if strings.HasPrefix(dsn, "mysql://") {
			return mysqlURLToDSN(dsn)
		}
		return dsn
	case DriverSQLite:
		if strings.HasPrefix(dsn, "sqlite://") {
			return strings.TrimPrefix(dsn, "sqlite://")
		}
		return dsn
	default:
		return dsn
	}
}

// mysqlURLToDSN 简单地把 mysql://user:pwd@host:port/db?params 改写成
// user:pwd@tcp(host:port)/db?params。不做严格 URL 解析,够用。
func mysqlURLToDSN(u string) string {
	s := strings.TrimPrefix(u, "mysql://")
	// 拆 auth@host/path?query
	var auth, rest string
	if i := strings.Index(s, "@"); i >= 0 {
		auth, rest = s[:i+1], s[i+1:]
	} else {
		rest = s
	}
	host, tail := rest, ""
	if i := strings.Index(rest, "/"); i >= 0 {
		host, tail = rest[:i], rest[i:]
	}
	return auth + "tcp(" + host + ")" + tail
}

// ── postgres ──────────────────────────────────────────────────────────────

type postgresDialect struct{}

func (postgresDialect) Driver() Driver              { return DriverPostgres }
func (postgresDialect) LastInsertIDSupported() bool { return false }

func (postgresDialect) Rebind(q string) string {
	var b strings.Builder
	b.Grow(len(q) + 8)
	idx := 1
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(idx))
			idx++
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

func (postgresDialect) Upsert(s UpsertSpec) (string, bool) {
	cols := strings.Join(s.Columns, ", ")
	placeholders := strings.Repeat("?, ", len(s.Columns))
	placeholders = strings.TrimSuffix(placeholders, ", ")
	var sets []string
	for _, c := range s.UpdateCols {
		sets = append(sets, c+" = EXCLUDED."+c)
	}
	sql := "INSERT INTO " + s.Table + " (" + cols + ") VALUES (" + placeholders +
		") ON CONFLICT (" + strings.Join(s.ConflictCols, ", ") + ") DO UPDATE SET " +
		strings.Join(sets, ", ")
	if s.ReturningID {
		sql += " RETURNING id"
		return sql, true
	}
	return sql, false
}

// ── mysql ─────────────────────────────────────────────────────────────────

type mysqlDialect struct{}

func (mysqlDialect) Driver() Driver              { return DriverMySQL }
func (mysqlDialect) LastInsertIDSupported() bool { return true }
func (mysqlDialect) Rebind(q string) string      { return q } // ? 原样

func (mysqlDialect) Upsert(s UpsertSpec) (string, bool) {
	cols := strings.Join(s.Columns, ", ")
	placeholders := strings.Repeat("?, ", len(s.Columns))
	placeholders = strings.TrimSuffix(placeholders, ", ")
	var sets []string
	for _, c := range s.UpdateCols {
		sets = append(sets, c+" = VALUES("+c+")")
	}
	sql := "INSERT INTO " + s.Table + " (" + cols + ") VALUES (" + placeholders +
		") ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
	return sql, false
}

// ── sqlite ────────────────────────────────────────────────────────────────

type sqliteDialect struct{}

func (sqliteDialect) Driver() Driver              { return DriverSQLite }
func (sqliteDialect) LastInsertIDSupported() bool { return true }
func (sqliteDialect) Rebind(q string) string      { return q }

func (sqliteDialect) Upsert(s UpsertSpec) (string, bool) {
	// SQLite 3.24+ 支持 ON CONFLICT;3.35+ 支持 RETURNING。modernc.org/sqlite 已经够新。
	cols := strings.Join(s.Columns, ", ")
	placeholders := strings.Repeat("?, ", len(s.Columns))
	placeholders = strings.TrimSuffix(placeholders, ", ")
	var sets []string
	for _, c := range s.UpdateCols {
		sets = append(sets, c+" = excluded."+c)
	}
	sql := "INSERT INTO " + s.Table + " (" + cols + ") VALUES (" + placeholders +
		") ON CONFLICT(" + strings.Join(s.ConflictCols, ", ") + ") DO UPDATE SET " +
		strings.Join(sets, ", ")
	if s.ReturningID {
		sql += " RETURNING id"
		return sql, true
	}
	return sql, false
}
