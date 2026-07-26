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

func (s *Store) MirrorGroupsAll(ctx context.Context) ([]MirrorGroup, error) {
	rows, err := s.query(ctx,
		`SELECT id, name, sort_order FROM mirror_groups ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MirrorGroup
	for rows.Next() {
		var g MirrorGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) MirrorsByGroup(ctx context.Context, groupID int64) ([]Mirror, error) {
	rows, err := s.query(ctx,
		`SELECT id, group_id, kind, url, bucket, region, files, sort_order
		 FROM mirrors WHERE group_id = ? ORDER BY sort_order, id`,
		groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mirror
	for rows.Next() {
		var m Mirror
		var files []byte
		if err := rows.Scan(&m.ID, &m.GroupID, &m.Kind, &m.URL,
			&m.Bucket, &m.Region, &files, &m.SortOrder); err != nil {
			return nil, err
		}
		if len(files) > 0 {
			m.Files = json.RawMessage(files)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MirrorsFlatList(ctx context.Context) ([]string, error) {
	groups, err := s.MirrorGroupsAll(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, g := range groups {
		ms, err := s.MirrorsByGroup(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			out = append(out, m.URL)
		}
	}
	return out, nil
}

func (s *Store) MirrorsReplaceFlat(ctx context.Context, urls []string) error {
	out := make([]MirrorWithGroup, 0, len(urls))
	for _, u := range urls {
		out = append(out, MirrorWithGroup{
			Mirror:    Mirror{Kind: "http", URL: u},
			GroupName: "默认线路",
		})
	}
	return s.MirrorsReplaceAll(ctx, out)
}

// MirrorsReplaceAll 用 mirrors 整体覆盖当前列表。按 GroupName 聚合;
// 同名连续条目自动归同组,sort_order 按数组顺序赋值。
// 空 GroupName 视作 "默认线路"。
func (s *Store) MirrorsReplaceAll(ctx context.Context, mirrors []MirrorWithGroup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM mirrors`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mirror_groups`); err != nil {
		return err
	}

	// 按出现顺序去重组名;每个组创建一行,记录 group_id。
	groupOrder := make([]string, 0)
	groupIDs := make(map[string]int64)
	for _, m := range mirrors {
		name := m.GroupName
		if name == "" {
			name = "默认线路"
		}
		if _, ok := groupIDs[name]; ok {
			continue
		}
		gid, err := s.insertReturningID(ctx, tx, "mirror_groups",
			[]string{"name", "sort_order"}, name, len(groupOrder))
		if err != nil {
			return err
		}
		groupIDs[name] = gid
		groupOrder = append(groupOrder, name)
	}

	// 每组内按数组顺序写 mirrors。
	perGroupOrder := map[string]int{}
	for _, m := range mirrors {
		name := m.GroupName
		if name == "" {
			name = "默认线路"
		}
		kind := m.Kind
		if kind == "" {
			kind = "http"
		}
		var filesArg any
		if len(m.Files) > 0 {
			filesArg = []byte(m.Files)
		}
		if _, err := tx.ExecContext(ctx, s.rebind(
			`INSERT INTO mirrors (group_id, kind, url, bucket, region, files, sort_order)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`),
			groupIDs[name], kind, m.URL, m.Bucket, m.Region, filesArg, perGroupOrder[name]); err != nil {
			return err
		}
		perGroupOrder[name]++
	}
	return tx.Commit()
}

// MirrorsListAll 给管理后台用:返回带组名 + 完整 kind/bucket/region/files 的清单。
// 按 group sort_order,组内按 mirror sort_order。
func (s *Store) MirrorsListAll(ctx context.Context) ([]MirrorWithGroup, error) {
	groups, err := s.MirrorGroupsAll(ctx)
	if err != nil {
		return nil, err
	}
	var out []MirrorWithGroup
	for _, g := range groups {
		ms, err := s.MirrorsByGroup(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			out = append(out, MirrorWithGroup{Mirror: m, GroupName: g.Name})
		}
	}
	return out, nil
}

// MirrorGroupsListWithMirrors 给客户端 /client/online-download 用:
// 嵌套结构,直接序列化成 {groups:[{name, mirrors:[...]}]} 即可。
func (s *Store) MirrorGroupsListWithMirrors(ctx context.Context) ([]MirrorGroupWithMirrors, error) {
	groups, err := s.MirrorGroupsAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MirrorGroupWithMirrors, 0, len(groups))
	for _, g := range groups {
		ms, err := s.MirrorsByGroup(ctx, g.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, MirrorGroupWithMirrors{Group: g, Mirrors: ms})
	}
	return out, nil
}

// insertReturningID 处理 PG/SQLite 的 RETURNING id 与 MySQL 的 LastInsertId 差异。
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

func (s *Store) HotBundleGet(ctx context.Context, kind string) (*HotBundle, error) {
	const q = `SELECT kind, version, COALESCE(sha256,''), COALESCE(download_url,''), size, published_at
	           FROM hot_bundles WHERE kind = ?`
	var b HotBundle
	err := s.queryRow(ctx, q, kind).Scan(&b.Kind, &b.Version, &b.SHA256, &b.DownloadURL, &b.Size, &b.PublishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &HotBundle{Kind: kind, Size: -1}, nil
	}
	return &b, err
}

func (s *Store) HotBundleSet(ctx context.Context, b HotBundle) error {
	upsert, _ := s.d.Upsert(UpsertSpec{
		Table:        "hot_bundles",
		Columns:      []string{"kind", "version", "sha256", "download_url", "size", "published_at"},
		ConflictCols: []string{"kind"},
		UpdateCols:   []string{"version", "sha256", "download_url", "size", "published_at"},
	})
	_, err := s.exec(ctx, upsert, b.Kind, b.Version, b.SHA256, b.DownloadURL, b.Size, b.PublishedAt)
	return err
}

// ── 离线包 offline_package(单例 id=1)────────────────────────────────────

func (s *Store) OfflinePackageGet(ctx context.Context) (*OfflinePackage, error) {
	const q = `SELECT COALESCE(download_url,''), COALESCE(package_version,''), COALESCE(sha256,''),
	                  size, uploaded_at
	           FROM offline_package WHERE id = 1`
	var p OfflinePackage
	err := s.queryRow(ctx, q).Scan(&p.DownloadURL, &p.PackageVersion, &p.SHA256, &p.Size, &p.UploadedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &OfflinePackage{}, nil
	}
	return &p, err
}

func (s *Store) OfflinePackageSet(ctx context.Context, p OfflinePackage) error {
	upsert, _ := s.d.Upsert(UpsertSpec{
		Table:        "offline_package",
		Columns:      []string{"id", "download_url", "package_version", "sha256", "size", "uploaded_at"},
		ConflictCols: []string{"id"},
		UpdateCols:   []string{"download_url", "package_version", "sha256", "size", "uploaded_at"},
	})
	_, err := s.exec(ctx, upsert, 1, p.DownloadURL, p.PackageVersion, p.SHA256, p.Size, p.UploadedAt)
	return err
}

// ── 封禁 bans ────────────────────────────────────────────────────────────

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

// MirrorTrafficGetAll 返回所有镜像的流量统计行。
func (s *Store) MirrorTrafficGetAll(ctx context.Context) ([]MirrorTrafficRow, error) {
	rows, err := s.query(ctx,
		`SELECT url, today_bytes, month_bytes, cum_bytes, today_date, month_date,
		        daily_limit_bytes, speed_limit_bps
		 FROM mirror_traffic`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MirrorTrafficRow
	for rows.Next() {
		var r MirrorTrafficRow
		if err := rows.Scan(&r.URL, &r.TodayBytes, &r.MonthBytes, &r.CumBytes,
			&r.TodayDate, &r.MonthDate, &r.DailyLimitBytes, &r.SpeedLimitBps); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MirrorTrafficGet 返回单个镜像的流量行（不存在时返回空行，仅 URL 有值）。
func (s *Store) MirrorTrafficGet(ctx context.Context, url string) (MirrorTrafficRow, error) {
	r := MirrorTrafficRow{URL: url}
	err := s.queryRow(ctx,
		`SELECT today_bytes, month_bytes, cum_bytes, today_date, month_date,
		        daily_limit_bytes, speed_limit_bps
		 FROM mirror_traffic WHERE url = ?`, url).
		Scan(&r.TodayBytes, &r.MonthBytes, &r.CumBytes,
			&r.TodayDate, &r.MonthDate, &r.DailyLimitBytes, &r.SpeedLimitBps)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	return r, err
}

// MirrorTrafficAddBytes 累加指定镜像的今日/月/累计字节数；自动检测日期跨越并清零。
// 返回更新后的行（含重置后的值），方便调用方判断是否触发超限。
func (s *Store) MirrorTrafficAddBytes(ctx context.Context, url string, addBytes int64, today, month string) (MirrorTrafficRow, error) {
	cur, err := s.MirrorTrafficGet(ctx, url)
	if err != nil {
		return cur, err
	}
	// 跨日/跨月清零
	if cur.TodayDate != today {
		cur.TodayBytes = 0
		cur.TodayDate = today
	}
	if cur.MonthDate != month {
		cur.MonthBytes = 0
		cur.MonthDate = month
	}
	cur.TodayBytes += addBytes
	cur.MonthBytes += addBytes
	cur.CumBytes += addBytes

	upsert, _ := s.d.Upsert(UpsertSpec{
		Table:   "mirror_traffic",
		Columns: []string{"url", "today_bytes", "month_bytes", "cum_bytes", "today_date", "month_date", "daily_limit_bytes", "speed_limit_bps"},
		ConflictCols: []string{"url"},
		UpdateCols:   []string{"today_bytes", "month_bytes", "cum_bytes", "today_date", "month_date"},
	})
	_, err = s.exec(ctx, upsert,
		url, cur.TodayBytes, cur.MonthBytes, cur.CumBytes,
		cur.TodayDate, cur.MonthDate, cur.DailyLimitBytes, cur.SpeedLimitBps)
	return cur, err
}

// MirrorTrafficSetLimits 更新镜像的日限额和速度上限（不影响流量统计字段）。
func (s *Store) MirrorTrafficSetLimits(ctx context.Context, url string, dailyLimitBytes, speedLimitBps int64) error {
	upsert, _ := s.d.Upsert(UpsertSpec{
		Table:        "mirror_traffic",
		Columns:      []string{"url", "today_bytes", "month_bytes", "cum_bytes", "today_date", "month_date", "daily_limit_bytes", "speed_limit_bps"},
		ConflictCols: []string{"url"},
		UpdateCols:   []string{"daily_limit_bytes", "speed_limit_bps"},
	})
	_, err := s.exec(ctx, upsert, url, 0, 0, 0, "", "", dailyLimitBytes, speedLimitBps)
	return err
}
