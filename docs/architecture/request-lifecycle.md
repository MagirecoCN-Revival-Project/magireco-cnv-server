# 请求生命周期

一个 HTTP 请求从进入主节点到返回响应,经过哪些环节。理解这条链,改任何接口都有底。

## 中间件链

主节点在 `cmd/node/main.go` 里装配路由。**全局中间件**对每个请求生效,顺序如下:

```mermaid
flowchart LR
    REQ["请求进入"] --> R["Recovery<br/>panic→500"]
    R --> L["Logger<br/>记一行 INFO"]
    R2["SecurityHeaders<br/>注入安全响应头"]
    L --> R2
    R2 --> BL["BodyLimit<br/>全站 8 MiB 上限"]
    BL --> RL["按端点限流<br/>(部分路由组)"]
    RL --> AUTH["按端点鉴权<br/>(部分路由组)"]
    AUTH --> H["业务 handler"]
    H --> RESP["respond.* 统一 JSON"]
```

| 中间件 | 作用 |
|---|---|
| `Recovery` | 捕获任何 panic,转成 500 + 结构化日志(含栈),保证单个请求崩溃不拖垮进程 |
| `Logger` | 每请求一行 INFO:方法、路径、状态码、字节数、耗时、来源 IP |
| `SecurityHeaders` | 注入 `X-Frame-Options`、`X-Content-Type-Options`、`Referrer-Policy`、CSP;TLS 下加 HSTS |
| `BodyLimit(8 MiB)` | 全站请求体上限,防超大 body 撑爆内存。客户端 `/client/*` 另有 1 MiB 内限 |

## 路由分组与各自的"门禁"

不同路由组挂不同的限流和鉴权中间件:

```mermaid
flowchart TB
    ROOT["chi.Router(全局中间件已挂)"]
    ROOT --> C["/client/*<br/>init 公开;其余校验 authTriple"]
    ROOT --> ACC["/account/* /auth/*<br/>登录/注册/验证码 各自限流"]
    ROOT --> API["/api/*<br/>验证码限流"]
    ROOT --> ADM["/admin/*<br/>RequireWritableAdmin"]
    ROOT --> USR["/user/api/*<br/>RequireAccount(滑动续期)"]
    ROOT --> INT["/internal/*<br/>SharedKey 鉴权"]
    ROOT --> STATIC["静态:/res /dl 前端页"]
```

- `/client/init` 是**公开**的(握手起点),但内部做签名/渠道/版本校验。其余 `/client/*` 端点用 `requireClientSession` 校验 authTriple(`device_id` + `access_token` + `signature`)。
- `/admin/*` 默认要求**可写管理员**;`/admins/*` 子路由额外要求超管。
- `/user/api/*` 要求玩家会话,并在中间件里做**滑动续期**。
- `/internal/*` 用副节点共享密钥鉴权(等时比较)。

## 一个握手请求的完整旅程

以最核心的 `POST /client/init` 为例:

```mermaid
sequenceDiagram
    autonumber
    participant C as 客户端
    participant MW as 全局中间件
    participant H as init handler
    participant ST as 存储层
    participant DB as 数据库

    C->>MW: POST /client/init {device_id, protocol_versions, version, signature, channel}
    MW->>MW: Recovery/Logger/SecurityHeaders/BodyLimit
    MW->>H: 进入 handler
    H->>H: 校验 device_id 非空
    H->>H: negotiateProtocol(取版本交集)
    alt 与服务端支持集无交集
        H-->>C: 400 protocol_version_unsupported
    end
    H->>H: checkSignature(签名白名单)
    alt 签名不在白名单
        H->>ST: 记 integrity_rejected 审计
        H-->>C: 403 signature_rejected
    end
    H->>H: 校验 channel 白名单
    H->>ST: DeviceTouch(更新设备指纹)
    H->>ST: BanActive(device_id)?
    alt 设备被封
        H-->>C: 200 {banned:true, ban_reason}
    end
    H->>ST: 读 config:server / features
    H->>ST: ClientSessionInsert(签发 access_token)
    H-->>C: 200 {protocol_version, access_token, server_time_at,<br/>server, features, asset_auth, directory}
```

注意几个**刻意的协议约定**:

- **协议版本协商排在最前面**,早于签名与封禁判定。双方连"用哪套线格式说话"都没谈拢时,后面的字段谁也不该解读。无交集时握手终止,**客户端不得降级**。
- **封禁返回 HTTP 200**,把状态放在 body 的顶层 flag 里。封禁是正常的业务结果而非请求错误,客户端要读到 `ban_reason` 与 `expire_time` 才能提示玩家;用 4xx 会让"被封禁"和"请求写错了"在客户端看来是同一件事。
- 校验顺序:协议版本 → 签名 → 渠道 → 封禁 → 才签发会话。任何一关不过就提前返回,不签 token。
- **APK 版本闸门(`allowed_versions` / `force_update` / `update_url_*`)已移除**,`config.versions` 不再被 `/client/*` 读取。

详见 [客户端握手协议](./client-protocol)。

## authTriple:握手后的请求如何鉴权

`/client/init` 之后的端点(`heartbeat`、`scene-manifest`)都要带 **authTriple**:

```mermaid
flowchart TB
    REQ["POST /client/scene-manifest<br/>{device_id, access_token, signature, scene_id}"]
    REQ --> RW["readAndRewind<br/>(body 只能读一次,读出后塞回)"]
    RW --> WF{access_token<br/>格式合法?}
    WF -->|否| E1["401 missing_access_token"]
    WF -->|是| LK["ClientSessionLookup<br/>(token 有效且绑定该 device_id?)"]
    LK -->|失败| E2["401 session_invalid"]
    LK -->|成功| SIG{signature 与<br/>握手时一致?}
    SIG -->|否| E3["403 + 作废会话<br/>(疑似换包/劫持)"]
    SIG -->|是| BAN{设备被封?}
    BAN -->|是| E4["200 action=ban"]
    BAN -->|否| OK["注入 session 到 ctx<br/>→ 业务 handler"]
```

关键深度防御:**后续请求的 signature 必须与握手时写入会话的那个一致**。会话存续期间它不该变;若变了,说明会话被劫持或客户端被换包,直接作废会话。

> 这条对 Android 端强度最高(APK 签名证书在单次安装期不可能变)。Web 客户端整个跑在玩家浏览器里,**没有不可绕过的完整性凭据**——它在那里是一致性检查,不是安全边界。

这里还有个工程细节:`http.Request.Body` 只能读一次,但中间件要读 body 取 authTriple、业务 handler 又要再读一次解析完整参数。解决办法是 `readAndRewind` 把 body 读进内存再塞回一个可重读的 reader。

## 响应统一出口

所有响应走 `internal/api/respond` 包,保证形状一致:

| 函数 | 用途 |
|---|---|
| `respond.OK(w, data)` | 成功,`{success:true, ...data}` |
| `respond.OKRaw(w, map)` | 成功但要精确控制顶层字段(如握手响应) |
| `respond.Fail(w, status, code, msg)` | 失败,`{success:false, error:{code, message}}` |
| `respond.JSON(w, status, v)` | 任意状态码 + 任意结构 |

这层统一让客户端永远能用同一套逻辑解析成功/失败。

## 优雅关闭

进程监听 `SIGINT`/`SIGTERM`。收到后:

1. `http.Server.Shutdown` 停止接收新连接,给在途请求最多 10 秒收尾。
2. 调度器的 context 被取消,各后台 goroutine 退出。

不会粗暴中断在途请求。
