# 开发环境搭建

把服务端在本地跑起来,几分钟搞定。开发用 SQLite 内存/文件库最省事。

## 前置

- **Go 1.25+**(见 `go.mod`)
- Git
- (可选)改后台前端需要浏览器即可,无需 Node 构建

::: tip 这套文档站才需要 Node
你正在读的 VitePress 文档站在 `docs/` 下,需要 `npm install`。但**业务代码本身不需要 Node** —— 后台前端是浏览器内 Babel,直接发 `.jsx`。
:::

## 拉代码

```bash
git clone https://github.com/magirecocn-revival-project/magirecocn-api-server.git
cd magirecocn-api-server
go mod download
```

## 最小本地配置

开发用 SQLite,把数据库文件丢临时目录:

```bash
export CNV_DB_URL='sqlite:///tmp/magireco-dev.db'
export CNV_ADMIN_JWT_SECRET='dev-secret-至少16字符-aaaaaaaa'
export CNV_EMAIL_DEV_MODE=on          # 验证码打到日志,方便测注册/找回
# 开发不配签名白名单 → 放行所有(会打 WARN,正常)
```

## 跑起来

```bash
go run ./cmd/node
# 看到 "业务节点启动" addr=:8080 即成功（同时打印首次生成的节点密钥与管控地址）
```

另开一个终端建管理员:

```bash
go run ./cmd/admintool create-admin \
  -dsn="$CNV_DB_URL" -email=dev@example.com -username=dev -role=super_admin
```

浏览器开 `http://localhost:8080/` 登录后台。

## 三个二进制

| 命令 | 作用 | 何时跑 |
|---|---|---|
| `go run ./cmd/node` | 节点:`CNV_NODE_ROLE=business`(默认,全部业务)/ `edge`(仅资源) | 主力开发 |
| `go run ./cmd/panel` | 面板:节点注册表 + WS 监控 + 管理 API | 调多节点时 |
| `go run ./cmd/admintool` | 运维 CLI(建/重置账号、签名节点目录) | 需要账号时 |

## 改代码后

Go 没有热重载,改完重启 `go run`。或者用 [air](https://github.com/air-verse/air) 之类的工具自动重启(可选,非项目依赖)。

改前端 `.jsx` 则**无需重启 Go**,刷新浏览器即可(浏览器内 Babel 实时转译)。

## 用 PostgreSQL/MySQL 开发(可选)

想在本地验证方言差异,用 Docker 起一个:

```bash
# PostgreSQL
docker run -d --name mr-pg -e POSTGRES_PASSWORD=pass -e POSTGRES_DB=magireco -p 5432:5432 postgres:16
export CNV_DB_URL='postgres://postgres:pass@localhost:5432/magireco?sslmode=disable'

# MySQL
docker run -d --name mr-mysql -e MYSQL_ROOT_PASSWORD=pass -e MYSQL_DATABASE=magireco -p 3306:3306 mysql:8
export CNV_DB_URL='mysql://root:pass@localhost:3306/magireco?parseTime=true'
```

迁移会自动跑。改了存储层 SQL 后,**最好三种库都验一遍**(至少 SQLite + 一种)。

## 验证环境正常

```bash
go vet ./...        # 应无输出
go test ./...       # 应全 ok
go build ./...      # 应无错误
```

三条都过,环境就绪。接下来去 [代码库导览](./codebase-tour) 熟悉结构。

## 常见坑

| 现象 | 原因 / 解决 |
|---|---|
| 启动报"缺少必需的环境变量" | 没设 `CNV_DB_URL` 或 `CNV_ADMIN_JWT_SECRET`(后者 ≥16 字符) |
| SQLite 报无法打开 | 连接串里的目录不存在;换 `/tmp/` 或先 `mkdir` |
| 收不到验证码 | 开 `CNV_EMAIL_DEV_MODE=on`,验证码会打到日志 |
| 后台登录后空白 | `CNV_WEB_DIR` 没指向 `./web`,或前端资源缺失 |
| `go test` 报 race 相关 | 跑 `go test -race` 需要 CGO;本地 `CGO_ENABLED=1 go test -race ./...` |
