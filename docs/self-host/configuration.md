# 环境变量参考

所有运行时配置都走 `CNV_*` 环境变量，**不读配置文件、不硬编码敏感值**。本页是完整速查。

加载逻辑见 `internal/config/config.go`。带 ⭐ 的是必填；带 🔒 的与安全强相关，生产务必正确设置。

## 通用（节点与面板共用）

| 变量 | 默认 | 说明 |
|---|---|---|
| `CNV_ADDR` | `:8080`（节点）/ `:8090`（面板） | HTTP 监听地址 |
| `CNV_TLS_CERT` / `CNV_TLS_KEY` | — | 进程内 TLS 证书/私钥；两者都设才启用 HTTPS |
| `CNV_TRUST_PROXY` 🔒 | — | 是否解析 `X-Forwarded-For`/`X-Real-IP`。详见下文 |
| `CNV_SKIP_MIGRATE` | `false` | 设 `1` 时跳过启动时的自动迁移 |

## 节点（`magireco-node`）

### 角色与通用

| 变量 | 默认 | 说明 |
|---|---|---|
| `CNV_NODE_ROLE` | `business` | 节点角色：`business`（全功能）或 `edge`（仅资源） |
| `CNV_NODE_ID` | hostname | 节点唯一标识，用于管控通道身份上报 |
| `CNV_NODE_KEY_FILE` | `./data/node.key` | 节点自持密钥文件路径。首次启动自动生成，管理员将密钥复制到面板注册表 |
| `CNV_CONTROL_ADDR` | `127.0.0.1:9090` | 管控 WS 监听地址（面板拨号到此）。**跨机部署时改为 `:9090` 并用防火墙限制** |
| `CNV_PUBLIC_URL` | — | 节点对外基准 URL，用于拼接资源地址 |

### 业务节点专用（`CNV_NODE_ROLE=business`）

