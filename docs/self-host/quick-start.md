# 快速部署

把业务节点跑起来，建第一个管理员，登录后台。预计 5–10 分钟。

## 1. 拿到代码

```bash
git clone https://github.com/magirecocn-revival-project/magirecocn-api-server.git
cd magirecocn-api-server
go mod download
```

## 2. 选一种数据库

三选一，设置 `CNV_DB_URL`。驱动会按**前缀自动识别**，你不用指定：

```bash
# A) SQLite —— 单机最省事，零运维
export CNV_DB_URL='sqlite:///srv/magireco/magireco.db'

# B) PostgreSQL ≥ 14
# export CNV_DB_URL='postgres://user:pass@localhost:5432/magireco?sslmode=disable'

# C) MySQL ≥ 5.7
# export CNV_DB_URL='mysql://user:pass@localhost:3306/magireco?parseTime=true'
```

迁移会在启动时自动跑，无需手动建表。详见 **[选择数据库](./database)**。

## 3. 设置必需的环境变量

```bash
# 游戏管理后台 cookie 完整性密钥（≥16 字符，建议 32+）
export CNV_ADMIN_JWT_SECRET="$(openssl rand -hex 32)"
```

::: tip resource_token 密钥不用管
resource_token 的 HMAC 签名根密钥（`CNV_RESOURCE_TOKEN_SECRET`）**不必手动提供** —— 首次启动会自动生成 32 字节并持久化到 `config` 表，重启复用。
:::

## 4. 启动业务节点

```bash
go run ./cmd/node
# 默认监听 :8080 + 管控通道 127.0.0.1:9090
# 看到 "业务节点启动" 日志即成功
# 同时会打印首次生成的节点密钥（保存下来，后续配到面板用）
```

预编译二进制：

```bash
./magireco-node
```

验证它活着：

```bash
curl -i http://localhost:8080/healthz
# 应返回 ok
```

## 5. 创建第一个游戏管理员

服务端**不会**自动建管理员。用随附的 `admintool` 引导：

```bash
go run ./cmd/admintool create-admin \
  -dsn="$CNV_DB_URL" \
  -email=admin@example.com \
  -username=admin \
  -role=super_admin
# 终端会提示输入两次密码；非 TTY 环境从 stdin 读一行
```

后续如果忘了密码：

```bash
go run ./cmd/admintool reset-admin   -dsn="$CNV_DB_URL" -email=admin@example.com
go run ./cmd/admintool reset-account -dsn="$CNV_DB_URL" -email=player@example.com
```

## 6. 登录游戏管理后台

管理后台前端由**面板**托管（见第 7 步）。若你现在只起了这一个业务节点、想先看看后台：业务节点根路径 `/` 现在是**只读状态页**，未接入面板时后台前端由节点回落托管——直接打开 `http://<你的服务器>:8080/login.html`，用管理员邮箱 + 密码登录即可。

> **生产形态**：起面板后统一打开**面板**地址（默认 `:8090`）登录；面板托管全部前端、浏览器跨域直连业务节点。给业务节点设 `CNV_PANEL_PUBLIC_URL=<面板URL>` 打通入口跳转与 CORS，详见 [节点与面板](./nodes#面板托管前端与跨域直连)。

进去后第一件事建议：

1. **版本管理** — 把允许的客户端版本加进白名单，配好更新通道 URL。
2. **服务器状态** — 确认是 `正常` 而非 `维护中`。
3. **资源管理** — 配置镜像组，或确认本地资源已就位。

## 7.（可选）启动面板

面板提供节点注册表管理与 WS 监控（类似 MCSManager 的 web 进程）：

```bash
export CNV_PANEL_KEY="$(openssl rand -hex 32)"
export CNV_ADDR=:8090
go run ./cmd/panel
```

面板**首次启动**后用浏览器打开它（如 `http://localhost:8090/`），会自动跳转到 **安装向导** `/install/`（WordPress 式）：

1. 检查面板本地存储（SQLite）连通性；
2. 配置并测试业务节点的游戏数据库（SQLite / MySQL / PostgreSQL），向导给出对应的 `CNV_DB_URL`；
3. 创建超级管理员账号 + **选择拓扑：本机也装业务节点（默认勾）/ 只装面板**。

勾"本机也装业务节点"时，面板会：

- 在自身二进制平级目录找 `magireco-node`（[`CNV_NODE_BIN`](./configuration#cnv_node_bin) 可覆盖）；
- 跑 `magireco-node --version` 与面板版本**硬比对**；
- 业务 DB 已经 `setup_state.done=true` 时**硬拒**（"已经装过了"），否则跑迁移 + 同凭证建超管 + 写标志；
- TCP dial 节点管控通道（默认 `127.0.0.1:9090`），没人在 listen 就 **setsid fork** 启动节点（脱离面板进程组，stdout/stderr → `./logs/node.log`，pid → `./data/node.pid`），然后轮询确认就绪。

一页填完，两边能用同一个账号登录，业务节点的 `/setup/*` 也自锁了。

不勾"本机也装业务节点"时，面板**只装面板自己**；业务节点需要你**手动跑** `magireco-node` 并用它自己的 `/setup/*` 单独装——分机部署或不用面板编排的场景走这条路。

安装一完成，**安装模块会立即从运行中的服务里移除**，`/install/*` 此后永久返回 404（不是简单关闭入口）。

详见 **[节点与面板](./nodes)**。

## 8.（可选）添加边缘节点

边缘节点用来在别处分摊下载带宽：

```bash
export CNV_NODE_ROLE=edge
export CNV_PRIMARY_RES_DIR='/srv/magireco-res'
./magireco-node
# 同样会打印节点密钥，在面板注册表里填入即可
```

## 完整最小脚本

```bash
#!/usr/bin/env bash
set -euo pipefail

export CNV_DB_URL='sqlite:///srv/magireco/magireco.db'
export CNV_ADMIN_JWT_SECRET="$(openssl rand -hex 32)"

# 生产再加上（强烈建议）：
# export CNV_SIGNATURE_WHITELIST='<你的 APK 签名证书 sha256>'
# export CNV_REQUIRE_SIGNATURE=true
# export CNV_TRUST_PROXY='loopback'          # 若放在本机反代后
# export CNV_TLS_CERT=/etc/ssl/magireco.crt  # 若本进程终结 TLS
# export CNV_TLS_KEY=/etc/ssl/magireco.key

go run ./cmd/node
```

## 下一步

- ⚠️ **上生产前**务必过一遍 **[安全加固清单](./security-checklist)**（签名白名单、trust proxy、TLS…）。
- 把进程托管成 systemd 服务、配好日志与备份，见 **[日常运维](./operations)**;
  一行命令在线部署（无需 clone 仓库，二进制从 Release 自动下载）：

  ```bash
  sudo su -c "wget -qO- https://raw.githubusercontent.com/MagirecoCN-Revival-Project/magirecocn-api-server/main/deploy/install.sh | bash"
  ```

  弹出交互菜单选 `panel` / `node-business` / `node-edge`，脚本把二进制、systemd unit、`.env`、用户、目录、logrotate 一次性下发。
- 配置反代与多域名，见 **[反向代理与域名](./reverse-proxy)**。
