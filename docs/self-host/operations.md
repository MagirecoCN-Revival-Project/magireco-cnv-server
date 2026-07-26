# 日常运维

把服务托管成长期运行的进程,配好日志、备份与监控。

## 用 systemd 托管

### 推荐:在线一键(root)

仿 MCSManager 风格的远程拉脚本 + `bash` 执行,**不需要 clone 仓库**:

```bash
sudo su -c "wget -qO- https://raw.githubusercontent.com/MagirecoCN-Revival-Project/magirecocn-resource-server/main/deploy/install.sh | bash"
# 或用 curl
sudo su -c "curl -fsSL https://raw.githubusercontent.com/MagirecoCN-Revival-Project/magirecocn-resource-server/main/deploy/install.sh | bash"
```

弹出交互菜单,选 `1) panel` / `2) node-business` / `3) node-edge`(管道里的 `stdin` 被脚本读 `/dev/tty` 拿到)。
也能跳过菜单直接指定角色:

```bash
sudo su -c "wget -qO- https://raw.githubusercontent.com/MagirecoCN-Revival-Project/magirecocn-resource-server/main/deploy/install.sh | bash -s -- panel"
sudo su -c "wget -qO- https://raw.githubusercontent.com/MagirecoCN-Revival-Project/magirecocn-resource-server/main/deploy/install.sh | bash -s -- node-business"
sudo su -c "wget -qO- https://raw.githubusercontent.com/MagirecoCN-Revival-Project/magirecocn-resource-server/main/deploy/install.sh | bash -s -- node-edge"
```

