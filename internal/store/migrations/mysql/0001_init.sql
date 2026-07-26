-- 魔法纪录 CNV 服务端 · MySQL 初始 schema(主节点)
-- 与 postgres/0001_init.sql 一一对应。要求 MySQL >= 5.7(JSON 列)。
-- 所有时间戳为 Unix 毫秒(BIGINT)。

SET NAMES utf8mb4;
SET sql_mode = (SELECT CONCAT(@@sql_mode, ',NO_ZERO_DATE,NO_ZERO_IN_DATE'));

CREATE TABLE IF NOT EXISTS admins (
  id              VARCHAR(64)  NOT NULL PRIMARY KEY,
  username        VARCHAR(64)  NOT NULL UNIQUE,
  email           VARCHAR(255) NOT NULL UNIQUE,
  password_hash   TEXT         NOT NULL,
  role            VARCHAR(32)  NOT NULL DEFAULT 'admin',
  created_at      BIGINT       NOT NULL,
  last_login_at   BIGINT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS accounts (
  id              VARCHAR(64)  NOT NULL PRIMARY KEY,
  username        VARCHAR(64)  NOT NULL UNIQUE,
  email           VARCHAR(255) NOT NULL UNIQUE,
  password_hash   TEXT         NOT NULL,
  status          VARCHAR(32)  NOT NULL DEFAULT 'active',
  created_at      BIGINT       NOT NULL,
  last_login_at   BIGINT,
  login_count     INT          NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS account_sessions (
  token           VARCHAR(128) NOT NULL PRIMARY KEY,
  account_id      VARCHAR(64)  NOT NULL,
  device_name     VARCHAR(255),
  os              VARCHAR(128),
  ip              VARCHAR(64),
  region          VARCHAR(128),
  created_at      BIGINT       NOT NULL,
  last_seen_at    BIGINT       NOT NULL,
  expires_at      BIGINT       NOT NULL,
  INDEX idx_account_sessions_account (account_id),
  FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS admin_sessions (
  token           VARCHAR(128) NOT NULL PRIMARY KEY,
  admin_id        VARCHAR(64)  NOT NULL,
  created_at      BIGINT       NOT NULL,
  expires_at      BIGINT       NOT NULL,
  FOREIGN KEY (admin_id) REFERENCES admins(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS devices (
  device_id       VARCHAR(128) NOT NULL PRIMARY KEY,
  signature       VARCHAR(128),
  first_seen      BIGINT       NOT NULL,
  last_seen       BIGINT       NOT NULL,
  client_version  VARCHAR(64)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS bans (
  id              VARCHAR(64)  NOT NULL PRIMARY KEY,
  device_id       VARCHAR(128) NOT NULL,
  reason          TEXT         NOT NULL,
  issued_at       BIGINT       NOT NULL,
  expire_time     BIGINT,
  issued_by       VARCHAR(64)  NOT NULL,
  auto            BOOLEAN      NOT NULL DEFAULT FALSE,
  lifted_at       BIGINT,
  lifted_by       VARCHAR(64),
  active          BOOLEAN      NOT NULL DEFAULT TRUE,
  INDEX idx_bans_device_active (device_id, active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS saves (
  account_id      VARCHAR(64)  NOT NULL PRIMARY KEY,
  data            JSON         NOT NULL,
  updated_at      BIGINT       NOT NULL,
  size_bytes      BIGINT       NOT NULL DEFAULT 0,
  FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS email_codes (
  id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  email           VARCHAR(255) NOT NULL,
  code            VARCHAR(64)  NOT NULL,
  purpose         VARCHAR(32)  NOT NULL,
  expires_at      BIGINT       NOT NULL,
  consumed        BOOLEAN      NOT NULL DEFAULT FALSE,
  INDEX idx_email_codes_lookup (email, purpose)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS cap_challenges (
  token           VARCHAR(128) NOT NULL PRIMARY KEY,
  c               INT          NOT NULL,
  d               INT          NOT NULL,
  s               TEXT         NOT NULL,
  expires_at      BIGINT       NOT NULL,
  solved          BOOLEAN      NOT NULL DEFAULT FALSE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS cap_tokens (
  token           VARCHAR(128) NOT NULL PRIMARY KEY,
  expires_at      BIGINT       NOT NULL,
  used            BOOLEAN      NOT NULL DEFAULT FALSE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS config (
  `key`           VARCHAR(64)  NOT NULL PRIMARY KEY,
  value           JSON         NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS mirror_groups (
  id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name            VARCHAR(128) NOT NULL,
  sort_order      INT          NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS mirrors (
  id              BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  group_id        BIGINT       NOT NULL,
  kind            VARCHAR(16)  NOT NULL DEFAULT 'http',
  url             VARCHAR(512) NOT NULL,
  bucket          VARCHAR(255),
  region          VARCHAR(64),
  files           JSON,
  sort_order      INT          NOT NULL DEFAULT 0,
  FOREIGN KEY (group_id) REFERENCES mirror_groups(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS hot_bundles (
  kind            VARCHAR(32)  NOT NULL PRIMARY KEY,
  version         INT          NOT NULL DEFAULT 0,
  sha256          VARCHAR(128),
  download_url    VARCHAR(512),
  size            BIGINT       NOT NULL DEFAULT -1,
  published_at    BIGINT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS offline_package (
  id              INT          NOT NULL PRIMARY KEY,
  download_url    VARCHAR(512),
  package_version VARCHAR(64),
  sha256          VARCHAR(128),
  size            BIGINT       NOT NULL DEFAULT 0,
  uploaded_at     BIGINT,
  CHECK (id = 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS audit_log (
  id              VARCHAR(64)  NOT NULL PRIMARY KEY,
  ts              BIGINT       NOT NULL,
  actor           VARCHAR(64)  NOT NULL,
  type            VARCHAR(64)  NOT NULL,
  target          VARCHAR(255),
  details         JSON         NOT NULL,
  INDEX idx_audit_log_ts (ts DESC),
  INDEX idx_audit_log_type (type),
  INDEX idx_audit_log_actor (actor)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS secondary_nodes (
  id              VARCHAR(128) NOT NULL PRIMARY KEY,
  public_url      VARCHAR(512) NOT NULL,
  region          VARCHAR(64),
  region_label    VARCHAR(64),
  files           JSON,
  cpu_pct         DOUBLE,
  mem_pct         DOUBLE,
  uptime_sec      BIGINT,
  bytes_served    BIGINT,
  active_dl       INT,
  cache_hit       DOUBLE,
  egress_bps      BIGINT,
  registered_at   BIGINT       NOT NULL,
  last_seen       BIGINT       NOT NULL,
  INDEX idx_secondary_last_seen (last_seen DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
