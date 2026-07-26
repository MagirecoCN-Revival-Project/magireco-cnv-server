-- client_sessions:握手阶段(/client/init)签发的 access_token。

CREATE TABLE IF NOT EXISTS client_sessions (
  access_token    VARCHAR(128) NOT NULL PRIMARY KEY,
  device_id       VARCHAR(128) NOT NULL,
  signature       VARCHAR(128),
  client_version  VARCHAR(64),
  channel         VARCHAR(32),
  created_at      BIGINT       NOT NULL,
  expires_at      BIGINT       NOT NULL,
  last_seen_at    BIGINT       NOT NULL,
  INDEX idx_client_sessions_device  (device_id),
  INDEX idx_client_sessions_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