二进制脚本会从 [GitHub Release latest](https://github.com/MagirecoCN-Revival-Project/magirecocn-resource-server/releases/latest) 按 `uname -m` 选 `amd64` / `arm64` 自动下载;systemd unit 与边缘 `.env` 模板**内嵌**在脚本里,
没有外部模板文件依赖。

### 从仓库 checkout(开发/离线包)

仓库 `deploy/` 下有同源的 unit 模板,本地执行时优先用本地文件,二进制也优先用 `./magireco-X`、`./bin/`、`/usr/local/bin/`:

```bash
sudo ./deploy/install.sh panel          # 面板
sudo ./deploy/install.sh node-business  # 业务节点
sudo ./deploy/install.sh node-edge      # 边缘节点
#   --bin PATH           指定二进制位置(默认本地查找,都没就下载 Release)
#   --non-interactive    node-edge 跳过交互式 prompt
```

脚本会:

- 建系统用户 `magireco:magireco`(`/usr/sbin/nologin`,无家目录);
- 把二进制装到 `/opt/magireco/<角色>/bin/`,状态目录 `/var/lib/magireco/<角色>/`,日志 `/var/log/magireco/<角色>.log`;
- 把 `deploy/systemd/magireco-<角色>.service` 复制到 `/etc/systemd/system/`,加固项(`NoNewPrivileges` / `ProtectSystem=strict` / `ReadWritePaths` …)已经填好;
- 写 `/etc/magireco/<panel|node-business|node-edge>.env`(权限 `0640 root:magireco`,**原子写**)。

**配置归属**:

| 角色 | 配置归谁管 | 脚本管什么 |
|---|---|---|
| `panel` | 面板自己的 `/install/*` 向导 | 只生成 `CNV_PANEL_KEY` 与 `CNV_ADDR`,其余由向导落地 |
| `node-business` | 面板向导(同凭证写入)或节点自己的 `/setup/*` 向导 | 生成 `CNV_ADMIN_JWT_SECRET`,`CNV_DB_URL` 留空待向导填,`enable` 但**不 start** |
| `node-edge` | `deploy/edge.env.example` 复制成 `/etc/magireco/node-edge.env` | 必填 `CNV_PUBLIC_URL` / `CNV_PRIMARY_RES_DIR` 在交互式提问 |

边缘节点 `.env` 在仓库 `.gitignore` 里以 `*.env` 模式忽略(`*.env.example` 是模板,显式保留),
防止把节点 secret 误提交。

跑完后:

```bash
sudo systemctl status magireco-panel
sudo systemctl status magireco-node-business
sudo systemctl status magireco-node-edge
```

业务节点 `enable` 但不 start —— 等面板向导或 `/setup/*` 填完 `CNV_DB_URL` 再 `systemctl start magireco-node-business`。

### 手工写 unit(理解部署细节用)

不用脚本时,基本结构(以业务节点为例):

```ini
# /etc/systemd/system/magireco-node-business.service
[Unit]
Description=MagiReco Revival Business Node
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=magireco
Group=magireco
WorkingDirectory=/var/lib/magireco/node-business
EnvironmentFile=/etc/magireco/node-business.env
ExecStart=/opt/magireco/node-business/bin/magireco-node
Restart=on-failure
RestartSec=5s

NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/magireco/node-business /var/log/magireco
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

`/etc/magireco/node-business.env`(权限 `0640 root:magireco`):

```bash
CNV_NODE_ROLE=business
CNV_ADDR=:8080
CNV_CONTROL_ADDR=127.0.0.1:9090
CNV_ADMIN_JWT_SECRET=...
CNV_DB_URL=postgres://user:pass@localhost:5432/magireco?sslmode=require
CNV_SIGNATURE_WHITELIST=...
CNV_REQUIRE_SIGNATURE=true
CNV_TRUST_PROXY=loopback
```

启用:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now magireco-node-business
sudo systemctl status magireco-node-business
```

面板与边缘节点同构,服务名分别为 `magireco-panel.service` 与 `magireco-node-edge.service`。

## 日志

进程用结构化 JSON 日志打到 stdout,systemd 会收进 journald:

```bash
journalctl -u magireco-node-business -f              # 实时跟踪
journalctl -u magireco-node-business --since "1h ago" # 最近一小时
journalctl -u magireco-node-business -p warning       # 只看 WARN 及以上
```

unit 也通过 `StandardOutput=append:/var/log/magireco/<角色>.log` 把日志旁路到文件,
方便 logrotate 处理(脚本会写 `/etc/logrotate.d/magireco`)。

每条 HTTP 请求一行 INFO(方法、路径、状态码、字节数、耗时、来源 IP)。`panic` 会被恢复中间件转成 500 并打 ERROR + 栈。

**值得留意的 WARN**:

| 日志 | 含义 | 处理 |
|---|---|---|
| `收到空 signature,但未配置白名单` | 签名闸门没开 | 生产应配 `CNV_SIGNATURE_WHITELIST` |
| `client integrity rejected` | 有改包/伪造渠道客户端被拒 | 正常风控,频繁则关注来源 IP |
| `接受任意 signature` | 白名单为空,放行所有 | 同上 |

## 后台定时任务

业务节点内置一组定时任务(无需 cron),周期可在管理后台「定时任务」页调整:

| 任务 | 默认周期 | 作用 |
|---|---|---|
| 封禁过期清理 | 60s | 把到期的封禁置为失效 |
| 会话 GC | 300s | 清理过期的玩家/管理员会话 |
| 心跳超时清理 | 30s | 从内存表移除超时(>120s)的在线设备 |
| 副节点失联清理 | 60s | 删除 >180s 没心跳的副节点 |
| 离线包自动打包 | 按配置 | 定期把资源目录打成离线整包 |

任务在进程内以独立 goroutine 运行,随进程优雅退出(收到 SIGINT/SIGTERM 时停止)。

## 备份

最重要的是数据库。按你选的库定期快照:

```bash
# PostgreSQL —— 每日 cron
pg_dump magireco | gzip > /backup/magireco-$(date +%F).sql.gz

# SQLite —— 热备(不停服)
sqlite3 /srv/magireco/magireco.db ".backup /backup/magireco-$(date +%F).db"
```

**核心三张表**:`config`(运行配置)、`accounts`(玩家身份)、`saves`(云存档)。整库备份自然都覆盖。

资源文件和离线整包通常可由源头重建,优先级低于数据库;但若来之不易也一并备份。

## 升级

1. 备份数据库。
2. 拉新代码 / 换新二进制。
3. 重启服务。迁移在启动时自动跑(幂等,只会新增表/索引,不动既有数据)。
4. 观察日志确认 `业务节点启动` 且无迁移报错。

```bash
sudo systemctl restart magireco-node-business
journalctl -u magireco-node-business --since "1 min ago"
```

> 用 `deploy/install.sh` 重跑同一角色就是升级二进制 —— 脚本幂等,只覆盖 `bin/`
> 与 unit,**不动** 现有 `.env` 与 `/var/lib/magireco/<角色>/`。跑完别忘 `systemctl restart`。

::: tip 灰度建议
重大升级先在一台预发环境用**生产数据库的副本**跑一遍,确认迁移与行为无误再上。
:::

## 平滑停机

进程监听 `SIGINT` / `SIGTERM`,收到后:

- 停止接收新连接,给在途请求最多 10 秒优雅收尾。
- 定时任务 goroutine 随 context 取消而退出。

`systemctl stop` / `Ctrl-C` 都会触发,不会粗暴 kill 掉在途请求。

## 监控指标(自建)

项目没有内置 Prometheus 端点。轻量做法:

- 用 journald 日志里的 `status` / `dur_ms` 字段聚合错误率与延迟(如 Loki + Grafana)。
- 边缘节点用 `/healthz` 给负载均衡探活。
- 数据库层面监控连接数(连接池上限 16)、慢查询。
- 管理后台「仪表盘」「心跳监控」可肉眼看在线规模与下载健康度。

## 常见运维场景

| 场景 | 怎么做 |
|---|---|
| 临时停服维护 | 后台「服务器控制」切 `维护中` + 维护文案;无需重启 |
| 紧急封禁某设备 | 后台「设备封禁」按 device_id 封;或「心跳监控」里直接封在线设备 |
| 强制所有人更新 | 「版本管理」把旧版本移出白名单,旧客户端握手即被 `force_update` |
| 换下载线路 | 「资源管理」改镜像组;或「心跳监控」给个别卡住的设备手动换线 |
| 重置玩家/管理员密码 | `admintool reset-account` / `reset-admin` |
| 轮换资源签名密钥 | 「资源管理」页操作 |
