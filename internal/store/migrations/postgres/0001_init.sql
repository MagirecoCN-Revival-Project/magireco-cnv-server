-- 魔法纪录 CNV 服务端 · 主节点 PostgreSQL 初始 schema
-- 所有时间戳为 Unix 毫秒(BIGINT),便于与客户端协议对齐。
-- 副节点不连接数据库,这份 schema 仅在主节点应用。

CREATE TABLE IF NOT EXISTS admins (
  id              TEXT PRIMARY KEY,
  username        TEXT NOT NULL UNIQUE,
  email           TEXT NOT NULL UNIQUE,
  password_hash   TEXT NOT NULL,
  role            TEXT NOT NULL DEFAULT 'admin',       -- super_admin | admin | readonly
  created_at      BIGINT NOT NULL,
  last_login_at   BIGINT
);

CREATE TABLE IF NOT EXISTS accounts (
  id              TEXT PRIMARY KEY,                    -- acc_xxxxxxxx
  username        TEXT NOT NULL UNIQUE,
  email           TEXT NOT NULL UNIQUE,
  password_hash   TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'active',      -- active | disabled
  created_at      BIGINT NOT NULL,
  last_login_at   BIGINT,
  login_count     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS account_sessions (
  token           TEXT PRIMARY KEY,
  account_id      TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  device_name     TEXT,
  os              TEXT,
  ip              TEXT,
  region          TEXT,
  created_at      BIGINT NOT NULL,
  last_seen_at    BIGINT NOT NULL,
  expires_at      BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_account_sessions_account ON account_sessions(account_id);

CREATE TABLE IF NOT EXISTS admin_sessions (
  token           TEXT PRIMARY KEY,
  admin_id        TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  created_at      BIGINT NOT NULL,
  expires_at      BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
  device_id       TEXT PRIMARY KEY,
  signature       TEXT,
  first_seen      BIGINT NOT NULL,
  last_seen       BIGINT NOT NULL,
  client_version  TEXT
);

-- 封禁:活跃 + 历史同表,active 区分
CREATE TABLE IF NOT EXISTS bans (
  id              TEXT PRIMARY KEY,
  device_id       TEXT NOT NULL,
  reason          TEXT NOT NULL,
  issued_at       BIGINT NOT NULL,
  expire_time     BIGINT,                              -- NULL = 永久;否则 Unix 毫秒
  issued_by       TEXT NOT NULL,                       -- 管理员 username 或 "system"
  auto            BOOLEAN NOT NULL DEFAULT FALSE,
  lifted_at       BIGINT,
  lifted_by       TEXT,
  active          BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE INDEX IF NOT EXISTS idx_bans_device_active ON bans(device_id, active);

-- 云存档:每账号一份 JSON blob
CREATE TABLE IF NOT EXISTS saves (
  account_id      TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  data            JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at      BIGINT NOT NULL,
  size_bytes      BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS email_codes (
  id              BIGSERIAL PRIMARY KEY,
  email           TEXT NOT NULL,
  code            TEXT NOT NULL,
  purpose         TEXT NOT NULL,                       -- register | change_email | reset
  expires_at      BIGINT NOT NULL,
  consumed        BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX IF NOT EXISTS idx_email_codes_lookup ON email_codes(email, purpose);

CREATE TABLE IF NOT EXISTS cap_challenges (
  token           TEXT PRIMARY KEY,
  c               INTEGER NOT NULL,
  d               INTEGER NOT NULL,
  s               TEXT NOT NULL,
  expires_at      BIGINT NOT NULL,
  solved          BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS cap_tokens (
  token           TEXT PRIMARY KEY,
  expires_at      BIGINT NOT NULL,
  used            BOOLEAN NOT NULL DEFAULT FALSE
);

-- 单例 key-value 配置:server 状态 / features / versions / tasks / s3 / captcha 等
CREATE TABLE IF NOT EXISTS config (
  key             TEXT PRIMARY KEY,
  value           JSONB NOT NULL
);

-- 镜像组与镜像
CREATE TABLE IF NOT EXISTS mirror_groups (
  id              BIGSERIAL PRIMARY KEY,
  name            TEXT NOT NULL,
  sort_order      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS mirrors (
  id              BIGSERIAL PRIMARY KEY,
  group_id        BIGINT NOT NULL REFERENCES mirror_groups(id) ON DELETE CASCADE,
  kind            TEXT NOT NULL DEFAULT 'http',        -- http | s3
  url             TEXT NOT NULL,
  bucket          TEXT,                                -- 仅 kind='s3' 用:桶名
  region          TEXT,                                -- 仅 kind='s3' 用:区域
  files           JSONB,                               -- 内联文件清单(可空,触发 S3 XML 发现)
  sort_order      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS hot_bundles (
  kind            TEXT PRIMARY KEY,                    -- js | scenario
  version         INTEGER NOT NULL DEFAULT 0,
  sha256          TEXT,
  download_url    TEXT,
  size            BIGINT NOT NULL DEFAULT -1,
  published_at    BIGINT
);

CREATE TABLE IF NOT EXISTS offline_package (
  id              INTEGER PRIMARY KEY CHECK (id = 1),  -- 单例
  download_url    TEXT,
  package_version TEXT,
  sha256          TEXT,
  size            BIGINT NOT NULL DEFAULT 0,
  uploaded_at     BIGINT
);

CREATE TABLE IF NOT EXISTS audit_log (
  id              TEXT PRIMARY KEY,
  ts              BIGINT NOT NULL,
  actor           TEXT NOT NULL,
  type            TEXT NOT NULL,
  target          TEXT,
  details         JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_audit_log_ts ON audit_log(ts DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_type ON audit_log(type);
CREATE INDEX IF NOT EXISTS idx_audit_log_actor ON audit_log(actor);

-- 副节点心跳记录(主节点持有)
CREATE TABLE IF NOT EXISTS secondary_nodes (
  id              TEXT PRIMARY KEY,
  public_url      TEXT NOT NULL,
  region          TEXT,
  region_label    TEXT,
  files           JSONB,
  cpu_pct         REAL,
  mem_pct         REAL,
  uptime_sec      BIGINT,
  bytes_served    BIGINT,
  active_dl       INTEGER,
  cache_hit       REAL,
  egress_bps      BIGINT,
  registered_at   BIGINT NOT NULL,
  last_seen       BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_secondary_last_seen ON secondary_nodes(last_seen DESC);
