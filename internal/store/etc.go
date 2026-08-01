package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// ── 配置 config(kv)──────────────────────────────────────────────────────

func (s *Store) ConfigGet(ctx context.Context, key string, dst any) (bool, error) {
	var raw []byte
	err := s.queryRow(ctx, `SELECT value FROM config WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if dst != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, dst); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Store) ConfigSet(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	upsert, _ := s.d.Upsert(UpsertSpec{
		Table:        "config",
		Columns:      []string{"key", "value"},
		ConflictCols: []string{"key"},
		UpdateCols:   []string{"value"},
	})
	_, err = s.exec(ctx, upsert, key, raw)
	return err
}

func (s *Store) ConfigEnsure(ctx context.Context, key string, value any) error {
	exists, err := s.ConfigGet(ctx, key, nil)
	if err != nil || exists {
		return err
	}
	return s.ConfigSet(ctx, key, value)
}

// ── 镜像 mirrors ─────────────────────────────────────────────────────────

func (s *Store) insertReturningID(ctx context.Context, tx *sql.Tx, table string, cols []string, args ...any) (int64, error) {
	placeholders := strings.Repeat("?, ", len(cols))
	placeholders = strings.TrimSuffix(placeholders, ", ")
	base := `INSERT INTO ` + table + ` (` + strings.Join(cols, ", ") + `) VALUES (` + placeholders + `)`
	if s.d.LastInsertIDSupported() {
		res, err := tx.ExecContext(ctx, s.rebind(base), args...)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	var id int64
	err := tx.QueryRowContext(ctx, s.rebind(base+` RETURNING id`), args...).Scan(&id)
	return id, err
}

// ── 热更新 hot_bundles ────────────────────────────────────────────────────

func (s *Store) BanInsert(ctx context.Context, b Ban) error {
	const q = `INSERT INTO bans (id, device_id, reason, issued_at, expire_time, issued_by, auto, active)
	           VALUES (?, ?, ?, ?, ?, ?, ?, TRUE)`
	_, err := s.exec(ctx, q, b.ID, b.DeviceID, b.Reason, b.IssuedAt, b.ExpireTime, b.IssuedBy, b.Auto)
	return err
}

// BanActive 查询设备当前生效的封禁(若有)。
func (s *Store) BanActive(ctx context.Context, deviceID string) (*Ban, error) {
	const q = `SELECT id, device_id, reason, issued_at, expire_time, issued_by, auto, lifted_at, lifted_by, active
	           FROM bans WHERE device_id = ? AND active = TRUE`
	rows, err := s.query(ctx, q, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := nowMs()
	for rows.Next() {
		var b Ban
		if err := rows.Scan(&b.ID, &b.DeviceID, &b.Reason, &b.IssuedAt, &b.ExpireTime,
			&b.IssuedBy, &b.Auto, &b.LiftedAt, &b.LiftedBy, &b.Active); err != nil {
			return nil, err
		}
		if b.ExpireTime == nil || *b.ExpireTime > now {
			return &b, nil
		}
	}
	return nil, nil
}

func (s *Store) BanListActive(ctx context.Context) ([]Ban, error) {
	return s.banList(ctx, true)
}

func (s *Store) BanListHistory(ctx context.Context) ([]Ban, error) {
	return s.banList(ctx, false)
}

func (s *Store) banList(ctx context.Context, active bool) ([]Ban, error) {
	const q = `SELECT id, device_id, reason, issued_at, expire_time, issued_by, auto, lifted_at, lifted_by, active
	           FROM bans WHERE active = ? ORDER BY issued_at DESC`
	rows, err := s.query(ctx, q, active)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ban
	for rows.Next() {
		var b Ban
		if err := rows.Scan(&b.ID, &b.DeviceID, &b.Reason, &b.IssuedAt, &b.ExpireTime,
			&b.IssuedBy, &b.Auto, &b.LiftedAt, &b.LiftedBy, &b.Active); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) BanByID(ctx context.Context, id string) (*Ban, error) {
	const q = `SELECT id, device_id, reason, issued_at, expire_time, issued_by, auto, lifted_at, lifted_by, active
	           FROM bans WHERE id = ?`
	var b Ban
	err := s.queryRow(ctx, q, id).Scan(&b.ID, &b.DeviceID, &b.Reason, &b.IssuedAt,
		&b.ExpireTime, &b.IssuedBy, &b.Auto, &b.LiftedAt, &b.LiftedBy, &b.Active)
	return &b, mapErr(err)
}

func (s *Store) BanLift(ctx context.Context, id, by string) error {
	_, err := s.exec(ctx,
		`UPDATE bans SET active = FALSE, lifted_at = ?, lifted_by = ? WHERE id = ? AND active = TRUE`,
		nowMs(), by, id)
	return err
}

// BanSweepExpired 把过期的活跃封禁标记为失效;返回处理条数。
func (s *Store) BanSweepExpired(ctx context.Context) (int64, error) {
	now := nowMs()
	res, err := s.exec(ctx,
		`UPDATE bans SET active = FALSE, lifted_at = ?, lifted_by = 'system'
		 WHERE active = TRUE AND expire_time IS NOT NULL AND expire_time <= ?`,
		now, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ── 设备 devices ─────────────────────────────────────────────────────────

func (s *Store) DeviceTouch(ctx context.Context, deviceID, signature, clientVersion string) error {
	// 用 dialect.Upsert + COALESCE 保留旧的非空 signature/client_version。
	// 三种方言对 EXCLUDED/excluded/VALUES() 各有写法,故按方言分支生成 SET 子句。
	now := nowMs()
	cols := []string{"device_id", "signature", "first_seen", "last_seen", "client_version"}
	var setClause string
	switch s.d.Driver() {
	case DriverMySQL:
		setClause = `last_seen = VALUES(last_seen),
			signature = COALESCE(NULLIF(VALUES(signature), ''), devices.signature),
			client_version = COALESCE(NULLIF(VALUES(client_version), ''), devices.client_version)`
	case DriverSQLite:
		setClause = `last_seen = excluded.last_seen,
			signature = COALESCE(NULLIF(excluded.signature, ''), devices.signature),
			client_version = COALESCE(NULLIF(excluded.client_version, ''), devices.client_version)`
	default:
		setClause = `last_seen = EXCLUDED.last_seen,
			signature = COALESCE(NULLIF(EXCLUDED.signature, ''), devices.signature),
			client_version = COALESCE(NULLIF(EXCLUDED.client_version, ''), devices.client_version)`
	}
	placeholders := strings.Repeat("?, ", len(cols))
	placeholders = strings.TrimSuffix(placeholders, ", ")
	var conflict string
	if s.d.Driver() == DriverMySQL {
		conflict = " ON DUPLICATE KEY UPDATE "
	} else {
		conflict = " ON CONFLICT (device_id) DO UPDATE SET "
		if s.d.Driver() == DriverSQLite {
			conflict = " ON CONFLICT(device_id) DO UPDATE SET "
		}
	}
	q := "INSERT INTO devices (" + strings.Join(cols, ", ") + ") VALUES (" + placeholders + ")" +
		conflict + setClause
	_, err := s.exec(ctx, q, deviceID, nullIfEmpty(signature), now, now, nullIfEmpty(clientVersion))
	return err
}

// nullIfEmpty 把空字符串转成 NULL,便于 COALESCE 取旧值。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ── 云存档 saves ─────────────────────────────────────────────────────────

func (s *Store) SaveGet(ctx context.Context, accountID string) (json.RawMessage, int64, int64, error) {
	const q = `SELECT data, updated_at, size_bytes FROM saves WHERE account_id = ?`
	var data []byte
	var updated, size int64
	err := s.queryRow(ctx, q, accountID).Scan(&data, &updated, &size)
	if errors.Is(err, sql.ErrNoRows) {
		return json.RawMessage("{}"), 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, err
	}
	if len(data) == 0 {
		data = []byte("{}")
	}
	return json.RawMessage(data), updated, size, nil
}

func (s *Store) SavePut(ctx context.Context, accountID string, data json.RawMessage) error {
	upsert, _ := s.d.Upsert(UpsertSpec{
		Table:        "saves",
		Columns:      []string{"account_id", "data", "updated_at", "size_bytes"},
		ConflictCols: []string{"account_id"},
		UpdateCols:   []string{"data", "updated_at", "size_bytes"},
	})
	_, err := s.exec(ctx, upsert, accountID, []byte(data), nowMs(), int64(len(data)))
	return err
}

func (s *Store) SaveDelete(ctx context.Context, accountID string) error {
	_, err := s.exec(ctx,
		`UPDATE saves SET data = ?, size_bytes = 0, updated_at = ? WHERE account_id = ?`,
		[]byte("{}"), nowMs(), accountID)
	return err
}

// ── 审计 audit_log ───────────────────────────────────────────────────────

func (s *Store) AuditInsert(ctx context.Context, e AuditEntry) error {
	if e.Details == nil {
		e.Details = json.RawMessage("{}")
	}
	const q = `INSERT INTO audit_log (id, ts, actor, type, target, details)
	           VALUES (?, ?, ?, ?, ?, ?)`
	_, err := s.exec(ctx, q, e.ID, e.Ts, e.Actor, e.Type, e.Target, []byte(e.Details))
	return err
}

func (s *Store) AuditList(ctx context.Context, typ, actor string, sinceDays, offset, limit int) ([]AuditEntry, int, error) {
	var args []any
	cond := "1=1"
	if typ != "" {
		args = append(args, typ)
		cond += " AND type = ?"
	}
	if actor != "" {
		args = append(args, actor)
		cond += " AND actor = ?"
	}
	if sinceDays > 0 {
		args = append(args, nowMs()-int64(sinceDays)*86400000)
		cond += " AND ts >= ?"
	}
	var total int
	if err := s.queryRow(ctx, "SELECT COUNT(*) FROM audit_log WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.query(ctx,
		"SELECT id, ts, actor, type, target, details FROM audit_log WHERE "+cond+
			" ORDER BY ts DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var details []byte
		if err := rows.Scan(&e.ID, &e.Ts, &e.Actor, &e.Type, &e.Target, &details); err != nil {
			return nil, 0, err
		}
		e.Details = json.RawMessage(details)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// ── cap-worker ───────────────────────────────────────────────────────────

func (s *Store) CapChallengeInsert(ctx context.Context, ch CapChallenge) error {
	_, err := s.exec(ctx,
		`INSERT INTO cap_challenges (token, c, d, s, expires_at, solved) VALUES (?, ?, ?, ?, ?, FALSE)`,
		ch.Token, ch.C, ch.D, ch.S, ch.ExpiresAt)
	return err
}

func (s *Store) CapChallengeGet(ctx context.Context, token string) (*CapChallenge, error) {
	var ch CapChallenge
	err := s.queryRow(ctx,
		`SELECT token, c, d, s, expires_at, solved FROM cap_challenges WHERE token = ?`,
		token).Scan(&ch.Token, &ch.C, &ch.D, &ch.S, &ch.ExpiresAt, &ch.Solved)
	return &ch, mapErr(err)
}

func (s *Store) CapChallengeMarkSolved(ctx context.Context, token string) error {
	_, err := s.exec(ctx, `UPDATE cap_challenges SET solved = TRUE WHERE token = ?`, token)
	return err
}

func (s *Store) CapTokenInsert(ctx context.Context, token string, expiresAt int64) error {
	_, err := s.exec(ctx,
		`INSERT INTO cap_tokens (token, expires_at, used) VALUES (?, ?, FALSE)`,
		token, expiresAt)
	return err
}

// CapTokenConsume 一次性消费;返回 true 才放行。
func (s *Store) CapTokenConsume(ctx context.Context, token string) (bool, error) {
	res, err := s.exec(ctx,
		`UPDATE cap_tokens SET used = TRUE WHERE token = ? AND used = FALSE AND expires_at > ?`,
		token, nowMs())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ── 镜像流量统计 mirror_traffic ──────────────────────────────────────────
