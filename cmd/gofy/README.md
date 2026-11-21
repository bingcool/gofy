# gofy 脚手架

`gofy` 生成一个**可立即启动**的 gofy 应用，目录形状与 `examples/demo-service` 一致：
入口层拆成 `main/config/options/reload/fatal/nacos_watch`，`app/` 下是
`config / appio / container / middleware / module`，再加 `script/`、`worker/`、
`migrations/` 与 `shell/`。不含 `internal/`，生成物不含密钥、不预生成 `go.sum`，
`go mod tidy` 之后 `go run . start` 就能跑。

## 安装

```bash
# 已发布版本
go install github.com/bingcool/gofy/cmd/gofy@latest

# 在框架仓库内直接运行，无需安装
go run ./cmd/gofy new ai-service --module example.com/ai-service
```

## 快速开始

### A. 从零新建目录

```bash
gofy new ai-service --module example.com/ai-service
cd ai-service
go mod tidy
go run . start
```

```
$ curl localhost:8080/healthz
{"status":"ok"}
$ curl localhost:8080/api/v1/examples
{"code":0,"msg":"success","data":{"items":[],"total":0,"greeting":"hello"},"req_id":"..."}
$ curl -X POST localhost:8080/api/v1/examples -H 'Content-Type: application/json' -d '{"name":"first"}'
{"code":0,"msg":"success","data":{"id":1,"name":"first","created_at":"..."},"req_id":"..."}
```

### B. 在已有 `go mod init` 的目录里生成

这是把脚手架接进已有仓库的流程：模块路径已经在 `go.mod` 里，不必再传 `--module`。

```bash
mkdir ai-service && cd ai-service
go mod init example.com/ai-service
gofy new .            # 名字取自目录名，模块路径取自 go.mod
go mod tidy
go run . start
```

`gofy new .` 与 `gofy new ai-service --dir .` 等价。生成时会：

- 从 `go.mod` 读取模块路径，并把 `require github.com/bingcool/gofy` 合并进去；
- 保留 `go.mod` 原有的 `module`、`go` 与其它依赖，**不重写**该文件；
- 已存在的源文件一律保留（`--force` 才覆盖），因此重复执行是安全的。

## 生成的结构

```
ai-service/
├── main.go                     # 入口：命令行分类 → Nacos 拉取 → 配置 → app.Execute
├── config.go                   # configPath / loadNacosConfig / planCommand
├── options.go                  # appOptions：按命令分层装配 gofy.Option
├── reload.go                   # reloader：热更新重建 logger、连接池与模块
├── nacos_watch.go              # Nacos 配置监听模块（reload / restart / exit 三种策略）
├── fatal.go                    # 启动期致命错误的彩色输出
├── config.yaml                 # 应用配置：http / log / sql_log / example / middleware / pid
├── application.yaml            # Nacos 行为：注册、发现、变更策略
├── nacos.yaml                  # Nacos 连接（默认 enabled: false）
├── Makefile                    # build / test / fmt / vet / run / script / worker / clean
├── .gitignore
├── app/
│   ├── config/                 # Config 类型、Load 校验、按模式追加日志路径
│   ├── appio/                  # IO 装配代：db + 快照 + RequestBinder
│   │   └── db/                 # 命名 MySQL 连接与 registry
│   ├── container/              # 装配根：仓库、service 构造槽、模块工厂
│   ├── middleware/             # 应用层中间件别名：auth / audit
│   └── module/example/         # 一个完整模块
│       ├── module.go           # 路由表
│       ├── controller/         # HTTP 适配，不碰 model/repository
│       ├── dto/                # 请求/响应结构
│       ├── service/            # 用例，实现 innerapi
│       ├── repository/         # 持久化（默认内存，注释说明如何换 MySQL）
│       ├── model/examplemodel/ # gorm 模型
│       └── innerapi/           # 跨模块契约
├── migrations/0001_example.sql # 切到 MySQL 时使用
├── script/                     # go run . script ping
│   ├── register.go
│   └── ping.go
├── worker/                     # go run . worker --group=group1
│   ├── register.go
│   ├── worker.go
│   └── group1/
│       ├── abc_register.go     # 分组构造
│       └── heartbeat.go        # 一个常驻任务
└── shell/check-imports.sh      # 守卫：controller 不得 import 兄弟模块的内部包
```

## 分层职责

