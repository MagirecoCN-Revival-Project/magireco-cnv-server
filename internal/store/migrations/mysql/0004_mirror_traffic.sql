CREATE TABLE IF NOT EXISTS mirror_traffic (
  url              VARCHAR(2048) NOT NULL PRIMARY KEY,
  today_bytes      BIGINT        NOT NULL DEFAULT 0,
  month_bytes      BIGINT        NOT NULL DEFAULT 0,
  cum_bytes        BIGINT        NOT NULL DEFAULT 0,
  today_date       VARCHAR(16)   NOT NULL DEFAULT '',
  month_date       VARCHAR(8)    NOT NULL DEFAULT '',
  daily_limit_bytes BIGINT       NOT NULL DEFAULT 0,
  speed_limit_bps  BIGINT        NOT NULL DEFAULT 0
);
