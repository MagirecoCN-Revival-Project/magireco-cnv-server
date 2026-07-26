# 安全加固清单

上生产前**逐项确认**。每一项都对应一类真实威胁。打勾的是必做。

## 🔒 客户端完整性

- [ ] **配置 `CNV_SIGNATURE_WHITELIST`**
  填入你的 APK 签名证书 SHA-256(64 位小写 hex)。这是防改包的核心闸门 —— 重打包必须重签名,摘要就对不上。为空 = 放行所有改包客户端。
- [ ] **开启 `CNV_REQUIRE_SIGNATURE=true`**
  即便配了白名单也建议开。堵住"改包客户端读不到证书时发空串就被放行"的边界情况。
- [ ] (可选)**配置 `CNV_CHANNEL_WHITELIST`**
  如 `normal,internal-test`,拒绝伪造的第三方渠道。

> 原理见 [防改包闸门](/security/anti-tamper)。

## 🔒 来源 IP 与代理

- [ ] **正确设置 `CNV_TRUST_PROXY`**
  - 直接对外:留空(默认,只信 TCP 对端)。
  - 在反代后:设为网关网段(如 `loopback` / `10.0.0.0/8`)。
- [ ] **前置网关重写转发头**
  网关必须用 `$remote_addr` 覆盖 `X-Forwarded-For`,而非透传客户端伪造的链。
  > 配错的后果:限流失效、审计 IP 全错。见 [受信任代理](/security/trust-proxy)。

## 🔒 传输安全

- [ ] **全程 HTTPS**
  TLS 终结在前置网关或本进程(`CNV_TLS_CERT`/`CNV_TLS_KEY`)。客户端入口必须是 HTTPS。
- [ ] **管理后台不暴露在公网裸 HTTP 上**
  cookie 的 `Secure` 标志只在 HTTPS 下生效;裸 HTTP 下会话易被窃听。

## 🔒 密钥管理

- [ ] **`CNV_ADMIN_JWT_SECRET` 足够随机且 ≥32 字符**
  `openssl rand -hex 32` 生成,**不要**用弱口令或示例值。
- [ ] **`CNV_PANEL_KEY` ≥32 字符且足够随机**(若部署面板)
  面板会话 cookie 的 HMAC 签名密钥,太短会被启动校验拒绝。
- [ ] **节点连接密钥妥善保管,管控端口受限**
  节点首次启动生成的密钥只在面板注册表持有;跨机时 `CNV_CONTROL_ADDR` 端口仅对面板放行。
- [ ] **密钥不进版本库、不进日志**
  用环境变量 / secret 管理器注入,别写进 `.env` 提交。
- [ ] **resource_token 密钥已自动生成**
  默认无需管理;如怀疑泄露,去资源管理页轮换。

## 🔒 账号安全

- [ ] **第一个管理员用强密码**
  `admintool create-admin` 时设强口令。口令以版本化 scrypt(N=32768)存储。
- [ ] **按需分配角色**
  审计岗用 `readonly`,日常运维用 `admin`,`super_admin` 严格控制人数。
- [ ] **`CNV_EMAIL_DEV_MODE` 在生产是关闭的**
  开着会把邮箱验证码打进日志,等于验证码公开。默认关闭,确认没误开。

## 🔒 限流与防爆破(默认已开,确认未误关)

服务端内置了多道限流,无需配置,但要知道它们在:

| 端点 | 限制 | 维度 |
|---|---|---|
| 登录 | 10 次/分钟 | IP |
| 注册/找回 | 30 次/分钟 | IP |
| 邮箱验证码 | 8 次/10 分钟 | IP |
| 验证码挑战 | 60 次/分钟 | IP |
| 云存档上传 | 2 次/分钟 | 会话 token |

- [ ] **确认 trust proxy 配对了** —— 否则 IP 维度的限流会退化成全局共享。

> 机制见 [限流与防爆破](/security/rate-limiting)。

## 🔒 数据与备份

- [ ] **数据库定期备份**
  至少覆盖 `config` / `accounts` / `saves`。见 [选择数据库](./database#备份建议)。
- [ ] **数据库连接启用 TLS**(PG/MySQL)
  `sslmode=require` 等,别让数据库流量裸奔在网络上。
- [ ] **资源/离线包目录权限收敛**
  进程对资源目录只需读 + 对离线产物目录读写,别给多余权限。

## 上线前最终核对

```bash
# 快速自检:这些必须有值
printenv CNV_DB_URL                 >/dev/null && echo "✓ DB_URL"        || echo "✗ DB_URL 缺失"
printenv CNV_ADMIN_JWT_SECRET       >/dev/null && echo "✓ JWT_SECRET"    || echo "✗ JWT_SECRET 缺失"
printenv CNV_SIGNATURE_WHITELIST    >/dev/null && echo "✓ SIGNATURE"     || echo "⚠ 未配签名白名单(放行所有客户端!)"
[ "$CNV_EMAIL_DEV_MODE" = "" ] || [ "$CNV_EMAIL_DEV_MODE" = "off" ] && echo "✓ EMAIL_DEV_MODE 已关" || echo "✗ 邮件开发模式开着!"
```

## 威胁 → 防线对照

| 威胁 | 对应防线 |
|---|---|
| 重打包/破解客户端 | 签名白名单 + `REQUIRE_SIGNATURE` |
| 伪造来源 IP 绕过限流 | trust proxy 默认不信任 + 网关重写头 |
| 暴力撞库 | 登录限流 + scrypt 高成本 + 防枚举等时 |
| 撞库时探测账号是否存在 | `DummyVerifyTiming` 等时返回同一错误 |
| 会话窃取 | `HttpOnly`+`Secure`+`SameSite=Strict` cookie |
| 中间人推恶意更新包 | `update_apk_sha256` 客户端安装前校验 |
| 机器人批量注册 | PoW 人机验证 + 注册限流 |
| 副节点被冒充 | 共享密钥等时比较 + 长度下限 |

逐项确认无误,就可以安心上线了。