| 目录 | 职责 | 换成业务代码时改哪里 |
|---|---|---|
| `main.go` / `config.go` | 命令分类、配置文件定位 | 一般不动 |
| `options.go` | 装配选项：HTTP、日志、中间件、模块、脚本、worker | 新增能力时加一个 `gofy.With...` |
| `reload.go` | 热更新重建；新旧代各自一套仓库与 service | 新增模块时改 `factories()` |
| `app/config` | 配置结构与校验，一段一个类型 | 加字段后同步 `config.yaml` |
| `app/appio` | 每代 IO 句柄与快照；请求由框架 binder 固定 | 接 Redis/Kafka 时在这里扩 |
| `app/container` | 仓库与 service 的惰性构造槽 | 新增模块时加一个 `ModuleFactories` 条目 |
| `app/module/<name>` | 业务模块，按 controller→service→repository→model 分层 | 复制 `example` 改包名 |
| `app/middleware` | 应用层中间件，按逻辑名注册进共享注册表 | 加别名后在模块里 `r.Middleware("name")` |
| `worker/` | 常驻任务，独立进程运行，复用 service/repository | 在 `group1` 加一个文件，或在 `register.go` 加分组 |
| `script/` | 一次性维护脚本，跑完退出 | 在 `Register()` 里追加 |

## 生成后能做什么

| 命令 | 行为 |
|---|---|
| `go run . start` | 前台启动 HTTP 服务（默认 `:8080`，API 前缀 `/api/v1`） |
| `go run . start --daemon` | 后台启动，父进程等到子进程上报 ready 才返回（仅 Unix） |
| `./bin/ai-service stop --force` | 停止 pid 文件对应的实例 |
| `./bin/ai-service status` | 只读 pid 文件，报告状态/PID/监听地址/健康检查 |
| `./bin/ai-service worker --group=group1` | 启动一个 worker 分组（HTTP 进程不会启动它） |
| `./bin/ai-service worker --list` | 列出分组与并发度 |
| `./bin/ai-service script ping` | 打印生效配置并探测数据库 |
| `./bin/ai-service script --list` | 列出已注册脚本 |
| `./bin/ai-service version` | 构建信息（由 Makefile 的 ldflags 注入） |

