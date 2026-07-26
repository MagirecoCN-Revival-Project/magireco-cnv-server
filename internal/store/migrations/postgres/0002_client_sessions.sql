-- client_sessions:握手阶段(/client/init)签发的 access_token。
-- 客户端在 /client/method-select、/online-download、/heartbeat、/hot-update 的
-- authTriple{device_id, access_token, signature} 里把 token 带回来,服务端校验存在
-- 且未过期即放行;无须关联玩家账号(那是 account_sessions 的事)。

CREATE TABLE IF NOT EXISTS client_sessions (
  access_token    TEXT PRIMARY KEY,
  device_id       TEXT NOT NULL,
  signature       TEXT,
  client_version  TEXT,
  channel         TEXT,
  created_at      BIGINT NOT NULL,
  expires_at      BIGINT NOT NULL,
  last_seen_at    BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_client_sessions_device ON client_sessions(device_id);
CREATE INDEX IF NOT EXISTS idx_client_sessions_expires ON client_sessions(expires_at);
