# gofy Library 详细设计（v1）

> 状态：**已锁定**（2026-11-03）
>
> 关联文档：[架构与设计评审](./architecture-review.md)、[gofy-interface-service 设计](./gofy-interface-service-design.md)
>
> 生态中有两个仓库：本仓库 `gofy`（框架）与 `gofy-interface-service`（契约仓库）。gofy 不依赖契约仓库，业务服务按需组合。
>
> 本文的结论只有一个：**gofy 是一个 Go library / package，业务项目是独立的 Go module，通过 `go get github.com/bingcool/gofy` 依赖它。** 本文不再讨论"项目模板"与"library"的取舍，只描述 library 形态下每一个包、每一个导出符号、每一条运行时语义应该如何设计。
>
> 旧版本的第 11 节"如果只有一个服务就继续用 Project 模板"已废弃。当前仓库在迁移完成后不再是可运行服务，而是"框架库 + 可编译示例 + 脚手架"。
>
> **本版相对上一版的变化：**
> - 锁定 library-first，删除"项目模板 / library 二选一"的讨论；
> - 补全每个公共包的导出 API、默认值、校验规则与错误语义；
> - 补全 CLI、生命周期、配置、HTTP 请求/响应契约；
> - 增加完整 `demo-service` 示例、`gofy new` 脚手架、CI、逐文件迁移与验收清单；
> - 明确移除 `viper`、`gogf/gf`、`go-daemon`、`robfig/cron` 等不适用于 library 的依赖。

---

## 目录