`make build` 产出 `bin/<name>`；`stop` 默认要求输入 `yes`，容器/CI 里用 `--force`。

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--module` | 读 `<dir>/go.mod` | 模块路径。目标没有 `go.mod` 时必填；与已有 `go.mod` 不一致会直接报错 |
| `--dir` | `./<name>` | 输出目录。`gofy new .` 表示当前目录 |
| `--with-db` | `false` | 在 `config.yaml` 生成启用的 `db.default` 段（DSN 为 `${DB_DSN}`）。数据库接线本身始终存在，只是默认没有配置段 |
| `--with-scripts` | `true` | 生成 `script/` 与 `WithScripts` |
| `--with-worker` | `true` | 生成 `worker/` 与 `WithWorkerGroups` |
| `--with-makefile` | `true` | 生成 `Makefile` |
| `--with-docker` | `false` | 生成多阶段 `Dockerfile` 与 `.dockerignore` |
| `--replace` | 见下 | 为 `github.com/bingcool/gofy` 追加本地 `replace` 指向 |
| `--go` | `1.26` | 仅在**新建** `go.mod` 时写入的 `go` 指令；已有 `go.mod` 不动 |
| `--force` | `false` | 覆盖同名生成文件，并允许写入含无关文件的目录；目录里的其它文件不会被删除或改写 |

### `--replace` 与本地开发

不带 `--replace` 时，`go.mod` 里写的是占位版本 `v1.0.0`，需要你的模块代理上真实存在
该版本。在框架仓库内运行脚手架（`go run ./cmd/gofy ...`）会自动把 `--replace` 指向仓库
根目录，并在输出里打印 `using local framework at ...`；其它情况显式传：

```bash
gofy new ai-service --module example.com/ai-service --replace /path/to/gofy
```

## 生成规则

- **目录判定**：目标可以是空目录、只有 `go.mod`/`go.sum`/`.git` 等模块文件的目录，或
  本命令生成过的目录；含有无关文件时拒绝执行，什么都不写（`--force` 可越过；它只覆盖
  同名生成文件，不动其它内容）。
- **文件冲突**：已存在的文件保留，输出里列出 `kept existing: ...`；`--force` 才覆盖。
- **`go.mod` 合并**：只补 `require github.com/bingcool/gofy` 与（可选）`replace`，
  已存在则不重复添加——同一模块写两条 `replace` 是语法错误。
- **命名**：应用名限小写字母、数字与短横线，且以字母开头；环境变量前缀由名字大写、
  短横线换下划线得到（`ai-service` → `AI_SERVICE`）。
- **失败即退出**：模板渲染失败、Go 代码 `gofmt` 失败、目录不可写都会直接返回错误，
  不留下半个项目。

## 配置与环境变量

三个文件各管一件事，互不覆盖：

| 文件 | 内容 | 是否从 Nacos 拉取 |
|---|---|---|
| `config.yaml` | 应用配置：http、log、sql_log、db、example、middleware、pid | 是（Nacos 会覆盖它） |
| `application.yaml` | Nacos 行为：注册、发现、变更策略、监听参数 | 否，属于本机部署属性 |
| `nacos.yaml` | Nacos 连接：地址、账号、命名空间，`enabled` 默认 false | 否 |

`config.yaml` 由 `<NAME>_CONFIG` 指向（默认当前目录的 `config.yaml`）：

```bash
AI_SERVICE_CONFIG=/etc/ai-service/config.yaml ./bin/ai-service start
```

**认证默认关闭**。`middleware.api_key` 写成 `${AI_SERVICE_API_KEY}`，未导出该变量时展开
为空，`auth` 中间件不注册，写接口不带认证也照常启动。需要认证时：

```bash
export AI_SERVICE_API_KEY=secret
curl -X POST localhost:8080/api/v1/examples -H 'X-API-Key: secret' ...
```

**数据库默认不配置**。`--with-db` 生成的 `db.dsn` 写成 `${DB_DSN}`，启动时从环境变量
解析，**不要**把连接串写进配置文件。`make run` 会先检查 `DB_DSN` 是否设置。

## 与 `examples/demo-service` 的关系

`demo-service` 是全量示例：多模块、多实例装配、Nacos、租户、限流、审计、契约路由、
集成测试。脚手架保留同样的目录形状与装配顺序，但只带一个模块、一个 worker、一个脚本，
并且**不生成 `internal/`**（那是 demo 自己的集成测试目录，不属于应用骨架）。

需要看某项能力怎么接，到 `examples/demo-service` 找对应目录即可：`app/appio` 的 broker
装配、`app/middleware/rate_limit.go`、`internal/integration` 的端到端测试。

## 常见问题

**`go mod tidy` 拉不到 `github.com/bingcool/gofy`**
框架未发布到你的代理时，加 `--replace /path/to/gofy`，或在 `go.mod` 里手动补一条
`replace`。在框架仓库内运行脚手架会自动处理。

**接口返回 404**
模块挂在共享前缀 `/api/v1` 下，模块自身的 `Prefix()` 返回空串。路由是
`/api/v1/examples`，不是 `/examples`，也不是 `/api/v1/example/examples`。

**`auth` 没生效**
检查 `AI_SERVICE_API_KEY` 是否导出；未设置时启动日志里不会有 `auth`，写接口也不要求
`X-API-Key`。这是"没配就不启用"，不是静默失败。

**端口被占用**
改 `config.yaml` 的 `http.port`。配置加载默认开启 `${VAR}` 展开，所以也可以写
`port: ${PORT}`，再由环境变量决定——但没有"任意字段都能被同名环境变量覆盖"的机制，
别按那个假设部署。

**`stop` 说进程未运行**
`status` 只读 pid 文件：`missing` 表示没有文件，`stale` 表示 pid 已被别的进程占用。
确认 `pid_file`（默认 `app/<name>.pid`）与实际启动目录一致——pid 路径是相对**工作目录**
解析的。

**想加第二个模块**
复制 `app/module/example` 为新包名，改 `module.go` 里的 `Name()` 与路由前缀，然后在
`app/container/module_factories.go` 追加一个工厂，并在 `app/container` 里加一个仓库与
service 构造槽。跨模块能力走 `innerapi`，不要 import 兄弟模块的 service/repository。

## 子命令：`gofy new interface-service`

同目录下还有 `gofy new interface-service`，用于生成项目的**契约模块**
（`appinterface` + OpenAPI 生成器），与运行时脚手架互不依赖。它要求同级目录存在
`gofy-interface-api` 与 `gofy-adapter`，细节见 `--help`。
