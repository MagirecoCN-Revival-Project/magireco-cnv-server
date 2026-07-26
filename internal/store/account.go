package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ── 管理员 admins ─────────────────────────────────────────────────────────

func (s *Store) AdminByEmail(ctx context.Context, email string) (*Admin, error) {
	const q = `SELECT id, username, email, password_hash, role, created_at, last_login_at
	           FROM admins WHERE email = ?`
	a := &Admin{}
	err := s.queryRow(ctx, q, email).Scan(&a.ID, &a.Username, &a.Email,
		&a.PasswordHash, &a.Role, &a.CreatedAt, &a.LastLoginAt)
	return a, mapErr(err)
}

func (s *Store) AdminByID(ctx context.Context, id string) (*Admin, error) {
	const q = `SELECT id, username, email, password_hash, role, created_at, last_login_at
	           FROM admins WHERE id = ?`
	a := &Admin{}
	err := s.queryRow(ctx, q, id).Scan(&a.ID, &a.Username, &a.Email,
		&a.PasswordHash, &a.Role, &a.CreatedAt, &a.LastLoginAt)
	return a, mapErr(err)
}

func (s *Store) AdminInsert(ctx context.Context, a Admin) error {
	const q = `INSERT INTO admins (id, username, email, password_hash, role, created_at)
	           VALUES (?, ?, ?, ?, ?, ?)`
	_, err := s.exec(ctx, q, a.ID, a.Username, a.Email, a.PasswordHash, a.Role, a.CreatedAt)
	return err
}

