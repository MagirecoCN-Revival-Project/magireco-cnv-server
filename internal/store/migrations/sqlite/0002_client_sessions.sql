-- client_sessions:握手阶段(/client/init)签发的 access_token。

CREATE TABLE IF NOT EXISTS client_sessions (
  access_token    TEXT    NOT NULL PRIMARY KEY,
  device_id       TEXT    NOT NULL,
  signature       TEXT,
  client_version  TEXT,
  channel         TEXT,
  created_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL,
  last_seen_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_client_sessions_device  ON client_sessions(device_id);
CREATE INDEX IF NOT EXISTS idx_client_sessions_expires ON client_sessions(expires_at);
