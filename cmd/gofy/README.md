# gofy 脚手架

`gofy` 生成一个能立即启动的 gofy 应用：一个 HTTP 模块、一个常驻 worker 分组、一个维护
脚本，加上配置、Makefile 与可选的数据库接线。生成物不含密钥、不预生成 `go.sum`，
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
$ curl "localhost:8080/api/v1/example/hello?name=gofy"
{"code":0,"msg":"success","data":{"message":"hello gofy"},"req_id":"..."}
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
├── main.go                     # 装配：配置 → 选项 → app.Execute
├── config.yaml                 # HTTP/日志/pid，--with-db 时含 db 段
├── go.mod                      # 模块路径 + 框架依赖（+ 可选 replace）
├── Makefile                    # build / test / fmt / vet / run / script / clean
├── .gitignore
├── app/
│   ├── config/config.go        # 配置结构体与 gofyconfig.Load
│   └── module/example/
│       ├── module.go           # 模块注册：/api/v1/example
│       ├── dto/example.go      # 请求/响应结构
│       └── handler/example.go  # GET /hello
├── script/                     # go run . script <name>
│   ├── register.go
│   └── example.go
└── worker/                     # go run . worker --group=default
    ├── register.go
    └── example.go
```

| 目录 | 职责 | 换成业务代码时改哪里 |
|---|---|---|
| `app/module/<name>` | HTTP 模块：路由、DTO、handler | 复制一份改包名，在 `main.go` 的 `WithModuleFactories` 里追加 |
| `worker/` | 常驻任务，独立进程运行 | 替换 `Example()` 里的 ticker |
| `script/` | 一次性维护脚本，跑完退出 | 在 `Register()` 里追加脚本 |
| `app/config` | 配置结构与加载 | 加字段后同步 `config.yaml` |

模块之间不互相 import service/repository，跨模块走 `innerapi` 或 HTTP 契约；这条约束
在生成的目录里天然成立，因为只有一个模块。

## 生成后能做什么

| 命令 | 行为 |
|---|---|
| `go run . start` | 前台启动 HTTP 服务（默认 `:8080`，API 前缀 `/api/v1`） |
| `go run . start --daemon=1` | 后台启动，父进程等到子进程上报 ready 才返回（仅 Unix） |
| `./bin/ai-service stop --force=1` | 停止 pid 文件对应的实例 |
| `./bin/ai-service status --json` | 只读 pid 文件，报告状态/PID/监听地址/健康检查 |
| `./bin/ai-service worker --group=default` | 启动一个 worker 分组（HTTP 进程不会启动它） |
| `./bin/ai-service worker --list` | 列出分组与并发度 |
| `./bin/ai-service script example --message=hi` | 执行维护脚本 |
| `./bin/ai-service script --list` | 列出已注册脚本 |
| `./bin/ai-service version` | 构建信息（由 Makefile 的 ldflags 注入） |

`make build` 产出 `bin/<name>`；`stop` 默认要求输入 `yes`，容器/CI 里用 `--force=1`。

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `--module` | 读 `<dir>/go.mod` | 模块路径。目标没有 `go.mod` 时必填；与已有 `go.mod` 不一致会直接报错 |
| `--dir` | `./<name>` | 输出目录。`gofy new .` 表示当前目录 |
| `--with-db` | `false` | 生成 `db` 配置段、`gofy.WithDatabase`，脚本里加一条 `SELECT 1` |
| `--with-scripts` | `true` | 生成 `script/` |
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

`config.yaml` 由 `<NAME>_CONFIG` 指向（默认当前目录的 `config.yaml`）：

```bash
AI_SERVICE_CONFIG=/etc/ai-service/config.yaml ./bin/ai-service start
```

`--with-db` 生成的 `db.dsn` 写成 `${DB_DSN}` 占位，启动时从环境变量解析，**不要**把
连接串写进配置文件。`make run` 会先检查 `DB_DSN` 是否设置。

## 与 `examples/demo-service` 的关系

`demo-service` 是全量示例：多模块、多实例装配、Nacos、租户、限流、审计、集成测试。
脚手架刻意只保留最小可运行子集——一个模块、一个 worker、一个脚本——目录形状一致，
所以从脚手架长到 demo 的规模不需要重新组织工程。需要看某项能力怎么接，到
`examples/demo-service` 找对应目录即可。

## 常见问题

**`go mod tidy` 拉不到 `github.com/bingcool/gofy`**
框架未发布到你的代理时，加 `--replace /path/to/gofy`，或在 `go.mod` 里手动补一条
`replace`。在框架仓库内运行脚手架会自动处理。

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
`main.go` 的 `WithModuleFactories(...)` 里追加一个工厂。

## 子命令：`gofy new interface-service`

同目录下还有 `gofy new interface-service`，用于生成项目的**契约模块**
（`appinterface` + OpenAPI 生成器），与运行时脚手架互不依赖。它要求同级目录存在
`gofy-interface-api` 与 `gofy-adapter`，细节见 `--help`。
