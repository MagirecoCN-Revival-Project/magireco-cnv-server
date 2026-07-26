# 代码规范

提交前对照。大原则:**写得像周围的代码**。匹配既有的命名、注释密度、惯用法。

## 语言与格式

- **Go 标准格式**:提交前 `gofmt`(或 `go fmt ./...`)。CI 的 `go vet` 会挡明显问题。
- **不引入新依赖**除非必要。这个项目刻意保持依赖精简(chi + 三个数据库驱动 + scrypt)。要加依赖,先想清楚能不能用标准库 + 现有模式解决。
- **错误处理**:Go 惯例,显式 `if err != nil`。不 panic(除非真的不可恢复 —— 而且有 `Recovery` 中间件兜底)。

## 注释:中文,讲"为什么"

项目注释是**中文**的,且偏重解释**为什么**而非**做什么**。看一段现有注释的风格:

```go
// ── 封禁:HTTP 200 + {banned:true, ban_reason}。
//    封禁是**正常的业务结果**而非传输/请求错误,故不走错误模型的 4xx——
//    客户端需要读到 body 里的理由与到期时间才能给玩家一个可理解的提示。
```

它解释的是"为什么是 200 而不是 403"这个非显而易见的决策。这类**约束的理由**最值得写下来,因为后来者很容易"顺手改对成 4xx"然后弄坏客户端。

- 关键约束、反直觉的选择、与客户端协议相关的细节 → **务必注释,说明理由**。
- 显而易见的 getter/setter → 不用废话注释。
- 包级注释(`// Package xxx ...`)说明这个包的职责。

## 命名

跟随既有约定:

| 场景 | 约定 | 例子 |
|---|---|---|
| Handler 结构 | `Handler` | `client.Handler` |
| 路由注册 | `Routes(r chi.Router)` | `func (h *Handler) Routes(...)` |
| 配置结构 | `xxxCfg` | `versionCfg`、`servicesCfg` |
| 存储方法 | 动宾,`Store` 的方法 | `BanActive`、`AccountSessionTouch` |
| 测试 | `Test<场景>_<条件>` | `TestInit_LatestVersion_OmittedWhenUnset` |
| ID 前缀 | 类型短前缀 | `acc-xxx`(账号)、`log_xxx`(审计) |

## 协议字段:神圣不可随意改

`/client/*` 的字段名、嵌套、空值处理以**架构协议文档**为准,**不是你说改就能改**:

- 加可选字符串字段用 `putIfNonEmpty`(空则省略 key)。
- 加可选整数用 `putIfNonZero`。
- bool 字段可以直接放(`false` 是合法值)。
- 改动必须让 `protocol_test.go` 继续绿,新字段补测试。

详见 [协议保真原则](./protocol-fidelity)。这是最容易出事、也最该谨慎的地方。

## SQL:三方言通用

- 用 `?` 占位符(存储层会按方言 `rebind`,PG 转 `$N`)。
- 用 `Store` 的 `query`/`queryRow`/`exec`/`Upsert`,别直接写 `db.Query`。
- UPSERT 用 `dialect.Upsert(UpsertSpec{...})` 生成,别手写 `ON CONFLICT`(MySQL 语法不同)。
- 不用某数据库专属函数(如 PG 的 `jsonb_set`)。JSON 列当文本存,Go 侧用 `json.RawMessage`。

详见 [多方言抽象](./store-dialects)。

## 时间戳

- 数据库内存 **Unix 毫秒**(`nowMs()`)。
- 下发给客户端的某些字段转 **Unix 秒**(如 `end_time`、封禁 `expire_time`)—— 客户端按秒解析。换算在 handler 做。

加涉及时间的字段时,想清楚是毫秒还是秒,跟随同类字段。

## 安全相关的硬规矩

| 规矩 | 原因 |
|---|---|
| 敏感比较用 `auth.SafeStrEq` / `subtle.ConstantTimeCompare` | 防时序侧信道 |
| 口令只经 `auth.HashPassword`/`VerifyPassword` | 统一 scrypt + 防枚举 |
| token 用 `auth.NewToken`(crypto/rand) | 不可预测 |
| 不信任客户端字段 | 都要服务端验证 |
| 来源 IP 用 `middleware.ClientIP(r)` | 走 trust proxy 逻辑,别直接读 `RemoteAddr` 或 XFF |
| 错误信息不泄露账号是否存在 | 防枚举 |

这些不是风格偏好,是安全要求。改 `auth`/`middleware` 前先读 [安全机制](/security/)。

## 响应统一出口

| 函数 | 场景 |
|---|---|
| `respond.OK(w, data)` | 普通成功 |
| `respond.OKRaw(w, map)` | 需精确控制顶层字段(如握手) |
| `respond.Fail(w, status, code, msg)` | 失败 |
| `respond.JSON(w, status, v)` | 任意状态码 + 结构 |

别自己 `w.Write(json...)`,走 respond 包保证形状一致。

## Commit 规范

看仓库历史的风格:

```
feat(client-api): /client/init 新增 latest_version 软提示字段
fix(ci): -race 测试启用 CGO_ENABLED=1
security(auth): 版本化 scrypt 哈希并将 N 提升至 32768
```

- 格式:`<类型>(<范围>): <中文描述>`
- 类型:`feat` / `fix` / `security` / `refactor` / `docs` / `chore`
- 范围:`client-api` / `auth` / `store` / `ci` / `admin` …
- 描述用**中文**,讲清这个 commit 做了什么。
- **一功能一 commit**,别把多个无关改动塞一个提交。
- **直接提交 `main`**:本仓库不走功能分支 / PR 流程。改 `/client/*`、`/account/*` 契约的代码必须**同提交**带上文档,详见 [契约纪律与文档绑定](./discipline)。

## 提交前三连

```bash
go vet ./...                    # 静态检查
go test ./...                   # 全部测试
go test -race -count=1 ./...    # 竞态(CI 会跑)
```

外加:改了协议 → `protocol_test` 绿;改了存储 → 至少 SQLite 上 `store` 测试绿。

## 一句话

**模仿周围的代码**。这个仓库的风格相当一致,照着最近的相似实现写,八成就对了。拿不准就读 `git log` 看相关改动怎么做的。