1. [决策、目标与非目标](#1-决策目标与非目标)
2. [仓库结构与包依赖](#2-仓库结构与包依赖)
3. [公共 API 完整规格](#3-公共-api-完整规格)
4. [CLI 规格](#4-cli-规格)
5. [生命周期与运行语义](#5-生命周期与运行语义)
6. [配置规范](#6-配置规范)
7. [HTTP 请求与响应契约](#7-http-请求与响应契约)
8. [并发、资源所有权与优雅关停](#8-并发资源所有权与优雅关停)
9. [demo-service 完整接入示例](#9-demo-service-完整接入示例)
10. [脚手架 gofy new](#10-脚手架-gofy-new)
11. [测试策略](#11-测试策略)
12. [版本、CI 与发布](#12-版本ci-与发布)
13. [从当前仓库迁移](#13-从当前仓库迁移)
14. [验收标准](#14-验收标准)
15. [风险、开放问题与决策记录](#15-风险开放问题与决策记录)

---

## 1. 决策、目标与非目标

### 1.1 已锁定的决策

| # | 决策 | 结果 |
| --- | --- | --- |
| D1 | gofy 的发布形态 | 只作为 library / package，不维护项目模板 |
| D2 | 业务项目形态 | 独立 module，如 `github.com/yourorg/demo-service` |
| D3 | 业务入口 | 业务方拥有 `main.go`，调用 `gofy.New(...)` / `app.Execute(ctx)` |
| D4 | 业务目录 | 业务方自己创建 `app/`、`script/`，gofy 不感知其内部结构 |
| D5 | 配置所有权 | 业务方拥有配置文件和配置结构；gofy 只定义自身需要的配置 |
| D6 | 生命周期 | `New` 无副作用；`Run` / `Execute` 启动；`Shutdown` 幂等关闭 |
| D7 | 全局状态 | 公共包内禁止 `init()` 副作用、禁止包级可变单例 |
| D8 | 退出 | 库不调用 `os.Exit`；由业务 `main` 根据返回的 `error` 决定退出码 |
| D9 | 示例 | 仓库保留 `examples/demo-service`，独立 module，CI 中随框架编译 |
| D10 | 脚手架 | 提供 `gofy new <service>`，生成业务项目骨架 |
| D11 | 定时任务系统 | 不在 gofy 内实现；由业务或外部调度系统负责 |
| D12 | 守护进程 | v1 不内置 daemon；推荐 systemd / 容器编排；`start/stop` 只做前台进程 + pid |
| D13 | 代码生成 | 不在 gofy 内实现；DAO 由业务使用 `bingcool/gen` 等工具生成 |
| D14 | 兼容目标 | v1 提供稳定公共 API；内部实现可自由重构 |

### 1.2 目标

1. **可依赖**：任何业务服务只改 `go.mod` 版本号即可升级基础设施。
2. **无副作用**：`import` 任何 gofy 包都不会读文件、连数据库、打印日志、修改 `time.Local`、注册全局变量。
3. **可测试**：框架和业务都能在不监听真实端口、不依赖工作目录的情况下测试。
4. **可替换**：日志、响应格式、错误映射、DB 连接、HTTP Server 配置都有明确扩展点。
5. **可演进**：公共 API 有边界、有语义化版本、有 CI 兼容性检查。
6. **小依赖**：不把 `viper`、`gogf/gf`、`go-daemon` 这类重依赖带进业务服务。

### 1.3 非目标

- 不做服务网格、配置中心、注册中心；
- 不做 ORM 代码生成；
- 不做定时任务调度；
- 不做认证/授权平台：只内置 HMAC JWT 校验中间件，token 签发、角色模型和用户体系仍由业务负责；
- 不做指标、链路追踪的默认实现（v1 只留扩展点）；
- 不做热加载配置；
- 不承诺 Windows 下的 `stop` 信号语义；
- 不兼容当前仓库 `src/`、`app/` 的所有旧 API。

### 1.4 迁移后的使用方式（一句话）

```go
reg := registry.NewRegistry() // 组合根每次装配新建，传给所有模块工厂
app, _ := gofy.New(
    gofy.WithName("demo-service"),
    gofy.WithHTTP(gofy.HTTPConfig{Port: 8080}),
    gofy.WithMiddlewareRegistrar(demoMiddlewareRegistrar(cfg)),
    gofy.WithModuleFactories(
        product.NewModuleFactory(reg), // 属主先登记 innerapi
        order.NewModuleFactory(reg),   // 消费方构造期取用
    ),
)
if err := app.Execute(ctx); err != nil {
    os.Exit(gofy.ExitCode(err))
}
```

---

## 2. 仓库结构与包依赖

### 2.1 最终目录

```
github.com/bingcool/gofy/
├── go.mod                       # module github.com/bingcool/gofy
├── go.sum
├── gofy.go                      # package gofy：App、New、Run、Execute、Shutdown、ExitCode
├── option.go                    # Option 与全部 WithXxx
├── config.go                    # HTTPConfig、CORSConfig、VersionInfo、Runtime 别名
├── errors.go                    # 哨兵错误、ExitCode
├── start.go                     # HTTP 运行、模块构建、优雅关停
├── execute.go                   # CLI 分发：start/stop/script/version/自定义
├── stop.go                      # start/stop 的进程控制
├── signal.go                    # SignalContext 辅助函数
├── apperr/
│   └── apperr.go                # 业务错误模型
├── core/
│   └── runtime.go               # Runtime（被 gofy/script/command 共享）
├── support/
│   ├── module.go                # Module、ModuleFactory、Prefixer、Starter、Stopper
│   ├── router.go                # Router 薄封装
│   ├── handler.go               # 泛型 Handler 与 GET/POST/...
│   ├── bind.go                  # 请求绑定
│   ├── validate.go              # 校验与 message 标签
│   ├── responder.go             # Responder 接口与默认响应包络
│   ├── cors.go                  # CORS 配置
│   ├── jwtauth.go               # JWT 配置
│   ├── ratelimit.go             # 限流配置
│   ├── securityheaders.go       # 安全响应头配置
│   ├── middleware/              # 请求 ID、访问日志、恢复、CORS、限流、JWT、安全头、透传、别名注册表
│   ├── registry/                # 通用 name -> instance 注册表（Register/Lookup/Names/Get[T]）
│   ├── ratelimit/               # Store 接口与内存滑窗
│   │   └── redisstore/          # Redis 共享限流存储
│   ├── cache/                   # 基于注入 Redis 实例的键值缓存
│   ├── queues/                  # 基于注入 Redis 实例的即时队列与延迟队列
│   ├── amqp/                    # AMQP 连接、拓扑、发布、消费与 TTL 延迟队列
│   ├── kafka/                   # Kafka 连接、同步生产与消费组消费
│   ├── rocketmq/                # RocketMQ 连接、同步生产与拉取消费
│   ├── otel/                    # OpenTelemetry 配置、OTLP 导出与埋点助手
│   ├── encryption/              # AES-CBC/GCM 对称加解密，与 PHP 密文互通
│   ├── headerpropagation/       # 入口请求头白名单与透传上下文
│   ├── frameworkcontext/        # 统一身份读取（本地认证优先，透传头兜底）
│   ├── jwtauth/                 # HMAC JWT 校验
│   ├── nacos/                   # Nacos 配置与注册
│   ├── db/
│   │   └── db.go             # Config、New、Close
│   ├── pg/                      # PostgreSQL 连接（gorm.io/driver/postgres + pgx/v5）
│   ├── sqlite/                  # SQLite 连接（glebarez/sqlite，纯 Go 无 cgo）
│   ├── uuidint/                 # 基于 Redis 的分布式自增 int64 ID
│   ├── phpoffice/               # Excel .xlsx 导出/导入（excelize，纯 Go）
│   ├── image/                   # 图像缩放/裁剪/旋转/水印/编码（纯 Go）
│   └── redis/
│       └── redis.go             # Config、New、Ping、Close
├── logger/
│   ├── logger.go               # Config、New、Nop、Sync
│   └── gorm.go                  # GORM logger 适配（可选使用）
├── script/
│   └── script.go                # Script、Runtime 别名、Validate
├── command/
│   └── command.go               # Command、Runtime 别名、Validate
├── config/
│   └── load.go                  # 可选 YAML 加载器
├── gofytest/
│   └── app.go                   # 测试辅助
├── internal/
│   ├── pidfile/                 # pid 文件原子读写
│   ├── process/                 # 进程存活检测、SIGTERM、轮询退出
│   ├── version/                 # debug.ReadBuildInfo
│   └── commandline/             # cobra 树构建
├── cmd/
│   └── gofy/
│       ├── main.go              # 脚手架入口（go install github.com/bingcool/gofy/cmd/gofy@latest）
│       └── templates/           # go:embed 模板
├── examples/
│   └── demo-service/           # 独立 module，通过 replace 指向 ../..
├── docs/
│   ├── architecture-review.md
│   └── library-design.md
├── CHANGELOG.md
├── Makefile
└── .github/workflows/ci.yml
```

规则：

- **只有上表中的公共包是契约**：`gofy`、`apperr`、`core`、`support`、`logger`、`support/db`、`support/pg`、`support/sqlite`、`support/uuidint`、`support/phpoffice`、`support/image`、`support/redis`、`support/cache`、`support/queues`、`support/amqp`、`support/kafka`、`support/rocketmq`、`support/otel`、`support/encryption`、`support/jwtauth`、`support/headerpropagation`、`support/frameworkcontext`、`support/registry`、`script`、`command`、`config`、`gofytest`。
- `internal/` 可自由重构，不承诺兼容。
- `cmd/gofy` 只是脚手架工具，不被业务 import；它的存在不影响库的依赖。
- `examples/demo-service` 是**独立 module**，不进入 gofy 的依赖图；它通过 `replace` 指向 `../..`，并通过 `replace` 消费同级的 `gofy-interface-service` 契约模块。

### 2.2 包依赖图

```text
gofy (root)
 ├── core
 ├── support ──> apperr, core, support/jwtauth, support/ratelimit
 ├── support/middleware ──> support, support/headerpropagation, support/frameworkcontext
 ├── support/ratelimit/redisstore ──> support/ratelimit, support/redis
 ├── support/cache ──> go-redis
 ├── support/queues ──> go-redis
 ├── support/amqp ──> amqp091-go
 ├── support/kafka ──> franz-go
 ├── support/rocketmq ──> rocketmq-clients/golang/v5
 ├── script ─────> core
 ├── command ────> core
 ├── logger
 ├── support/db
 ├── support/pg ──> gorm.io/driver/postgres (jackc/pgx/v5)
 ├── support/sqlite ──> glebarez/sqlite (modernc, 无 cgo)
 ├── support/uuidint ──> support/redis, go-redis
 ├── support/phpoffice ──> xuri/excelize/v2
 ├── support/image ──> golang.org/x/image
 ├── support/redis ──> go-redis
 ├── support/jwtauth
 ├── support/headerpropagation
 ├── support/frameworkcontext ──> support/headerpropagation, support/jwtauth
 └── config

gofytest ──> gofy, support
internal/* ──> core
```

硬性规则：

| 规则 | 原因 |
| --- | --- |
| `support`、`script`、`command`、`logger`、`support/db`、`support/pg`、`support/sqlite`、`support/uuidint`、`support/phpoffice`、`support/image`、`config`、`apperr`、`core` **不得 import 根包 `gofy`** | 防止导入环，保证子包可被业务独立使用 |
| 根包 **不得 import** `gofytest`、`examples`、`cmd/gofy` | 测试/工具不能进入运行时依赖 |
| `core` 只依赖 `context`、`time`、`zap`、`gorm` | 它是共享叶子，必须最小 |
| `logger` 不依赖任何 gofy 子包 | 可被业务独立使用 |
| `support/db` 不依赖 `logger` | 通过参数接收 `*zap.Logger`，避免耦合 |
| `support/pg` 不依赖 `logger` | 通过参数接收 `*zap.Logger`，避免耦合 |
| `support/sqlite` 不依赖 `logger` | 通过参数接收 `*zap.Logger`，避免耦合 |
| 全仓库禁止 cgo：`CGO_ENABLED=0` 必须可构建，依赖图不得含 `CgoFiles` | 交叉编译与静态镜像无需 C 工具链；SQLite 用 `glebarez/sqlite`（modernc 纯 Go），`make cgo-free` 和 CI guards 校验 |
| `support/uuidint` 不依赖 `logger` | 错误通过返回值、`WithErrorHandler` 回调和 `ctx` 交给调用方 |
| `support/phpoffice` 不依赖 `logger` | 错误通过哨兵错误返回，日志由业务决定 |
| `support/image` 不依赖 `logger` | 错误通过哨兵错误返回，日志由业务决定 |
| `support/redis` 只依赖 go-redis | 不 import 任何 gofy 包，可独立使用 |

CI 中用一个脚本强制检查（见 §12.3）。

### 2.3 为什么要单独有 `core`

根包需要把 `script.Script` 作为 `WithScripts` 的参数，而 `script.Script.Run` 又需要拿到运行时依赖。如果 `Runtime` 定义在根包，就会形成：

```text
gofy -> script -> gofy   // 导入环
```

解决办法是把共享类型下沉到 `core`，根包、`script`、`command` 都用类型别名暴露：

```go
// package core
type Runtime struct { /* ... */ }

// package gofy
type Runtime = core.Runtime

// package script
type Runtime = core.Runtime

// package command
type Runtime = core.Runtime
```

因为别名是**同一个类型**，下面三种写法完全等价，`gofy.Runtime`、`script.Runtime`、`command.Runtime` 可以互相赋值：

```go
func(ctx context.Context, rt *gofy.Runtime, fs *pflag.FlagSet) error
func(ctx context.Context, rt *script.Runtime, fs *pflag.FlagSet) error
func(ctx context.Context, rt *core.Runtime, fs *pflag.FlagSet) error
```

业务方通常只需要写 `*gofy.Runtime` 或 `*gofyscript.Runtime`，不需要直接 import `core`。

### 2.4 为什么公共包禁止 `init()`

当前仓库的 `src/conf`、`src/log`、`src/request`、`src/system`、`src/cmd/command` 都在 `init()` 里读 YAML、初始化 logger、解析 `os.Args`、甚至 panic。作为 library，这会造成：

- 业务无法控制初始化顺序；
- 测试无法在不准备 `config.yaml` 的情况下 import；
- 同一个进程内无法创建两个隔离的 App；
- `go test ./...` 在某些目录下直接 panic。

v1 的规则是：

> **公共包中不允许出现任何 `func init()`。** 唯一例外是 `cmd/gofy`（可执行工具，不被 import）。

CI 用 `grep -rn "func init()" --include="*.go"` 对公共包做检查。

---

## 3. 公共 API 完整规格

### 3.1 根包 `gofy`

```go
package gofy

import (
    "context"
    "net/http"
    "time"

    "github.com/bingcool/gofy/core"
    "github.com/bingcool/gofy/logger"
    gofydb "github.com/bingcool/gofy/support/db"
    "github.com/bingcool/gofy/support"
    "go.uber.org/zap"
)

type Runtime = core.Runtime
type VersionInfo = core.VersionInfo
type ModuleFactory = support.ModuleFactory
type Middleware = support.Middleware
type Responder = support.Responder
type CORSConfig = support.CORSConfig
type DatabaseConfig = gofydb.Config
type LogConfig = logger.Config

// App 是一次应用运行的容器。
// 它持有配置、logger、DB、HTTP Server、模块、脚本与命令。
// App 的零值不可用，必须通过 New 创建。
type App struct {
    // unexported
}

// New 只做参数校验与对象构建：
//   - 不读配置文件
//   - 不连接数据库
//   - 不创建目录
//   - 不写 pid 文件
//   - 不监听端口
//   - 不注册信号
func New(opts ...Option) (*App, error)

// Execute 读取 os.Args[1:]，分发 start / stop / script / version / 自定义命令。
// 不调用 os.Exit；返回 nil 表示正常结束。
func (a *App) Execute(ctx context.Context) error

// ExecuteArgs 与 Execute 相同，但显式传入参数，便于测试。
func (a *App) ExecuteArgs(ctx context.Context, args []string) error

// Run 直接以 HTTP 服务运行，不解析命令行。
// 阻塞直到 ctx 取消、监听失败或运行时致命错误。
func (a *App) Run(ctx context.Context) error

// Handler 构建并返回 http.Handler，不监听端口。
// 用于测试、Serverless 或嵌入到已有 HTTP Server。
// 它不调用 Module.Start / Module.Stop；完整生命周期请使用 Run。
func (a *App) Handler(ctx context.Context) (http.Handler, error)

// Shutdown 主动触发关闭，幂等；未启动时是 no-op。
// 如果 Run 正在执行，会等待其退出或 ctx 到期。
func (a *App) Shutdown(ctx context.Context) error

// Logger 返回应用 logger；New 之后始终非 nil。
func (a *App) Logger() *zap.Logger

// Version 返回构建版本信息。
func (a *App) Version() VersionInfo

// Name 返回应用名。
func (a *App) Name() string

// SignalContext 返回一个在 SIGINT/SIGTERM 时取消的 ctx。
// 它只是语法糖，App 本身不会隐式注册信号。
func SignalContext(parent context.Context) (context.Context, context.CancelFunc)

// ExitCode 把 error 映射为进程退出码，供 main 使用。
func ExitCode(err error) int
```

#### `Run` / `Execute` / `Handler` 的选择

| 场景 | 使用 | 说明 |
| --- | --- | --- |
| 可执行服务，需要 `start` / `stop` / `script` / `version` | `app.Execute(ctx)` | CLI 语义；`start` 是默认命令 |
| 容器 / systemd 前台服务，不需要 CLI 分发 | `app.Run(ctx)` | 等价于 `start`，但没有参数解析 |
| 单元测试、Serverless、嵌入已有 HTTP Server | `app.Handler(ctx)` | 不监听端口，不启动模块生命周期 |
| 需要 `stop` 命令 | `app.Execute(ctx)` | `stop` 不需要构建 Runtime / DB |

#### 状态机

```text
        New
         │
         ▼
       new ────── Execute(stop/version) ──────► stopped
         │
         ├── Execute(start) / Run / Handler ──► building
         │                                         │
         │                                         ├─ error ──► failed
         │                                         ▼
         │                                      running
         │                                         │
         └──────────── Shutdown / ctx 取消 / 致命错误 ──► stopping ──► stopped
```

约束：

- `Run`、`Execute`、`Handler` 三者**只能调用其中一个**；第二次调用返回 `ErrAlreadyStarted`。
- `Shutdown` 可以在任何状态调用；多次调用返回相同结果。
- `Handler` 之后仍可 `Shutdown`，用于释放 DB。
- `New` 返回错误后，`App` 不可用。

### 3.2 Option 全表

```go
type Option func(*options) error
```

| Option | 默认值 | 说明 | 校验规则 / 时机 |
| --- | --- | --- | --- |
| `WithName(string)` | `"app"` | 应用名，用于日志、pid、version | 非空，匹配 `^[a-z][a-z0-9-]{0,62}$` |
| `WithEnv(string)` | `"dev"` | 环境标识，写入 `Runtime.Env` 和基础日志字段 | 非空 |
| `WithLogger(*zap.Logger)` | 由 `WithLogConfig` 构建 | 注入 logger；App 不负责 `Sync` | 非 nil |
| `WithLogConfig(LogConfig)` | console + info | 未注入 logger 时使用 | `Validate()` |
| `WithHTTP(HTTPConfig)` | 见 §3.6 | HTTP 监听与超时 | `Validate()` |
| `WithAPIPrefix(string)` | `"/api/v1"` | 业务路由统一前缀；`""` 表示不加前缀 | 规范化后必须以 `/` 开头 |
| `WithMiddleware(...Middleware)` | 空 | 业务全局中间件，位于框架中间件之后 | 不允许 nil |
| `WithMiddlewareRegistrar(MiddlewareRegistrar)` | 空 | 模块绑定路由前，把应用层中间件别名注册进 App 的共享注册表 | 不允许 nil；执行错误中止启动 / 热更新；`Reload` 可用 `ReloadSpec.MiddlewareRegistrars` 替换 |
| `WithPIDFile(string)` | `""`（关闭） | pid 文件路径；相对路径基于 `WithWorkDir` | 路径非空；启动阶段校验父目录 |
| `WithPIDFilePerm(os.FileMode)` | `0o644` | pid 文件权限 | 必须包含写权限 |
| `WithPIDRefreshInterval(time.Duration)` | `0`（关闭） | 周期性重写 pid；`0` 表示只写一次 | `>= 0` |
| `WithWorkDir(string)` | 进程当前目录 | pid、相对路径的基准目录 | 启动阶段校验存在且是目录 |
| `WithDatabase(DatabaseConfig)` | 未配置 | App 创建、持有并关闭 DB | DSN 非空、连接池参数合法 |
| `WithDB(*gorm.DB)` | 未配置 | 注入已有 DB；**App 不 Ping、不关闭** | 与 `WithDatabase` 互斥 |
| `WithModuleFactories(...ModuleFactory)` | 空 | 启动时构建模块 | 不允许 nil；名称在构建阶段去重 |
| `WithModules(...support.Module)` | 空 | 直接注入模块实例（测试/简单场景） | 名称去重 |
| `WithScripts(...script.Script)` | 空 | 注册脚本 | 名称唯一、Run 非 nil、保留名检查 |
| `WithCommands(...command.Command)` | 空 | 注册自定义命令 | 名称唯一、不与内置冲突 |
| `WithShutdownTimeout(time.Duration)` | `10s` | HTTP 优雅关闭 + 模块停止的总预算 | `> 0` |
| `WithResponder(support.Responder)` | `support.NewResponder(support.ResponderConfig{})` | 统一响应格式 | 非 nil |
| `WithRecovery(bool)` | `true` | 是否安装 panic 恢复中间件 | — |
| `WithAccessLog(bool)` | `true` | 是否安装访问日志 | — |
| `WithHeaderPropagation(bool)` | `false` | 入口白名单请求头写入 request context，供下游客户端透传 | — |
| `WithCORS(CORSConfig)` | 关闭 | 跨域配置 | credentials + wildcard 冲突时报错 |
| `WithJWTAuth(JWTAuthConfig)` | 关闭 | 模块路由的 Bearer Token 校验 | `Parser` 非 nil |
| `WithRateLimit(RateLimitConfig)` | 关闭 | 模块路由的滑动窗口限流 | Store 非 nil、Limit/Window > 0 |
| `WithSecurityHeaders(SecurityHeadersConfig)` | 关闭 | 统一安全响应头 | 头值不得含换行 |
| `WithHealthEndpoints(bool)` | `false` | 是否注册 `/healthz`、`/readyz` | — |
| `WithMaxBodyBytes(int64)` | `4 << 20` | 请求体上限 | `> 0` |
| `WithTrustedProxies(...string)` | 空（不信任代理） | 设置 gin 的 trusted proxies | CIDR 合法 |
| `WithAlwaysOK(bool)` | `false` | 兼容旧行为：所有响应 HTTP 200 | — |
| `WithServerConfigurator(func(*http.Server) error)` | nil | HTTP Server 逃生口（TLS、ConnState 等） | 执行错误会中止启动 |
| `WithVersionInfo(VersionInfo)` | 从 `debug.ReadBuildInfo` 推导 | 构建信息 | — |
| `WithTelemetry(TelemetryConfig)` | 关闭 | OpenTelemetry 追踪；启用后安装 Gin 中间件、为自有 DB 埋点 | `Config.Validate()`；启动时固定，`Reload` 不重建 |

多次调用 `WithModuleFactories`、`WithModules`、`WithScripts`、`WithCommands` 是**累加**的，不是覆盖。构建顺序固定为：

```text
全局 engine 中间件 + 内建别名注册
  -> MiddlewareRegistrar（按注册顺序，补充应用层别名）
  -> ModuleFactory（按注册顺序）
  -> 直接注入的 Module（按注册顺序）
  -> 脚本 / 命令只注册，不参与启动顺序
```

直接注入的 `Module` 在 `New` 之前就已存在，拿不到 `Runtime`；需要 DB / logger / 配置的模块应使用 `WithModuleFactories`。`WithModules` 适合无运行时依赖的模块、静态路由和测试替身。

`New` 的错误策略：**第一个错误立即返回**，不做部分初始化。

`Option` 实现示例：

```go
func WithName(name string) Option {
    return func(o *options) error {
        name = strings.TrimSpace(name)
        if !namePattern.MatchString(name) {
            return fmt.Errorf("gofy: invalid app name %q", name)
        }
        o.name = name
        return nil
    }
}
```

> 不提供 `WithDefaultDB()`、`WithDefaultConfig()` 这类"读默认文件"的选项。库不猜测业务环境。

### 3.3 `core.Runtime`

```go
package core

import (
    "context"
    "time"

    "go.opentelemetry.io/otel/propagation"
    "go.opentelemetry.io/otel/trace"
    "go.uber.org/zap"
    "gorm.io/gorm"
)

type VersionInfo struct {
    Version   string
    Commit    string
    BuildTime string
    GoVersion string
}

// Runtime 是框架在启动阶段构建好的共享依赖。
// 它不包含业务对象，也不提供服务容器。
type Runtime struct {
    Name      string
    Env       string
    Logger    *zap.Logger
    DB        *gorm.DB      // 未配置 DB 时为 nil
    Version   VersionInfo
    StartedAt time.Time

    // 追踪器；未启用 OpenTelemetry 时 TracerProvider 为 no-op，
    // Tracing 为 false，业务无需自行判空。
    TracerProvider trace.TracerProvider
    Propagator     propagation.TextMapPropagator
    Tracing        bool

    ctx  context.Context
    fail func(error)
}

// RuntimeConfig 仅供框架构建 Runtime 使用；业务通常不直接调用。
type RuntimeConfig struct {
    Name      string
    Env       string
    Logger    *zap.Logger
    DB        *gorm.DB
    Version   VersionInfo
    StartedAt time.Time
    Ctx       context.Context
    Fail      func(error)

    TracerProvider trace.TracerProvider
    Propagator     propagation.TextMapPropagator
    Tracing        bool
}

// NewRuntime 由 gofy 根包在启动阶段调用。
func NewRuntime(cfg RuntimeConfig) *Runtime

// Context 在应用开始关闭时取消。
// 后台 goroutine 应监听它，而不是自己注册信号。
func (r *Runtime) Context() context.Context

// Fail 上报一个致命错误，触发应用关闭。
// 第一次调用生效，后续调用被忽略；不会 panic。
func (r *Runtime) Fail(err error)
```

设计说明：

- `Runtime` 由 `core.NewRuntime` 在启动阶段构造；业务只读使用，不要自己创建或修改字段。
- `Runtime` **不是**依赖注入容器。业务自己的 service、repository、cache 不应该塞进 `Runtime`。
- 业务依赖的正确注入方式是**闭包**：在 `main` 里构建配置，传给 `NewModuleFactory`，工厂再用 `rt.DB`、`rt.Logger` 构建业务对象。
- `Runtime.Fail` 用于后台 worker：例如 consumer 协程发现无法恢复的错误时调用，应用会像收到信号一样优雅关闭。
- `Fail(err)` 内部通过 `context.WithCancelCause` 取消 Runtime 的 context；`Run` 结束时用 `context.Cause` 取出原始错误返回给调用方。
- 脚本/命令拿到的 `Runtime` 中 `Fail` 是 no-op（一次性任务由返回的 `error` 表达失败）。

### 3.4 `support`

#### 3.4.1 Module 与生命周期

```go
package support

import (
    "context"

    "github.com/bingcool/gofy/core"
    "github.com/gin-gonic/gin"
)

type Middleware = gin.HandlerFunc

// Module 是业务接入框架的唯一入口。
type Module interface {
    Name() string
    Register(r *Router) error
}

// ModuleFactory 在启动阶段被调用，此时 Runtime 已构建完成。
type ModuleFactory func(rt *core.Runtime) (Module, error)

// Prefixer 可选：自定义模块挂载前缀。
// 未实现时默认 "/" + kebab-case(Module.Name())。
type Prefixer interface {
    Prefix() string
}

// Starter 可选：HTTP 开始监听后调用。
type Starter interface {
    Start(ctx context.Context) error
}

// Stopper 可选：HTTP 停止接收新请求后调用。
type Stopper interface {
    Stop(ctx context.Context) error
}
```

模块约束：

| 约束 | 说明 |
| --- | --- |
| `Name()` 非空、唯一 | 匹配 `^[a-z][a-z0-9-]{0,62}$`；重复时 `New`/启动失败 |
| `Register` 只注册路由 | 不要在这里连接 DB、启动 goroutine；用 `Start` |
| `Register` 返回错误 | 框架会把模块名包进错误返回 |
| `Register` 内 panic | 框架 recover 并转换为错误，不会让进程崩溃 |
| `Start` 顺序 | 按注册顺序串行调用 |
| `Start` 的 ctx | 与 `rt.Context()` 相同，关停时取消 |
| `Stop` 顺序 | 按注册**逆序**串行调用 |
| `Stop` 的 ctx | 新建的、带 `WithShutdownTimeout` 的 context；不是已取消的 app ctx |
| 前缀冲突 | 构建阶段检测，冲突返回错误，不监听端口 |

路由前缀规则：

```text
base = WithAPIPrefix()            // 默认 /api/v1
prefix = Module.Prefix()          // 未实现时 "/" + Name()
moduleRouter = base.Group(prefix)
```

`Prefix()` 返回值会被规范化为以 `/` 开头、不以 `/` 结尾；`Prefix() == ""` 表示直接挂在 base 根上。

例如 `order` 模块注册 `"/orders/:id"`，最终路径是：

```text
/api/v1/order/orders/:id
```

#### 3.4.2 Router

```go
package support

import "github.com/gin-gonic/gin"

type Router struct {
    group  *gin.RouterGroup
    engine *engine // 持有 validator、responder、日志等
    prefix string
}

type RouterConfig struct {
    Prefix    string
    Responder Responder
    Validator *validator.Validate // 为空时使用默认校验器
}

// NewRouter 由 gofy 根包在构建 HTTP 层时调用。
func NewRouter(e *gin.Engine, cfg RouterConfig) *Router

func (r *Router) Group(prefix string, mw ...Middleware) *Router
// ModuleGroup 由框架创建模块 Router 时使用，不累计契约绑定前缀。
func (r *Router) ModuleGroup(prefix string, mw ...Middleware) *Router
func (r *Router) GroupPrefix() string
func (r *Router) Use(mw ...Middleware)
func (r *Router) GET(path string, h gin.HandlerFunc, mw ...Middleware)
func (r *Router) POST(path string, h gin.HandlerFunc, mw ...Middleware)
func (r *Router) PUT(path string, h gin.HandlerFunc, mw ...Middleware)
func (r *Router) PATCH(path string, h gin.HandlerFunc, mw ...Middleware)
func (r *Router) DELETE(path string, h gin.HandlerFunc, mw ...Middleware)
func (r *Router) HEAD(path string, h gin.HandlerFunc, mw ...Middleware)
func (r *Router) OPTIONS(path string, h gin.HandlerFunc, mw ...Middleware)
func (r *Router) Handle(method, path string, h gin.HandlerFunc, mw ...Middleware)
func (r *Router) Static(relativePath, root string)
func (r *Router) Prefix() string
func (r *Router) Gin() *gin.RouterGroup // 逃生口，明确标注为非兼容承诺
```

`Router` 只做四件事：

1. 维护统一前缀；
2. 保证中间件顺序；
3. 提供泛型 handler 的注册入口；
4. 提供 `Gin()` 逃生口。

它**不**重新实现路由、参数解析、分组继承等 gin 已有能力。

#### 3.4.3 泛型 Handler

Go 不支持泛型方法，所以泛型注册函数必须是**自由函数**，并显式接收 `*Router`：

```go
package support

import "context"

type Handler[Req any, Res any] func(ctx context.Context, req *Req) (*Res, error)

func GET[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware)
func POST[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware)
func PUT[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware)
func PATCH[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware)
func DELETE[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware)
func Any[Req any, Res any](r *Router, method, path string, h Handler[Req, Res], mw ...Middleware)

// NoContent 用于没有响应体的 handler，成功时返回 204。
func NoContent[Req any](r *Router, method, path string, h func(context.Context, *Req) error, mw ...Middleware)
```

使用：

```go
import "net/http"

func (m *Module) Register(r *support.Router) error {
    h := handler.New(m.svc)
    support.GET(r, "/orders/:id", h.Get)
    support.POST(r, "/orders", h.Create)
    support.GET(r, "/orders", h.List)
    support.NoContent(r, http.MethodDelete, "/orders/:id", h.Cancel)
    return nil
}
```

为什么不是 `r.GET(path, support.Handle(h.Get))`：

- `Handle` 需要拿到当前 App 的 validator 和 responder；
- 这些对象属于 App 实例，不能放在包级全局变量里；
- 自由函数接收 `*Router`，从 `r.engine` 读取实例配置，既没有全局状态，也不需要把 `r` 传两次。

#### 3.4.4 请求绑定与校验

适配器的执行顺序是固定的：

```text
defaults.Set(&req)
    -> ShouldBindUri(&req)        // 路径参数
    -> ShouldBind(&req)           // query / form / JSON，由 gin 按 method + Content-Type 选择
    -> validator.Struct(&req)     // 单例 validator
    -> 调用业务 handler
    -> Responder.Success / Responder.Error
```

规则：

| 项目 | 规则 |
| --- | --- |
| 默认值 | 在绑定前执行 `defaults.Set`，支持 `default:"..."` |
| 路径参数 | 使用 `uri:"id"` 标签 |
| Query | GET/DELETE 的 `form:"page"` 标签 |
| JSON | POST/PUT/PATCH 的 `json:"..."` 标签 |
| 绑定失败 | 返回 `400` + `apperr.InvalidArgument`，不 panic |
| 校验失败 | 返回 `400` + 字段级 `details`，不 panic |
| 校验器 | 每个 App 一个 `*validator.Validate`，不在每次请求里 `validator.New()` |
| 上下文 | handler 收到 `c.Request.Context()`，请求取消可向下传递 |
| 请求体上限 | 由 `WithMaxBodyBytes` 控制，超限返回 `413` |
| `*gin.Context` | 不允许传入业务 handler，不允许跨 goroutine 使用 |
| handler 返回 `(nil, nil)` | 响应 `data: null` |

校验消息标签：

```go
type GetOrderRequest struct {
    ID uint64 `uri:"id" validate:"required,min=1" message:"required:订单ID不能为空,min:订单ID非法"`
}
```

`message` 标签格式为 `tag:文案,tag:文案`，由框架解析；未匹配到文案时使用 validator 的默认英文消息。

#### 3.4.5 Responder 与响应包络

```go
package support

import (
    "github.com/gin-gonic/gin"
)

type Responder interface {
    Success(c *gin.Context, data any)
    Error(c *gin.Context, err error)
}

type ResponderConfig struct {
    SuccessCode int    // 默认 0
    SuccessMsg  string // 默认 "success"
    RequestIDKey string // 默认 "req_id"
}

func NewResponder(cfg ResponderConfig) Responder
```

默认成功响应：

```json
{
  "code": 0,
  "msg": "success",
  "data": {},
  "req_id": "0b0f1f6e-..."
}
```

默认错误响应：

```json
{
  "code": 40001,
  "msg": "invalid argument",
  "data": null,
  "req_id": "0b0f1f6e-...",
  "details": {
    "id": "订单ID非法"
  }
}
```

约定：

- `code = 0` 表示成功；非 0 表示业务错误。
- `details` 仅在存在字段错误时出现。
- 堆栈**永远不进入响应体**。
- 自定义响应格式通过 `WithResponder` 注入；框架不提供继承式扩展。

#### 3.4.6 中间件

框架中间件位于 `support/middleware`，全部以"显式接收依赖"的方式实现，不使用包级 logger。

```go
func RequestID(header string) gin.HandlerFunc
func HeaderPropagation(trustedProxies []string) gin.HandlerFunc
func AccessLog(logger *zap.Logger, opts AccessLogOptions) gin.HandlerFunc
func Recovery(logger *zap.Logger, responder support.Responder) gin.HandlerFunc
func CORS(cfg support.CORSConfig) (gin.HandlerFunc, error)
func BodyLimit(maxBytes int64) gin.HandlerFunc
func SecurityHeaders(cfg support.SecurityHeadersConfig) (gin.HandlerFunc, error)
func RateLimit(cfg support.RateLimitConfig, responder support.Responder) (gin.HandlerFunc, error)
func JWTAuth(cfg support.JWTAuthConfig, responder support.Responder) (gin.HandlerFunc, error)
```

每个中间件在自己的文件里导出一个别名常量，供 gofy-interface-service 的契约路由
按名字引用（契约层不依赖 gofy/gin，只写字符串）：

```go
const (
	NameRequestID         = "request-id"          // req_trace_id.go
	NameHeaderPropagation = "header-propagation"  // headerpropagation.go
	NameAccessLog         = "access-log"          // accesslog.go
	NameRecovery          = "recovery"            // recovery.go
	NameCORS              = "cors"                // cors.go
	NameBodyLimit         = "body-limit"          // bodylimit.go
	NameSecurityHeaders   = "security-headers"    // securityheaders.go
	NameRateLimit         = "rate-limit"          // ratelimit.go
	NameJWTAuth           = "jwt-auth"            // jwt.go
)
```

别名只是名字，不会自动注册。`registry.go` 提供显式的「别名 → 实例」注册表，
由 App 在装配 HTTP 层时创建一份，先把内建中间件逐个注册进去，再执行应用注册的
`MiddlewareRegistrar` 补充应用到别名：

```go
func NewRegistry() *Registry
func (r *Registry) Register(name string, mw support.Middleware) error
func (r *Registry) MustRegister(name string, mw support.Middleware)
func (r *Registry) Lookup(name string) (support.Middleware, bool)
func (r *Registry) Names() []string

type MiddlewareRegistration struct {
    Runtime   *core.Runtime
    Registry  *Registry
    Responder support.Responder
}
type MiddlewareRegistrar func(MiddlewareRegistration) error

func WithMiddlewareRegistrar(fn MiddlewareRegistrar) Option
```

- 别名与实例一一对应：`Register` 校验别名非空（去首尾空白）、实例非 `nil`、
  别名未被占用，重复注册返回错误而不是覆盖；`MustRegister` 是启动期 panic 版本；
- 内部 `sync.RWMutex`，注册与查找可并发；`Names()` 返回排序副本用于诊断；
  `Lookup` 对 `nil` 注册表返回 `(nil, false)`；
- 注册表由 App 持有并随根 `support.Router`（`support.RouterConfig.Middleware`）
  分发到所有模块分组，业务模块只按契约名字解析，不再自己创建或注入；
- `MiddlewareRegistrar` 在模块绑定路由之前执行，失败会使启动（或热更新）失败；
  每次启动/`Reload` 都用新的 Runtime、Responder 和空注册表重新执行，Nacos
  更新 jwt 等配置后立即得到新实例；
- adapter 只依赖 `Lookup(name string) (support.Middleware, bool)`
  （`gofyadapter.MiddlewareRegistry` 接口），解析优先级为
  `BindOptions.Middleware`（单次覆盖）→ `BindOptions.Registry`（可选显式注入）
  → Router 携带的 App 注册表，三处都缺失时启动期报错；
- 认证不再有特殊通道：`AuthRequired` 只是 OpenAPI 标记，需要认证的契约路由
  显式声明 `group.Use("auth")` 或 `spec.Middleware = []string{"auth"}`，与其他
  别名一样从注册表解析；不存在默认别名或 `"jwt-auth"` 兜底；
- **注册表只服务契约路由**，不接管下面的 App 级安装顺序；把全局链里已安装的
  中间件再注册给契约路由会执行两次，只在需要按路由覆盖全局行为时才这么做。

安装顺序（由外到内）：

```text
1. RequestID
2. HeaderPropagation（可选，WithHeaderPropagation）
3. SecurityHeaders（可选）
4. AccessLog
5. CORS（可选）
6. Recovery
7. BodyLimit
8. 业务全局中间件（WithMiddleware）
9. 模块/路由中间件
10. RateLimit（可选，WithRateLimit）
11. JWTAuth（可选，WithJWTAuth）
12. 业务 handler
```

顺序理由：

- `RequestID` 最外层，后续所有日志都能拿到 `req_id`；
- `HeaderPropagation` 紧跟 `RequestID`，用 `req_id` 作为 `X-Trace-Id` 的兜底值，
  同时保证业务中间件和 handler 都能从 context 读到透传头；
- `SecurityHeaders` 在 `AccessLog` 外层，404/405 和 panic 后的错误响应也带安全头；
- `AccessLog` 必须在 `Recovery` 外层，它的 `defer` 才能记录到 panic 后的 `500`；
- `CORS` 在 `Recovery` 外层，保证 panic 响应也带跨域头；
- `Recovery` 在业务中间件外层，业务 panic 不会击穿进程；
- `BodyLimit` 在业务之前，避免大请求进入业务逻辑；
- `RateLimit` 与 `JWTAuth` 只挂在业务路由组上：`/healthz`、`/readyz` 和未匹配路径不受影响，
  限流先于 JWT 执行，未通过认证的请求同样计入配额；
- `JWTAuth` 在业务 handler 之前，handler 通过 `support.JWTClaims(c)` 读取已校验的 claims。

`AccessLogOptions`：

```go
type AccessLogOptions struct {
    SkipPaths      []string      // 默认 ["/healthz", "/readyz"]
    SlowThreshold  time.Duration // 默认 500ms，超过打 warn
    LogRequestID   bool          // 默认 true
    LogUserAgent   bool          // 默认 true
    LogClientIP    bool          // 默认 true
    MaxPathLength  int           // 默认 1024，防止超长 URL 污染日志
    LogQuery       bool          // 默认 true，记录脱敏后的 query
    SensitiveQuery []string      // 默认 DefaultSensitiveQueryKeys()
}
```

`SensitiveQuery` 为 nil 时使用默认敏感键（password、token、authorization、api_key、
signature 等，大小写不敏感），匹配到的值替换为 `***` 后重新编码；query 无法解析时
整个字段记为 `***`。传入空切片可关闭脱敏，但不建议。

访问日志字段：

```json
{
  "level": "info",
  "msg": "http access",
  "req_id": "...",
  "method": "GET",
  "path": "/api/v1/order/orders/1",
  "query": "page=2&token=%2A%2A%2A",
  "status": 200,
  "bytes": 123,
  "latency_ms": 3.1,
  "client_ip": "10.0.0.1",
  "user_agent": "curl/8.0"
}
```

`query` 仅在请求带 query 时出现，且已按 `SensitiveQuery` 脱敏。

级别规则：`5xx -> error`，`4xx -> warn`，其余 `info`。

`Recovery` 规则：

- 记录 `req_id`、method、path、panic 值、堆栈；
- 响应固定 `500` + `code 50000` + `"internal server error"`；
- 不把 panic 值或堆栈返回给客户端；
- `WithRecovery(false)` 时不安装，panic 会按 gin 默认行为冒泡（不推荐）。

`RequestID` 规则（实现文件 `support/middleware/req_trace_id.go`）：

- 上游优先：请求头 `X-Trace-ID` 合法时直接沿用；即使业务配置了自定义头，
  也不会覆盖上游已建立的 trace；都没有时才生成 UUID v4；
- 只接受 `^[A-Za-z0-9._-]{1,128}$`，否则重新生成；
- 写回响应头，并放入 `gin.Context`，key 为 `gofy.request_id`；
- 同时写入 request context：
  - `support.ContextWithRequestID`：业务用
    `support.RequestIDFromContext(ctx)` 直接读取；
  - `headerpropagation` 的 `X-Trace-Id`：`gofy-interface-service/client` 会从
    同一个 context key 读取并注入下游请求，因此无需手工调用
    `client.ContextWithRequestID`；上游已有 `X-Trace-ID` 时传递的就是原值。

`HeaderPropagation` 规则（`WithHeaderPropagation(true)` 时安装）：

- 白名单：`X-Trace-Id`、`X-User-Id`、`X-User-Code`、`X-Tenant-Id`、
  `X-User-Name`、`X-Client-Ip`、`X-User-Agent`；`Authorization`、`Cookie`
  等用户凭证永远不进入透传上下文；
- 身份头（`X-User-Id`、`X-User-Code`、`X-Tenant-Id`、`X-User-Name`）只在
  `RemoteAddr` 命中 `WithTrustedProxies` 的 IP/CIDR 时采信；否则丢弃，
  并把 `X-Client-Ip` 覆盖为直连地址，防止公网客户端冒充租户；
- `X-Trace-Id` 缺失时用 `RequestID` 的 `req_id` 补齐，保证下游始终有 trace；
- 结果写入 request context，key 为 `gofy.header_propagation.headers`，
  handler 用 `support/headerpropagation.HeadersFromContext(ctx)` 读取；
- `gofy-interface-service/client` 用同一个字符串 key 读取并注入下游请求，
  两个模块互不 import。合并顺序为「透传头 < Request `header` tag < `CallOption`」，
  单次调用可以覆盖；`X-User-Agent` 无透传值时默认 `gofy-api-sdk`；
- 认证中间件在 handler 前解析出用户后，应通过 `frameworkcontext.WithUser`
  写入本地身份（见 §3.4.10）；它会把身份头对齐后写回 context，下游客户端自动携带。

#### 3.4.7 CORS

行为对齐 swoolefy `CorsMiddleware`：

```go
type CORSConfig struct {
    AllowOrigins        []string // 完整来源或裸域名；"*" 表示任意来源
    AllowOriginPatterns []string // *.example.com / example.com（含子域）
    AllowPaths          []string // RequestURI 白名单，shell 通配符；空 = ["*"]
    AllowMethods        []string
    AllowHeaders        []string
    ExposeHeaders       []string
    AllowCredentials    bool
    MaxAge              time.Duration
}
```

默认（`WithCORS` 不调用、或来源与模式都为空时完全不启用）：

```go
CORSConfig{
    AllowOrigins:  []string{},
    AllowMethods:  []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
    AllowHeaders:  []string{"*"},           // 与 PHP 一致
    MaxAge:        24 * time.Hour,          // 86400 秒
}
```

匹配与响应：

- `AllowOrigins` 中的裸域名（`example.com`）自动展开为 `http://` 与
  `https://` 两种；已带 scheme 的来源按原样精确匹配；
- `AllowOriginPatterns` 只接受纯域名或 `*.域名`，命中 http/https 的该域名及
  任意子域；带 scheme/路径/端口/@ 的模式在构造中间件时报错；
- 带 `Origin` 的请求先校验来源，再校验 `AllowPaths`（对含 query 的
  `RequestURI` 做 fnmatch 风格匹配），任一不通过立即返回 403 纯文本
  （`Origin 403 Forbidden` / `api 403 Forbidden, path=...`）；
- 命中后**所有方法**都写 `Allow-Origin`、`Allow-Methods`、`Allow-Headers`
  （配置值，默认 `*`）、`Expose-Headers`、`Max-Age` 与 `Vary: Origin`；
  任意来源且不带凭证时写 `*`，否则回显具体 Origin，凭证模式追加
  `Allow-Credentials: true`；
- `OPTIONS` 统一返回 204（无论是否带 Origin）；无 Origin 的普通请求不做任何
  CORS 处理。

校验与差异：

- `AllowCredentials == true` 且 `AllowOrigins` 包含 `"*"` -> 构造报错；
- `AllowOrigins` 与 `AllowOriginPatterns` 都为空 -> 不启用（PHP 默认是
  `["*"]` 且模式默认 `*.example.com`；Go 版要求显式开启，避免默认信任域名）；
- 不读取 `CORS_*` 环境变量，所有值由调用方显式传入。

#### 3.4.8 404 / 405 统一响应

`buildHTTP` 始终安装兜底处理器：

```go
engine.HandleMethodNotAllowed = true
engine.NoRoute(...)  // apperr.NotFound("route not found")        -> 404 / 40400
engine.NoMethod(...) // apperr.MethodNotAllowed("method not allowed") -> 405 / 40500
```

- 未匹配路径和错误方法返回与业务 handler 相同的包络，并带上 `req_id`；
- gin 在 405 响应上写 `Allow` 响应头，列出该路径支持的方法；
- 通过 `WithAlwaysOK` 时 HTTP 状态变为 200，`code` 仍为 40400 / 40500；
- 业务 handler 主动返回的 `apperr.NotFound`（如 `40401`）不受影响。

#### 3.4.9 JWT 认证

`support/jwtauth` 只做校验，不签发 token：

```go
type Config struct {
    Secret        []byte
    Algorithms    []string      // 默认 ["HS256"]；支持 HS256/HS384/HS512
    Issuer        string        // 非空时校验 iss
    Audience      string        // 非空时要求 aud 包含该值
    Leeway        time.Duration // exp/nbf 的时钟容差
    RequireExpiry bool          // true 时缺少 exp 直接拒绝
    Clock         func() time.Time
}

func NewParser(cfg Config) (*Parser, error)
func (p *Parser) Parse(token string) (*Claims, error)
```

`Claims` 暴露 `Subject`、`Issuer`、`Audience`、`ExpiresAt`、`NotBefore`、`IssuedAt`、
`ID` 和完整的 `Raw` map；`Get`/`String` 用于读取业务声明。

拒绝原因用哨兵错误区分，便于日志与测试：
`ErrMalformed`、`ErrUnsupportedAlgorithm`、`ErrSignature`、`ErrExpired`、
`ErrNotYetValid`、`ErrIssuer`、`ErrAudience`、`ErrMissingExpiry`。
`alg: none` 和配置外的算法一律拒绝，签名比较使用 `hmac.Equal`。

中间件配置：

```go
type JWTAuthConfig struct {
    Parser     *jwtauth.Parser
    Header     string   // 默认 "Authorization"
    Scheme     string   // 默认 "Bearer"
    Optional   bool     // 无 token/非法 token 时放行，由业务自行判断
    SkipPaths  []string
    ContextKey string   // 默认 support.JWTClaimsKey
}
```

- 失败固定返回 401 / 40100 / `"unauthorized"`，不返回 token 解析细节；
- 响应头 `WWW-Authenticate: Bearer realm="api"`；
- 成功时 claims 存入 gin.Context，`support.JWTClaims(c)` 或
  `middleware.Claims(c, key)` 读取；同时写入 `frameworkcontext`（§3.4.10），
  Service 等非 gin 层用 `frameworkcontext.UserID(ctx)` 读取；
- `WithJWTAuth` 只作用于模块路由，`/healthz`、`/readyz`、静态资源和未匹配路径不受影响；
- 需要按角色/租户授权时，在业务中间件里读取 claims 后返回 `apperr.Forbidden`。

#### 3.4.10 统一身份 FrameworkContext

`support/frameworkcontext` 把两类身份来源合并为一个读取入口，Service、Repository 等
只拿到 `context.Context` 的层不再关心身份来自本地 JWT 还是上游透传：

| 来源 | 写入方 | 读取方式 |
| --- | --- | --- |
| 本地认证（JWT / 自定义） | `JWTAuth` 中间件，或业务调用 `WithUser` | `UserFromContext`、`UserID` 等 |
| 上游透传头 | `HeaderPropagation` 中间件（仅可信代理） | 同名 getter 的兜底值 |

```go
type User struct {
    ID       string         // 用户主键；空值不写入 context
    TenantID string
    Code     string
    Name     string
    Via      string         // 凭证通道，JWT 为 "jwt"
    Claims   map[string]any // 已校验的原始 claim
}

func WithUser(ctx context.Context, user User) context.Context
func UserFromContext(ctx context.Context) (User, bool) // 只返回本地认证身份
func UserFromClaims(claims *jwtauth.Claims) User

func UserID(ctx context.Context) string   // 本地身份优先，其次透传头
func TenantID(ctx context.Context) string
func UserCode(ctx context.Context) string
func UserName(ctx context.Context) string
func TraceID(ctx context.Context) string
func ClientIP(ctx context.Context) string
func UserAgent(ctx context.Context) string
func Header(ctx context.Context, name string) string
func Headers(ctx context.Context) http.Header
```

规则：

- **本地优先**：`UserID`/`TenantID`/`UserCode`/`UserName` 先读 `WithUser` 写入的快照，
  没有本地身份时才回落到可信上游写入的 `X-User-Id` 等头，与 swoolefy
  `FrameworkContext` 的 `AuthUser` 优先语义一致；
- **写回对齐**：`WithUser` 先清空 context 中的四个身份头，再按 `User` 写回非空字段。
  token 没有租户时不会把上游残留的 `X-Tenant-Id` 继续下发给下游；`X-Trace-Id`、
  `X-Client-Ip`、`X-User-Agent` 保持不变；
- **自动接入**：`JWTAuth` 校验成功后自动执行
  `WithUser(ctx, UserFromClaims(claims))`，业务无需额外代码；
- **claim 映射**：`ID` 依次取 `user_id`、`uid`、`sub`；租户、编号、名称分别取
  `tenant_id`、`user_code`、`user_name`（`name` 兜底），字符串和数字类型的 claim
  都支持，其余声明通过 `Claims` 读取；
- **只读快照**：`WithUser` 深拷贝 `Claims`，`UserFromContext` 返回副本，调用方修改不会
  影响同一请求中的其他读取方；
- 自定义认证（session、API Key 等）在认证中间件里构造 `User` 后调用 `WithUser`，
  下游调用自动携带对齐后的身份头。

#### 3.4.11 限流

`support/ratelimit` 定义存储接口，内存实现使用滑动窗口日志：

```go
type Result struct {
    Allowed    bool
    Limit      int
    Remaining  int
    RetryAfter time.Duration
    ResetAt    time.Time
}

type Store interface {
    Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error)
}
```

`ratelimit.NewMemoryStore()` 适用于单进程部署；键在窗口过期后由周期性 sweep 清理。
`Store.SetClock` 用于注入测试时钟。多实例部署使用
`support/ratelimit/redisstore`：它用 ZSET + Lua 实现同一套滑动窗口语义，
以 Redis 服务器时间为准，多个实例共享计数；Redis 错误原样返回，由 `FailOpen`
决定放行还是 503。

```go
type RateLimitConfig struct {
    Store     ratelimit.Store
    Limit     int
    Window    time.Duration
    KeyFunc   func(*gin.Context) string // 默认 ClientIP|Method|FullPath
    SkipPaths []string
    FailOpen  bool // Store 出错时放行；默认 false -> 503 / 50300
}
```

- 每个响应都带 `X-RateLimit-Limit`、`X-RateLimit-Remaining`、`X-RateLimit-Reset`；
- 超限返回 429 / 42900，并带 `Retry-After`（秒，向上取整）；
- 默认按 `客户端 IP + 方法 + 路由` 分桶，单个热点接口不会耗尽其他接口的配额；
- 限流在 JWT 之前执行，暴力尝试同样消耗配额；
- 只作用于模块路由，健康检查不受影响；
- `FailOpen` 只建议在限流器不可用不影响安全性的场景开启。

#### 3.4.12 安全响应头

```go
type SecurityHeadersConfig struct {
    ContentTypeOptions    string            // 默认 "nosniff"
    FrameOptions          string            // 默认 "DENY"
    ReferrerPolicy        string            // 默认 "strict-origin-when-cross-origin"
    HSTS                  time.Duration     // 0 表示不发送
    HSTSIncludeSubdomains bool
    HSTSPreload           bool
    ContentSecurityPolicy string            // 非空时发送
    PermissionsPolicy     string            // 非空时发送
    Custom                map[string]string // 最后写入，可覆盖默认值
}
```

- 空字段使用 `support.DefaultSecurityHeadersConfig()` 的默认值；
- HSTS 只在确认全站 HTTPS 后开启；
- 所有值在 `New` 阶段校验，包含 `\r`/`\n` 或非法头名直接返回错误，避免响应头注入；
- 安全头安装在最外层，404、405、401、429 和 500 响应同样携带。

### 3.5 `apperr`

```go
package apperr

type Error struct {
    Code    int               // 业务码，例如 40001
    Status  int               // HTTP 状态码，例如 400
    Message string            // 对外文案
    Details map[string]string // 字段级错误，可选
    cause   error             // 内部原因，仅日志
}

func (e *Error) Error() string
func (e *Error) Unwrap() error
func (e *Error) WithCause(err error) *Error
func (e *Error) WithDetails(details map[string]string) *Error

func New(code, status int, message string) *Error
func BadRequest(message string) *Error        // 40000 / 400
func InvalidArgument(message string) *Error   // 40001 / 400
func Unauthorized(message string) *Error      // 40100 / 401
func Forbidden(message string) *Error         // 40300 / 403
func NotFound(message string) *Error          // 40400 / 404
func MethodNotAllowed(message string) *Error  // 40500 / 405
func Conflict(message string) *Error          // 40900 / 409
func TooManyRequests(message string) *Error   // 42900 / 429
func Internal(message string) *Error          // 50000 / 500
func Unavailable(message string) *Error       // 50300 / 503
func As(err error) (*Error, bool)
```

约定：

- `WithCause` / `WithDetails` 返回**副本**，不修改原错误，避免共享错误值被意外污染。
- `Error()` 只返回对外文案；内部原因通过 `Unwrap()` 获取。
- 业务层只负责"是什么错误"，不负责"怎么记录日志"；日志由 `Responder`/`Recovery` 统一处理。
- 错误链支持 `errors.Is` / `errors.As`。

使用：

```go
if errors.Is(err, gorm.ErrRecordNotFound) {
    return nil, apperr.NotFound("order not found")
}
return nil, apperr.Internal("get order failed").WithCause(err)
```

### 3.6 `logger`

```go
package logger

type Config struct {
    Level              string            `yaml:"level" default:"info"`        // debug|info|warn|error
    Encoding           string            `yaml:"encoding" default:"json"`     // json|console
    Color              string            `yaml:"color" default:"auto"`        // auto|always|never，仅 stdout/stderr
    Outputs            []string          `yaml:"outputs"`                     // stdout|stderr|文件路径
    ErrorOutputs       []string          `yaml:"error_outputs"`               // 仅 error 及以上
    SystemErrorOutputs []string          `yaml:"system_error_outputs"`        // 仅框架捕获的系统错误
    TimeLayout         string            `yaml:"time_layout" default:"2006-01-02 15:04:05"`
    Rotation           RotationConfig    `yaml:"rotation"`
    BaseFields         map[string]string `yaml:"base_fields"`
}

type RotationConfig struct {
    Enabled           bool   `yaml:"enabled"`
    Mode              string `yaml:"mode" default:"size"`  // size|hourly，仅作用于 Outputs
    DateDir           bool   `yaml:"date_dir"`             // 按 20261106 这样的日期子目录存放
    DateLayout        string `yaml:"date_layout" default:"20060102"`
    DateDirMaxAgeDays int    `yaml:"date_dir_max_age_days" default:"7"` // 整个日期目录保留天数
    MaxSizeMB         int    `yaml:"max_size_mb" default:"100"`
    MaxAgeDays        int    `yaml:"max_age_days" default:"30"`         // 文件备份保留天数
    MaxBackups        int    `yaml:"max_backups" default:"7"`
    Compress   bool   `yaml:"compress" default:"true"`
    LocalTime  bool   `yaml:"local_time" default:"true"`
}

func New(cfg Config) (*zap.Logger, error)
func NewSystem(cfg Config) (*zap.Logger, error)
func Nop() *zap.Logger
func Sync(l *zap.Logger) error
func (c Config) Validate() error
```

行为：

- 不修改 `time.Local`；时间编码使用 `TimeLayout`。
- `Outputs` 为空时默认 `["stdout"]`。
- `Rotation.Enabled == false` 时直接 `os.OpenFile` 追加，不做任何切割。
- `Rotation.Enabled == true` 且 `DateDir == true` 时，每个文件先落到日期子目录，
  例如 `storage/log/http/20261106/info.log`；进程跨天时自动切换到新目录，无需重启。
- 路径中的 `{date}` 占位符会替换为 `DateLayout` 格式的日期。当日期目录需要排在
  其他子目录之前时使用它，例如 `storage/log/script/{date}/info/fix-order.log`
  得到 `storage/log/script/20261106/info/fix-order.log`；没有占位符时日期目录
  追加在最后一层（`storage/log/script/info/20261106/fix-order.log`）。关闭
  `Rotation.Enabled` 时占位符在打开文件时展开一次。
- `Mode` 只作用于 `Outputs`（info 流）：
  - `size`：在当天目录内由 lumberjack 按 `MaxSizeMB` 切割，`MaxBackups`/`Compress` 生效；
  - `hourly`：按小时切换为 `info-h0.log` ... `info-h23.log`，不产生 lumberjack 备份。
- `ErrorOutputs` / `SystemErrorOutputs` 始终按 `size` 模式滚动，避免 error 日志
  每小时碎片化；它们同样受 `DateDir` 控制。
- `DateDir == true` 或路径含 `{date}` 时，启动和跨天时会删除超过
  `DateDirMaxAgeDays`（默认 7 天，配置为 0 也取默认值）的整个日期目录；只处理
  能用 `DateLayout` 解析的目录名，其他目录不动。`{date}` 位于整个路径段时，
  扫描它前面的目录（如 `storage/log/script/`）；否则回落到目标文件所在目录。
  `MaxAgeDays` 只用于 lumberjack 在当前日期目录内的备份文件清理。
- `DateLayout` 不能包含路径分隔符，避免目录逃逸。
- `ErrorOutputs` 非空时，error 级别同时写入 `Outputs` 与 `ErrorOutputs`。
- `SystemErrorOutputs` 非空时，`NewSystem` 返回只接收 `error` 及以上、只写这些目标的 logger；
  App 把它用于 HTTP recovery，panic 不会进入主 logger。为空时回落到主 logger，
  panic 仍写入 `ErrorOutputs`，保持旧行为。
- `BaseFields` 会作为固定字段附加到每条日志（如 `service`、`env`、`version`）。
- `Color` 只作用于 `stdout`/`stderr`：`auto`（默认）按 TTY 检测，`always` 供
  IDE 这类非 TTY 控制台强制开启，`never` 关闭；非法值在 `Validate` 阶段报错。
  文件 sink（含滚动文件）永不着色，`NO_COLOR` 与 Windows 始终优先关闭。
- 着色生效时，`error` 及以上条目渲染成红色多行块：首行是时间、大写级别、
  caller、消息和其余字段（`BaseFields` 与 `logger.With` 的字段同样保留），
  `error`/`zap.NamedError` 各自单独成行，`stacktrace` 逐帧展开，不再挤在一行
  JSON 里。低于 `error` 的条目仍按 `Encoding` 输出，文件里的格式完全不变。
- 返回的 logger 由调用方负责 `Sync`；App 内建的 logger 由 App 在关闭时 `Sync`。
- 无 `init()`、无包级 logger、无后台切日 goroutine。

`gorm.go` 提供可选的 GORM 适配：

```go
func GormLogger(l *zap.Logger, cfg GormLoggerConfig) gormlogger.Interface
```

### 3.7 `support/db`

面向所有兼容 MySQL 协议的服务器（MySQL、MariaDB、TiDB、OceanBase、
SelectDB/Doris 等）：驱动固定为 `gorm.io/driver/mysql`，DSN 指向哪种服务器
由配置决定，实例名也可以按用途自由命名（如 `selectDb`）。

```go
package db

import (
    "context"

    "go.uber.org/zap"
    "gorm.io/gorm"
)

type Config struct {
    DSN             string        `yaml:"dsn"`
    MaxIdleConns    int           `yaml:"max_idle_conns" default:"10"`
    MaxOpenConns    int           `yaml:"max_open_conns" default:"50"`
    ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime" default:"1h"`
    ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time" default:"10m"`
    PingTimeout     time.Duration `yaml:"ping_timeout" default:"5s"`
    SlowThreshold   time.Duration `yaml:"slow_threshold" default:"200ms"`
    LogLevel        string        `yaml:"log_level" default:"warn"` // silent|error|warn|info
    PrepareStmt     bool          `yaml:"prepare_stmt" default:"false"`
}

func New(ctx context.Context, cfg Config, logger *zap.Logger) (*gorm.DB, error)
func Close(db *gorm.DB) error
func (c Config) Validate() error

// Transaction 在 db 上开启事务，并把事务放进传给 fn 的 context。
func Transaction(ctx context.Context, db *gorm.DB, fn func(ctx context.Context) error) error
// FromContext 优先返回 ctx 中的事务，没有时返回 fallback。
func FromContext(ctx context.Context, fallback *gorm.DB) *gorm.DB
```

行为：

- `New` 内部执行 `PingContext`，失败返回错误，不 panic。
- `logger` 为 nil 时使用 `zap.NewNop()`。
- `DSN` 为空时返回 `ErrDSNRequired`。
- 连接池参数为 0 时使用默认值。
- `Close` 是 `sqlDB.Close()` 的薄封装。
- 不在 `New` 中打印 DSN；错误信息中的 DSN 需脱敏（至少隐藏密码段）。
- 不使用 `sync.Once`、不提供包级 `GetDb()`。

事务上下文用于“同一次请求内多个 repository 共用同一个事务”：`Transaction`
把 `*gorm.DB` 事务存进 context，repository 通过 `FromContext(ctx, fallback)`
取连接。约定：

- 嵌套 `Transaction` 复用外层事务，不创建 savepoint；内层错误冒泡到外层统一
  回滚，内层成功也不会提前提交；
- `FromContext` 优先返回事务，没有事务时才返回 fallback；显式连接不会覆盖事务；
- `fn` 为 nil 返回 `ErrTxFuncRequired`；ctx 无事务且 `db` 为 nil 返回
  `ErrTxDBRequired`；`ctx` 为 nil 时按 `context.Background()` 处理；
- 事务由调用方 service 开启，repository 只消费 ctx；context 不存放 repository /
  service，只承载 trace、request id、事务等请求范围数据。

> v1 把 GORM 作为主依赖。若未来要支持"不用 DB 的用户零 GORM 依赖"，可把 `support/db` 拆成独立子 module；这属于 v2 的开放问题（见 §15.2）。

### 3.7.1 `support/redis`

```go
package redis

import goredis "github.com/redis/go-redis/v9"

type Config struct {
    Network      string        `yaml:"network" default:"tcp"` // tcp|unix
    Addr         string        `yaml:"addr"`
    Username     string        `yaml:"username"`
    Password     string        `yaml:"password"`
    DB           int           `yaml:"db"`
    PoolSize     int           `yaml:"pool_size" default:"10"`
    MinIdleConns int           `yaml:"min_idle_conns" default:"2"`
    DialTimeout  time.Duration `yaml:"dial_timeout" default:"5s"`
    ReadTimeout  time.Duration `yaml:"read_timeout" default:"3s"`
    WriteTimeout time.Duration `yaml:"write_timeout" default:"3s"`
    PingTimeout  time.Duration `yaml:"ping_timeout" default:"5s"`
    MaxRetries   int           `yaml:"max_retries"`
}

func New(ctx context.Context, cfg Config) (*goredis.Client, error)
func Ping(ctx context.Context, client *goredis.Client, timeout time.Duration) error
func Close(client *goredis.Client) error
func (c Config) Validate() error

type Registry
func Configure(map[string]Config) (*Registry, error)
func NewRegistry(map[string]Config) (*Registry, error)
func (*Registry) Install()
func NewIO(context.Context, string) (*goredis.Client, error)
func CloseAllIO() error
func (*Registry) NewIO(context.Context, string) (*goredis.Client, error)
func (*Registry) Close() error
var ErrNotConfigured, ErrUnknownInstance
```

行为：

- `Addr` 必填，空值返回 `ErrAddrRequired`；
- `Network` 只接受 `tcp`、`unix`；`DB`、连接数和时长不能为负；
  `MinIdleConns <= PoolSize`；
- `MaxRetries == 0` 使用 go-redis 默认值 3，`-1` 关闭重试；
- `New` 在 `PingTimeout` 内 `PING`，失败时关闭客户端并返回错误；
- 错误信息包含 network、地址、DB 和超时，不包含密码；
- 不调用 `redis.SetLogger`，不修改全局日志配置；
- 与其他 support 包一样无全局状态、无 `init()`、不 panic。

Redis 不进入 `Runtime`：需要时由业务自己创建并关闭，再把客户端传给
`support/ratelimit/redisstore` 等适配器。

### 3.7.2 命名实例注册表（db / pg / sqlite / redis / amqp / kafka / rocketmq）

七个数据包提供同一套可选 API，用于按配置别名直接取用实例：

```go
type Registry
func Configure(map[string]Config [, *zap.Logger]) (*Registry, error) // db、pg、sqlite 额外接收 logger
func NewRegistry(map[string]Config [, *zap.Logger]) (*Registry, error)
func NewIO(context.Context, string) (T, error)
func CloseAllIO() error
func (*Registry) NewIO(context.Context, string) (T, error)
func (*Registry) Install()
func (*Registry) Close() error
var ErrNotConfigured, ErrUnknownInstance
```

- `Configure` 校验每个别名和 `Config`：空别名或非法配置立即失败，空 map 表示
  禁用全部实例（之后 `NewIO` 返回 `ErrUnknownInstance`）。
- `NewIO` 首次调用时建连并缓存，同一别名后续返回同一实例；建连失败不缓存，
  下一次重试。并发安全，同一别名只建一个连接。
- 每次 `Configure` 生成一代注册表并成为“当前代”。旧代的 `Close` 只关闭本代
  实例，不会清空或关闭已经切换到的新代；这正是 `App.Reload` 先启动新模块、
  再停止旧模块所需的顺序。
- `Configure` 等价于 `NewRegistry` + `Install`。需要原子切换时先用
  `NewRegistry` 创建新代、用 `(*Registry).NewIO` 建连，全部成功后再 `Install`；
  失败只关闭新代，当前代保持不变。
- 未注册名称返回 `ErrUnknownInstance`；未调用 `Configure` 或当前代已关闭返回
  `ErrNotConfigured`。
- `db` 的 `db.default` 由 `WithDatabase` 托管，业务继续使用 `Runtime.DB`；
  把同一别名放进注册表会创建第二个连接池。
- `rocketmq` 注册表只缓存 producer；消费者不属于生产者客户端，业务用同一份
  `Config` 调用 `NewConsumer` 单独创建和关闭。v5 的 producer 不注册组名，
  因此没有组名去重；消费者组在 `ConsumerConfig.Group` 里显式指定。
- 不使用 `Configure` 时七个包保持无全局状态，`New`/`Wrap`/`Close` 行为不变。

### 3.7.3 `support/pg`

PostgreSQL 连接包，API 与 `support/db` 完全对齐（`Config` 字段、默认值、
`New`/`Close`、zap GORM 日志、§3.7.2 的命名实例注册表），差异只在驱动和 DSN：

- 驱动为 `gorm.io/driver/postgres`，底层 `jackc/pgx/v5` 经 database/sql 接入，
  无 cgo；
- `DSN` 接受 pgx 两种形式：URL（`postgres://user:pass@host:5432/db?sslmode=disable`）
  和 keyword/value（`host=... user=... password=... dbname=...`）；建议显式带
  `connect_timeout`（秒），避免网络不通时建连挂起；
- 错误脱敏覆盖两种形式：URL 隐藏 userinfo 密码段，keyword/value 把
  `password=xxx`（含单引号包裹）替换为 `password=***`；
- 错误前缀为 `pg:`；哨兵错误与 db 同名（`ErrDSNRequired`、`ErrNotConfigured`、
  `ErrUnknownInstance`）；
- 不接入根包 `WithDatabase`（它仍只托管 MySQL 协议主库）；业务单独使用，
  或按 `app/io` 模式自行托管生命周期。

### 3.7.4 `support/sqlite`

SQLite 连接包，API 与 `support/db`、`support/pg` 对齐（`Config` 字段、默认值、
`New`/`Close`、zap GORM 日志、§3.7.2 的命名实例注册表），差异在驱动与并发默认值：

- dialector 为 `github.com/glebarez/sqlite`（modernc.org/sqlite），纯 Go、无 cgo。
  它与 `gorm.io/driver/sqlite` 调用方式一致，但后者依赖 `mattn/go-sqlite3`，
  交叉编译和静态镜像需要 C 工具链；
- SQLite 单写多读，默认 `max_idle_conns`/`max_open_conns` 都是 `1`，避免同进程
  写锁竞争；提高并发需在 DSN 里用 `_pragma=journal_mode(WAL)` 和
  `_pragma=busy_timeout(5000)`；
- `DSN` 支持文件路径、`:memory:` 和带 `_pragma` 的 URI；`:memory:` 每个连接
  是独立库，多连接共享需 `file::memory:?cache=shared`；
- DSN 不含账号密码，错误信息直接带出库路径，不做脱敏；
- 错误前缀为 `sqlite:`；哨兵错误与 db/pg 同名（`ErrDSNRequired`、
  `ErrNotConfigured`、`ErrUnknownInstance`）；
- 嵌入式库，测试用 `t.TempDir()` 中的真实文件库，不需要外部服务。

### 3.7.5 `support/uuidint`

基于 Redis 的分布式自增 `int64`，移植自 swoolefy PHP `Uuid`：

- Lua 与 PHP 完全一致：`INCRBY` 段内序号（上限 `MaxIncr=999990`），用 Redis
  `TIME` 的秒 + 毫秒拼 19 位前缀（秒 + 毫秒 + 6 个 0），新段首次写入时比对
  上一毫秒戳，同毫秒冲突返回空由 Go 重试；最终 `id = prefix + incr`。PHP 与
  Go 可共享同一 Redis key 混合发号。
- `New(key, opts...)` 通过 Option 注入连接：`WithInstance(name)` 使用
  `support/redis` 命名实例（每次取号经 `redis.NewIO` 懒解析，热更新后自动用
  新代，默认别名 `default`）；`WithClient` 直接注入 `*goredis.Client`，二者
  互斥。另有 `WithFallbackClients`/`WithFallbackInstances`、`WithTTL`、
  `WithRetry`、`WithErrorHandler`。
- `Get(ctx)` 取单个；`GetBatch(ctx, n)` 返回闭区间 `[max-n+1, max]` 升序，
  超过 `MaxIncr` 自动钳制。同段内严格 +1，跨毫秒前缀增加、序号重置，因此
  只保证趋势递增。
- `WithPool(size, interval)` 启用每 Generator 独立的本地预取池：
  `StartPool` 同步预热一批后由 ticker 定时补货，`Get`/`GetBatch` 先消费池内
  ID；池满跳过补货，连续 3 个周期无人消费清空旧池，`StopPool` 幂等并回落到
  单次 Redis 取号。池内未使用的号会成为空洞，不影响唯一性（PHP 的 Channel
  池是静态共享的，Go 版按实例隔离）。
- 主连接最多尝试 `retryTimes` 次（默认 3、间隔 150ms）；耗尽后回调
  `WithErrorHandler`，再依次尝试备用连接各一次；全部失败返回包裹
  `ErrUnavailable` 的错误，`ctx` 取消时返回 `context.Canceled`。
- 不依赖 `logger`，也不引入根包；与 PHP 版的差异是没有 Channel 池和定时
  预热（一次 `GetBatch` 一次 Redis 往返），由业务自行决定是否缓存批号。
- demo 通过 `app/io/uuidint` 在启动时发布 `uuidint.Default`（实例别名来自
  `uuidint.redis_instance_name`），脚本模式用 `script gen-uuid` 按同一配置验证。

### 3.7.6 `support/phpoffice`

Excel（.xlsx）导出/导入，移植自 swoolefy PHP `PhpOffice\Excel`，底层由
`ext-xlswriter` 换成纯 Go 的 `github.com/xuri/excelize/v2`（无扩展、无 cgo）。

- `Export(ctx, dir, ExportSpec)`：文件名只接受 basename、自动补 `.xlsx`、
  用 `O_CREATE|O_EXCL` 排他预留且绝不覆盖；校验表头/sheet 名/列宽（`A` 或
  `B:D`，0.1~255）/冻结行数；逐行归一化 `nil`、布尔、整数/浮点、字符串、
  `time.Time`（按 `DateLayout`）和 `fmt.Stringer`；支持表头样式、多行表头、
  合计行、自定义筛选和保存前钩子；失败删除预留/半成品文件。
- 内存策略：小数据用常规 `Export`（工作簿在 `SaveAs` 前驻留内存）；大数据
  **必须**用 `ExportStream`（excelize `StreamWriter` 逐行写盘，内存与行数无关，
  固定单行表头、无钩子与自动筛选）；导入小文件用 `Read`，大数据用 `Import`
  （`excelize.Rows` 流式迭代）。依赖 excelize v2.11.1 要求 Go 1.26，升级前须
  核对 `go.mod` 的最低版本。
- `Read(ctx, path, ImportSpec)` 全量读取；`Import(ctx, path, ImportSpec)` 用
  `excelize.Rows` 流式逐行交给 `HandleRow`。两者共用同一套校验：文件、sheet、
  表头（trim、非空、去重、`ExpectedHeaders` 严格比较）、行宽（短行补空、多余
  非空列报错）、空行跳过、`NormalizeCells` 归一化。
- `CellTypes` 按 0 基列覆盖读取类型：`CellString`（默认，保留前导零）、
  `CellInt`、`CellFloat`、`CellTime`（Excel 日期序列号 → `time.Time`）。
- 行对象 `Row` 提供 `RowNumber`、`Cells`、`Values`、`Cell`/`Value` 默认值和
  `IsEmpty`。
- 哨兵错误：`ErrInvalidExport`、`ErrInvalidRow`、`ErrExportFailed`、
  `ErrInvalidImport`、`ErrInvalidImportRow`、`ErrImportFailed`，统一用
  `errors.Is` 判断；`ctx` 取消原样返回。
- 与 PHP 版的差异：excelize 在 `SaveAs` 前把工作簿保存在内存中（无 constMemory
  开关与 batchSize）；导出/导入是无状态函数，不强制单实例一次性；钩子用函数
  字段代替继承覆写。

### 3.7.7 `support/image`

图像处理，移植自 swoolefy PHP `Image`（Intervention Image），实现只用标准库
与 `golang.org/x/image`（纯 Go、无 cgo、无 GD/Imagick）：

- `Manager` 构造时校验 `Config`：`max_pixels`（0=默认 4000 万，-1=不限制）、
  `encode` 默认值、`presets`（模式/尺寸/preset encode）、`watermark` 取值、
  `allowed_roots`。不读取水印文件。
- `Open`/`OpenBytes` 先 `DecodeConfig` 预检像素再解码；`Create` 生成纯色/透明
  画布。解码结果统一转 `*image.NRGBA`。
- `Editor` 是请求级对象：`Resize`（contain 只缩不放 / cover 铺满后居中裁剪 /
  scale 允许放大 / stretch 双维拉伸，单维 0 表示不限制）、`Thumbnail`
  （只缩不放）、`Crop`、`Rotate`（90 倍数精确重排，任意角度扩展画布 + 双线性
  插值）、`Greyscale`、`ApplyPreset`。
- 水印：九宫格锚点 + 偏移 + 透明度；图片水印和文字字体路径拒绝 `..` 段，解析
  后必须位于 `allowed_roots`（未配置时 `base_path`）；文字水印用 TTF/OTF 渲染，
  支持旋转。
- 编码：`jpeg`/`png`/`gif`；格式按「全局 → preset → 本次参数」合并，未配置时
  透明来源默认 png、其余默认 jpeg；`quality`（1~100，默认 82）与 `max_bytes`
  校验；webp 仅解码，avif/其它格式返回 `ErrUnsupportedFormat`。重编码丢弃
  EXIF/GPS，渐进式 JPEG 不支持。
- 哨兵错误：`ErrInvalidImage`、`ErrTooLarge`、`ErrInvalidConfig`、
  `ErrUnsupportedFormat`，统一 `errors.Is`；像素上限同时约束解码、创建、
  缩放/裁剪目标和水印图层。
- 与 PHP 版的差异：无 driver/扩展检测；GIF 不做多帧编辑；不提供 strip/
  progressive 开关；`Manager` 不缓存已解码图像；`Base64` 不带 data URI 前缀。

### 3.7.8 `support/registry`

通用「名字 → 实例」注册表，供应用在启动装配时登记跨模块共享的实例
（实现 `innerapi` 接口的 service、client 等），再按名字取用。它解决模块如何拿到
其他模块共享依赖的问题，同时避免包级单例。

```go
reg := registry.NewRegistry()
if err := reg.Register(name, instance); err != nil { ... }
reg.MustRegister(name, instance)              // 启动期装配，失败 panic
instance, ok := reg.Lookup(name)              // 原始取用
typed, err := registry.Get[T](reg, name)      // 类型化取用（推荐）
names := reg.Names()                          // 排序副本，供启动日志
```

- 空名字（trim 后）、`nil` 实例（含 nil 指针 / map / slice）、重复名字都返回错误，
  重复注册不覆盖；
- 注册表是普通对象而非全局：每次装配（启动 / `Reload`）新建一份，随依赖显式传递；
- 所有方法并发安全；`Lookup`/`Names`/`Get` 对 nil 注册表安全；
- 名字由属主模块的 `innerapi` 包定义为常量（如 `product.innerapi`），注册表不校验格式；
- `Get[T]` 的 `T` 就是注册进去的类型；demo 的跨模块依赖注册的是实现 `innerapi`
  接口的 service（产品 service 实现 `productapi.ProductAPI`），消费方按接口
  `registry.Get[productapi.ProductAPI](reg, productapi.Name)` 取用，不接触对方实现；
  本模块 repository 不声明接口、也不登记注册表，由工厂零值构造后直接注入；
- 与 `support/middleware.Registry` 分工：后者强类型、只存中间件，由 Router 解析
  契约别名；通用注册表存任意跨模块实例，由属主模块工厂登记、消费方工厂构造期取用，两者不合并。

### 3.8 `script`

```go
package script

import (
    "context"

    "github.com/bingcool/gofy/core"
    "github.com/spf13/pflag"
)

type Runtime = core.Runtime

type Script struct {
    Name            string
    Short           string
    Flags           func(fs *pflag.FlagSet)
    Run             func(ctx context.Context, rt *Runtime, fs *pflag.FlagSet) error
    WithoutDatabase bool // true 时不连接 DB，即使 App 配置了 DB
}

func Validate(scripts []Script) error
func Find(scripts []Script, name string) (Script, bool)
```

约束：

- `Name` 非空、唯一，匹配 `^[a-z][a-z0-9-]{0,62}$`；
- 保留名：`list`、`help`、`completion`；
- `Run` 非 nil；
- 脚本通过 `Execute` 的 `script` 子命令执行，不启动 HTTP；
- 脚本成功返回 `nil`，失败返回 `error`；`Execute` 将其透传给 `main`；
- 脚本的 `Runtime.Fail` 是 no-op；
- 默认连接 DB（如果配置了）；`WithoutDatabase: true` 时跳过，`rt.DB` 为 nil。

### 3.9 `command`

```go
package command

import (
    "context"

    "github.com/bingcool/gofy/core"
    "github.com/spf13/pflag"
)

type Runtime = core.Runtime

type Command struct {
    Use             string   // 单个单词，例如 "migrate"
    Short           string
    Flags           func(fs *pflag.FlagSet)
    Run             func(ctx context.Context, rt *Runtime, fs *pflag.FlagSet) error
    WithoutDatabase bool
}

func Validate(cmds []Command, reserved ...string) error
```

约束：

- `Use` 只允许单个单词（不含空格）；`Use: "migrate up"` 属于错误用法；
- 保留名：`start`、`stop`、`restart`、`script`、`version`、`help`、`completion`、`list`；
- 名称唯一；
- 自定义命令挂在根命令下，例如 `demo-service migrate`。

### 3.10 `config`

可选加载器；业务可以完全不用它，自己用 viper/koanf 加载。

```go
package config

import "context"

type Option func(*options) error

func Load[T any](ctx context.Context, path string, opts ...Option) (*T, error)

func WithExpandEnv(enabled bool) Option
func WithAllowMissingEnv(enabled bool) Option
func WithStrict(enabled bool) Option
func WithApplyDefaults(enabled bool) Option
func WithValidate(enabled bool) Option
```

加载顺序与语义：

```text
1. 若 ApplyDefaults（默认 true）：defaults.Set(&cfg)
2. 读取 path
3. 若 ExpandEnv（默认 true）：展开 ${VAR}
4. yaml.Unmarshal（Strict=true 时禁止未知字段）
5. 若 Validate（默认 true）：
   a. 如果 *T 实现了 interface{ Validate() error }，调用它
   b. 如果结构体有 validate 标签，调用 validator.Struct
6. 返回 &cfg
```

环境变量规则：

- 只支持 YAML 中的 `${VAR}` 展开，**不做**自动的 `ORDER_HTTP_PORT` 式覆盖；
- 缺失变量默认返回错误；`WithAllowMissingEnv(true)` 时替换为空字符串；
- `$$` 转义为字面量 `$`；
- 不使用 viper，不读取全局配置，不修改进程环境。

为什么不支持自动环境变量覆盖：

- viper 的 `AutomaticEnv + Unmarshal` 对"YAML 中不存在的 key"不会生效，行为容易误解；
- 自动覆盖会把环境变量名和结构体路径隐式绑定，重构字段时容易破坏部署；
- `${VAR}` 显式展开更可预测，也更容易做 secret 注入。

### 3.11 `gofytest`

```go
package gofytest

import (
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/bingcool/gofy"
)

type App struct {
    // unexported
}

// New 强制关闭 pid 文件、信号、真实端口；logger 默认 Nop。
// 自动注册 t.Cleanup 关闭 App。
func New(t *testing.T, opts ...gofy.Option) *App

func (a *App) Do(req *http.Request) *httptest.ResponseRecorder
func (a *App) GET(path string, headers ...string) *httptest.ResponseRecorder
func (a *App) POSTJSON(path string, body any, headers ...string) *httptest.ResponseRecorder
func (a *App) PUTJSON(path string, body any, headers ...string) *httptest.ResponseRecorder
func (a *App) PATCHJSON(path string, body any, headers ...string) *httptest.ResponseRecorder
func (a *App) DELETE(path string, headers ...string) *httptest.ResponseRecorder
func (a *App) Close() error

func Status(t *testing.T, rr *httptest.ResponseRecorder, want int)
func JSON[T any](t *testing.T, rr *httptest.ResponseRecorder) T
func RawJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any
```

`JSON[T]` 解码默认包络中的 `data` 字段；`RawJSON` 返回完整包络，用于断言 `code` / `msg` / `details`。

`gofytest.New` 的内部行为：

- 调用 `gofy.New`；
- 强制 `WithPIDFile("")`、`WithAccessLog(false)`、`WithRecovery(true)`、`WithLogger(logger.Nop())`；
- 使用 `app.Handler(ctx)` 获取 `http.Handler`；
- 不调用 `Module.Start`，因此测试中的模块应把后台逻辑放在显式方法里。

### 3.12 `internal/`

| 包 | 职责 | 关键函数 |
| --- | --- | --- |
| `internal/pidfile` | pid 文件原子写、读、删、路径解析 | `Resolve(path, workDir) string`、`Write(path, pid, perm) error`、`Read(path) (int, error)`、`RemoveIfOwner(path, pid) error` |
| `internal/process` | 进程存活、SIGTERM、轮询退出 | `Alive(pid) bool`、`Terminate(pid) error`、`WaitExit(ctx, pid, interval) error` |
| `internal/version` | 构建信息 | `Read() core.VersionInfo` |
| `internal/commandline` | 构建 cobra 树；只接收回调与描述符，不 import 根包 | `Build(name string, deps Deps) *cobra.Command` |

`pidfile.Write` 必须：

- 创建父目录 `0o755`；
- 写临时文件后 `rename`，避免读到半个 pid；临时文件必须与目标同目录，保证 `rename` 原子；
- 关闭文件并检查 `Close` 错误；
- 任何失败返回 `error`，**不 panic**。

---

## 4. CLI 规格

### 4.1 命令表

| 命令 | 是否需要 Runtime | 是否需要 DB | 说明 |
| --- | --- | --- | --- |
| `app start` | 是 | 配置了才连 | 默认命令；前台运行 HTTP |
| `app stop` | 否 | 否 | 读取 pid，发送 SIGTERM，等待退出 |
| `app restart` | 是 | 配置了才连 | 先 stop（无实例时忽略），再前台 start |
| `app script <name>` | 是 | 配置了才连（可 opt-out） | 执行注册脚本 |
| `app script --list` | 否 | 否 | 列出脚本名与描述 |
| `app version` | 否 | 否 | 输出 `VersionInfo` |
| `app version --json` | 否 | 否 | JSON 格式输出 |
| `app <custom>` | 是 | 配置了才连（可 opt-out） | `WithCommands` 注册 |
| `app help` / `app --help` | 否 | 否 | 输出帮助，返回 nil |
| `app completion` | 否 | 否 | cobra 自动生成 |

`start` 不提供 `--port`、`--config` 等参数：

- 配置由业务在 `New` 之前加载；
- 如果部署需要覆盖端口，使用环境变量 + `${VAR}` 展开，或在业务 `main` 里处理。

`stop` / `restart` 参数：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--pid-file` | `WithPIDFile` 的值 | 覆盖 pid 路径 |
| `--timeout` | `WithShutdownTimeout` | 等待进程退出的时间 |
| `--force` | `false` | 超时后发送 SIGKILL（危险，显式开启） |

`restart` 不改变"无 daemon"的定位：它在当前进程内先停旧实例，再以**前台**方式
启动；没有运行中的实例（`ErrNotRunning`）、未启用 pid 文件
（`ErrPIDFileDisabled`）或平台不支持 `stop`（`ErrUnsupported`）时直接启动。

### 4.2 参数解析规则

- `ExecuteArgs(ctx, args)` 只解析传入的 `args`，不读 `os.Args`；`Execute` 等价于 `ExecuteArgs(ctx, os.Args[1:])`。
- `Execute` 在返回未知命令或参数错误前，会把错误写到 stderr（终端下为红色
  `[FAIL]`）。参数解析失败时应用 logger 仍未构建，不能只依赖 `main` 里的
  `app.Logger()`；`ExecuteArgs` 只返回错误，不产生输出。
- 脚本 flag 必须带 `--`。漏写时（如 `order-id=1222`）会作为位置参数被拒绝，
  错误信息提示正确写法（`--order-id=1222`）。
- `args` 为空时默认 `start`。
- 未知命令返回 `ErrUnknownCommand`（包装为 usage 错误），**不 panic**。
- `--help` 返回 `nil`。
- flag 解析错误返回 usage 错误，退出码 2。
- 不使用全局 cobra 变量；每次 `New` 构建独立的命令树（虽然通常只有一个 App）。

### 4.3 退出码

```go
const (
    ExitOK    = 0
    ExitError = 1
    ExitUsage = 2
)

func ExitCode(err error) int
```

映射：

| error | 退出码 |
| --- | --- |
| `nil` | 0 |
| `context.Canceled`（优雅关停） | 0 |
| `ErrUnknownCommand` / `ErrUsage` / flag 错误 | 2 |
| `ErrNotRunning` / `ErrStopTimeout` / 其他 | 1 |

业务 `main` 的标准写法：

```go
if err := app.Execute(ctx); err != nil {
    app.Logger().Error("exit", zap.Error(err))
    os.Exit(gofy.ExitCode(err))
}
```

### 4.4 哨兵错误

```go
var (
    ErrAlreadyStarted   = errors.New("gofy: application already started")
    ErrAlreadyRunning   = errors.New("gofy: another instance is running")
    ErrNotRunning       = errors.New("gofy: process is not running")
    ErrStopTimeout      = errors.New("gofy: stop timeout")
    ErrPIDFileDisabled  = errors.New("gofy: pid file is disabled")
    ErrUnsupported      = errors.New("gofy: operation not supported on this platform")
    ErrUnknownCommand   = errors.New("gofy: unknown command")
    ErrScriptNotFound   = errors.New("gofy: script not found")
    ErrUsage            = errors.New("gofy: usage error")
)
```

---

## 5. 生命周期与运行语义

### 5.1 `start` 的完整流程

```text
Execute("start")
  │
  ├─ 1. 解析参数、构建 cobra 命令
  ├─ 2. 进入 building
  ├─ 3. 构建 logger（若未注入）
  ├─ 4. 构建 Runtime（Name/Env/Version/StartedAt/ctx）
  ├─ 5. 若配置 DB：db.New -> Ping
  ├─ 6. 调用 ModuleFactory（按注册顺序）
  ├─ 7. 校验模块名 / 前缀 / 路由
  ├─ 8. gin.SetMode + 创建 gin.Engine + 框架中间件 + 业务中间件
  ├─ 9. 注册 health endpoints（可选）
  ├─ 10. 调用 Module.Register（recover panic -> error）
  ├─ 11. 检查 pid：已有存活实例 -> ErrAlreadyRunning
  ├─ 12. net.Listen(host:port)          // 同步，端口错误立即返回
  ├─ 13. 写 pid 文件（可选；失败则关闭 listener 并返回）
  ├─ 14. 启动 pid 刷新 ticker（可选）
  ├─ 15. 调用 Module.Start（顺序；失败则 Stop 已启动模块 + 关闭 listener）
  ├─ 16. go srv.Serve(listener)         // 进入 running
  ├─ 17. 等待：ctx.Done / serve error / Runtime.Fail
  ├─ 18. HTTP Shutdown(timeout)         // 停止接收新请求，等待在途请求
  ├─ 19. Module.Stop（逆序）
  ├─ 20. DB.Close（仅 WithDatabase 创建的）
  ├─ 21. logger.Sync（仅 App 创建的）
  ├─ 22. 删除 pid（仅当内容仍是自己的 pid）
  ├─ 23. 返回 nil / 原始错误
```

关键点：

- **先 `net.Listen` 再 `Serve`**：端口占用、权限错误同步返回，不会出现"启动成功但实际没监听"。
- **先 Stop HTTP，再 Stop Module，最后 Close DB**：避免模块停止后仍有请求访问已关闭的 DB。
- `Module.Start` 失败：执行逆序 `Stop`（已启动的模块）+ 关闭资源，返回错误。
- `Module.Stop` 失败：记录日志，继续停止其余模块；最终返回第一个 Stop 错误（如果能与主错误合并，用 `errors.Join`）。
- `Shutdown` 总预算 = `WithShutdownTimeout`；HTTP 与模块共享这个预算，避免无限等待。

### 5.2 `Run` 的流程

`Run` 与 `start` 完全一致，只是不解析参数、不处理 `stop/version/script`：

```text
Run(ctx) -> build -> listen -> serve -> wait ctx -> shutdown -> return
```

`Run` 不注册信号；由调用方通过 `gofy.SignalContext` 或自己的 signal 处理控制 `ctx`。

### 5.3 `stop` 的流程

```text
Execute("stop")
  │
  ├─ 1. 解析 --pid-file / --timeout / --force
  ├─ 2. 若 pid 文件未配置 -> ErrPIDFileDisabled
  ├─ 3. 读取 pid 文件；不存在 -> ErrNotRunning
  ├─ 4. 解析 pid；非法 -> ErrNotRunning（并记录 warn）
  ├─ 5. 检查进程是否存在
  │      - 不存在 -> 删除 stale pid，返回 ErrNotRunning
  │      - 存在 -> 继续
  ├─ 6. 发送 SIGTERM
  ├─ 7. 轮询进程是否退出（默认 100ms 一次）
  │      - 在 timeout 内退出 -> 0
  │      - 超时且 --force -> SIGKILL，等待 1s
  │      - 超时且无 --force -> ErrStopTimeout
  └─ 8. 不主动删除 pid（目标进程在关停时删除）
```

平台差异：

- Linux/macOS 支持 `SIGTERM` 与 `signal 0` 存活检测；
- Windows 上 `stop` 返回 `ErrUnsupported`，推荐使用服务管理器停止；
- `start` 在 Windows 上可以正常工作。

### 5.3.1 `restart` 的流程

```text
Execute("restart")
  │
  ├─ 1. 复用 stop 的 --pid-file / --timeout / --force
  ├─ 2. stop
  │      - 成功 -> 继续
  │      - ErrNotRunning / ErrPIDFileDisabled / ErrUnsupported -> 忽略
  │      - 其他错误（含 ErrStopTimeout）-> 返回，不启动新实例
  └─ 3. Run(ctx) 前台启动
```

### 5.4 pid 文件

| 项目 | 规则 |
| --- | --- |
| 默认 | 不启用（`WithPIDFile("")`） |
| 路径 | 相对路径基于 `WithWorkDir`（默认进程 cwd） |
| 写入时机 | 监听成功后 |
| 写入方式 | 临时文件 + `rename`，权限 `WithPIDFilePerm` |
| 重复启动 | 已存在且进程存活 -> `ErrAlreadyRunning` |
| 陈旧文件 | 进程不存在 -> 覆盖 |
| 刷新 | `WithPIDRefreshInterval > 0` 时用 `time.Ticker` 重写；默认关闭 |
| 删除 | 关闭时仅当文件内容 == 自己的 pid 才删除 |
| 容器 | 建议不配置 pid 文件 |

> 当前仓库 `start.go` 里用 `robfig/cron` 每 10 秒重写 pid。v1 用标准库 `time.Ticker` 重写同样的能力，并把它变成显式可选配置 `WithPIDRefreshInterval(10*time.Second)`；默认关闭，因为 library 不再内置 daemon，pid 被外部覆盖的场景大幅减少。

### 5.5 信号

- gofy **不隐式**调用 `signal.Notify`；
- `gofy.SignalContext(parent)` 是唯一的内置辅助：

```go
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
    return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}
```

- `ctx` 取消后，`Run` / `Execute(start)` 进入优雅关停并返回 `nil`（不是错误）；
- 第二次收到信号是否强制退出由业务 `main` 决定（可以在 `main` 里再监听一次）。

### 5.6 健康检查

`WithHealthEndpoints(true)` 时注册：

| 路径 | 行为 |
| --- | --- |
| `GET /healthz` | 永远 `200 {"status":"ok"}`，不查 DB |
| `GET /readyz` | 配置 DB 时执行 `PingContext`（1s 超时）；失败 `503` |

健康检查不挂在 `/api/v1` 下，也不写访问日志（默认 `SkipPaths`）。

### 5.7 优雅关停细节

| 资源 | 关闭时机 | 失败处理 |
| --- | --- | --- |
| 新连接 | `srv.Shutdown` 开始后拒绝 | 记录日志 |
| 在途请求 | `srv.Shutdown(ctx)` 等待 | 超时后 `srv.Close` 强制断开 |
| 模块后台任务 | HTTP 停止后，`Stop` 逆序 | 记录日志，继续 |
| DB | 模块停止后 | 记录日志 |
| logger | 最后 | 忽略 `Sync` 的 `EINVAL`/`ENOTTY` |
| pid | 最后 | 记录日志 |

`Shutdown` 幂等：内部使用 `sync.Once` + `done` channel；重复调用等待同一个 `done`。

---

## 6. 配置规范

### 6.1 谁拥有配置

- **业务方拥有配置文件、配置结构和加载时机**；
- gofy 只定义自身需要的结构：`HTTPConfig`、`CORSConfig`、`LogConfig`、`DatabaseConfig`、`VersionInfo`；
- gofy 不读取固定的 `config.yaml`，不依赖 `GOFY_ENV`，不修改 `time.Local`。

### 6.2 框架配置结构

```go
type HTTPConfig struct {
    Host              string        `yaml:"host" default:"0.0.0.0"`
    Port              int           `yaml:"port" default:"8080"`
    ReadHeaderTimeout time.Duration `yaml:"read_header_timeout" default:"5s"`
    ReadTimeout       time.Duration `yaml:"read_timeout" default:"15s"`
    WriteTimeout      time.Duration `yaml:"write_timeout" default:"30s"`
    IdleTimeout       time.Duration `yaml:"idle_timeout" default:"60s"`
    MaxHeaderBytes    int           `yaml:"max_header_bytes" default:"1048576"`
    Mode              string        `yaml:"mode" default:"release"` // debug|release|test
}

func (c HTTPConfig) Validate() error
```

校验：

- `Port` 在 `1..65535`；
- 超时不能为负；
- `Mode` 只能是 `debug|release|test`；
- `Host` 为空时使用 `0.0.0.0`。

> gin 的 debug/release/test 模式是进程级全局状态；同一进程创建多个 App 时，以第一次启动时的设置为准。

### 6.3 业务配置示例

```go
// demo-service/app/config/config.go
package config

import (
    "context"

    "github.com/bingcool/gofy"
    gofyconfig "github.com/bingcool/gofy/config"
    "github.com/bingcool/gofy/logger"
    "github.com/yourorg/demo-service/app/io/redis"
    "github.com/yourorg/demo-service/app/middleware"
)

type Config struct {
    Env     string              `yaml:"env" default:"dev"`
    HTTP    gofy.HTTPConfig     `yaml:"http"`
    Log     logger.Config      `yaml:"log"`
    DB      map[string]gofy.DatabaseConfig `yaml:"db"` // 按实例名分组
    Redis   redis.Config `yaml:"redis"`             // 多实例，按名字索引
    PIDFile string              `yaml:"pid_file"`
    JWT     middleware.JWTConfig `yaml:"jwt"`
}

func Load(ctx context.Context, path string) (*Config, error) {
    return gofyconfig.Load[Config](ctx, path)
}
```

```yaml
env: prod

http:
  host: 0.0.0.0
  port: 8080
  read_header_timeout: 5s
  read_timeout: 15s
  write_timeout: 30s
  idle_timeout: 60s
  mode: release

log:
  level: info
  encoding: json
  color: auto             # auto|always|never；IDE 控制台非 TTY，需要彩色时设 always
  outputs:
    - stdout
    - /var/log/demo-service/app.log
  error_outputs:
    - /var/log/demo-service/error.log
  system_error_outputs:
    - /var/log/demo-service/system_error.log
  rotation:
    enabled: true
    mode: hourly          # info.log 按小时；error/system_error 固定按大小
    date_dir: true        # /var/log/demo-service/20261106/info-h14.log
    date_layout: "20060102"
    date_dir_max_age_days: 7  # 超过 7 天删除整个日期目录
    max_size_mb: 100
    max_age_days: 30
    max_backups: 7
    compress: true
    local_time: true
  base_fields:
    service: demo-service
    env: prod

db:
  default:
    dsn: &main_dsn ${ORDER_DB_DSN}
    max_idle_conns: 10
    max_open_conns: 50
    conn_max_lifetime: 1h
    ping_timeout: 5s
  order:
    dsn: *main_dsn
  product:
    dsn: *main_dsn

# 多实例 Redis：enabled 为 false 时不建连，适合没有 Redis 的本地环境
redis:
  cache:
    enabled: true
    addr: ${CACHE_REDIS_ADDR}
    db: 0
    pool_size: 10
  session:
    enabled: true
    addr: ${SESSION_REDIS_ADDR}
    db: 1
    pool_size: 5
  ratelimit:
    enabled: true
    addr: ${RATE_LIMIT_REDIS_ADDR}
    password: ${RATE_LIMIT_REDIS_PASSWORD}
    db: 2
    pool_size: 5

# 多实例 AMQP：hosts 按顺序尝试，default 发布为 amqp.Default
amqp:
  default:
    enabled: true
    hosts:
      - host: ${AMQP_HOST}
        port: 5672
        username: ${AMQP_USERNAME}
        password: ${AMQP_PASSWORD}
        vhost: orders
    heartbeat: 10s
    connection_timeout: 5s
  order:
    enabled: false

# 多实例 Kafka：brokers 是 seed 节点，集群信息由 metadata 发现
kafka:
  default:
    enabled: true
    brokers:
      - ${KAFKA_BROKER_1}
      - ${KAFKA_BROKER_2}
    client_id: demo-service
    dial_timeout: 5s
    # 幂等生产默认关闭（与 franz-go 默认一致）；需要严格去重时显式开启
    idempotent: false
    # 0 表示使用默认值 10，-1 表示不限制重试次数
    record_retries: 10
  order:
    enabled: false

pid_file: /var/run/demo-service.pid

# app/middleware.Auth 的内部 JWT 校验参数；契约在分组/路由上显式声明 "auth"，
# 再由 App 注册表解析。secret 为空时示例放行
jwt:
  secret: ""
  algorithms: [HS256]
  issuer: demo-service
  audience: demoservice
  require_expiry: true
```

`${ORDER_DB_DSN}` 在加载时展开；缺失时启动失败（除非显式允许）。

`db` 和 `redis` 都按实例名分组，业务通过 `app/io` 下的包级句柄直接取用：

| 句柄 | 来源 |
| --- | --- |
| `db.Db` | 框架托管的 `db.default`（`Runtime.DB`） |
| `db.OrderDb` / `db.ProductDb` | `db.order` / `db.product`，由 `app/io` 创建 |
| `db.NewIO(ctx, name)` | 其它命名 `db` 实例（如 `selectDb`），首次取用时建连 |
| `redis.Cache` / `redis.Session` | `redis.cache` / `redis.session`，`enabled: false` 时为 nil |
| `redis.RateLimit` | `redis.ratelimit`，供分布式限流使用 |
| `amqp.Default` | `amqp.default` 的 RabbitMQ 连接，未启用时为 nil |
| `amqp.Get(name)` | 其它命名 `amqp` 实例，未启用时为 nil |
| `kafka.Default` | `kafka.default` 的 Kafka 客户端，未启用时为 nil |
| `kafka.Get(name)` | 其它命名 `kafka` 实例，未启用时为 nil |

`WithDatabase(cfg.DB["default"])` 负责主库的创建、热更新与关闭；`app/io`
在 `Start` 中打开额外实例并 `Install` 到包级句柄，在 `Stop` 中关闭。热更新时新模块
先 `Start`、旧模块再 `Stop`，关闭逻辑会比对句柄指针，不会误关新连接。没有 Redis
或 RabbitMQ 的环境把对应实例设为 `enabled: false` 即可跳过。

### 6.4 配置加载发生在 `New` 之前

```text
main
 ├─ 解析配置路径（环境变量 / 预扫描参数 / 固定路径）
 ├─ config.Load
 ├─ gofy.New(...)
 └─ app.Execute(ctx)
```

gofy 的 `Execute` 不解析 `--config`，因为配置必须在 `New` 之前完成。脚手架使用：

```go
func configPath() string {
    if p := os.Getenv("ORDER_CONFIG"); p != "" {
        return p
    }
    return "config.yaml"
}
```

### 6.5 安全要求

- 框架不在日志中输出 DSN、密码、Token；
- 访问日志默认对 `password`、`token`、`authorization`、`api_key`、`signature` 等
  query 参数脱敏，无法解析的 query 整体记为 `***`；
- JWT 校验失败只返回 `unauthorized`，不区分“token 过期”“签名错误”等内部原因；
- 安全响应头在 `New` 阶段校验，拒绝含换行的头值；
- 限流默认失败关闭（503），避免存储故障时失去保护；
- `db.New` 的错误信息必须脱敏 DSN；
- 配置加载错误只输出文件路径与解析位置，不输出文件内容；
- 生产配置建议使用环境变量注入 secret，而不是把密码写进 YAML。

---

## 7. HTTP 请求与响应契约

### 7.1 请求处理流水线

```text
RequestID
  -> SecurityHeaders（可选）
  -> AccessLog
  -> CORS
  -> Recovery
  -> BodyLimit
  -> 业务全局中间件
  -> RateLimit（可选，仅模块路由）
  -> JWTAuth（可选，仅模块路由）
  -> 参数绑定
  -> 默认值
  -> 校验
  -> 业务 handler
  -> Responder
```

### 7.2 绑定与校验矩阵

| 输入 | 来源 | 失败响应 |
| --- | --- | --- |
| 路径参数 | `uri:"id"` | 400 / 40002 |
| Query | `form:"page"`（GET/DELETE） | 400 / 40002 |
| JSON Body | `json:"..."`（POST/PUT/PATCH） | 400 / 40002 |
| Form Body | `form:"..."`（`application/x-www-form-urlencoded`） | 400 / 40002 |
| 默认值 | `default:"..."` | — |
| 校验 | `validate:"..."` | 400 / 40001 + details |
| 请求体超限 | `WithMaxBodyBytes` | 413 / 41300 |

实现上由 `BodyLimit` 中间件用 `http.MaxBytesReader` 包装请求体；绑定阶段通过 `errors.As(err, new(*http.MaxBytesError))` 识别并映射为 413，不把底层错误文案返回给客户端。

绑定错误不暴露底层字段名和类型，只返回 `"invalid request"`；校验错误返回可读文案。

### 7.3 错误映射表

| 条件 | HTTP | code | msg |
| --- | --- | --- | --- |
| 绑定失败 | 400 | 40002 | invalid request |
| 校验失败 | 400 | 40001 | invalid argument |
| JWT 校验失败 | 401 | 40100 | unauthorized |
| 未匹配路径 | 404 | 40400 | route not found |
| 方法不允许 | 405 | 40500 | method not allowed |
| 触发限流 | 429 | 42900 | too many requests |
| 限流存储不可用 | 503 | 50300 | rate limiter unavailable |
| `apperr.Error` | 其 `Status` | 其 `Code` | 其 `Message` |
| `context.Canceled` | 499 | 49900 | client closed request |
| `context.DeadlineExceeded` | 504 | 50400 | request timeout |
| 请求体超限 | 413 | 41300 | request entity too large |
| 其他 error | 500 | 50000 | internal server error |

`WithAlwaysOK(true)` 时，以上所有响应 HTTP 状态统一为 `200`，仅保留包络中的 `code`（兼容当前仓库行为）。新项目不建议开启。`support.NoContent` 成功时固定返回 `204`，不受该选项影响。

### 7.4 日志与隐私

- 访问日志默认不记录请求体、响应体、Header；
- 访问日志记录 query 时按 `SensitiveQuery` 脱敏，密码、token、签名等值不会落盘；
- `req_id` 必须出现在访问日志、错误日志、panic 日志中；
- 错误日志记录 `cause` 与堆栈，但不记录 DSN/密码；
- 客户端 IP 通过 `WithTrustedProxies` 控制是否信任 `X-Forwarded-For`；默认不信任，避免伪造。

### 7.5 响应示例

成功：

```json
{
  "code": 0,
  "msg": "success",
  "data": {
    "id": 1,
    "user_id": 100,
    "amount": 9900,
    "status": "paid"
  },
  "req_id": "9b2f6a3e-..."
}
```

业务错误：

```json
{
  "code": 40400,
  "msg": "order not found",
  "data": null,
  "req_id": "9b2f6a3e-..."
}
```

校验错误：

```json
{
  "code": 40001,
  "msg": "invalid argument",
  "data": null,
  "req_id": "9b2f6a3e-...",
  "details": {
    "id": "订单ID非法"
  }
}
```

---

## 8. 并发、资源所有权与优雅关停

### 8.1 并发契约

| 对象 | 契约 |
| --- | --- |
| `*gofy.App` | `New` 返回后只读；`Run`/`Execute`/`Handler` 只能调用一次；`Shutdown` 可并发、幂等 |
| `*support.Router` | 只在 `Register` 阶段使用，不保证并发安全 |
| `*core.Runtime` | 只读；`Fail` 并发安全 |
| `*gin.Context` | 只在当前请求 goroutine 内使用，禁止逃逸 |
| `Module.Register` | 串行调用 |
| `Module.Start` / `Stop` | 串行调用；模块内部自行管理 goroutine |
| `Responder` | 必须并发安全（默认实现是） |
| `validator.Validate` | 并发安全（go-playground 保证） |

### 8.2 资源所有权

| 资源 | 创建者 | 关闭者 | 说明 |
| --- | --- | --- | --- |
| logger | `WithLogger` 注入 | 业务 | App 不 `Sync` |
| logger | `WithLogConfig` / 默认 | App | App 关闭时 `Sync` |
| DB | `WithDatabase` | App | App 关闭时 `Close` |
| DB | `WithDB` 注入 | 业务 | App 不关闭 |
| listener | App | App | `Shutdown` 关闭 |
| pid 文件 | App | App | 仅删除自己的 pid |
| 模块内部资源 | 模块 | 模块 `Stop` | 框架不感知 |

### 8.3 后台任务写法

模块如果需要在后台运行 consumer/worker，必须：

1. 在 `Start` 中启动 goroutine；
2. 监听 `rt.Context().Done()` 或自己的 stop channel；
3. 在 `Stop` 中等待 goroutine 退出；
4. 不可恢复的错误调用 `rt.Fail(err)`。

```go
func (m *Module) Start(ctx context.Context) error {
    m.stop = make(chan struct{})
    go func() {
        defer close(m.done)
        for {
            select {
            case <-m.rt.Context().Done():
                return
            case <-m.stop:
                return
            case msg := <-m.queue:
                if err := m.handle(msg); err != nil {
                    m.rt.Fail(err)
                    return
                }
            }
        }
    }()
    return nil
}

func (m *Module) Stop(ctx context.Context) error {
    close(m.stop)
    select {
    case <-m.done:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

---

## 9. demo-service 完整接入示例

### 9.1 module 与目录

```go
// demo-service/go.mod
module github.com/yourorg/demo-service

go 1.26.0

require (
    github.com/bingcool/gofy v1.0.0
    gofy/gofy-interface-service v0.1.0
)

// 本地联调：
// replace github.com/bingcool/gofy => ../gofy
// replace gofy/gofy-interface-service => ../gofy-interface-service
```

```text
demo-service/
├── go.mod
├── go.sum
├── main.go                     # 启动流程：加载配置 → gofy.New → Execute
├── options.go                  # appOptions：汇总 gofy.Option
├── config.go                   # 配置路径、Nacos 配置加载、启动子命令判断
├── fatal.go                    # 启动期致命错误输出（TTY 红色）
├── reload.go                   # reloader：按新配置重建模块
├── nacos_watch.go              # nacosWatchModule：监听变更并触发热更新
├── config.yaml
├── Makefile
├── Dockerfile
├── app/
│   ├── config/
│   │   └── config.go
│   ├── io/                     # 实例句柄 + 建连/Install/Close 生命周期
│   │   ├── io_runtime.go       # 生命周期模块（NewModuleFactory）
│   │   ├── db/
│   │   │   └── db.go        # Db / OrderDb / ProductDb
│   │   └── redis/
│   │       └── redis.go        # Cache / Session / RateLimit
│   ├── middleware/
│   │   ├── auth.go         # Auth + JWTConfig（调用框架 JWTAuth）
│   │   ├── audit.go        # Audit + NameAudit / NameOrderAudit
│   │   └── registry_middleware.go  # Register：集中注册应用层别名
│   └── module/
│       ├── order/
│       │   ├── module.go       # 装配：取 product.innerapi，登记 order.innerapi，绑定路由
│       │   ├── controller/
│       │   │   ├── order_controller.go        # 契约适配 + 视图↔DTO/状态/错误码映射
│       │   │   ├── order_config_controller.go # GetConfig 契约实现
│       │   │   └── order_status_test.go       # 领域状态↔契约枚举一致性
│       │   ├── service/
│       │   │   ├── order_service.go  # 实现 innerapi.OrderAPI，返回领域视图
│       │   │   ├── order_config_service.go # OrderConfigService（同包协作）+ Config 视图
│       │   │   ├── order_view.go     # 领域状态常量 + 订单域哨兵错误 + CreateItem 别名
│       │   │   ├── deps.go           # Dependencies（本模块仓库 + 跨模块 innerapi + 同包 Config + Logger）
│       │   │   └── transaction_test.go
│       │   ├── innerapi/
│       │   │   └── innerapi.go       # OrderAPI 接口 + Order 视图 + CreateItem + 错误 + Name
│       │   ├── repository/
│       │   │   └── order_repository.go
│       │   └── model/
│       │       └── order.go
│       └── product/
│           ├── module.go       # 装配 service，自登记 product.innerapi；本轮无路由
│           ├── controller/
│           │   └── product_controller.go  # 预留内存示例，本轮不绑定路由
│           ├── service/
│           │   ├── product_service.go  # 实现 innerapi.ProductAPI：读取 + 扣库存
│           │   └── deps.go             # Dependencies{Products, Logger}
│           ├── innerapi/
│           │   └── innerapi.go       # ProductAPI 接口 + Product 视图 + 错误 + Name
│           ├── repository/
│           │   └── product_repository.go  # 具体类型 + DecreaseStock 条件更新
│           └── model/
│               └── product.go
├── script/
│   ├── register.go
│   └── fix_order.go
└── migrations/
    ├── 0001_orders.sql
    └── 0002_products.sql
```

### 9.2 `main.go`

示例入口按职责拆分：`main.go` 只保留启动流程（配置 → `gofy.New` →
`Execute`），`options.go` 汇总 `gofy.Option`，`config.go` 负责配置路径与
Nacos 配置加载，`reload.go` 负责按新配置重建模块，`nacos_watch.go` 负责监听
Nacos 变更，`fatal.go` 负责启动期致命错误输出。下面是最小形态的等价代码：

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    _ "go.uber.org/automaxprocs"
    "go.uber.org/zap"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/support/registry"
    "github.com/yourorg/demo-service/app/config"
    appio "github.com/yourorg/demo-service/app/io"
    "github.com/yourorg/demo-service/app/module/order"
    "github.com/yourorg/demo-service/app/module/product"
    appscript "github.com/yourorg/demo-service/script"
)

var (
    version   = "dev"
    commit    = "none"
    buildTime = "unknown"
)

func main() {
    ctx, stop := gofy.SignalContext(context.Background())
    defer stop()

    cfg, err := config.Load(ctx, configPath())
    if err != nil {
        log.Fatalf("load config: %v", err)
    }

    // 组合根每次装配新建一份共享注册表，传给所有模块工厂：属主模块（product）
    // 自登记 innerapi，消费方（order）构造期按接口取用。工厂顺序即依赖顺序，
    // 被依赖方在前。实际 wiring 在 reload.go 的 factories(cfg)，启动与 Reload 共用。
    reg := registry.NewRegistry()
    app, err := gofy.New(
        gofy.WithName("demo-service"),
        gofy.WithEnv(cfg.Env),
        gofy.WithLogConfig(cfg.Log),
        gofy.WithHTTP(cfg.HTTP),
        gofy.WithAPIPrefix("/api/v1"),
        gofy.WithPIDFile(cfg.PIDFile),
        gofy.WithDatabase(cfg.Database(config.DefaultDatabase)),
        // middlewareRegistrar 定义在 options.go，把 app/middleware 的别名注册进
        // App 共享注册表（见 §9.10）。
        gofy.WithMiddlewareRegistrar(middlewareRegistrar(cfg)),
        gofy.WithModuleFactories(
            appio.NewModuleFactory(cfg.DB, cfg.Redis, cfg.AMQP),
            product.NewModuleFactory(reg),
            order.NewModuleFactory(reg),
        ),
        gofy.WithScripts(appscript.Register(cfg)...),
        gofy.WithVersionInfo(gofy.VersionInfo{
            Version:   version,
            Commit:    commit,
            BuildTime: buildTime,
        }),
        gofy.WithShutdownTimeout(10*time.Second),
    )
    if err != nil {
        log.Fatalf("init application: %v", err)
    }

    if err := app.Execute(ctx); err != nil {
        app.Logger().Error("demo-service exit", zap.Error(err))
        os.Exit(gofy.ExitCode(err))
    }
}

func configPath() string {
    if p := os.Getenv("ORDER_CONFIG"); p != "" {
        return p
    }
    return "config.yaml"
}
```

> 说明：`main` 只负责"读配置、建 App、决定退出码"三件事；业务装配全部通过 `WithXxx` 完成。

### 9.3 `app/config/config.go`

```go
package config

import (
    "context"

    "github.com/bingcool/gofy"
    gofyconfig "github.com/bingcool/gofy/config"
    "github.com/bingcool/gofy/logger"
    "github.com/yourorg/demo-service/app/io/redis"
    "github.com/yourorg/demo-service/app/middleware"
)

type Config struct {
    Env     string              `yaml:"env" default:"dev"`
    HTTP    gofy.HTTPConfig     `yaml:"http"`
    Log     logger.Config      `yaml:"log"`
    DB      map[string]gofy.DatabaseConfig `yaml:"db"` // 按实例名分组
    Redis   redis.Config `yaml:"redis"`             // 多实例，按名字索引
    PIDFile string              `yaml:"pid_file"`
    JWT     middleware.JWTConfig `yaml:"jwt"`
}

func Load(ctx context.Context, path string) (*Config, error) {
    return gofyconfig.Load[Config](ctx, path)
}
```

### 9.3.1 `app/io` 实例句柄

```go
// app/io/db/db.go
var (
    Db        *gorm.DB // db.default，框架托管
    OrderDb   *gorm.DB // db.order
    ProductDb *gorm.DB // db.product
)

// app/io/redis/redis.go
var (
    Cache     *goredis.Client // redis.cache
    Session   *goredis.Client // redis.session
    RateLimit *goredis.Client // redis.ratelimit
)

// app/io/amqp/amqp.go
var Default *amqp.Client            // amqp.default
func Get(name string) *amqp.Client  // amqp.<name>

// app/io/kafka/kafka.go
var Default *kafka.Client            // kafka.default
func Get(name string) *kafka.Client  // kafka.<name>

// app/io/rocketmq/rocketmq.go
var Default *rocketmq.Client            // rocketmq.default
func Get(name string) *rocketmq.Client  // rocketmq.<name>
```

业务代码直接取用，不需要依赖注入容器：

```go
o, err := db.OrderDb.WithContext(ctx).First(&order)
s, err := redis.Cache.Get(ctx, "order:1").Result()
q, err := amqp.NewQueue(amqp.Default, queueConfig)
```

生命周期由 `app/io` 统一管理：`Start` 打开 `db.order`、`db.product` 和启用的
Redis、AMQP、Kafka、RocketMQ 实例并 `Install` 到句柄，`Stop` 关闭并清空。`Open`
内部把每个启用实例注册到各包的 `Configure`，再用 `NewIO` 取用；`Set.Close` 关闭
本代注册表。`db` 的其它实例名（如 `selectDb`，可指向任何兼容 MySQL 协议的服务器）
只注册不预建连，业务首次 `db.NewIO` 时才打开，也不会自动安装 GORM tracing 插件。
热更新时新模块先安装、旧模块后关闭，旧实例不会误清或误关新句柄。
业务也可以直接调用 `redis.NewIO(ctx, "cache")` 等按别名取用；`app/io` 包同时
提供同名薄包装，避免业务再 import 一次 support 包。RocketMQ 的 `app/io` 只发布
producer 句柄；需要消费时业务用配置里的 `Config` 调用 `rocketmq.NewConsumer`。

### 9.4 `app/module/order/module.go`

```go
package order

import (
    "errors"
    "fmt"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/support"
    "github.com/bingcool/gofy/support/registry"
    "github.com/yourorg/demo-service/app/module/order/controller"
    orderapi "github.com/yourorg/demo-service/app/module/order/innerapi"
    "github.com/yourorg/demo-service/app/module/order/repository"
    "github.com/yourorg/demo-service/app/module/order/service"
    productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
    "go.uber.org/zap"
    gofyadapter "gofy/gofy-interface-service/adapter/gofy"
    "gofy/gofy-interface-service/appinterface/demoservice/orderroute"
    "gofy/gofy-interface-service/contracts/common/endpoint"
)

type Module struct {
    svc    *service.OrderService
    logger *zap.Logger
}

// NewModuleFactory 从组合根传入的共享注册表取用产品 innerapi，构造订单 service
// 并登记订单自己的 innerapi。注册表每次装配由 reload.factories 新建，产品工厂
// 先于本工厂执行；缺登记或顺序错时构造期 Get 失败、启动即报错。
func NewModuleFactory(reg *registry.Registry) gofy.ModuleFactory {
    return func(rt *gofy.Runtime) (support.Module, error) {
        if rt.DB == nil {
            return nil, errors.New("order module requires database")
        }
        products, err := registry.Get[productapi.ProductAPI](reg, productapi.Name)
        if err != nil {
            return nil, fmt.Errorf("order module: %w", err)
        }
        svc := service.NewOrderService(service.Dependencies{
            Orders:   &repository.OrderRepository{},
            Products: products,
            Config:   service.NewOrderConfigService(rt.Logger),
            Logger:   rt.Logger,
        })
        // 登记订单自己的 innerapi，保持与产品模块对称（本轮无跨模块消费方）。
        if err := reg.Register(orderapi.Name, svc); err != nil {
            return nil, fmt.Errorf("order module: %w", err)
        }
        return NewModule(svc, rt.Logger), nil
    }
}

func NewModule(svc *service.OrderService, logger *zap.Logger) *Module {
    if logger == nil {
        logger = zap.NewNop()
    }
    return &Module{svc: svc, logger: logger}
}

func (m *Module) Name() string { return "order" }

func (m *Module) Register(r *support.Router) error {
    h := controller.NewOrderController(m.svc, m.logger)
    // 契约只声明别名（分组 "audit"/"auth"、CreateOrder 的 "order-audit"）。
    // 这些实例由 App 启动时通过 WithMiddlewareRegistrar 注册进 Router 携带的
    // 共享注册表，模块不再创建或注入注册表；未注册的名字在启动期报错。
    for _, group := range []*endpoint.Group{orderroute.Orders, orderroute.Config} {
        if err := gofyadapter.BindGroup(r, group, h, gofyadapter.BindOptions{}); err != nil {
            return err
        }
    }
    return nil
}
```

仓库不持有连接和日志：每次查询从 `app/io` 安装的 `db.Db` 解析连接（ctx 事务优先），
模块工厂执行时 `app/io` 尚未 `Start`、句柄还是 nil 也没关系，等 `Start` 安装连接后
仓库自动使用新句柄，热更新也会切到新连接。脚本模式没有模块生命周期，
`script/fix_order.go` 调用前把 `rt.DB` 临时发布到 `appdb.Db`，结束后恢复。

依赖声明不在 `module.go`，而在 `service/deps.go`：`Dependencies` 结构显式列出本模块
仓库（`Orders *repository.OrderRepository`）、跨模块产品契约（`Products productapi.ProductAPI`）、
同包协作 service（`Config *OrderConfigService`）和 `Logger`，构造函数
`NewOrderService(deps Dependencies)` 接收它。本模块仓库在工厂里
零值构造（`&repository.OrderRepository{}`）后直接注入，没有名字、不登记注册表，也没有
包级默认 `RepoDeps`（已删除）。跨模块依赖只持有产品 `innerapi` 接口：实例由产品工厂在
装配期自登记进共享注册表，订单工厂构造期用 `registry.Get[productapi.ProductAPI](reg,
productapi.Name)` 取出后注入。共享注册表由组合根（`reload.factories`）每次装配新建并
传给所有工厂，service 不得把它缓存到包级变量或跨请求保存。仓库日志由 service/module 用
`rt.Logger` 记录，仓库本身只包装并返回错误。repository 只导出具体类型
（`*OrderRepository` / `*ProductRepository`），不声明接口：只有一种 GORM 实现，接口除了
让测试塞 fake 之外没有替换价值，测试改用纯 Go SQLite 跑真实 SQL。`innerapi` 则是模块间
唯一合法的耦合点——纯契约包（接口 + 只读视图 + 入参 + 哨兵错误 + `Name`），只允许标准库
（必要时 `support` 纯类型），名字常量归各自模块的 `innerapi` 包。完整取舍见
[module-layering-design.md](./module-layering-design.md) §3–§6、§10，以及
[repository-composition-design.md](./repository-composition-design.md) §5.2、§5.7–§5.8。

`BindGroup` 先解析分组声明的中间件名并创建 `router.Group(prefix, mw...)`，内部 `BindAll` 再校验并注册组内每条路由：路由路径、`{id}` 占位符、方法签名和中间件名都在启动时检查；任何一项不符合直接返回聚合错误。契约中的 `{id}` 会被适配器转换成 gin 的 `:id`，完整路径按分组前缀裁剪后注册。名字解析先查 `BindOptions.Middleware`（本次绑定的显式覆盖），再查可选的 `BindOptions.Registry`，最后查 Router 携带的 App 注册表，三处都缺失即启动失败。认证没有特殊通道：`AuthRequired` 只用于 OpenAPI 标记，契约必须显式声明 `"auth"`（`group.Use("auth")` 或 `spec.Middleware = []string{"auth"}`）。

最终路由：

```text
GET    /api/v1/order/orders/:id
POST   /api/v1/order/orders
GET    /api/v1/order/orders
DELETE /api/v1/order/orders/:id
GET    /api/v1/order/config
```

### 9.5 请求/响应类型（来自 `gofy-interface-service`）

示例服务不保留 `app/module/order/dto`。所有载荷类型由公共契约层提供：

| 用途 | 包 | 类型 |
| --- | --- | --- |
| 请求 | `appinterface/demoservice/orderrequest` | `GetOrderRequest`、`ListOrdersRequest`、`CreateOrderRequest`、`CancelOrderRequest` |
| 响应 | `appinterface/demoservice/orderresponse` | `GetOrderResponse`、`CreateOrderResponse` |
| 列表 | `contracts/common/paging` | `ListData[orderdto.OrderDTO]` |
| DTO | `appinterface/demoservice/orderdto` | `OrderDTO` |
| 枚举 | `appinterface/demoservice/orderenum` | `OrderStatus`、`PayType` |
| 错误 | `appinterface/demoservice` | `ErrOrderNotFound`（40401）、`ErrOrderClosed`（40402） |

`go.mod` 通过 replace 指向同级仓库：

```go
require gofy/gofy-interface-service v0.0.0

replace gofy/gofy-interface-service => ../../../gofy-interface-service
```

示例因此成为契约的第一个真实消费者：服务端、客户端和文档使用同一组类型，不再出现本地重复 DTO。

分层约定：

| 层 | 输入/输出 | 约束 |
| --- | --- | --- |
| controller | 契约 `orderrequest` / `orderresponse` / `orderdto` | 协议转换 + 视图↔DTO/状态/错误码映射；只 import 本模块 `service` 与契约，不导入 `model`/`repository`/`innerapi` |
| service | 普通值入参，返回 `innerapi` 领域视图（`orderapi.Order`） | 实现本模块 `innerapi`，只在 `toOrderView` 中接触 `model`；不 import HTTP 契约 |
| innerapi | 接口 + 只读视图 + 入参 + 哨兵错误 + `Name` | 纯契约，只允许标准库（必要时 `support` 纯类型）；模块间唯一耦合点 |
| repository | 返回 `model.Order` | 导出具体类型（不声明接口）并持有 `model`，是唯一直接操作 `model` 的层 |
| model | 仅 repository 内部使用 | 不得出现在 handler、module 或 script 中 |

HTTP 载荷类型来自公共契约层，跨模块只读视图来自各模块 `innerapi`，两者不混用：
controller 把 service 返回的 `innerapi` 视图按字段映射成契约 DTO（不 import
`innerapi`，视图类型由 service 方法返回值推断）。维护脚本同样通过 repository
访问数据，不直接拼 GORM 查询。

### 9.6 `app/module/order/controller/order_controller.go`

```go
package controller

import (
    "context"
    "errors"
    "time"

    "github.com/bingcool/gofy/apperr"
    "github.com/yourorg/demo-service/app/module/order/service"
    "go.uber.org/zap"
    gofyadapter "gofy/gofy-interface-service/adapter/gofy"
    "gofy/gofy-interface-service/appinterface/demoservice"
    "gofy/gofy-interface-service/appinterface/demoservice/orderdto"
    "gofy/gofy-interface-service/appinterface/demoservice/orderenum"
    "gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
    "gofy/gofy-interface-service/appinterface/demoservice/orderresponse"
    "gofy/gofy-interface-service/contracts/common/paging"
    "gofy/gofy-interface-service/contracts/common/types"
)

var _ demoservice.Service = (*OrderController)(nil)

type OrderController struct {
    orderService *service.OrderService
    logger       *zap.Logger
}

// NewOrderController 注入 logger（nil 退化为 Nop）。成功请求交给访问日志，
// controller 只记录最终映射成 500 的意外错误。
func NewOrderController(svc *service.OrderService, logger *zap.Logger) *OrderController {
    if logger == nil {
        logger = zap.NewNop()
    }
    return &OrderController{
        orderService: svc,
        logger:       logger.With(zap.String("component", "order.controller")),
    }
}

func (c *OrderController) GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error) {
    o, err := c.orderService.Get(ctx, req.ID.Uint64())
    if err != nil {
        if errors.Is(err, service.ErrOrderNotFound) {
            return nil, gofyadapter.Error(demoservice.ErrOrderNotFound)
        }
        c.logger.Error("get order failed", zap.Uint64("order_id", req.ID.Uint64()), zap.Error(err))
        return nil, apperr.Internal("get order failed").WithCause(err)
    }
    return &orderresponse.GetOrderResponse{Order: toOrderDTO(o.ID, o.UserID, o.Amount, o.Status, o.CreatedAt)}, nil
}

func (c *OrderController) ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error) {
    items, total, err := c.orderService.List(ctx, req.UserID.Uint64(), req.Page, req.PageSize)
    if err != nil {
        c.logger.Error("list orders failed", zap.Uint64("user_id", req.UserID.Uint64()), zap.Error(err))
        return nil, apperr.Internal("list orders failed").WithCause(err)
    }
    list := make([]orderdto.OrderDTO, 0, len(items))
    for i := range items {
        list = append(list, *toOrderDTO(items[i].ID, items[i].UserID, items[i].Amount, items[i].Status, items[i].CreatedAt))
    }
    return &paging.ListData[orderdto.OrderDTO]{Total: total, List: list}, nil
}

func (c *OrderController) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error) {
    items := make([]service.CreateItem, 0, len(req.Items))
    for _, item := range req.Items {
        items = append(items, service.CreateItem{SkuID: item.SkuID.Uint64(), Num: item.Num})
    }
    o, err := c.orderService.Create(ctx, req.UserID.Uint64(), req.Amount, items)
    if err != nil {
        switch {
        case errors.Is(err, service.ErrProductNotFound),
            errors.Is(err, service.ErrProductUnavailable),
            errors.Is(err, service.ErrInsufficientStock):
            return nil, apperr.BadRequest(err.Error())
        default:
            c.logger.Error("create order failed",
                zap.Uint64("user_id", req.UserID.Uint64()),
                zap.Int64("amount", req.Amount),
                zap.Error(err),
            )
            return nil, apperr.Internal("create order failed").WithCause(err)
        }
    }
    return &orderresponse.CreateOrderResponse{Order: toOrderDTO(o.ID, o.UserID, o.Amount, o.Status, o.CreatedAt)}, nil
}

func (c *OrderController) CancelOrder(ctx context.Context, req *orderrequest.CancelOrderRequest) error {
    if err := c.orderService.Cancel(ctx, req.ID.Uint64()); err != nil {
        if errors.Is(err, service.ErrOrderNotFound) {
            return gofyadapter.Error(demoservice.ErrOrderNotFound)
        }
        c.logger.Error("cancel order failed", zap.Uint64("order_id", req.ID.Uint64()), zap.Error(err))
        return apperr.Internal("cancel order failed").WithCause(err)
    }
    return nil
}

// toOrderDTO 把 service 返回的领域视图字段映射成契约 DTO。controller 不 import
// innerapi：视图类型由 service 方法返回值推断，这里只按字段映射——状态字符串
// 转契约枚举、uint64 转 types.Uint64String。
func toOrderDTO(id, userID uint64, amount int64, status string, createdAt time.Time) *orderdto.OrderDTO {
    return &orderdto.OrderDTO{
        ID:        types.Uint64String(id),
        UserID:    types.Uint64String(userID),
        Amount:    amount,
        Status:    orderenum.OrderStatus(status),
        CreatedAt: createdAt,
    }
}
```

方法名必须与契约 `OperationID` 一致（`GetOrder`、`ListOrders`、`CreateOrder`、`CancelOrder`），签名在 `BindAll` 启动时校验。业务错误通过 `gofyadapter.Error(demoservice.ErrOrderNotFound)` 映射为 `40401`，与 OpenAPI 声明保持一致；`CancelOrder` 由契约声明为 `NoContent`，适配器写入 `204`。

controller 只做协议转换与视图↔DTO 映射，不接触 `model`，也不 import `innerapi`（视图类型由 service 方法返回值推断，按字段映射）；`service` 返回 `innerapi` 领域视图，`model` 仅在 `repository` 内部使用。`GetConfig` 在同包的 `order_config_controller.go` 实现，委托 `OrderService.GetConfig`（再委托同包 `OrderConfigService`）取领域视图后映射成契约响应。领域状态常量与契约枚举的一致性由 `order_status_test.go` 保证。

### 9.7 `app/module/order/service/order_service.go`（节选）

```go
package service

import (
    "context"
    "errors"
    "fmt"

    gofydb "github.com/bingcool/gofy/support/db"
    appdb "github.com/yourorg/demo-service/app/io/db"
    orderapi "github.com/yourorg/demo-service/app/module/order/innerapi"
    "github.com/yourorg/demo-service/app/module/order/model"
    "github.com/yourorg/demo-service/app/module/order/repository"
    productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
    "go.uber.org/zap"
)

// 编译期保证 OrderService 满足订单模块对外发布的契约。
var _ orderapi.OrderAPI = (*OrderService)(nil)

type OrderService struct {
    orders   *repository.OrderRepository
    products productapi.ProductAPI
    logger   *zap.Logger
}

func (s *OrderService) Get(ctx context.Context, id uint64) (*orderapi.Order, error) {
    o, err := s.orders.FindByID(ctx, id)
    if err != nil {
        if errors.Is(err, repository.ErrNotFound) {
            return nil, ErrOrderNotFound
        }
        return nil, err
    }
    s.logger.Debug("get order", zap.Uint64("order_id", id))
    return toOrderView(o), nil
}

// Create 先按 SKU 批量校验产品（存在且上架），再在同一事务里扣库存 + 落单。
// 跨模块调用传同一 ctx，产品仓库从 ctx 取到同一个事务，同提交同回滚。
func (s *OrderService) Create(ctx context.Context, userID uint64, amount int64, items []CreateItem) (*orderapi.Order, error) {
    names, err := s.validateItems(ctx, items)
    if err != nil {
        return nil, err
    }
    var created *model.Order
    err = gofydb.Transaction(ctx, appdb.Db, func(ctx context.Context) error {
        for _, it := range items {
            if err := s.products.DecreaseStock(ctx, it.SkuID, int(it.Num)); err != nil {
                return translateProductErr(it.SkuID, err)
            }
        }
        o, err := s.orders.Create(ctx, &model.Order{
            UserID: userID,
            Amount: amount,
            Status: StatusPending,
        })
        if err != nil {
            return err
        }
        created = o
        return nil
    })
    if err != nil {
        return nil, err
    }
    s.logger.Info("order created",
        zap.Uint64("order_id", created.ID),
        zap.Uint64("user_id", userID),
        zap.Int64("amount", amount),
        zap.Strings("products", names),
    )
    return toOrderView(created), nil
}

// validateItems 按 SKU 批量查产品 innerapi，确认全部存在且上架，返回产品名称
// 用于日志；错误使用订单域哨兵值。
func (s *OrderService) validateItems(ctx context.Context, items []CreateItem) ([]string, error) {
    if len(items) == 0 {
        return nil, nil
    }
    ids := make([]uint64, 0, len(items))
    seen := make(map[uint64]struct{}, len(items))
    for _, item := range items {
        if _, ok := seen[item.SkuID]; ok {
            continue
        }
        seen[item.SkuID] = struct{}{}
        ids = append(ids, item.SkuID)
    }
    views, err := s.products.ListByIDs(ctx, ids)
    if err != nil {
        return nil, err
    }
    names := make([]string, 0, len(ids))
    for _, id := range ids {
        view, ok := views[id]
        if !ok {
            return nil, fmt.Errorf("%w: sku %d", ErrProductNotFound, id)
        }
        if !view.OnSale {
            return nil, fmt.Errorf("%w: sku %d", ErrProductUnavailable, id)
        }
        names = append(names, view.Name)
    }
    return names, nil
}

func (s *OrderService) List(ctx context.Context, userID uint64, page, size int) ([]orderapi.Order, int64, error) {
    items, total, err := s.orders.ListByUser(ctx, userID, (page-1)*size, size)
    if err != nil {
        return nil, 0, err
    }
    list := make([]orderapi.Order, 0, len(items))
    for i := range items {
        list = append(list, *toOrderView(&items[i]))
    }
    return list, total, nil
}

func (s *OrderService) Cancel(ctx context.Context, id uint64) error {
    if err := s.orders.UpdateStatus(ctx, id, StatusCancelled); err != nil {
        if errors.Is(err, repository.ErrNotFound) {
            return ErrOrderNotFound
        }
        return err
    }
    s.logger.Info("order cancelled", zap.Uint64("order_id", id))
    return nil
}

// toOrderView 把持久化模型换算成跨模块只读视图，model 不出 service 边界。
func toOrderView(o *model.Order) *orderapi.Order {
    return &orderapi.Order{
        ID:        o.ID,
        UserID:    o.UserID,
        Amount:    o.Amount,
        Status:    o.Status,
        CreatedAt: o.CreatedAt,
    }
}

// translateProductErr 把产品 innerapi 哨兵错误翻译成订单域哨兵错误，保留 SKU
// 上下文。controller 只认订单域错误，产品错误语义的变化被 service 吸收。
func translateProductErr(sku uint64, err error) error {
    switch {
    case errors.Is(err, productapi.ErrNotFound):
        return fmt.Errorf("%w: sku %d", ErrProductNotFound, sku)
    case errors.Is(err, productapi.ErrUnavailable):
        return fmt.Errorf("%w: sku %d", ErrProductUnavailable, sku)
    case errors.Is(err, productapi.ErrInsufficientStock):
        return fmt.Errorf("%w: sku %d", ErrInsufficientStock, sku)
    default:
        return err
    }
}
```

构造函数 `NewOrderService(deps Dependencies)` 在 `service/deps.go`：`Dependencies`
显式列出本模块仓库 `Orders`、跨模块产品契约 `Products productapi.ProductAPI`、同包协作
service `Config *OrderConfigService` 和 `Logger`（nil 退化为 `zap.NewNop()`）。本模块仓库由工厂零值构造后注入，不登记名字、
没有包级 `RepoDeps`。跨模块依赖只持有产品 `innerapi` 接口，实例由产品工厂自登记进
共享注册表、订单工厂构造期 `registry.Get` 取出后注入；service 不 import 其他模块的
service/repository/model。`Config` 则是**模块内** service→service 组合：`OrderConfigService`
与 `OrderService` 同包，工厂先构造它再注入具体类型，`OrderService.GetConfig` 直接委托，
不走 innerapi / 注册表（配置无跨模块消费方，不发布到 innerapi）。领域状态常量（`StatusPending` 等）、订单域哨兵错误
（`ErrOrderNotFound` / `ErrProductNotFound` / `ErrProductUnavailable` /
`ErrInsufficientStock`）和 `CreateItem` 别名都在 `service/order_view.go`，不 import
HTTP 契约枚举；契约 DTO/枚举的映射留在 controller。`Create` 用
`gofydb.Transaction(ctx, appdb.Db, fn)` 把扣库存与建订单包进同一事务，产品 innerapi
错误经 `translateProductErr` 翻译成订单域错误。

### 9.8 `app/module/order/repository/order_repository.go`（节选）

```go
package repository

import (
    "context"
    "errors"
    "fmt"

    gofydb "github.com/bingcool/gofy/support/db"
    appdb "github.com/yourorg/demo-service/app/io/db"
    "github.com/yourorg/demo-service/app/module/order/model"
    "gorm.io/gorm"
)

var ErrNotFound = errors.New("order not found")

// OrderRepository 是唯一实现，直接导出具体类型；service、脚本都依赖它，
// 不再声明接口。仓库不持有连接和日志：每次查询按需解析 app/io 安装的 db.Db。
type OrderRepository struct{}

// conn 优先使用 ctx 中的事务；没有事务时用 app/io 安装的 db.Db。跨模块调用
// 因此自动共享最外层 service 开启的事务。
func (r *OrderRepository) conn(ctx context.Context) *gorm.DB {
    return gofydb.FromContext(ctx, appdb.Db)
}

func (r *OrderRepository) FindByID(ctx context.Context, id uint64) (*model.Order, error) {
    var o model.Order
    err := r.conn(ctx).WithContext(ctx).First(&o, id).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, ErrNotFound
    }
    if err != nil {
        return nil, fmt.Errorf("find order %d: %w", id, err)
    }
    return &o, nil
}

func (r *OrderRepository) Create(ctx context.Context, o *model.Order) (*model.Order, error) {
    if err := r.conn(ctx).WithContext(ctx).Create(o).Error; err != nil {
        return nil, fmt.Errorf("create order: %w", err)
    }
    return o, nil
}

func (r *OrderRepository) ListByUser(ctx context.Context, userID uint64, offset, limit int) ([]model.Order, int64, error) {
    var (
        items []model.Order
        total int64
    )
    q := func() *gorm.DB {
        return r.conn(ctx).WithContext(ctx).Model(&model.Order{}).Where("user_id = ?", userID)
    }
    if err := q().Count(&total).Error; err != nil {
        return nil, 0, fmt.Errorf("count orders: %w", err)
    }
    if err := q().Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
        return nil, 0, fmt.Errorf("list orders: %w", err)
    }
    return items, total, nil
}

func (r *OrderRepository) UpdateStatus(ctx context.Context, id uint64, status string) error {
    res := r.conn(ctx).WithContext(ctx).Model(&model.Order{}).Where("id = ?", id).Update("status", status)
    if res.Error != nil {
        return fmt.Errorf("update order %d: %w", id, res.Error)
    }
    if res.RowsAffected == 0 {
        return ErrNotFound
    }
    return nil
}

// CancelPending 把订单从 from 状态改成 to 状态并返回受影响行数。状态由调用方
// 传入（service 的领域常量），仓库不 import HTTP 契约枚举。
func (r *OrderRepository) CancelPending(ctx context.Context, id uint64, from, to string) (int64, error) {
    res := r.conn(ctx).WithContext(ctx).
        Model(&model.Order{}).
        Where("id = ? AND status = ?", id, from).
        Update("status", to)
    if res.Error != nil {
        return 0, fmt.Errorf("cancel pending order %d: %w", id, res.Error)
    }
    return res.RowsAffected, nil
}
```

### 9.9 `script/register.go` 与 `script/fix_order.go`

```go
package script

import (
    gofyscript "github.com/bingcool/gofy/script"
    "github.com/yourorg/demo-service/app/config"
)

func Register(cfg *config.Config) []gofyscript.Script {
    return []gofyscript.Script{
        FixOrder(),
    }
}
```

```go
package script

import (
    "context"

    "github.com/bingcool/gofy/apperr"
    gofyscript "github.com/bingcool/gofy/script"
    "github.com/spf13/pflag"
    appdb "github.com/yourorg/demo-service/app/io/db"
    "github.com/yourorg/demo-service/app/module/order/repository"
    orderservice "github.com/yourorg/demo-service/app/module/order/service"
    "go.uber.org/zap"
)

func FixOrder() gofyscript.Script {
    return gofyscript.Script{
        Name:  "fix-order",
        Short: "fix inconsistent order data",
        Flags: func(fs *pflag.FlagSet) {
            fs.Uint64("order-id", 0, "order id")
            fs.Bool("dry-run", false, "only print what would be changed")
        },
        Run: func(ctx context.Context, rt *gofyscript.Runtime, fs *pflag.FlagSet) error {
            id, _ := fs.GetUint64("order-id")
            dryRun, _ := fs.GetBool("dry-run")
            if id == 0 {
                return apperr.BadRequest("order-id is required")
            }
            rt.Logger.Info("fix order",
                zap.Uint64("order_id", id),
                zap.Bool("dry_run", dryRun),
            )
            if dryRun {
                rt.Logger.Info("fix order dry-run, no data changed", zap.Uint64("order_id", id))
                return nil
            }
            if rt.DB == nil {
                return apperr.Internal("database is not configured")
            }
            // 脚本模式不执行 app/io 生命周期，仓库按 app/io 句柄取连接，
            // 因此把脚本连接发布到句柄，结束后恢复原值。
            prev := appdb.Db
            appdb.Db = rt.DB
            defer func() { appdb.Db = prev }()
            rows, err := (&repository.OrderRepository{}).CancelPending(ctx, id, orderservice.StatusPending, orderservice.StatusCancelled)
            if err != nil {
                return apperr.Internal("fix order failed").WithCause(err)
            }
            rt.Logger.Info("order fixed", zap.Uint64("order_id", id), zap.Int64("rows", rows))
            return nil
        },
    }
}
```

执行：

```bash
./demo-service script fix-order --order-id=1001 --dry-run=false
./demo-service script --list
```

### 9.10 业务中间件

业务中间件不需要自己写校验：契约在需要认证的分组/路由上显式声明 `"auth"`，
实例由应用层 `app/middleware.Auth` 构造，内部调用框架
`support/middleware.JWTAuth`（§3.4.9 的 `WithJWTAuth` 同款实现）。`Auth` 把
`config.yaml` 的 `jwt` 段映射成 `jwtauth.Config` 后交给框架，并以别名 `auth`
注册到 App 的共享注册表；`AuthRequired` 本身只写入 OpenAPI 标记，不会触发
任何自动装配：

```go
// app/middleware/auth.go
package middleware

import (
    "fmt"

    "github.com/bingcool/gofy/support"
    "github.com/bingcool/gofy/support/jwtauth"
    gofymiddleware "github.com/bingcool/gofy/support/middleware"
    "github.com/gin-gonic/gin"
)

// NameAuth 是认证中间件的显式别名，契约在分组/路由上声明 "auth"。
const NameAuth = "auth"

type JWTConfig struct {
    // Secret 是 HMAC 密钥；为空时示例放行所有请求。
    Secret        string   `yaml:"secret"`
    Algorithms    []string `yaml:"algorithms"`
    Issuer        string   `yaml:"issuer"`
    Audience      string   `yaml:"audience"`
    RequireExpiry bool     `yaml:"require_expiry"`
}

func Auth(cfg JWTConfig, responder support.Responder) (support.Middleware, error) {
    if cfg.Secret == "" {
        return func(c *gin.Context) { c.Next() }, nil
    }
    parser, err := jwtauth.NewParser(jwtauth.Config{
        Secret:        []byte(cfg.Secret),
        Algorithms:    cfg.Algorithms,
        Issuer:        cfg.Issuer,
        Audience:      cfg.Audience,
        RequireExpiry: cfg.RequireExpiry,
    })
    if err != nil {
        return nil, fmt.Errorf("middleware: jwt: %w", err)
    }
    return gofymiddleware.JWTAuth(support.JWTAuthConfig{Parser: parser}, responder)
}
```

业务自己实现的中间件（如审计）和框架中间件一样，在自己的文件里导出别名常量，
装配时注册进 App 创建的 `support/middleware.Registry`，契约路由再按名字取用
（§3.4.6）。demo-service 的 `app/middleware/audit.go` 导出
`NameAudit = "audit"`（订单分组）与 `NameOrderAudit = "order-audit"`
（CreateOrder 单路由），两者是同一个 `Audit` 构造器配不同 `scope`，便于在日志里
区分名字来自哪一层。

所有应用层别名集中在 `app/middleware/registry_middleware.go`：

```go
type Deps struct {
    Logger    *zap.Logger
    JWT       JWTConfig
    Responder support.Responder
}

func Register(reg *gofymiddleware.Registry, deps Deps) error {
    auth, err := Auth(deps.JWT, deps.Responder)
    if err != nil {
        return err
    }
    for _, e := range []struct {
        alias string
        mw    support.Middleware
    }{
        {NameAuth, auth},
        {NameAudit, Audit(deps.Logger, "group")},
        {NameOrderAudit, Audit(deps.Logger, "route")},
    } {
        if err := reg.Register(e.alias, e.mw); err != nil {
            return err
        }
    }
    return nil
}
```

注册时机由框架负责：`options.go` 用 `gofy.WithMiddlewareRegistrar` 传入一个
闭包，App 在模块绑定路由之前执行它；`reload.go` 在每次热更新时把用新配置
构造的 registrar 放进 `ReloadSpec.MiddlewareRegistrars`：

```go
func middlewareRegistrar(cfg *config.Config) gofy.MiddlewareRegistrar {
    return func(reg gofy.MiddlewareRegistration) error {
        return middleware.Register(reg.Registry, middleware.Deps{
            Logger:    reg.Runtime.Logger,
            JWT:       cfg.JWT,
            Responder: reg.Responder,
        })
    }
}
```

业务模块只按契约名字 `BindGroup(..., BindOptions{})`，新增别名只改
`app/middleware`，不用改任何模块。所有名字都必须显式注册，没有默认别名或
框架兜底：`AuthRequired` 只是 OpenAPI 标记，需要认证的路由必须在契约里写
`group.Use("auth")` 或 `spec.Middleware = []string{"auth"}`。

### 9.11 CLI 行为

| 命令 | 行为 |
| --- | --- |
| `./demo-service` | 等价于 `./demo-service start` |
| `./demo-service start` | 前台启动，阻塞到信号 |
| `./demo-service stop` | 读取 pid，SIGTERM，等待退出 |
| `./demo-service restart` | 先停止已有实例，再前台启动 |
| `./demo-service stop --force` | 超时后 SIGKILL |
| `./demo-service version` | 输出版本/commit/构建时间/Go 版本 |
| `./demo-service version --json` | JSON 输出 |
| `./demo-service script --list` | 列出脚本 |
| `./demo-service script fix-order --order-id=1` | 执行脚本 |
| `./demo-service <custom>` | 自定义命令 |

---

## 10. 脚手架 `gofy new`

### 10.1 安装与用法

```bash
go install github.com/bingcool/gofy/cmd/gofy@latest

gofy new demo-service \
  --module github.com/yourorg/demo-service \
  --dir ./demo-service \
  --with-db \
  --with-scripts \
  --with-docker
```

### 10.2 参数

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--module` | 必填 | 生成 `go.mod` 的 module path |
| `--dir` | `./<name>` | 输出目录 |
| `--with-db` | `false` | 生成 `support/db` 接入代码与 `db` 配置 |
| `--with-scripts` | `true` | 生成 `script/` 目录 |
| `--with-docker` | `false` | 生成 `Dockerfile`、`.dockerignore` |
| `--with-makefile` | `true` | 生成 `Makefile` |
| `--force` | `false` | 目录非空时覆盖（默认拒绝） |

### 10.3 生成内容

```text
<name>/
├── go.mod                 # require github.com/bingcool/gofy v1.x
├── go.sum                 # 不预生成，提示用户 go mod tidy
├── main.go
├── config.yaml
├── .gitignore
├── Makefile
├── Dockerfile             # 可选
├── app/
│   ├── config/config.go
│   └── module/example/
│       ├── module.go
│       ├── dto/example.go
│       └── handler/example.go
└── script/
    ├── register.go
    └── example.go
```

生成后输出：

```text
created demo-service
next steps:
  cd demo-service
  go mod tidy
  go run . start
```

脚手架**不**自动执行 `go mod tidy`（避免网络环境差异），也**不**写入任何 secret。

### 10.4 模板维护

- 模板放在 `cmd/gofy/templates/`，用 `//go:embed` 嵌入；
- 模板本身不参与 gofy 的库编译；
- CI 中执行一次 `go run ./cmd/gofy new /tmp/demo --module example.com/demo`，并对生成目录执行 `go build ./...`，保证脚手架不腐烂。

---

## 11. 测试策略

### 11.1 框架自身

| 测试类型 | 内容 |
| --- | --- |
| 单元测试 | `Option` 校验、配置校验、`apperr` 拷贝语义、`pidfile` 原子写 |
| handler 测试 | 绑定、默认值、校验文案、错误映射、`NoContent` |
| 中间件测试 | RequestID 透传/生成、HeaderPropagation 白名单与代理信任、AccessLog 字段、Recovery 不泄漏堆栈、CORS 校验 |
| 生命周期测试 | 启动顺序、Stop 逆序、Shutdown 幂等、端口占用错误 |
| 进程测试 | pid 写入/读取/stale 处理、`stop` 超时（用子进程） |
| 依赖测试 | 公共包 import 方向、公共包无 `init()` |
| 示例测试 | `examples/demo-service` 编译 + handler 测试 |

测试约束：

- 不读取工作目录下的 `config.yaml`；
- 不监听固定端口（用 `httptest` 或 `net.Listen("tcp", "127.0.0.1:0")`）；
- 不依赖真实 MySQL；需要 DB 的测试用 `sqlmock`，业务仓库测试用纯 Go SQLite
  （`support/sqlite`）内存库跑真实 SQL，cgo-free 且不连外部数据库；
- 所有测试支持 `-race` 与 `-count=1`。

### 11.2 业务方测试

```go
package controller_test

import (
    "testing"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/gofytest"
    "gofy/gofy-interface-service/appinterface/demoservice/orderresponse"
)

func TestGetOrder(t *testing.T) {
    app := gofytest.New(t,
        gofy.WithName("demo-service-test"),
        gofy.WithModules(newFakeOrderModule(t)),
    )

    rr := app.GET("/api/v1/order/orders/1")
    gofytest.Status(t, rr, 200)

    body := gofytest.JSON[orderresponse.GetOrderResponse](t, rr)
    if body.Order == nil || body.Order.ID.Uint64() != 1 {
        t.Fatalf("want id=1, got %+v", body.Order)
    }
}
```

`gofytest` 保证：

- 不写 pid；
- 不监听端口；
- 不发信号；
- 不连接真实 DB（除非测试显式注入）；
- 测试结束自动 `Shutdown`。

### 11.3 契约测试（框架升级的回归网）

在 `examples/demo-service` 中维护一组"行为契约"测试：

- 成功响应包络字段；
- 校验失败的状态码、code、details；
- `apperr` 映射；
- panic 响应不含堆栈；
- `stop` 不连接 DB；
- `version` 不构建 Runtime。

业务升级 gofy 版本时，这组测试就是最小回归集。

---

## 12. 版本、CI 与发布

### 12.1 语义化版本

- module path：`github.com/bingcool/gofy`；
- `go.mod` 的 `go` 指令：`1.26.0`（最低支持版本）；CI 覆盖 `1.26`，业务可使用更高版本；
- v1 之前（`v0.x`）允许调整 API；
- `v1.0.0` 之后：
  - 删除/重命名导出符号 -> major；
  - 修改导出函数签名 -> major；
  - 新增导出符号 -> minor；
  - 修复、内部重构 -> patch；
- 废弃流程：先在一个 minor 版本中标记 `Deprecated:`，至少保留一个 minor 周期。

### 12.2 依赖策略

v1 直接依赖控制在以下范围：

```text
github.com/gin-gonic/gin
github.com/go-playground/validator/v10
github.com/creasty/defaults
github.com/spf13/cobra
github.com/spf13/pflag
go.uber.org/zap
gopkg.in/natefinch/lumberjack.v2
gopkg.in/yaml.v3
gorm.io/gorm
gorm.io/driver/mysql
gorm.io/driver/postgres
github.com/glebarez/sqlite
github.com/xuri/excelize/v2
golang.org/x/image
github.com/google/uuid
```

从当前仓库移除的依赖及原因：

| 依赖 | 移除原因 |
| --- | --- |
| `spf13/viper` | 全局单例、隐式 env、配置所有权应归业务；改用 `yaml.v3` |
| `gogf/gf/v2` | 仅为 `gconv.Map` 引入的超大依赖；改为类型化日志字段 |
| `sevlyar/go-daemon` | 库不应接管 fork/pid/log/workdir；交给 systemd/容器 |
| `robfig/cron/v3` | 定时任务不属于框架；pid 刷新用 `time.Ticker` |
| `gin-contrib/requestid` | 逻辑很小；内部实现可控制 Header 与注入校验 |
| `gin-contrib/cors` | 内部实现可做 credentials + wildcard 校验 |
| `go.uber.org/automaxprocs` | 有 `init()` 副作用；由业务 `main` 显式 `_` import |
| `jinzhu/copier` | 未使用/可被显式赋值替代 |

依赖升级策略：

- gofy 统一管理 gin/zap/gorm 版本，业务一般不直接依赖它们；
- 每季度检查一次依赖安全公告；
- 升级 major 依赖视为框架 minor（行为可能变化时升 major）。

### 12.3 CI 流水线

```yaml
# .github/workflows/ci.yml（示意）
jobs:
  test:
    strategy:
      matrix:
        go: ["1.26"]
        os: [ubuntu-latest, macos-latest]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go }}
      - run: go build ./...
      - run: go vet ./...
      - run: go test -race -count=1 ./...
      - name: example (separate module)
        run: |
          cd examples/demo-service
          go build ./...
          go test -race -count=1 ./...

  lint:
    steps:
      - run: golangci-lint run

  guards:
    steps:
      - name: no init in public packages
        run: |
          if grep -rn "func init()" --include="*.go" \
            *.go apperr core logger support script command config gofytest; then
            echo "public packages must not use init()"
            exit 1
          fi
      - name: import direction
        run: ./scripts/check-imports.sh

  api:
    steps:
      - name: API diff
        run: |
          go install golang.org/x/exp/cmd/apidiff@latest
          apidiff -w /tmp/api.txt github.com/bingcool/gofy@latest
          apidiff -incompatible /tmp/api.txt .
```

`check-imports.sh` 的核心检查：

```bash
#!/usr/bin/env bash
set -euo pipefail

pkgs=(apperr core logger support/... script command config)
for pkg in "${pkgs[@]}"; do
  if go list -deps "./$pkg" | grep -qx 'github.com/bingcool/gofy'; then
    echo "import cycle: $pkg must not depend on the root package"
    exit 1
  fi
done

if go list -deps . | grep -q 'github.com/bingcool/gofy/gofytest'; then
  echo "root package must not depend on gofytest"
  exit 1
fi
```

### 12.4 发布流程

1. 更新 `CHANGELOG.md`；
2. 确认 `apidiff` 无不兼容变更（或已升 major）；
3. 打 tag：`v1.2.0`；
4. `go list -m github.com/bingcool/gofy@v1.2.0` 验证可拉取；
5. 在 `examples/demo-service` 中把版本改成新 tag 并跑一次 CI；
6. 同步更新脚手架模板中的默认版本。

---

## 13. 从当前仓库迁移

### 13.1 迁移原则

- **先拆库，再删 demo**；
- 每一步都可编译、可回退；
- 不迁移任何 `init()`、全局变量、硬编码路径；
- 当前 `main` 先打 tag，例如 `v0.9.0-project`，保留项目形态回退点。

### 13.2 文件级迁移表

| 当前文件/目录 | 目标 | 处理 |
| --- | --- | --- |
| `main.go` | `examples/demo-service/main.go` | 改写为 `gofy.New` 接入；根目录删除 |
| `src/cmd/root.go` | `gofy.go` / `option.go` / `internal/commandline` | 拆分；删除 `init()`、`os.Args` 解析、全局 `rootCmd` |
| `src/cmd/start.go` | `start.go` / `internal/pidfile` | `gin.New` + 中间件 + `net.Listen` + `Shutdown`；daemon 分支删除 |
| `src/cmd/stop.go` | `stop.go` / `internal/process` | 改为返回 error；删除 `os.Exit` |
| `src/cmd/script.go` | `execute.go` + `script` | 改为 `script <name>` 子命令；删除 `--c=` |
| `src/cmd/version.go` | `internal/version` + `execute.go` | 使用 `debug.ReadBuildInfo` |
| `src/cmd/daemon-start.go` | 删除 | v1 不内置 daemon |
| `src/cmd/daemon-stop.go` | 删除 | 同上 |
| `src/cmd/command/cmd.go` | 删除 | 命令常量与运行模式不再需要 |
| `src/conf/yaml.go` | `config/load.go` | 去掉 viper、`init()`、全局配置 |
| `src/log/logger.go` | `logger/logger.go` | 去掉 `init()`、包级 logger、切日协程；修正 `Compress` 读取 `MaxAge` 的 bug |
| `src/request/requestLog.go` | `support/middleware/accesslog.go` | 改为显式 logger；不再单独初始化 |
| `src/system/system_env.go` | 删除 | `Env` 放 `Runtime`；`WorkDir` 放 Option |
| `src/timelocal/timezone.go` | 删除 | 不在库内修改 `time.Local`；时区由 DSN `loc` 或业务 `main` 决定 |
| `src/utils` | `internal/` | 仅保留仍在使用的工具；删除 `IsNumber` 等无人调用的函数 |
| `src/console` | 删除 | 未被使用 |
| `src/http/route.go` | `support/router.go` | 改写为 `Router` 薄封装 |
| `app/middleware/cors.go` | `support/middleware/cors.go` | 内部实现；校验 credentials + wildcard |
| `app/middleware/recovery.go` | `support/middleware/recovery.go` | 不再返回堆栈给客户端；不使用 `*gin.Context` 跨 goroutine |
| `app/middleware/requestid.go` | `support/middleware/requestid.go` | 内部实现；Header 校验 |
| `app/middleware/response.go` | `support/responder.go` | 包络结构保留，改为 `Responder` 接口 |
| `app/middleware/registermiddleware.go` | 删除 | 中间件顺序由 App 统一安装 |
| `app/middleware/validateLogin.go` | `app/middleware/auth.go` | `Auth` 内部调用 `support/middleware.JWTAuth` 校验标准 Bearer JWT |
| `app/middleware/test.go` | 删除 | 测试用中间件不属于框架 |
| `app/route/build/route.go` | `support/handler.go` / `bind.go` / `validate.go` | 改为泛型 handler；删除 `panic`、`validator.New()`/请求、`gconv` |
| `app/route/init.go` | `examples/demo-service/app/module/order/module.go` | 业务路由注册 |
| `app/route/demo/*` | `examples/demo-service/app/module/order/*` | 改写为 order 示例 |
| `app/module/demo/controller/order_controller.go` | `examples/demo-service/app/module/order/controller` | 改为 `context.Context` handler |
| `app/Io/db/db.go` | `support/db/db.go` | 配置驱动、可关闭、可 Ping、无 `sync.Once` |
| `app/config/db.go` | `examples/demo-service/app/config/config.go` | 业务配置 |
| `app/repository/tool.go` | `examples/demo-service/app/module/order/repository` | 改写为业务仓储 |
| `script/kernel.go` | `examples/demo-service/script/register.go` | 改为返回 `[]script.Script` |
| `script/user/test.go` | `examples/demo-service/script/fix_order.go` | 改为示例脚本 |
| `config.yaml` | `examples/demo-service/config.yaml` | 业务自己维护；删除机器相关路径与明文密码 |
| `gen.yaml` | 业务方维护 | 不进入框架库 |
| `cron.sql` / `cron.yaml` | 删除 | 定时任务系统已移除 |
| `app/server.pid`、`app/storage/logs` | 删除并加入 `.gitignore` | 运行时产物 |

### 13.3 迁移阶段

#### Phase 0：冻结与基线

- 打 tag `v0.9.0-project`；
- 记录当前构建、启动、停止、脚本行为；
- 新建 `v1` 分支。

#### Phase 1：建立 library 骨架（不改变行为）

- 新建 `core`、`apperr`、`support`、`logger`、`support/db`、`script`、`command`、`config`；
- 先实现最小可编译版本：`Runtime`、`Router`、`Responder`、`logger.New`、`db.New`；
- 保持 `examples/demo-service` 能跑通；
- 此时旧 `src/` 仍在，CI 同时编译两者。

#### Phase 2：迁移示例

- 把 `app/`、`script/`、`main.go` 复制到 `examples/demo-service`，改写为图书馆 API；
- 删除对 `src/system`、`src/conf`、`src/log` 的引用；
- 修复评审文档中的 P0 问题（绑定错误、panic 校验、堆栈泄漏、CORS、日志级别等）。

#### Phase 3：删除旧代码

- 删除根目录 `main.go`、`app/`、`script/`、`src/`；
- 删除 `config.yaml`、`gen.yaml`、`cron.sql`、`cron.yaml`；
- 根目录只保留 library、docs、examples、cmd/gofy；
- CI 切换为 library + example 双构建。

#### Phase 4：发布 v1.0.0

- 补齐 API 文档与 CHANGELOG；
- 跑 `apidiff`；
- 两个真实业务服务试用一个 minor 周期；
- 打 `v1.0.0`。

### 13.4 迁移后的根目录

```text
gofy/
├── go.mod
├── go.sum
├── gofy.go
├── option.go
├── config.go
├── errors.go
├── start.go
├── execute.go
├── stop.go
├── signal.go
├── apperr/
├── core/
├── support/
├── logger/
├── support/db/
├── support/pg/
├── support/sqlite/
├── support/uuidint/
├── support/phpoffice/
├── support/image/
├── script/
├── command/
├── config/
├── gofytest/
├── internal/
├── cmd/gofy/
├── examples/demo-service/
├── docs/
├── CHANGELOG.md
├── Makefile
└── .github/workflows/ci.yml
```

---

## 14. 验收标准

### 14.1 库本身

- [ ] `go build ./...` 通过；
- [ ] `go vet ./...` 通过；
- [ ] `go test -race -count=1 ./...` 通过；
- [ ] 公共包（`apperr`、`core`、`support`、`logger`、`script`、`command`、`config`、`gofytest`）中不存在 `func init()`；
- [ ] 公共包中不存在包级可变 `var`（常量、哨兵错误、类型别名除外）；
- [ ] 公共包中不存在 `os.Exit`、`log.Fatal`、`panic`（`Recovery` 内部的 `recover` 除外）；
- [ ] `go list -deps` 检查通过：子包不依赖根包，根包不依赖 `gofytest`；
- [ ] `WithDatabase` 未配置时，`start`、`script` 不接触 DB；
- [ ] `stop`、`version` 不构建 Runtime、不连接 DB；
- [ ] `New` 不读文件、不连 DB、不监听端口、不写 pid；
- [ ] `Shutdown` 可重复调用，返回一致结果。

### 14.2 HTTP 行为

- [ ] 绑定失败返回 400，不 panic；
- [ ] 校验失败返回 400 + `details`；
- [ ] 业务 `apperr` 正确映射状态码与业务码；
- [ ] 未知错误返回 500，响应中不含堆栈；
- [ ] panic 被 Recovery 捕获，进程不退出；
- [ ] `req_id` 在响应头与响应体中一致；
- [ ] CORS 在 `AllowCredentials + "*"` 时拒绝启动；
- [ ] 请求体超限返回 413；
- [ ] 路由重复注册返回错误，不 panic；
- [ ] 中间件顺序符合 §7.1。

### 14.3 生命周期与进程

- [ ] 端口占用时 `Run` 同步返回错误；
- [ ] `SIGTERM` 触发优雅关停，退出码 0；
- [ ] 在途请求在 `ShutdownTimeout` 内完成；
- [ ] 模块 `Stop` 逆序执行；
- [ ] 模块 `Start` 失败时已启动模块被 `Stop`；
- [ ] pid 文件原子写入，陈旧 pid 被覆盖；
- [ ] 重复 `start` 返回 `ErrAlreadyRunning`；
- [ ] `stop` 在进程不存在时返回 `ErrNotRunning`；
- [ ] `stop --force` 在超时后发送 SIGKILL；
- [ ] `WithPIDRefreshInterval(10s)` 能周期性重写 pid，且关停后 ticker 停止。

### 14.4 示例与脚手架

- [ ] `examples/demo-service` 可独立 `go build ./...`；
- [ ] 示例覆盖成功、校验失败、404、panic 四类契约测试；
- [ ] `go run ./cmd/gofy new /tmp/demo --module example.com/demo` 生成的项目可编译；
- [ ] 脚手架不写入 secret，不自动联网。

### 14.5 文档

- [ ] README 给出 5 分钟接入示例；
- [ ] `docs/library-design.md`（本文）与实现一致；
- [ ] `CHANGELOG.md` 记录首个版本；
- [ ] 每个导出符号都有 godoc 注释。

---

## 15. 风险、开放问题与决策记录

### 15.1 风险

| 风险 | 说明 | 应对 |
| --- | --- | --- |
| API 冻结成本 | v1 后每个导出符号都是承诺 | v0.x 阶段至少两个真实服务试用；apidiff 把关 |
| GORM 强制依赖 | 不用 DB 的业务也要下载 GORM | v1 接受；v2 可拆 `gofy/db` 子 module |
| gin 版本耦合 | 业务可能与自有 gin 版本冲突 | gofy 统一管理版本；`Router.Gin()` 提供逃生口 |
| 响应格式变化 | 从"永远 HTTP 200"改为真实状态码 | 提供 `WithAlwaysOK(true)` 兼容开关 |
| 配置加载差异 | 业务已有 viper/koanf | `config.Load` 完全可选；gofy 不强制 |
| 移除 daemon | 依赖 daemon 的部署方式需要改造 | 迁移文档提供 systemd/容器方案 |
| 脚手架腐烂 | 模板与 API 不同步 | CI 中生成并编译模板 |
| 抽象过度 | 为了"框架感"包装过多 gin | Router 只做薄封装；保留 `Gin()` |

### 15.2 开放问题

| # | 问题 | 当前倾向 | 决策时间 |
| --- | --- | --- | --- |
| Q1 | 是否把 `support/db` 拆成独立子 module，减少非 DB 用户的依赖？ | v1 不拆；v2 评估 | v2 规划时 |
| Q2 | 是否需要内置 OpenTelemetry 中间件？ | v1 只留扩展点；业务自行接入 | v1.1 评估 |
| Q3 | 是否需要 `gofy new` 支持从远端模板拉取？ | 不；只用内置 embed 模板 | 暂不 |
| Q4 | 是否支持 Windows 的 `stop`？ | v1 返回 `ErrUnsupported` | 有真实需求时 |
| Q5 | 是否需要配置热加载？ | 不做；由业务自行实现 | 暂不 |

### 15.3 决策记录

| 日期 | 决策 | 理由 |
| --- | --- | --- |
| 2026-11-03 | gofy 只作为 library 发布 | 多服务统一升级基础设施，避免 fork/merge 分叉 |
| 2026-11-03 | 业务拥有 `main.go` 与配置 | 库不能假设部署形态、配置来源和退出策略 |
| 2026-11-03 | `Runtime` 下沉到 `core`，根包/`script`/`command` 用别名 | 避免 `gofy -> script -> gofy` 导入环 |
| 2026-11-03 | 泛型路由使用自由函数 `support.GET(r, path, h)` | Go 不支持泛型方法；同时避免包级 validator/responder |
| 2026-11-03 | 移除 `viper` | 全局单例、隐式 env、配置所有权应归业务 |
| 2026-11-03 | 移除 `gogf/gf` | 仅为 `gconv.Map` 引入超大依赖 |
| 2026-11-03 | 移除 `go-daemon` | 库不接管进程守护；交给 systemd/容器 |
| 2026-11-03 | 移除 `robfig/cron` | 定时任务不属于框架；pid 刷新用 `time.Ticker` |
| 2026-11-03 | 框架不隐式注册信号 | 生命周期由调用方通过 `ctx` 控制；`SignalContext` 只是辅助 |
| 2026-11-03 | 错误响应使用真实 HTTP 状态码 | 更符合 HTTP 语义；`WithAlwaysOK` 兼容旧行为 |
| 2026-11-03 | 不提供依赖注入容器 | 业务依赖用闭包/构造函数注入；`Runtime` 只放框架依赖 |
| 2026-11-11 | 模块按 controller/service/innerapi/repository/model 五包分层，跨模块只走对方 `innerapi` 契约 | 模块间稳定接缝，避免 service→service 与跨模块 repository 耦合；详见 [module-layering-design.md](./module-layering-design.md) |
| 2026-11-11 | 组合根每次装配新建共享 `support/registry`，属主模块自登记 innerapi、消费方构造期取用 | 装配期解析依赖、启动即报错，避免包级单例与 service 缓存注册表 |
| 2026-11-11 | demo repository 无状态、不声明接口，连接每次查询从 `app/io` 句柄解析 | 只有一种 GORM 实现，接口无替换价值；装配早于 `app/io.Start`、Reload 换连接自动生效 |

---

## 附录 A：公共 API 清单（实现对照）

> 本附录用于实现时逐项打勾；任何新增导出符号都应先更新本文。

### A.1 `gofy`

```text
type App
func New(opts ...Option) (*App, error)
func (a *App) Name() string
func (a *App) Logger() *zap.Logger
func (a *App) Version() VersionInfo
func (a *App) Execute(ctx context.Context) error
func (a *App) ExecuteArgs(ctx context.Context, args []string) error
func (a *App) Run(ctx context.Context) error
func (a *App) Handler(ctx context.Context) (http.Handler, error)
func (a *App) Shutdown(ctx context.Context) error
func SignalContext(parent context.Context) (context.Context, context.CancelFunc)
func ExitCode(err error) int

type Option
func WithName(string) Option
func WithEnv(string) Option
func WithLogger(*zap.Logger) Option
func WithLogConfig(LogConfig) Option
func WithHTTP(HTTPConfig) Option
func WithAPIPrefix(string) Option
func WithMiddleware(...Middleware) Option
func WithPIDFile(string) Option
func WithPIDFilePerm(os.FileMode) Option
func WithPIDRefreshInterval(time.Duration) Option
func WithWorkDir(string) Option
func WithDatabase(DatabaseConfig) Option
func WithDB(*gorm.DB) Option
func WithModuleFactories(...ModuleFactory) Option
func WithModules(...support.Module) Option
func WithScripts(...script.Script) Option
func WithCommands(...command.Command) Option
func WithShutdownTimeout(time.Duration) Option
func WithResponder(Responder) Option
func WithRecovery(bool) Option
func WithAccessLog(bool) Option
func WithHeaderPropagation(bool) Option
func WithCORS(CORSConfig) Option
func WithHealthEndpoints(bool) Option
func WithMaxBodyBytes(int64) Option
func WithTrustedProxies(...string) Option
func WithAlwaysOK(bool) Option
func WithServerConfigurator(func(*http.Server) error) Option
func WithVersionInfo(VersionInfo) Option
func WithTelemetry(TelemetryConfig) Option

type HTTPConfig
func (HTTPConfig) Validate() error
type CORSConfig = support.CORSConfig
type VersionInfo = core.VersionInfo
type Runtime = core.Runtime
type ModuleFactory = support.ModuleFactory
type Middleware = support.Middleware
type Responder = support.Responder
type DatabaseConfig = gofydb.Config
type LogConfig = logger.Config
type TelemetryConfig = otel.Config
```

### A.2 `core`

```text
type VersionInfo
type Runtime
type RuntimeConfig
func NewRuntime(RuntimeConfig) *Runtime
func (*Runtime) Context() context.Context
func (*Runtime) Fail(error)
```

### A.3 `support`

```text
type Middleware = gin.HandlerFunc
type Module
type ModuleFactory
type Prefixer
type Starter
type Stopper
type CORSConfig

type Router
type RouterConfig
func NewRouter(*gin.Engine, RouterConfig) *Router
func (*Router) Group(string, ...Middleware) *Router
func (*Router) ModuleGroup(string, ...Middleware) *Router
func (*Router) GroupPrefix() string
func (*Router) Use(...Middleware)
func (*Router) GET(string, gin.HandlerFunc, ...Middleware)
func (*Router) POST(string, gin.HandlerFunc, ...Middleware)
func (*Router) PUT(string, gin.HandlerFunc, ...Middleware)
func (*Router) PATCH(string, gin.HandlerFunc, ...Middleware)
func (*Router) DELETE(string, gin.HandlerFunc, ...Middleware)
func (*Router) HEAD(string, gin.HandlerFunc, ...Middleware)
func (*Router) OPTIONS(string, gin.HandlerFunc, ...Middleware)
func (*Router) Handle(string, string, gin.HandlerFunc, ...Middleware)
func (*Router) Static(string, string)
func (*Router) Prefix() string
func (*Router) Gin() *gin.RouterGroup

type Handler[Req any, Res any]
func GET[Req any, Res any](*Router, string, Handler[Req, Res], ...Middleware)
func POST[Req any, Res any](*Router, string, Handler[Req, Res], ...Middleware)
func PUT[Req any, Res any](*Router, string, Handler[Req, Res], ...Middleware)
func PATCH[Req any, Res any](*Router, string, Handler[Req, Res], ...Middleware)
func DELETE[Req any, Res any](*Router, string, Handler[Req, Res], ...Middleware)
func Any[Req any, Res any](*Router, string, string, Handler[Req, Res], ...Middleware)
func NoContent[Req any](*Router, string, string, func(context.Context, *Req) error, ...Middleware)

type Responder
type ResponderConfig
func NewResponder(ResponderConfig) Responder
```

### A.4 `support/middleware`

```text
func RequestID(string) gin.HandlerFunc
func HeaderPropagation([]string) gin.HandlerFunc
func AccessLog(*zap.Logger, AccessLogOptions) gin.HandlerFunc
func Recovery(*zap.Logger, support.Responder) gin.HandlerFunc
func CORS(support.CORSConfig) (gin.HandlerFunc, error)
func BodyLimit(int64) gin.HandlerFunc
type AccessLogOptions
const NameRequestID / NameHeaderPropagation / NameAccessLog / NameRecovery /
      NameCORS / NameBodyLimit / NameSecurityHeaders / NameRateLimit / NameJWTAuth
type Registry
func NewRegistry() *Registry
func (*Registry) Register(string, support.Middleware) error
func (*Registry) MustRegister(string, support.Middleware)
func (*Registry) Lookup(string) (support.Middleware, bool)
func (*Registry) Names() []string
```

### A.5 `apperr`

```text
type Error
func (*Error) Error() string
func (*Error) Unwrap() error
func (*Error) WithCause(error) *Error
func (*Error) WithDetails(map[string]string) *Error
func New(int, int, string) *Error
func BadRequest(string) *Error
func InvalidArgument(string) *Error
func Unauthorized(string) *Error
func Forbidden(string) *Error
func NotFound(string) *Error
func Conflict(string) *Error
func TooManyRequests(string) *Error
func Internal(string) *Error
func Unavailable(string) *Error
func As(error) (*Error, bool)
```

### A.6 `logger`

```text
type Config
type RotationConfig
func (Config) Validate() error
func New(Config) (*zap.Logger, error)
func Nop() *zap.Logger
func Sync(*zap.Logger) error
func GormLogger(*zap.Logger, GormLoggerConfig) gormlogger.Interface
type GormLoggerConfig
```

### A.7 `support/headerpropagation`

```text
const HeaderTraceID, HeaderUserID, HeaderUserCode, HeaderTenantID,
      HeaderUserName, HeaderClientIP, HeaderUserAgent
const ContextKey
func CaptureIncoming(http.Header, traceID string, trustedProxies []string, remoteAddr string) http.Header
func IsTrustedProxy(remoteAddr string, trustedProxies []string) bool
func ContextWithHeaders(context.Context, http.Header) context.Context
func ContextWithHeader(context.Context, string, string) context.Context
func ContextWithoutHeader(context.Context, string) context.Context
func HeadersFromContext(context.Context) http.Header
func HeaderFromContext(context.Context, string) string
```

### A.8 `support/frameworkcontext`

```text
type User
func (User) Claim(string) (any, bool)
func (User) StringClaim(string) string
func WithUser(context.Context, User) context.Context
func UserFromContext(context.Context) (User, bool)
func UserFromClaims(*jwtauth.Claims) User
func UserID(context.Context) string
func TenantID(context.Context) string
func UserCode(context.Context) string
func UserName(context.Context) string
func TraceID(context.Context) string
func ClientIP(context.Context) string
func UserAgent(context.Context) string
func Header(context.Context, string) string
func Headers(context.Context) http.Header
```

### A.9 `support/jwtauth`

```text
type Config
type Claims
func (*Claims) Get(string) (any, bool)
func (*Claims) String(string) string
type Parser
func NewParser(Config) (*Parser, error)
func (*Parser) Parse(string) (*Claims, error)
var ErrMalformed, ErrUnsupportedAlgorithm, ErrSignature, ErrExpired,
    ErrNotYetValid, ErrIssuer, ErrAudience, ErrMissingExpiry
```

### A.10 `support/cache`

```text
type Cache
type Redis
func NewRedis(goredis.Cmdable) (*Redis, error)
func (*Redis) Set(context.Context, string, string, time.Duration) error
func (*Redis) SetJSON(context.Context, string, any, time.Duration) error
func (*Redis) Get(context.Context, string) (string, bool, error)
func (*Redis) GetJSON(context.Context, string, any) (bool, error)
func (*Redis) Delete(context.Context, ...string) (int64, error)
func (*Redis) Has(context.Context, string) (bool, error)
func (*Redis) TTL(context.Context, string) (time.Duration, bool, error)
func (*Redis) GetMany(context.Context, ...string) (map[string]string, error)
func (*Redis) SetMany(context.Context, map[string]string, time.Duration) error
var ErrClientRequired
```

### A.11 `support/queues`

```text
const DefaultMaxRetries = 3
var ErrClientRequired, ErrKeyRequired

type Config struct {
    Key        string
    MaxRetries int
}

type Message struct {
    ID        string
    RetryNum  int
    Timestamp int64
    Payload   json.RawMessage
}
func (*Message) Decode(any) error

type Queue
func NewQueue(goredis.Cmdable, Config) (*Queue, error)
func (*Queue) Push(context.Context, ...any) (int, error)
func (*Queue) PushPriority(context.Context, ...any) (int, error)
func (*Queue) Pop(context.Context, time.Duration) (*Message, bool, error)
func (*Queue) Retry(context.Context, *Message, time.Duration) (bool, error)
func (*Queue) Len(context.Context) (int64, error)
func (*Queue) Key() string
func (*Queue) PriorityKey() string
func (*Queue) RetryKey() string
func (*Queue) MaxRetries() int

type DelayQueue
func NewDelayQueue(goredis.Cmdable, Config) (*DelayQueue, error)
func (*DelayQueue) Add(context.Context, any, time.Duration) error
func (*DelayQueue) Pop(context.Context, int) ([]*Message, error)
func (*DelayQueue) Retry(context.Context, *Message, time.Duration) (bool, error)
func (*DelayQueue) Len(context.Context) (int64, error)
func (*DelayQueue) Key() string
func (*DelayQueue) MaxRetries() int
```

- 键布局与 swoolefy 一致：`{key}`（List）、`{key}:priority_queue`（List）、
  `{key}:retry_queue_sort`（ZSET）；延迟队列只使用 `{key}`（ZSET）。
- `Pop` 前用 Lua 把到期的重试消息原子回流到主队列（单次最多 100 条）；
  `DelayQueue.Pop` 用 Lua 一次完成取消息与删除，多消费者不会重复拿到同一条。
- 消息已删除后才交给业务，因此是 at-most-once：失败必须显式 `Retry`，
  `MaxRetries` 是额外投递次数，达到上限后 `Retry` 返回 `false`。
- `Queue.Pop` 的阻塞超时以秒为单位，非整秒向上取整；`timeout <= 0` 不阻塞。
- 不接管客户端生命周期；`Config.Key` 为空返回 `ErrKeyRequired`，负数
  `MaxRetries` 返回错误，`0` 归一为 `DefaultMaxRetries`。
- Cluster 下重试回流脚本同时操作两个键，`Key` 应包含 hash tag（如 `{orders}`）。

### A.12 `support/db`

```text
type Config
func (Config) Validate() error
func New(context.Context, Config, *zap.Logger) (*gorm.DB, error)
func Close(*gorm.DB) error

type Registry
func Configure(map[string]Config, *zap.Logger) (*Registry, error)
func NewRegistry(map[string]Config, *zap.Logger) (*Registry, error)
func NewIO(context.Context, string) (*gorm.DB, error)
func CloseAllIO() error
func (*Registry) NewIO(context.Context, string) (*gorm.DB, error)
func (*Registry) Install()
func (*Registry) Close() error
var ErrDSNRequired, ErrNotConfigured, ErrUnknownInstance
```

### A.13 `script`

```text
type Runtime = core.Runtime
type Script
func Validate([]Script) error
func Find([]Script, string) (Script, bool)
```

### A.14 `command`

```text
type Runtime = core.Runtime
type Command
func Validate([]Command, ...string) error
```

### A.15 `config`

```text
type Option
func Load[T any](context.Context, string, ...Option) (*T, error)
func LoadDocument(context.Context, string, ...Option) (*Document, error)
func WithExpandEnv(bool) Option
func WithAllowMissingEnv(bool) Option
func WithStrict(bool) Option
func WithApplyDefaults(bool) Option
func WithValidate(bool) Option

type Document
func (*Document) Get(string) (any, bool)
func (*Document) Has(string) bool
func (*Document) GetString(string) (string, bool)
func (*Document) GetInt(string) (int, bool)
func (*Document) GetInt64(string) (int64, bool)
func (*Document) GetFloat64(string) (float64, bool)
func (*Document) GetBool(string) (bool, bool)
func (*Document) GetDuration(string) (time.Duration, bool)
func (*Document) GetStringSlice(string) ([]string, bool)
```

- 路径用点号分隔：映射按键查找，序列按数字下标查找
  （`db.default.dsn`、`servers.0.host`）；键名本身不支持点号。
- `Get` 返回原始值；键存在但值为 `null` 时返回 `(nil, true)`。类型方法在
  类型不匹配、路径不存在或中途节点类型不符时返回 `false`。
- `GetString` 把字符串、数字、布尔转成文本；`GetInt`/`GetInt64` 接受 YAML
  整数和数字字符串；`GetBool` 额外接受 `strconv.ParseBool` 的文本形式；
  `GetDuration` 只接受 `1h`/`30s`/`200ms` 这类字符串，裸数字因含义不明确
  直接返回 `false`。
- `LoadDocument` 与 `Load` 共用文件读取和 `${VAR}` 展开；只有
  `WithExpandEnv`、`WithAllowMissingEnv` 对它生效，`WithStrict`、
  `WithApplyDefaults`、`WithValidate` 面向结构体解码，会被忽略。
- `Load[T]` 的行为不变：仍然应用默认值、解码和校验；`Document` 用于不需要
  完整结构体的零散读取，两者都不持有全局状态。

### A.16 `gofytest`

```text
type App
func New(*testing.T, ...gofy.Option) *App
func (*App) Do(*http.Request) *httptest.ResponseRecorder
func (*App) GET(string, ...string) *httptest.ResponseRecorder
func (*App) POSTJSON(string, any, ...string) *httptest.ResponseRecorder
func (*App) PUTJSON(string, any, ...string) *httptest.ResponseRecorder
func (*App) PATCHJSON(string, any, ...string) *httptest.ResponseRecorder
func (*App) DELETE(string, ...string) *httptest.ResponseRecorder
func (*App) Close() error
func Status(*testing.T, *httptest.ResponseRecorder, int)
func JSON[T any](*testing.T, *httptest.ResponseRecorder) T
func RawJSON(*testing.T, *httptest.ResponseRecorder) map[string]any
```

### A.17 `support/amqp`

```text
type Host
type Config
func (Config) Validate() error

type Client
func New(context.Context, Config) (*Client, error)
func Wrap(*amqp091.Connection) *Client
func (*Client) Channel() (*amqp091.Channel, error)
func (*Client) Close() error
func (*Client) IsClosed() bool
func (*Client) Config() Config

type Topology
type QueueConfig
type DelayConfig
const ExchangeDirect, ExchangeFanout, ExchangeTopic, ExchangeHeaders

type Queue
func NewQueue(*Client, QueueConfig) (*Queue, error)
type PublishOptions
type ConsumeOptions
type HandlerFunc
func (*Queue) Publish(context.Context, []byte, PublishOptions) error
func (*Queue) PublishJSON(context.Context, any, PublishOptions) error
func (*Queue) Consume(context.Context, HandlerFunc, ConsumeOptions) error
func (*Queue) Close() error

var ErrHostRequired, ErrClientClosed, ErrNotConfigured, ErrClientRequired,
    ErrHandlerRequired, ErrQueueRequired, ErrExchangeRequired,
    ErrInvalidExchangeType, ErrDirectKeyRequired, ErrRoutingKeyMismatch,
    ErrDeliveryClosed, ErrUnknownInstance

type Registry
func Configure(map[string]Config) (*Registry, error)
func NewRegistry(map[string]Config) (*Registry, error)
func (*Registry) Install()
func NewIO(context.Context, string) (*Client, error)
func CloseAllIO() error
func (*Registry) NewIO(context.Context, string) (*Client, error)
func (*Registry) Close() error
```

- `Config.Hosts` 按顺序尝试，建连和重连都会跳过不可用节点；错误只包含
  `host:port`，不会带出用户名或密码。
- `New` 建连后保留 `Config`，连接失效时 `Channel()` 自动重连；`Wrap` 接管
  已有连接但无法重连，连接失效后返回 `ErrNotConfigured`。
- `Topology` 同时描述 exchange、queue 和 binding；`direct` 要求
  `RoutingKey == BindingKey`（允许只写一边），`fanout` 忽略两者，
  `topic`/`headers` 原样使用；`Queue` 留空表示只发布不消费。
- `Queue` 用共享 channel 发布并加锁串行化；`Confirm` 开启发布确认，
  `Publish` 等待 broker ack，nack 返回错误。默认 transient，`Persistent`
  使用 delivery mode 2。
- `Consume` 每次都开独立 channel：处理函数返回 nil 自动 ack，返回错误自动
  nack（默认 requeue，`DiscardOnError` 关闭重入），panic 恢复为错误；连接或
  channel 断开后按 `ReconnectDelay` 重连并重新声明拓扑，`ctx` 取消时排空
  投递后返回 nil。
- `DelayConfig` 在主拓扑（暂存队列）上设置 `x-message-ttl`、
  `x-dead-letter-*`，发布时先声明 `Target` 再声明暂存队列；消费读取
  `Target.Queue`。TTL 最小 1ms，`PublishOptions.Expiration` 作为每消息 TTL，
  broker 取两者较小值。
- 手动 ack 是 at-least-once：handler 失败重入或连接中断可能重复投递，业务
  需幂等。`Mandatory` 只透传给 broker，本包不订阅 `basic.return`。
- 与 Redis 一致，多实例由应用装配层创建多个 `Client`；本包不保存全局句柄。

### A.18 `support/kafka`

```text
type Config
func (Config) Validate() error

type Client
func New(context.Context, Config) (*Client, error)
func Wrap(*kgo.Client) *Client
func (*Client) Close() error
func (*Client) IsClosed() bool
func (*Client) Ping(context.Context) error
func (*Client) Raw() *kgo.Client
func (*Client) Config() Config

type ProducerConfig
type Producer
func NewProducer(*Client, ProducerConfig) (*Producer, error)
type PublishOptions
func (*Producer) Publish(context.Context, []byte, PublishOptions) error
func (*Producer) PublishJSON(context.Context, any, PublishOptions) error

type ConsumerConfig
type Consumer
func NewConsumer(*Client, ConsumerConfig) (*Consumer, error)
type ConsumeOptions
type HandlerFunc
func (*Consumer) Consume(context.Context, HandlerFunc, ConsumeOptions) error
func (*Consumer) Close() error

var ErrBrokerRequired, ErrClientRequired, ErrClientClosed, ErrNotConfigured,
    ErrTopicRequired, ErrGroupRequired, ErrInvalidOffsetReset, ErrHandlerRequired,
    ErrUnknownInstance

type Registry
func Configure(map[string]Config) (*Registry, error)
func NewRegistry(map[string]Config) (*Registry, error)
func NewIO(context.Context, string) (*Client, error)
func CloseAllIO() error
func (*Registry) NewIO(context.Context, string) (*Client, error)
func (*Registry) Install()
func (*Registry) Close() error
```

- `Config.Brokers` 是 seed 节点列表，其余节点由 metadata 发现；`ClientID`
  默认 `gofy`，`DialTimeout` 默认 `5s`。`Ping` 用它作为连接超时，全部失败时
  错误只包含 broker 地址。
- 幂等生产（`Idempotent`）默认关闭，与 franz-go 默认一致；`RecordRetries`
  为 `0` 时取默认值 `10`，`-1` 表示不限制。`Wrap` 接管已有客户端但无法重连，
  也没有可用的 `Config`。
- `Producer` 用 `ProduceSync` 同步发布，返回时 broker 已接收；同一个 `Key`
  哈希到同一分区，因此同 Key 消息保序。`PublishJSON` 自动补
  `content-type: application/json`（不覆盖已有值，也不修改调用方的 header map）。
- Kafka 消费者组是客户端级配置，因此 `Producer` 共享 `New` 的客户端，
  `Consumer` 从同一 `Config` 单独建客户端；`Wrap` 注入的客户端用
  `AddConsumeTopics` 订阅，group 必须由注入方预先配置。
- `ConsumerConfig` 支持 `DisableAutoCommit`（配合 `CommitRecords`）、
  `AutoCommitInterval`（默认 `200ms`）、`AutoOffsetReset`（`earliest` 默认 /
  `latest`）、`SessionTimeout`（默认 `45s`）和
  `OnPartitionsAssigned` / `OnPartitionsRevoked` 回调。
- `Consume` 按分区顺序处理：handler 返回 nil 记为成功位点；返回错误时先提交此前
  成功位点再返回错误，消息保留为未提交以便重投（at-least-once）。
  `DiscardOnError` 把失败消息也标记为已处理，适合毒消息。handler panic 被恢复
  为错误，并按同样的规则提交或重投。`ctx` 取消时返回 nil。
- 自动提交开启时位点由客户端定时提交，崩溃可能丢失最后一批；需要严格控制时
  用 `DisableAutoCommit` + `ConsumeOptions` 手动提交。关闭阶段不会提交。
- 本包不创建 topic；开发环境可开启 broker 的 `auto.create.topics.enable`，
  生产环境应先由运维或初始化流程建好分区与副本。
- 与 Redis、AMQP 一致，多实例由应用装配层创建多个 `Client`；本包不保存
  全局句柄。

### A.19 `support/rocketmq`

```text
type Config
func (Config) Validate() error

type Client
func New(context.Context, Config) (*Client, error)
func Wrap(rmq.Producer) *Client
func (*Client) Raw() rmq.Producer
func (*Client) Config() Config
func (*Client) Close() error
func (*Client) IsClosed() bool
func (*Client) Publish(context.Context, []byte, PublishOptions) (*SendResult, error)
func (*Client) PublishJSON(context.Context, any, PublishOptions) (*SendResult, error)

type PublishOptions
type SendResult

type ConsumerConfig
type Consumer
type Message
type ConsumeOptions
type HandlerFunc
func NewConsumer(Config, ConsumerConfig) (*Consumer, error)
func (*Consumer) Consume(context.Context, HandlerFunc, ConsumeOptions) error
func (*Consumer) Config() ConsumerConfig
func (*Consumer) Close() error

var ErrEndpointRequired, ErrCredentialsIncomplete,
    ErrClientRequired, ErrClientClosed, ErrNotConfigured, ErrUnknownInstance,
    ErrTopicRequired, ErrInvalidDelay, ErrTooManyKeys, ErrInvalidKey,
    ErrTooManyProperties, ErrInvalidPropertyKey, ErrInvalidPropertyValue,
    ErrPropertiesTooLarge, ErrGroupRequired, ErrTagsConflictSQL, ErrInvalidTag,
    ErrHandlerRequired, ErrConsumerStarted

type Registry
func Configure(map[string]Config) (*Registry, error)
func NewRegistry(map[string]Config) (*Registry, error)
func NewIO(context.Context, string) (*Client, error)
func CloseAllIO() error
func (*Registry) NewIO(context.Context, string) (*Client, error)
func (*Registry) Install()
func (*Registry) Close() error
```

- 底层是官方 `apache/rocketmq-clients/golang/v5`（锁定 `v5.1.2`），走 gRPC
  proxy（默认 8081），不需要 NameServer 9876 或 sidecar。PHP 版通过
  go-sidecar 的 proxy 协议访问，Go 版直接连 proxy。
- `Config.Endpoint` 必填，多个地址可用 `;` 连接；`Namespace` 会加到 topic 和
  group 上；`AccessKey` 与 `AccessSecret` 必须同时设置或同时留空，
  `SecurityToken` 需要它们一起使用。`SendTimeout` 默认 `3s`，
  `MaxAttempts` 为 `0` 时取默认值 `3`。
- v5 的 producer 不绑定 topic，`Start` 只对已知 topic 查询路由。topic 属于
  业务代码，`Config` 默认不配置：不设置 `StartupTopics` 时启动不探测，第一次
  `Publish` 才建连并接触 broker。需要尽早发现配置错误时再设置
  `StartupTopics`，此时 endpoint 不可达或 topic 不存在会让启动失败；
  `SkipStartupCheck` 可临时关闭这个探测。错误信息只出现 endpoint，不带凭据。
  v5.1.2 没有可用的 `Ping`，快速失败只能依赖该探测。
- `Wrap` 接管已有 producer 的关闭权，但没有 `Config`，`Config()` 返回零值，
  `Publish` 仍可用。注意 v5.1.2 的 SDK 忽略 client 级连接选项，建连固定使用
  约 5 秒超时。
- `Publish` 调用 `Send` 同步发送，等待由 `SendTimeout` 与 `ctx` 中较早结束的
  一方限制；返回前检查回执非空，结果包含 `MessageID`、`Topic`、`Offset`。
  `PublishJSON` 自动补 `content-type: application/json` 属性。
- `PublishOptions` 在客户端校验 broker 限制：`Keys` 最多 32 个、每个不超过
  128 字节且不含空白，重复项去掉；`Properties` 最多 32 个，Key 只能是字母、
  数字、`_`、`.`、`-` 且不能以 `__` 开头，Value 不超过 1024 字节，合计不超过
  16 KiB；`DeliverAt` 是绝对投递时间，必须晚于当前时间，延时消息需要 DELAY
  类型 topic；`MessageGroup` 为 FIFO 消息选择队列，同 group 保序，需要 FIFO
  类型 topic。
- v5.1.2 没有 push consumer：`NewConsumer(Config, ConsumerConfig)` 只校验连接
  配置和订阅配置，创建 `SimpleConsumer`，不建连；第一次 `Consume` 时
  `Start`，再次调用返回 `ErrConsumerStarted`。消费者使用 Clustering 模式，
  `Group` 必填。
- `ConsumerConfig.Tags` 与 `SQL` 互斥（`ErrTagsConflictSQL`）。Tags 为空或
  `["*"]` 订阅全部，元素拼成 `a || b` 交给 broker，元素本身不能含 `||`；
  SQL92 过滤需要 broker 开启 `enablePropertyFilter=true`。`BatchSize` 默认
  16，`Concurrency` 默认 1，`AwaitDuration` 默认 5s，`InvisibleDuration`
  默认 30s。
- `Consume` 运行拉取循环：`Receive` 长轮询取回一批消息，每个 worker 按顺序
  逐条处理并 ack；handler 返回 nil 即 ack，返回错误则不 ack，不可见时间后
  broker 重投且 `DeliveryAttempt` 递增（at-least-once），业务按 `MessageID`
  或 `Keys` 幂等。`DiscardOnError` 上报错误后 ack 失败消息并继续；handler
  panic 被恢复为错误，按同样规则重投。长轮询超时视为空批次继续；其它接收
  错误通过 `OnError` 上报并退避 1 秒。`ctx` 取消或 `Close` 时返回 nil，可直接
  作为常驻 goroutine。
- `Close` 幂等且只发出停止信号并立即返回；底层客户端在 worker 退出后由
  `Consume` 调用 `GracefulStop`。从未启动就 `Close` 是安全的。
- 注册表只缓存 producer，按代关闭；消费者由业务用 `NewConsumer` 创建和关闭。
  与 Redis、AMQP、Kafka 一致，多实例由应用装配层负责；本包不保存全局句柄。

### A.20 `support/otel`

```text
type Config
type BatchConfig
type TLSConfig
func (Config) Validate() error
func (BatchConfig) Validate() error

const ProtocolGRPC, ProtocolHTTP
const CompressionGzip, CompressionNone
const SamplerParentBasedTraceIDRatio, SamplerParentBasedAlwaysOn,
      SamplerParentBasedAlwaysOff, SamplerTraceIDRatio,
      SamplerAlwaysOn, SamplerAlwaysOff

var ErrEndpointRequired

type Provider
type Option
func New(context.Context, Config, ...Option) (*Provider, error)
func WithLogger(*zap.Logger) Option
func WithServiceName(string) Option
func WithServiceVersion(string) Option
func WithEnvironment(string) Option
func WithSpanExporter(sdktrace.SpanExporter) Option
func (*Provider) Enabled() bool
func (*Provider) ServiceName() string
func (*Provider) TracerProvider() trace.TracerProvider
func (*Provider) Propagator() propagation.TextMapPropagator
func (*Provider) Tracer(string, ...trace.TracerOption) trace.Tracer
func (*Provider) Config() Config
func (*Provider) ForceFlush(context.Context) error
func (*Provider) Shutdown(context.Context) error

func (*Provider) GinMiddleware(func(*http.Request) bool) gin.HandlerFunc
func SkipPaths(...string) func(*http.Request) bool

func (*Provider) HTTPTransport(http.RoundTripper) http.RoundTripper
func (*Provider) HTTPClient(*http.Client) *http.Client

type GORMPlugin
func NewGORMPlugin(trace.TracerProvider) *GORMPlugin
func (*GORMPlugin) Name() string
func (*GORMPlugin) Initialize(*gorm.DB) error
func (*Provider) InstrumentGORM(*gorm.DB) error

func (*Provider) InstrumentRedis(goredis.UniversalClient) error
func InstrumentRedisWith(goredis.UniversalClient, trace.TracerProvider) error

func TraceIDs(context.Context) (traceID, spanID string)
func TraceFields(context.Context) []zap.Field
func Logger(context.Context, *zap.Logger) *zap.Logger
```

- 配置在 `New` 时校验并补默认值：`grpc` + `localhost:4317`，`http` +
  `localhost:4318`，`gzip`，`parentbased_traceidratio`，`SampleRatio` 缺省
  `1.0`，导出超时 `10s`，批处理 `5s / 2048 / 512 / 30s`，关停上限 `5s`。
  `SampleRatio: 0` 按默认值处理，关闭采样需显式选择 `always_off` 系列。
- 安全默认值：`Insecure: false` 时走 TLS（最低 1.2），支持 `CAFile`、
  双向 TLS、`ServerName`；`InsecureSkipVerify` 仅限开发。`Insecure` 与任何
  TLS 字段互斥。header 值只随导出发送，不写日志、不进 span。
- `New` 不连接 collector；导出失败由 SDK 后台重试，OTel 内部错误交给配置的
  logger，并对相同错误做 1 分钟限流。`Shutdown` 幂等，在 `ShutdownTimeout`
  内 flush，失败只记录日志，不影响进程退出。
- `Config.Global` 为 true 时安装进程级 provider 和 propagator；`Shutdown`
  不回滚全局安装。单进程只承载一个 App 时才应打开。
- `Provider.TracerProvider()` 与 `Propagator()` 永不返回 nil：禁用时分别返回
  no-op tracer 和 W3C TraceContext + Baggage 组合传播器，调用方无需分支。
- `GORMPlugin` 为 create/query/update/delete/row/raw 建立 client span，
  记录 `db.system`、`db.operation`、`db.sql.table`、截断到 4096 字符的
  `db.statement`（SQL 模板）和 `db.rows_affected`；绑定参数不记录，
  `ErrRecordNotFound` 不标记为错误。重复安装返回 GORM 的重复回调错误。
- `InstrumentRedis` 使用 `redisotel`，命令参数与值不记录
  （`WithDBStatement(false)`）。
- App 集成：`WithTelemetry` 在 `New` 时校验配置。启用后 `start`/`Run` 在
  `RequestID` 之后、`HeaderPropagation` 之前安装 Gin 中间件并跳过
  `/healthz`、`/readyz`；`WithDatabase` 托管的连接池安装 GORM 插件，
  `WithDB` 注入的连接不动；`Runtime.TracerProvider`、`Runtime.Propagator`、
  `Runtime.Tracing` 暴露给业务。关停顺序为模块 → App 自有 DB → telemetry。
- `Reload` 复用启动时构建的 provider 和导出器，新的 `Runtime` 携带相同值；
  修改 telemetry 配置需要重启进程。
- 访问日志与 panic 恢复日志输出 `trace_id`、`span_id`；启用 OTel 时
  `headerpropagation` 的 `X-Trace-Id` 使用 OTel trace id，否则回落到
  RequestID 生成的 UUID。

### A.21 `support/encryption`

```text
const CipherAES128CBC, CipherAES256CBC, CipherAES128GCM, CipherAES256GCM

func Supported([]byte, string) bool
func GenerateKey(string) ([]byte, error)

type Encrypter
func NewEncrypter([]byte, string) (*Encrypter, error)
func (*Encrypter) Cipher() string
func (*Encrypter) Encrypt(any) (string, error)
func (*Encrypter) Decrypt(string, any) error
func (*Encrypter) EncryptString(string) (string, error)
func (*Encrypter) DecryptString(string) (string, error)

var ErrUnsupportedCipher, ErrEncrypt, ErrDecrypt, ErrPayload, ErrMAC, ErrTag
```

- 对齐 swoolefy PHP `Encryption`（Laravel 风格 Encrypter）：密文为
  base64(JSON{iv, value, mac, tag})，iv/tag 是 base64，value 是 base64 密文，
  mac 是 hex HMAC-SHA256（输入为 base64(iv)+base64(value) 两个字符串的拼接，
  GCM 为空）。`EncryptString`/`DecryptString` 与 PHP 双向互通，测试含 PHP
  生成的四种 cipher 固定密文。
- 密钥是原始字节：aes-128-* 16 字节，aes-256-* 32 字节；cipher 不区分大小写，
  空字符串默认 `aes-256-gcm`（与 PHP 默认 `aes-128-cbc` 的唯一差异，AEAD 是
  更安全的生产默认值）。`NewEncrypter` 构造时复制密钥并预建 cipher，
  之后不可变、可并发使用；不提供读取密钥的方法。
- CBC 使用 PKCS7 填充（与 openssl 一致），先常量时间校验 MAC 再解密；
  GCM nonce 12 字节、tag 16 字节，由 AEAD 校验完整性。每次加密重新生成
  随机 IV。
- `Encrypt`/`Decrypt` 在字符串入口之上加 JSON 序列化；PHP `encrypt()` 用
  PHP serialize，值级入口不跨语言互通。
- 错误用 `errors.Is` 判断：`ErrPayload`（载荷结构/IV 长度）、`ErrMAC`、
  `ErrTag`、`ErrDecrypt`（密钥不符或认证失败）、`ErrEncrypt`、
  `ErrUnsupportedCipher`。错误信息不含密钥和明文。
- 用途是保护配置、Token、Cookie 等可逆数据；密码哈希不属于本包。

### A.22 `support/pg`

```text
type Config
func (Config) Validate() error
func New(context.Context, Config, *zap.Logger) (*gorm.DB, error)
func Close(*gorm.DB) error

type Registry
func Configure(map[string]Config, *zap.Logger) (*Registry, error)
func NewRegistry(map[string]Config, *zap.Logger) (*Registry, error)
func NewIO(context.Context, string) (*gorm.DB, error)
func CloseAllIO() error
func (*Registry) NewIO(context.Context, string) (*gorm.DB, error)
func (*Registry) Install()
func (*Registry) Close() error
var ErrDSNRequired, ErrNotConfigured, ErrUnknownInstance
```

- 与 `support/db` 同一套语义（§3.7.3）：驱动 `gorm.io/driver/postgres`
  （jackc/pgx/v5），DSN 支持 URL 和 keyword/value，两种形式的密码都会脱敏。

### A.23 `support/sqlite`

```text
type Config
func (Config) Validate() error
func New(context.Context, Config, *zap.Logger) (*gorm.DB, error)
func Close(*gorm.DB) error

type Registry
func Configure(map[string]Config, *zap.Logger) (*Registry, error)
func NewRegistry(map[string]Config, *zap.Logger) (*Registry, error)
func NewIO(context.Context, string) (*gorm.DB, error)
func CloseAllIO() error
func (*Registry) NewIO(context.Context, string) (*gorm.DB, error)
func (*Registry) Install()
func (*Registry) Close() error
var ErrDSNRequired, ErrNotConfigured, ErrUnknownInstance
```

- 与 `support/db`、`support/pg` 同一套语义（§3.7.4）：dialector 为
  `github.com/glebarez/sqlite`（纯 Go，无 cgo），默认单连接，WAL 与
  `busy_timeout` 通过 DSN 的 `_pragma` 参数开启。

### A.24 `support/uuidint`

```text
const MaxIncr = 999990

func New(string, ...Option) (*Generator, error)
func WithClient(*goredis.Client) Option
func WithInstance(string) Option
func WithFallbackClients(...*goredis.Client) Option
func WithFallbackInstances(...string) Option
func WithTTL(time.Duration) Option
func WithRetry(int, time.Duration) Option
func WithErrorHandler(func(error)) Option
func WithPool(int, time.Duration) Option

type Generator
func (*Generator) Get(context.Context) (int64, error)
func (*Generator) GetBatch(context.Context, int) ([]int64, error)
func (*Generator) StartPool(context.Context) error
func (*Generator) StopPool()

var ErrKeyRequired, ErrUnavailable
```

- 语义与 PHP `Uuid` 一致（§3.7.5）：与 PHP 共用 Lua 与 ID 布局，命名实例
  经 `support/redis.NewIO` 懒解析；重试 + 备用连接降级，失败返回
  `ErrUnavailable`；`WithPool` 提供可选的每实例预取池。

### A.25 `support/phpoffice`

```text
const (
    MaxColumns, MaxRows, MaxDataRows, MaxHeaderRows, MaxCellLength, MaxFileNameLength
)

type ExportSpec
type StreamExportSpec
type ExportHooks
type ExportContext
func (ExportContext) NextRowNumber() int
func (ExportContext) DefaultAutoFilterRange() string
type ExportResult
func Export(context.Context, string, ExportSpec) (*ExportResult, error)
func ExportStream(context.Context, string, StreamExportSpec) (*ExportResult, error)

type CellType
const CellString, CellInt, CellFloat, CellTime
type ImportSpec
type Row
func (Row) Cell(int, any) any
func (Row) Value(string, any) any
func (Row) IsEmpty() bool
type ReadResult
type ImportResult
func Read(context.Context, string, ImportSpec) (*ReadResult, error)
func Import(context.Context, string, ImportSpec) (*ImportResult, error)

var ErrInvalidExport, ErrInvalidRow, ErrExportFailed,
    ErrInvalidImport, ErrInvalidImportRow, ErrImportFailed
```

- 基于 `xuri/excelize/v2` 的纯 Go 实现（§3.7.6）：导出排他预留、类型归一化与
  钩子；大数据用 `ExportStream`（StreamWriter）保持内存平坦；导入支持全量
  `Read` 与流式 `Import`，按列覆盖读取类型。

### A.26 `support/image`

```text
type Mode
const ModeContain, ModeCover, ModeScale, ModeStretch
type Position
const PositionTopLeft ... PositionBottomRight
type EncodeOptions
type Preset
type ImageWatermark
type TextWatermark
type WatermarkConfig
type Config
type Manager
func NewManager(Config) (*Manager, error)
func (*Manager) Config() Config
func (*Manager) Open(string) (*Editor, error)
func (*Manager) OpenBytes([]byte) (*Editor, error)
func (*Manager) Create(int, int, string) (*Editor, error)

type ImageSize
type Editor
func (*Editor) Size() ImageSize
func (*Editor) Resize(int, int, Mode) error
func (*Editor) Thumbnail(int, int) error
func (*Editor) Crop(int, int, int, int) error
func (*Editor) Rotate(float64) error
func (*Editor) Greyscale()
func (*Editor) ApplyPreset(string) error
func (*Editor) WatermarkImage(ImageWatermark) error
func (*Editor) WatermarkText(TextWatermark) error
func (*Editor) Encode(*EncodeOptions) (*ProcessResult, error)
func (*Editor) Base64(*EncodeOptions) (string, error)

type ProcessResult
var ErrInvalidImage, ErrTooLarge, ErrInvalidConfig, ErrUnsupportedFormat
```

- 纯 Go 实现（§3.7.7）：标准库 + `golang.org/x/image`；`Manager` 校验配置，
  `Editor` 请求级操作；编码支持 jpeg/png/gif，webp 仅解码，像素与读取路径
  受 `max_pixels`、`allowed_roots` 约束。

---

## 附录 B：与旧行为的差异清单（升级必读）

| 旧行为 | 新行为 | 迁移建议 |
| --- | --- | --- |
| `import src/log` 自动读配置、建 logger | `import logger` 无副作用；`logger.New(cfg)` 显式创建 | 在 `main` 中创建并 `WithLogger` |
| `src/conf` 在 `init()` 读 `config.yaml` | 业务自己加载 | 使用 `config.Load` 或自有加载器 |
| `GOFY_ENV` 隐式环境 | `WithEnv(cfg.Env)` 显式传入 | 在配置中读取环境变量 |
| `system.GetWorkRootDir()` 依赖配置 | `WithWorkDir` 显式指定 | 迁移机器相关配置 |
| `script --c=xxx` | `script xxx` | 修改部署脚本 |
| `daemon-start` / `daemon-stop` | 移除 | 用 systemd/容器；或业务自建 daemon |
| 所有响应 HTTP 200 | 真实状态码 | 需要旧行为时 `WithAlwaysOK(true)` |
| 校验失败 `panic` | 返回 400 + `details` | 删除调用方对 panic 的依赖 |
| 错误响应包含堆栈 | 堆栈只进日志 | 客户端改为按 `code` 处理 |
| 每次请求 `validator.New()` | App 级单例 | 无需业务改动 |
| `gconv.Map(res)` 响应 | 直接 JSON 序列化 | 检查 DTO 的 `json` 标签 |
| 日志按天目录 + 切日协程 | lumberjack 轮转 | 调整日志采集路径 |
| `time.Local` 被全局修改 | 库不修改 | 在 DSN 用 `loc=Asia%2FShanghai`，或业务 `main` 显式设置 |
| pid 每 10 秒用 cron 重写 | 默认只写一次；可选 `WithPIDRefreshInterval` | 需要旧行为时显式开启 |
| 未知命令 `panic` | 返回 usage 错误，退出码 2 | 检查启动脚本 |

---

## 附录 C：最小可用接入（5 分钟）

```go
// main.go
package main

import (
    "context"
    "log"
    "os"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/support"
)

type pingModule struct{}

func (pingModule) Name() string { return "ping" }

func (pingModule) Register(r *support.Router) error {
    support.GET(r, "/ping", ping)
    return nil
}

func ping(ctx context.Context, _ *struct{}) (*map[string]string, error) {
    res := map[string]string{"pong": "ok"}
    return &res, nil
}

func main() {
    ctx, stop := gofy.SignalContext(context.Background())
    defer stop()

    app, err := gofy.New(
        gofy.WithName("ping-service"),
        gofy.WithHTTP(gofy.HTTPConfig{Port: 8080}),
        gofy.WithModules(pingModule{}),
    )
    if err != nil {
        log.Fatal(err)
    }
    if err := app.Execute(ctx); err != nil {
        os.Exit(gofy.ExitCode(err))
    }
}
```

```bash
go run . start
curl http://127.0.0.1:8080/api/v1/ping/ping
# {"code":0,"msg":"success","data":{"pong":"ok"},"req_id":"..."}
```

---

**本文是 gofy v1 的实现契约。任何与本文不一致的实现，要么修实现，要么先改本文并走评审。**
