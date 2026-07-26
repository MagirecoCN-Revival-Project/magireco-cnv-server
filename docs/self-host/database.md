# 选择数据库

主节点支持 **PostgreSQL / MySQL / SQLite** 三选一。同一套代码通过**方言抽象**适配三者(技术细节见 [存储层与多方言抽象](/contributing/store-dialects))。这里只讲怎么选、连接串怎么写。

## 怎么选

| | SQLite | PostgreSQL | MySQL |
|---|---|---|---|
| 运维成本 | **零**(单文件) | 中 | 中 |
| 适合规模 | 单机 / 小规模 | 任意 / 生产推荐 | 已有 MySQL 设施时 |
| 并发写 | 一般(WAL 模式已优化) | 强 | 强 |
| 备份 | 复制一个文件 | `pg_dump` | `mysqldump` |
| 跨节点共享 | ❌ 不适合 | ✅ | ✅ |

**没有特别理由就用 SQLite 起步**,需要横向扩展或更强并发时再迁到 PostgreSQL。

::: tip 副节点不需要数据库
只有**主节点**连数据库。副节点不碰库,所以"跨节点共享"那一栏只在你有**多个主节点**(罕见)时才相关。
:::

## 连接串(`CNV_DB_URL`)

驱动按 **DSN 前缀自动识别**,你不需要额外指定驱动名。

### SQLite

```bash
# 标准形式
export CNV_DB_URL='sqlite:///srv/magireco/magireco.db'

# 也接受裸文件路径 / file: 前缀 / 内存库
export CNV_DB_URL='/srv/magireco/magireco.db'
export CNV_DB_URL='file:/srv/magireco/magireco.db'
export CNV_DB_URL=':memory:'        # 仅测试用,重启即丢
```

启动时会自动启用这些 PRAGMA(无需手动配置):

| PRAGMA | 值 | 作用 |
|---|---|---|
| `journal_mode` | `WAL` | 预写日志,读写并发更好 |
| `synchronous` | `NORMAL` | 性能与安全的平衡点 |
| `foreign_keys` | `ON` | 启用外键级联(如删账号连带删会话) |
| `busy_timeout` | `5000` | 写锁竞争时最多等 5 秒 |

::: warning SQLite 目录要存在
连接串里的目录(如 `/srv/magireco/`)必须**已存在**且进程有写权限。文件本身会自动创建。
:::

### PostgreSQL(≥ 14)

```bash
export CNV_DB_URL='postgres://user:pass@localhost:5432/magireco?sslmode=disable'

# 生产里应当启用 TLS:
export CNV_DB_URL='postgres://user:pass@db.internal:5432/magireco?sslmode=require'
```

用的是 `pgx` 驱动。`postgresql://` 前缀也接受。

### MySQL(≥ 5.7)

```bash
export CNV_DB_URL='mysql://user:pass@localhost:3306/magireco?parseTime=true'
```

::: warning 一定要带 parseTime=true
MySQL 驱动需要 `parseTime=true` 才能正确处理时间列。内部会把 `mysql://host:port/db` 规范化成 go-sql-driver 需要的 `user:pass@tcp(host:port)/db` 形式,你只管按上面写即可。
:::

## 迁移(自动)

主节点启动时会**自动执行内嵌迁移**:

- 全部是 `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`,**幂等**,重复启动安全。
- 每种数据库有各自的建表 SQL(类型差异:`JSONB` vs `JSON` vs `TEXT`,自增主键写法不同等)。
- 你**不需要**手动建表。

如果你用外部工具(如 Flyway / Atlas)管理 schema,想关掉自动迁移:

```bash
export CNV_SKIP_MIGRATE=1
```

## 表清单(概览)

迁移会创建约 18 张表。完整字段见 **[数据模型](/architecture/data-model)**,这里给个全貌:

| 分类 | 表 |
|---|---|
| 身份 | `admins`, `accounts` |
| 会话 | `admin_sessions`, `account_sessions`, `client_sessions` |
| 设备/封禁 | `devices`, `bans` |
| 玩家数据 | `saves`(云存档) |
| 验证 | `email_codes`, `cap_challenges`, `cap_tokens` |
| 配置/资源 | `config`(KV), `mirror_groups`, `mirrors`, `hot_bundles`, `offline_package` |
| 运维 | `audit_log`, `secondary_nodes` |

## 备份建议

| 数据库 | 备份命令 |
|---|---|
| SQLite | 停写后 `cp magireco.db magireco.db.bak`,或用 `sqlite3 magireco.db ".backup bak.db"` 热备 |
| PostgreSQL | `pg_dump magireco > magireco.sql` |
| MySQL | `mysqldump magireco > magireco.sql` |

**最该备份的是 `config`、`accounts`、`saves` 三张表** —— 分别是你的运行配置、玩家身份、玩家存档。把数据库整体定期快照即可。

## 切换数据库

想从 SQLite 迁到 PostgreSQL?目前没有内置迁移工具,但因为 schema 简单,可以:

1. 用新的 `CNV_DB_URL` 启动一次,让它在目标库建好表结构(`CNV_SKIP_MIGRATE` 保持默认)。
2. 用通用工具(如 [pgloader](https://pgloader.io/) for SQLite→PG)迁移数据,或写个一次性脚本按表搬运。
3. 校验 `config` / `accounts` / `saves` 行数一致后切换。

数据量通常很小,手工搬运也不费事。