| 变量 | 默认 | 说明 |
|---|---|---|
| `CNV_DB_URL` ⭐ | — | 数据库连接串，按前缀识别驱动。见 [选择数据库](./database) |
| `CNV_ADMIN_JWT_SECRET` ⭐🔒 | — | 管理后台 cookie 完整性密钥，**≥16 字符** |
| `CNV_RESOURCE_TOKEN_SECRET` | — | 可选；resource_token 的 HMAC 签名根密钥。**不设则首次启动自动生成并持久化**（写入 `config` 表 `resource_token_secret`，重启复用） |
| `CNV_PANEL_PUBLIC_URL` | — | 面板对外 URL（如 `https://panel.example.com`）。设置后：客户端入口页 `/account/register`、`/account/forgot`、`/account/verify-email` **302 跳转到面板**；并作为 **CORS 放行来源**，允许面板托管的前端跨域直连本节点 API。**留空**=单机回落：节点本地托管入口页（零跨域）。见 [节点与面板](./nodes#面板托管前端与跨域直连) |
| `CNV_WEB_DIR` | `./web` | 前端静态目录。**面板**用它托管全部人类前端（登录/注册/管理后台/用户中心）；**业务节点**仅在未接入面板（`CNV_PANEL_PUBLIC_URL` 留空）时回落用它服务客户端入口页 |
| `CNV_DIRECTORY_FILE` | — | 已签名节点目录 JSON 文件路径；设置后随 `/client/init` 下发给客户端。**生产必配**，见下 |
| `CNV_DEV_MODE` 🔒 | `false` | `true` 时允许下发协议的**开发期临时值**。**生产必须为 false**，见下 |
| `CNV_BOOTSTRAP_ENDPOINT` | — | Android 底包引导端点 `/magica/api/snaa` 下发的业务服务器地址。**留空 = 本节点不接管 Android 底包**，该端点返回 503。见下 |
| `CNV_BOOTSTRAP_MAX_THREADS` | `4` | 下发给底包的并发下载线程数建议值 |
| `CNV_BOOTSTRAP_VERSION` | `0` | 当前底包版本号（`r128` → `128`），底包据此自行决定是否提示更新 |

### `CNV_BOOTSTRAP_ENDPOINT`：Android 底包接管开关

只有要接管 Android 底包（`io.kamihama.totentanz` 系）时才需要配置。
Web 客户端不经过这个端点，留空即可。

配置后 `POST /magica/api/snaa` 会下发该地址，底包用它替换引擎内的 `UrlConfig`。
协议细节见 [客户端握手协议 · Android 底包引导端点](../architecture/client-protocol#post-magica-api-snaa-android-底包引导端点)。

::: warning 留空与配错的区别是可见的
留空时端点返回 **503 + `bootstrap_not_configured`**，而不是 200 + 空 `endpoint`。
后者会让底包弹 `Empty endpoint URL`，把一次配置缺失误报成客户端故障。
:::

### `CNV_DEV_MODE`：生产守卫 🔒

协议里有若干**开发期临时值**——待决项定稿前的占位形状，让客户端与服务端能先并行
开工。协议文档的「生产守卫」要求：**生产环境不得下发任何临时值**。这个开关就是
那道守卫在服务端侧的落点。

当前受它管辖的是 **`/client/scene-manifest`**：清单的最小形状（只含 `path`）是
R2 定稿前的临时值，因此 `CNV_DEV_MODE=false` 时该端点一律返回 `503`，
**哪怕清单已经接进来了**。

::: danger 默认 false 是有意的
临时值的危险不在于它们存在，而在于**它们可能不被发现地留在生产里**。一个只含
`path` 的清单在生产里跑得好好的，直到某天需要靠内容哈希做缓存失效，才发现它
从来没有过。

忘了配这个变量的后果是**功能不可用（显眼）**，而不是临时值悄悄泄进生产（不显眼）。
:::

### `CNV_DIRECTORY_FILE`：生产必配

未配置时握手不下发 `directory`，客户端只能沿用本地缓存或内置列表——那意味着
**节点路由脱离了服务端的控制**：想把流量从一个出问题的节点挪走都做不到，
而客户端那份缓存可能是任意久以前的。

未配置时启动会打 WARN，但不阻止启动（开发部署需要能单机跑起来）。
按协议，**客户端在非开发构建中遇到 `directory` 缺省必须拒绝启动**。

> 签名私钥**不得出现在任何在线服务上**——它是客户端信任链的根。目录应在离线或
> 受控环境签好，只把签名产物放到节点上。见 [多节点协调](/architecture/multi-node)。

### 安全闸门 🔒

| 变量 | 默认 | 说明 |
|---|---|---|
| `CNV_SIGNATURE_WHITELIST` 🔒 | — | APK 签名证书 SHA-256 白名单（64 位小写 hex，逗号分隔）。为空时放行所有签名并打 WARN |
| `CNV_REQUIRE_SIGNATURE` 🔒 | `false` | `true` 时强制 `/client/init` 必须带非空 signature |
| `CNV_CHANNEL_WHITELIST` | — | 渠道白名单，空=放行所有。常用 `normal,internal-test` |

> **自动封禁**没有环境变量：启用开关与各路阈值都存 `config` 表，在后台「设备封禁」页运行时调整（改完即时生效，无需重启），见 [限流与防爆破 · 自动封禁](/security/rate-limiting#自动封禁)。

### `CNV_TRUST_PROXY` 取值

| 取值 | 含义 |
|---|---|
| 空 / `off` / `false` | 不信任任何转发头，只用 TCP 对端（默认，最安全） |
| `all` / `true` / `*` | 信任所有上游（仅在确有可信前置网关时） |
| `loopback` | 信任 `127.0.0.0/8` 与 `::1` |
| CIDR 列表 | 仅信任列出的网段，如 `10.0.0.0/8,192.168.0.0/16` |

### 会话有效期

| 变量 | 默认 | 说明 |
|---|---|---|
| `CNV_CLIENT_SESSION_TTL` | `7d` | `/client/init` 签发的 access_token 有效期 |
| `CNV_ADMIN_SESSION_TTL` | `7d` | 管理员 cookie 有效期 |
| `CNV_ACCOUNT_SESSION_TTL` | `30d` | 玩家 token 有效期（支持滑动续期） |

时长可写秒数（纯数字）或 Go duration（如 `720h`、`30m`）。

### 资源与离线包

| 变量 | 默认 | 说明 |
|---|---|---|
| `CNV_PRIMARY_RES_DIR` | — | 本地资源目录（业务/边缘节点均可设） |
| `CNV_SECONDARY_RES_DIR` | `$CNV_PRIMARY_RES_DIR` | 边缘节点资源目录（优先级高于 `CNV_PRIMARY_RES_DIR`） |
| `CNV_PRIMARY_RES_PATH` | `/res` | 资源对外 URL 前缀 |
| `CNV_OFFLINE_DIR` | `./data/offline` | 离线整包产物目录 |
| `CNV_OFFLINE_URL_PATH` | `/dl/offline-pack` | 离线整包对外 URL 前缀 |
| `CNV_HOTUPDATE_DIR` | `./data/hotupdate` | 热更新包（JS/剧情）托管目录；服务端下载或接收上传后存此目录并对外提供下载 |
| `CNV_HOTUPDATE_URL_PATH` | `/dl/hot-update` | 热更新包对外 URL 前缀 |
| `CNV_HOTUPDATE_MAX_MB` | `1024` | 热更新包上传/下载大小上限初值（MiB）；管理后台「服务器控制 → 上限」可运行时调整，无需重启 |
| `CNV_BODY_LIMIT_MB` | `8` | 全局请求体大小上限初值（MiB）；同上可后台运行时调整 |

### SMTP 邮件

| 变量 | 默认 | 说明 |
|---|---|---|
| `CNV_SMTP_HOST` | — | SMTP 服务器地址；为空则禁用邮件发送 |
| `CNV_SMTP_PORT` | `587` | SMTP 端口 |
| `CNV_SMTP_USER` / `CNV_SMTP_PASS` | — | SMTP 认证 |
| `CNV_SMTP_FROM` | — | 发件人地址 |
| `CNV_SMTP_FROM_NAME` | `魔法纪录复兴计划` | 发件人显示名 |

## 面板（`magireco-panel`）

| 变量 | 默认 | 说明 |
|---|---|---|
| `CNV_PANEL_KEY` ⭐🔒 | — | 面板 cookie HMAC 签名密钥，**≥16 字符** |
| `CNV_PANEL_DB_FILE` | `./data/panel.db` | 面板本地 SQLite 路径（节点注册表 + 面板管理员） |
| `CNV_WEB_DIR` | `./web` | 面板托管的游戏前端静态目录（与业务节点同一套 `web/`）。存在即托管；缺失时根路径回落到内置节点状态页 |
| `CNV_ADDR` | `:8090` | 面板 HTTP 监听地址（面板默认改为 8090） |
| <span id="cnv_node_bin">`CNV_NODE_BIN`</span> | — | 安装向导第 4 步勾"本机也装业务节点"时找的 `magireco-node` 二进制路径。**默认**：面板自身二进制所在目录的 `magireco-node`(Windows 是 `.exe`)。设此变量为绝对路径可覆盖；版本必须与面板**字符串严格相等**才放过 |

## 校验规则

启动时会校验：

- **业务节点**（`MustValidateNode`）：`CNV_DB_URL` 必填；`CNV_ADMIN_JWT_SECRET` 必须 ≥16 字符。
- **边缘节点**（`MustValidateNode`，`CNV_NODE_ROLE=edge`）：无强制必填，但需有资源目录。
- **面板**（`MustValidatePanel`）：`CNV_PANEL_KEY` 必须 ≥16 字符。

校验不过直接 `exit(2)` 并打印缺什么。

## 生产推荐基线

```bash
# ── 业务节点 ──
export CNV_DB_URL='postgres://user:pass@db:5432/magireco?sslmode=require'
export CNV_ADMIN_JWT_SECRET="$(openssl rand -hex 32)"
export CNV_SIGNATURE_WHITELIST='<APK 签名 sha256>'
export CNV_REQUIRE_SIGNATURE=true
export CNV_CHANNEL_WHITELIST='normal,internal-test'
export CNV_TRUST_PROXY='loopback'
# 跨机管控时取消注释：
# export CNV_CONTROL_ADDR=:9090

# ── 面板 ──
export CNV_PANEL_KEY="$(openssl rand -hex 32)"
export CNV_ADDR=:8090
```

上生产前请对照 **[安全加固清单](./security-checklist)** 逐项确认。

## 向后兼容（已废弃变量）

以下变量已从代码中移除（不再读取），仅列出迁移去向：

| 废弃变量 | 替代方案 |
|---|---|
| `CNV_SECONDARY_SHARED_KEY` | 节点自持密钥，面板通过注册表管理 |
| `CNV_PRIMARY_URL` | 不再需要；面板通过注册表管理节点地址 |
| `CNV_HEARTBEAT_SEC` | 不再需要；管控 WS 长连接替代心跳 |
| `CNV_NODE_ROLE=primary` | 改为 `CNV_NODE_ROLE=business` |
| `CNV_NODE_ROLE=secondary` | 改为 `CNV_NODE_ROLE=edge` |
