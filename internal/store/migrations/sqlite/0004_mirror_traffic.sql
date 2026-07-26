CREATE TABLE IF NOT EXISTS mirror_traffic (
  url              TEXT    NOT NULL PRIMARY KEY,
  today_bytes      INTEGER NOT NULL DEFAULT 0,
  month_bytes      INTEGER NOT NULL DEFAULT 0,
  cum_bytes        INTEGER NOT NULL DEFAULT 0,
  today_date       TEXT    NOT NULL DEFAULT '',
  month_date       TEXT    NOT NULL DEFAULT '',
  daily_limit_bytes INTEGER NOT NULL DEFAULT 0,
  speed_limit_bps  INTEGER NOT NULL DEFAULT 0
);