func (s *Store) AdminListAll(ctx context.Context) ([]Admin, error) {
	const q = `SELECT id, username, email, password_hash, role, created_at, last_login_at
	           FROM admins ORDER BY created_at`
	rows, err := s.query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Admin
	for rows.Next() {
		var a Admin
		if err := rows.Scan(&a.ID, &a.Username, &a.Email, &a.PasswordHash,
			&a.Role, &a.CreatedAt, &a.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) AdminUpdateRole(ctx context.Context, id, role string) error {
	_, err := s.exec(ctx, `UPDATE admins SET role = ? WHERE id = ?`, role, id)
	return err
}

func (s *Store) AdminUpdateEmail(ctx context.Context, id, email string) error {
	_, err := s.exec(ctx, `UPDATE admins SET email = ? WHERE id = ?`, email, id)
	return err
}

func (s *Store) AdminUpdatePassword(ctx context.Context, id, hash string) error {
	_, err := s.exec(ctx, `UPDATE admins SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

func (s *Store) AdminTouchLogin(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `UPDATE admins SET last_login_at = ? WHERE id = ?`, nowMs(), id)
	return err
}

func (s *Store) AdminDelete(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM admins WHERE id = ?`, id)
	return err
}

func (s *Store) AdminCountSuper(ctx context.Context) (int, error) {
	var n int
	err := s.queryRow(ctx, `SELECT COUNT(*) FROM admins WHERE role = 'super_admin'`).Scan(&n)
	return n, err
}

// ── 玩家账号 accounts ─────────────────────────────────────────────────────

func (s *Store) AccountByEmail(ctx context.Context, email string) (*Account, error) {
	const q = `SELECT id, username, email, password_hash, status, created_at, last_login_at, login_count, email_verified
	           FROM accounts WHERE email = ?`
	a := &Account{}
	err := s.queryRow(ctx, q, email).Scan(&a.ID, &a.Username, &a.Email,
		&a.PasswordHash, &a.Status, &a.CreatedAt, &a.LastLoginAt, &a.LoginCount, &a.EmailVerified)
	return a, mapErr(err)
}

func (s *Store) AccountByUsernameOrEmail(ctx context.Context, key string) (*Account, error) {
	const q = `SELECT id, username, email, password_hash, status, created_at, last_login_at, login_count, email_verified
	           FROM accounts WHERE username = ? OR email = ? LIMIT 1`
	a := &Account{}
	err := s.queryRow(ctx, q, key, key).Scan(&a.ID, &a.Username, &a.Email,
		&a.PasswordHash, &a.Status, &a.CreatedAt, &a.LastLoginAt, &a.LoginCount, &a.EmailVerified)
	return a, mapErr(err)
}

func (s *Store) AccountByID(ctx context.Context, id string) (*Account, error) {
	const q = `SELECT id, username, email, password_hash, status, created_at, last_login_at, login_count, email_verified
	           FROM accounts WHERE id = ?`
	a := &Account{}
	err := s.queryRow(ctx, q, id).Scan(&a.ID, &a.Username, &a.Email,
		&a.PasswordHash, &a.Status, &a.CreatedAt, &a.LastLoginAt, &a.LoginCount, &a.EmailVerified)
	return a, mapErr(err)
}

func (s *Store) AccountInsert(ctx context.Context, a Account) error {
	const q = `INSERT INTO accounts (id, username, email, password_hash, status, created_at, last_login_at, login_count, email_verified)
	           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.exec(ctx, q, a.ID, a.Username, a.Email, a.PasswordHash,
		a.Status, a.CreatedAt, a.LastLoginAt, a.LoginCount, a.EmailVerified)
	return err
}

// AccountSetEmailVerified 标记账号邮箱已验证。
func (s *Store) AccountSetEmailVerified(ctx context.Context, email string) error {
	_, err := s.exec(ctx, `UPDATE accounts SET email_verified = TRUE WHERE email = ?`, email)
	return err
}

// AccountList 简单分页 + 关键字过滤 + 状态过滤。
// 关键字匹配走 LOWER(col) LIKE LOWER(?),三种方言皆兼容。
func (s *Store) AccountList(ctx context.Context, q string, status string, offset, limit int) ([]Account, int, error) {
	var args []any
	cond := "1=1"
	if q != "" {
		needle := "%" + strings.ToLower(q) + "%"
		args = append(args, needle, needle, needle)
		cond += " AND (LOWER(username) LIKE ? OR LOWER(email) LIKE ? OR LOWER(id) LIKE ?)"
	}
	if status != "" && status != "all" {
		args = append(args, status)
		cond += " AND status = ?"
	}
	var total int
	if err := s.queryRow(ctx, "SELECT COUNT(*) FROM accounts WHERE "+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := s.query(ctx,
		"SELECT id, username, email, password_hash, status, created_at, last_login_at, login_count, email_verified "+
			"FROM accounts WHERE "+cond+" ORDER BY created_at DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.Username, &a.Email, &a.PasswordHash, &a.Status,
			&a.CreatedAt, &a.LastLoginAt, &a.LoginCount, &a.EmailVerified); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

func (s *Store) AccountUpdateStatus(ctx context.Context, id, status string) error {
	_, err := s.exec(ctx, `UPDATE accounts SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) AccountUpdatePassword(ctx context.Context, id, hash string) error {
	_, err := s.exec(ctx, `UPDATE accounts SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

func (s *Store) AccountUpdateEmail(ctx context.Context, id, email string) error {
	_, err := s.exec(ctx, `UPDATE accounts SET email = ? WHERE id = ?`, email, id)
	return err
}

func (s *Store) AccountTouchLogin(ctx context.Context, id string) error {
	const q = `UPDATE accounts SET last_login_at = ?, login_count = login_count + 1 WHERE id = ?`
	_, err := s.exec(ctx, q, nowMs(), id)
	return err
}

func (s *Store) AccountDelete(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	return err
}

// ── 玩家会话 account_sessions ────────────────────────────────────────────

func (s *Store) AccountSessionInsert(ctx context.Context, ses AccountSession) error {
	const q = `INSERT INTO account_sessions
	           (token, account_id, device_name, os, ip, region, created_at, last_seen_at, expires_at)
	           VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.exec(ctx, q, ses.Token, ses.AccountID, ses.DeviceName, ses.OS, ses.IP,
		ses.Region, ses.CreatedAt, ses.LastSeenAt, ses.ExpiresAt)
	return err
}

// AccountSessionLookup 一次查 session + account 信息(常用 join)。
func (s *Store) AccountSessionLookup(ctx context.Context, token string) (*AccountSession, *Account, error) {
	const q = `SELECT s.token, s.account_id, s.device_name, s.os, s.ip, s.region,
	                  s.created_at, s.last_seen_at, s.expires_at,
	                  a.id, a.username, a.email, a.password_hash, a.status,
	                  a.created_at, a.last_login_at, a.login_count, a.email_verified
	           FROM account_sessions s JOIN accounts a ON a.id = s.account_id
	           WHERE s.token = ?`
	ses := &AccountSession{}
	acc := &Account{}
	err := s.queryRow(ctx, q, token).Scan(
		&ses.Token, &ses.AccountID, &ses.DeviceName, &ses.OS, &ses.IP, &ses.Region,
		&ses.CreatedAt, &ses.LastSeenAt, &ses.ExpiresAt,
		&acc.ID, &acc.Username, &acc.Email, &acc.PasswordHash, &acc.Status,
		&acc.CreatedAt, &acc.LastLoginAt, &acc.LoginCount, &acc.EmailVerified)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	return ses, acc, nil
}

// AccountSessionTouch 更新 last_seen_at;若 ttl > 0 且剩余寿命不足 ttl/2,
// 顺便把 expires_at 滑到 now+ttl,实现"记住登录"的滑动续期。
// 单条 UPDATE + CASE,PG/MySQL/SQLite 都支持。
//
// 半 TTL 阈值的好处:活跃玩家无感续期;同时 DB 写仅在每半个 TTL 触发一次,
// 而不是每次 API 调用都写。被盗 token 持续滥用也会自动续命,所以 ttl 不宜
// 设得过长(默认 30 天是合理上限);需要强制下线时调 AccountSessionDelete*。
func (s *Store) AccountSessionTouch(ctx context.Context, token string, ttl time.Duration) {
	now := nowMs()
	if ttl <= 0 {
		_, _ = s.exec(ctx,
			`UPDATE account_sessions SET last_seen_at = ? WHERE token = ?`,
			now, token)
		return
	}
	ttlMs := ttl.Milliseconds()
	_, _ = s.exec(ctx, `
		UPDATE account_sessions
		   SET last_seen_at = ?,
		       expires_at = CASE
		           WHEN expires_at - ? < ? THEN ? + ?
		           ELSE expires_at
		       END
		 WHERE token = ?`,
		now, now, ttlMs/2, now, ttlMs, token)
}

func (s *Store) AccountSessionDelete(ctx context.Context, token string) error {
	_, err := s.exec(ctx, `DELETE FROM account_sessions WHERE token = ?`, token)
	return err
}

func (s *Store) AccountSessionDeleteAllExcept(ctx context.Context, accountID, keepToken string) error {
	_, err := s.exec(ctx,
		`DELETE FROM account_sessions WHERE account_id = ? AND token <> ?`,
		accountID, keepToken)
	return err
}

func (s *Store) AccountSessionDeleteByAccount(ctx context.Context, accountID string) error {
	_, err := s.exec(ctx, `DELETE FROM account_sessions WHERE account_id = ?`, accountID)
	return err
}

func (s *Store) AccountSessionListByAccount(ctx context.Context, accountID string) ([]AccountSession, error) {
	const q = `SELECT token, account_id, device_name, os, ip, region,
	                  created_at, last_seen_at, expires_at
	           FROM account_sessions WHERE account_id = ? ORDER BY last_seen_at DESC`
	rows, err := s.query(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountSession
	for rows.Next() {
		var v AccountSession
		if err := rows.Scan(&v.Token, &v.AccountID, &v.DeviceName, &v.OS, &v.IP, &v.Region,
			&v.CreatedAt, &v.LastSeenAt, &v.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ── 管理员会话 admin_sessions ─────────────────────────────────────────────

func (s *Store) AdminSessionInsert(ctx context.Context, ses AdminSession) error {
	const q = `INSERT INTO admin_sessions (token, admin_id, created_at, expires_at) VALUES (?, ?, ?, ?)`
	_, err := s.exec(ctx, q, ses.Token, ses.AdminID, ses.CreatedAt, ses.ExpiresAt)
	return err
}

func (s *Store) AdminSessionLookup(ctx context.Context, token string) (*AdminSession, *Admin, error) {
	const q = `SELECT s.token, s.admin_id, s.created_at, s.expires_at,
	                  a.id, a.username, a.email, a.password_hash, a.role, a.created_at, a.last_login_at
	           FROM admin_sessions s JOIN admins a ON a.id = s.admin_id
	           WHERE s.token = ?`
	ses := &AdminSession{}
	a := &Admin{}
	err := s.queryRow(ctx, q, token).Scan(
		&ses.Token, &ses.AdminID, &ses.CreatedAt, &ses.ExpiresAt,
		&a.ID, &a.Username, &a.Email, &a.PasswordHash, &a.Role, &a.CreatedAt, &a.LastLoginAt)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	return ses, a, nil
}

func (s *Store) AdminSessionDelete(ctx context.Context, token string) error {
	_, err := s.exec(ctx, `DELETE FROM admin_sessions WHERE token = ?`, token)
	return err
}

func (s *Store) AdminSessionDeleteByAdmin(ctx context.Context, adminID string) error {
	_, err := s.exec(ctx, `DELETE FROM admin_sessions WHERE admin_id = ?`, adminID)
	return err
}

// ── 客户端握手会话 client_sessions ────────────────────────────────────────

func (s *Store) ClientSessionInsert(ctx context.Context, c ClientSession) error {
	const q = `INSERT INTO client_sessions
	           (access_token, device_id, signature, client_version, channel,
	            created_at, expires_at, last_seen_at)
	           VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.exec(ctx, q,
		c.AccessToken, c.DeviceID, nullIfEmpty(c.Signature),
		nullIfEmpty(c.ClientVersion), nullIfEmpty(c.Channel),
		c.CreatedAt, c.ExpiresAt, c.LastSeenAt)
	return err
}

// ClientSessionLookup 校验 access_token 仍然存在且未过期;顺便绑定 device_id。
// 返回 nil 表示 token 无效或与 device_id 不匹配(防 token 跨设备复用)。
func (s *Store) ClientSessionLookup(ctx context.Context, token, deviceID string) (*ClientSession, error) {
	const q = `SELECT access_token, device_id, COALESCE(signature,''),
	                  COALESCE(client_version,''), COALESCE(channel,''),
	                  created_at, expires_at, last_seen_at
	           FROM client_sessions WHERE access_token = ?`
	c := &ClientSession{}
	err := s.queryRow(ctx, q, token).Scan(
		&c.AccessToken, &c.DeviceID, &c.Signature,
		&c.ClientVersion, &c.Channel,
		&c.CreatedAt, &c.ExpiresAt, &c.LastSeenAt)
	if err != nil {
		return nil, mapErr(err)
	}
	if c.ExpiresAt < nowMs() {
		return nil, ErrNotFound
	}
	if deviceID != "" && c.DeviceID != deviceID {
		return nil, ErrNotFound
	}
	return c, nil
}

func (s *Store) ClientSessionTouch(ctx context.Context, token string) {
	_, _ = s.exec(ctx,
		`UPDATE client_sessions SET last_seen_at = ? WHERE access_token = ?`,
		nowMs(), token)
}

func (s *Store) ClientSessionDelete(ctx context.Context, token string) error {
	_, err := s.exec(ctx, `DELETE FROM client_sessions WHERE access_token = ?`, token)
	return err
}

func (s *Store) ClientSessionDeleteByDevice(ctx context.Context, deviceID string) error {
	_, err := s.exec(ctx, `DELETE FROM client_sessions WHERE device_id = ?`, deviceID)
	return err
}

// ── GC ────────────────────────────────────────────────────────────────────

// SessionGC 删除所有过期的会话(client/admin/account 共用过期字段语义)。
func (s *Store) SessionGC(ctx context.Context) error {
	now := nowMs()
	for _, t := range []string{"admin_sessions", "account_sessions", "client_sessions", "cap_challenges", "cap_tokens", "email_codes"} {
		if _, err := s.exec(ctx, `DELETE FROM `+t+` WHERE expires_at < ?`, now); err != nil {
			return err
		}
	}
	return nil
}

// ── 邮箱验证码 email_codes ────────────────────────────────────────────────

// EmailCodeIssue 写新验证码;同 email+purpose 的旧未消费码先标记已用。
func (s *Store) EmailCodeIssue(ctx context.Context, email, purpose, code string, ttl time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.rebind(
		`UPDATE email_codes SET consumed = TRUE WHERE email = ? AND purpose = ? AND consumed = FALSE`),
		email, purpose); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(
		`INSERT INTO email_codes (email, code, purpose, expires_at, consumed)
		 VALUES (?, ?, ?, ?, FALSE)`),
		email, code, purpose, nowMs()+ttl.Milliseconds()); err != nil {
		return err
	}
	return tx.Commit()
}

// AccountPurgeUnverified 删除 email_verified=false 且 created_at < cutoff 的账号。
// 返回实际删除行数。
func (s *Store) AccountPurgeUnverified(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.exec(ctx,
		`DELETE FROM accounts WHERE email_verified = FALSE AND created_at < ?`,
		cutoff.UnixMilli(),
	)
	if err != nil {
		return 0, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// EmailCodeCheck 校验有效未过期的验证码;consume=true 时校验通过即作废。
func (s *Store) EmailCodeCheck(ctx context.Context, email, code, purpose string, consume bool) (bool, error) {
	var id int64
	err := s.queryRow(ctx,
		`SELECT id FROM email_codes WHERE email = ? AND code = ? AND purpose = ?
		  AND consumed = FALSE AND expires_at > ? ORDER BY id DESC LIMIT 1`,
		email, code, purpose, nowMs()).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, mapErr(err)
	}
	if consume {
		_, _ = s.exec(ctx, `UPDATE email_codes SET consumed = TRUE WHERE id = ?`, id)
	}
	return true, nil
}
