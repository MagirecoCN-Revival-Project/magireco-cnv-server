# 会话与令牌

各类令牌如何生成、存储、校验、失效。三套会话的整体设计见 [架构 · 三套会话体系](/architecture/sessions),本页聚焦**安全属性**。

## 令牌生成

所有会话 token 都来自同一个生成器:

```go
func NewToken() (string, error) {
    b := make([]byte, 32)
    rand.Read(b)               // crypto/rand,密码学随机
    return hex.EncodeToString(b), nil   // 64 字符 hex
}
```

- **32 字节 = 256 位**密码学随机,空间足够大,无法枚举/预测。
- 用 `crypto/rand`(非 `math/rand`),不可被种子推断。
- 校验先过 `IsWellFormedToken`(长度 64 + 仅 `[0-9a-f]`),格式不对直接拒,省一次查库。

## 安全 Cookie 属性

网页端(玩家 / 管理员)用 `mr_session` cookie 承载 token:

```go
http.SetCookie(w, &http.Cookie{
    Name:     "mr_session",
    Value:    token,
    Path:     "/",
    HttpOnly: true,                     // JS 读不到 → 防 XSS 窃取
    Secure:   r.TLS != nil,             // 仅 HTTPS 传输 → 防明文窃听
    SameSite: http.SameSiteStrictMode,  // 跨站不带 → 防 CSRF
    MaxAge:   int(ttl.Seconds()),
})
```

```mermaid
flowchart LR
    XSS["XSS 窃取 cookie"] -->|HttpOnly| B1["❌ JS 读不到"]
    MITM["明文窃听"] -->|Secure| B2["❌ 只走 HTTPS"]
    CSRF["跨站伪造请求"] -->|SameSite=Strict| B3["❌ 跨站不带 cookie"]
```

::: warning Secure 依赖 HTTPS
`Secure: r.TLS != nil` —— 只有 TLS 终结于本进程时才置 `Secure`。如果你在前置网关终结 TLS,本进程看到的是 HTTP,`Secure` 不会被置上。这种部署下务必确保**外部入口是 HTTPS**,且 cookie 仅经 HTTPS 下发。
:::

## 会话有效期与失效

| 会话 | TTL | 失效途径 |
|---|---|---|
| client_session | 7 天 | 过期、signature 中途变化、设备封禁 |
| account_session | 30 天(滑动) | 过期、改密(其它设备)、找回密码(全部)、停用 |
| admin_session | 7 天 | 过期、登出、找回密码 |

过期会话由调度器"会话 GC"任务定期清理(默认 300s)。

### 滑动续期(仅玩家会话)

```mermaid
flowchart TB
    REQ["玩家命中 /user/api 或 /account/save"] --> CHK{剩余 < TTL/2?}
    CHK -->|是| RENEW["expires_at = now + TTL"]
    CHK -->|否| TOUCH["仅更新 last_seen_at"]
```

"记住登录"的实现。安全权衡:被盗 token 持续使用也会自动续命,所以 TTL 不设过长(30 天)。需要时可强制下线(改密/找回)。

## 资源 token:无状态短时签名

握手响应里 `asset_auth` 承载的 `resource_token` 不走数据库,而是 **HMAC 短时签名**:

```go
bucket := now / windowSec          // 时间窗编号
mac := hmac.New(sha256.New, s3Secret)
mac.Write(deviceID + "|" + bucket)
token := base64.RawURLEncoding(mac.Sum(nil))
```

```mermaid
flowchart LR
    D["device_id + 当前时间窗"] --> H["HMAC-SHA256(密钥)"]
    H --> T["resource_token<br/>(短时有效)"]
    T --> V["资源服务端可独立验证<br/>无需查库"]
```

- 绑定 `device_id` + **时间窗**(默认 30 秒一个窗),过窗即失效。
- 用 HMAC,**服务端可独立验证**,无需存储 —— 资源节点拿密钥就能验,天然适配多节点。
- 签名根密钥(`CNV_RESOURCE_TOKEN_SECRET`,不设则首次启动自动生成 32 字节并持久化)可在后台轮换。

## 节点连接密钥(管控通道)

面板↔节点的管控 WebSocket 握手用节点自持的连接密钥鉴权(节点首次启动生成 32 字节随机密钥、管理员复制到面板注册表)。密钥比较走**等时常量比较**:

```go
func SafeStrEq(a, b string) bool {
    ha := sha256.Sum256([]byte(a))    // 先收敛成定长 32 字节
    hb := sha256.Sum256([]byte(b))    // (避免长度本身泄露信息)
    return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
```

```mermaid
flowchart TB
    G["收到 auth 帧的 key"] --> CHK{节点密钥已配置?}
    CHK -->|否| R0["拒绝(auth_fail)"]
    CHK -->|是| SHA["双方各 SHA-256 收敛"]
    SHA --> CT["subtle.ConstantTimeCompare"]
    CT -->|相等| OK["✅ auth_ok + NodeInfo"]
    CT -->|不等| R2["auth_fail unauthorized 并关连接"]
```

为什么先 SHA-256 再比较:即便 `ConstantTimeCompare` 本身等时,直接比较不等长字符串仍可能因长度差异泄露信息;先各自哈希成定长,彻底消除长度侧信道。密钥是 64 位十六进制(32 字节随机),不存在弱口令问题。客户端的多节点发现则另用 Ed25519 签名目录,见 [多节点协调](/architecture/multi-node)。

## 令牌泄露的影响面

| 泄露的 token | 攻击者能做什么 | 缓解 |
|---|---|---|
| client access_token | 冒充该设备调 `/client/*` | signature 一致性校验 + 7 天过期 + 封禁 |
| account_token | 冒充玩家读写云存档 | 安全 cookie + 改密下线 + 30 天上限 |
| admin_token | 管理后台操作 | 安全 cookie + 7 天短 TTL + 审计留痕 |
| resource_token | 短时下载资源 | 30 秒时间窗,过期即废 |
| 节点连接密钥 | 冒充面板向节点发管控指令 | 等时比较 + 32 字节随机 + 管控端口内网隔离 |

设计上**最敏感的 admin 会话 TTL 最短、不续期**;最需要"记住登录"的玩家会话 TTL 长但可强制下线;资源 token 干脆做成几十秒就失效的无状态签名。
