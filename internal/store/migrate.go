// 内嵌的 SQL 迁移。三个方言各自一份;按 Store.Migrate() 选择运行。
//
// 设计简单粗暴:文件按名字顺序执行,内部全部用 CREATE TABLE IF NOT EXISTS,
// 因此重复运行安全(idempotent)。新增字段一律新增 0002_xxx.sql,不改老文件。
package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*/*.sql
var migrationsFS embed.FS

// isAlreadyApplied 判断迁移错误是否属于"已经执行过"的幂等情形。
// ALTER TABLE ADD COLUMN 不支持 IF NOT EXISTS（SQLite < 3.37），
// 重复执行时会报 "duplicate column name"（SQLite）、
// "Duplicate column name"（MySQL）或 column already exists（PostgreSQL）。
func isAlreadyApplied(stmt, errMsg string) bool {
	upper := strings.ToUpper(stmt)
	if !strings.Contains(upper, "ALTER TABLE") || !strings.Contains(upper, "ADD COLUMN") {
		return false
	}
	low := strings.ToLower(errMsg)
	return strings.Contains(low, "duplicate column") ||
		strings.Contains(low, "already exists") ||
		strings.Contains(low, "column exists")
}

// Migrate 按方言执行内嵌迁移。已有的表/索引会被 IF NOT EXISTS 跳过。
// ALTER TABLE ADD COLUMN 的重复执行（列已存在）同样视为成功。
func (s *Store) Migrate(ctx context.Context) error {
	dirName := "migrations/" + string(s.d.Driver())
	// pgx 驱动名是 "pgx",但迁移目录用 "postgres"
	switch s.d.Driver() {
	case DriverPostgres:
		dirName = "migrations/postgres"
	case DriverMySQL:
		dirName = "migrations/mysql"
	case DriverSQLite:
		dirName = "migrations/sqlite"
	}

	entries, err := fs.ReadDir(migrationsFS, dirName)
	if err != nil {
		return fmt.Errorf("读取迁移目录失败 %s: %w", dirName, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		path := dirName + "/" + name
		buf, err := migrationsFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", path, err)
		}
		// SQLite / mysql 不支持单 Exec 跑多个语句的某些 setup 语句(如 PRAGMA / SET)
		// 已经在 Open() 里处理过;这里按 ";\n" 分段执行,跳过纯注释段。
		for _, stmt := range splitSQL(string(buf)) {
			if stmt == "" {
				continue
			}
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				if isAlreadyApplied(stmt, err.Error()) {
					continue
				}
				return fmt.Errorf("执行 %s 段失败: %w\n--- sql ---\n%s", path, err, stmt)
			}
		}
	}
	return nil
}

// splitSQL 把一份 SQL 文件按 ; 分段,剔除全行注释。
// 简单实现:足够覆盖我们手写的 schema(不含字符串内分号 / 存储过程)。
func splitSQL(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ";") {
		// 删除以 -- 开头的整行注释
		var keep []string
		for _, ln := range strings.Split(raw, "\n") {
			trim := strings.TrimSpace(ln)
			if strings.HasPrefix(trim, "--") || trim == "" {
				continue
			}
			keep = append(keep, ln)
		}
		seg := strings.TrimSpace(strings.Join(keep, "\n"))
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}
