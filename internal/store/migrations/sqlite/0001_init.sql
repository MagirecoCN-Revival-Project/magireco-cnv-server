-- 魔法纪录 CNV 服务端 · SQLite 初始 schema(主节点)
-- 与 postgres/0001_init.sql 一一对应。要求 SQLite >= 3.35(支持 RETURNING)。
-- 所有时间戳为 Unix 毫秒(INTEGER)。
-- JSON 使用 TEXT 存储 + Go 代码 marshal/unmarshal。

CREATE TABLE IF NOT EXISTS admins (
  id              TEXT    NOT NULL PRIMARY KEY,
  username        TEXT    NOT NULL UNIQUE,
  email           TEXT    NOT NULL UNIQUE,
  password_hash   TEXT    NOT NULL,
  role            TEXT    NOT NULL DEFAULT 'admin',
  created_at      INTEGER NOT NULL,
  last_login_at   INTEGER
);

CREATE TABLE IF NOT EXISTS accounts (
  id              TEXT    NOT NULL PRIMARY KEY,
  username        TEXT    NOT NULL UNIQUE,
  email           TEXT    NOT NULL UNIQUE,
  password_hash   TEXT    NOT NULL,
  status          TEXT    NOT NULL DEFAULT 'active',
  created_at      INTEGER NOT NULL,
  last_login_at   INTEGER,
  login_count     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS account_sessions (
  token           TEXT    NOT NULL PRIMARY KEY,
  account_id      TEXT    NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  device_name     TEXT,
  os              TEXT,
  ip              TEXT,
  region          TEXT,
  created_at      INTEGER NOT NULL,
  last_seen_at    INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_account_sessions_account ON account_sessions(account_id);

CREATE TABLE IF NOT EXISTS admin_sessions (
  token           TEXT    NOT NULL PRIMARY KEY,
  admin_id        TEXT    NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  created_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
  device_id       TEXT    NOT NULL PRIMARY KEY,
  signature       TEXT,
  first_seen      INTEGER NOT NULL,
  last_seen       INTEGER NOT NULL,
  client_version  TEXT
);

CREATE TABLE IF NOT EXISTS bans (
  id              TEXT    NOT NULL PRIMARY KEY,
  device_id       TEXT    NOT NULL,
  reason          TEXT    NOT NULL,
  issued_at       INTEGER NOT NULL,
  expire_time     INTEGER,
  issued_by       TEXT    NOT NULL,
  auto            INTEGER NOT NULL DEFAULT 0,     -- BOOLEAN as 0/1
  lifted_at       INTEGER,
  lifted_by       TEXT,
  active          INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_bans_device_active ON bans(device_id, active);

CREATE TABLE IF NOT EXISTS saves (
  account_id      TEXT    NOT NULL PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  data            TEXT    NOT NULL DEFAULT '{}',
  updated_at      INTEGER NOT NULL,
  size_bytes      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS email_codes (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  email           TEXT    NOT NULL,
  code            TEXT    NOT NULL,
  purpose         TEXT    NOT NULL,
  expires_at      INTEGER NOT NULL,
  consumed        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_email_codes_lookup ON email_codes(email, purpose);

CREATE TABLE IF NOT EXISTS cap_challenges (
  token           TEXT    NOT NULL PRIMARY KEY,
  c               INTEGER NOT NULL,
  d               INTEGER NOT NULL,
  s               TEXT    NOT NULL,
  expires_at      INTEGER NOT NULL,
  solved          INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS cap_tokens (
  token           TEXT    NOT NULL PRIMARY KEY,
  expires_at      INTEGER NOT NULL,
  used            INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS config (
  key             TEXT    NOT NULL PRIMARY KEY,
  value           TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
  id              TEXT    NOT NULL PRIMARY KEY,
  ts              INTEGER NOT NULL,
  actor           TEXT    NOT NULL,
  type            TEXT    NOT NULL,
  target          TEXT,
  details         TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_audit_log_ts ON audit_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_type ON audit_log(type);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor ON audit_log(actor);

