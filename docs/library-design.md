# gofy Library 详细设计（v1）

> 状态：**已锁定**（2026-11-03）
>
> 关联文档：[架构与设计评审](./architecture-review.md)
>
> 本文的结论只有一个：**gofy 是一个 Go library / package，业务项目是独立的 Go module，通过 `go get github.com/bingcool/gofy` 依赖它。** 本文不再讨论"项目模板"与"library"的取舍，只描述 library 形态下每一个包、每一个导出符号、每一条运行时语义应该如何设计。
>
> 旧版本的第 11 节"如果只有一个服务就继续用 Project 模板"已废弃。当前仓库在迁移完成后不再是可运行服务，而是"框架库 + 可编译示例 + 脚手架"。
>
> **本版相对上一版的变化：**
> - 锁定 library-first，删除"项目模板 / library 二选一"的讨论；
> - 补全每个公共包的导出 API、默认值、校验规则与错误语义；
> - 补全 CLI、生命周期、配置、HTTP 请求/响应契约；
> - 增加完整 `order-service` 示例、`gofy new` 脚手架、CI、逐文件迁移与验收清单；
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
9. [order-service 完整接入示例](#9-order-service-完整接入示例)
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
| D2 | 业务项目形态 | 独立 module，如 `github.com/yourorg/order-service` |
| D3 | 业务入口 | 业务方拥有 `main.go`，调用 `gofy.New(...)` / `app.Execute(ctx)` |
| D4 | 业务目录 | 业务方自己创建 `app/`、`script/`，gofy 不感知其内部结构 |
| D5 | 配置所有权 | 业务方拥有配置文件和配置结构；gofy 只定义自身需要的配置 |
| D6 | 生命周期 | `New` 无副作用；`Run` / `Execute` 启动；`Shutdown` 幂等关闭 |
| D7 | 全局状态 | 公共包内禁止 `init()` 副作用、禁止包级可变单例 |
| D8 | 退出 | 库不调用 `os.Exit`；由业务 `main` 根据返回的 `error` 决定退出码 |
| D9 | 示例 | 仓库保留 `examples/order-service`，独立 module，CI 中随框架编译 |
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
- 不做认证/授权体系（只提供中间件扩展点）；
- 不做指标、链路追踪的默认实现（v1 只留扩展点）；
- 不做热加载配置；
- 不承诺 Windows 下的 `stop` 信号语义；
- 不兼容当前仓库 `src/`、`app/` 的所有旧 API。

### 1.4 迁移后的使用方式（一句话）

```go
app, _ := gofy.New(
    gofy.WithName("order-service"),
    gofy.WithHTTP(gofy.HTTPConfig{Port: 8080}),
    gofy.WithModuleFactories(order.NewModuleFactory(cfg.Order)),
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
├── run.go                       # HTTP 运行、模块构建、优雅关停
├── execute.go                   # CLI 分发：start/stop/script/version/自定义
├── stop.go                      # start/stop 的进程控制
├── signal.go                    # SignalContext 辅助函数
├── apperr/
│   └── apperr.go                # 业务错误模型
├── core/
│   └── runtime.go               # Runtime（被 gofy/script/command 共享）
├── transport/
│   ├── module.go                # Module、ModuleFactory、Prefixer、Starter、Stopper
│   ├── router.go                # Router 薄封装
│   ├── handler.go               # 泛型 Handler 与 GET/POST/...
│   ├── bind.go                  # 请求绑定
│   ├── validate.go              # 校验与 message 标签
│   ├── responder.go             # Responder 接口与默认响应包络
│   └── middleware/
│       ├── requestid.go
│       ├── accesslog.go
│       ├── recovery.go
│       ├── cors.go
│       └── bodylimit.go
├── logging/
│   ├── logging.go               # Config、New、Nop、Sync
│   └── gorm.go                  # GORM logger 适配（可选使用）
├── support/
│   └── mysql/
│       └── mysql.go             # Config、New、Close
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
│   └── order-service/           # 独立 module，通过 replace 指向 ../..
├── docs/
│   ├── architecture-review.md
│   └── library-design.md
├── CHANGELOG.md
├── Makefile
└── .github/workflows/ci.yml
```

规则：

- **只有上表中的公共包是契约**：`gofy`、`apperr`、`core`、`transport`、`logging`、`support/mysql`、`script`、`command`、`config`、`gofytest`。
- `internal/` 可自由重构，不承诺兼容。
- `cmd/gofy` 只是脚手架工具，不被业务 import；它的存在不影响库的依赖。
- `examples/order-service` 是**独立 module**，不进入 gofy 的依赖图。

### 2.2 包依赖图

```text
gofy (root)
 ├── core
 ├── transport ──> apperr, core
 ├── script ─────> core
 ├── command ────> core
 ├── logging
 ├── support/mysql
 └── config

gofytest ──> gofy, transport
internal/* ──> core
```

硬性规则：

| 规则 | 原因 |
| --- | --- |
| `transport`、`script`、`command`、`logging`、`support/mysql`、`config`、`apperr`、`core` **不得 import 根包 `gofy`** | 防止导入环，保证子包可被业务独立使用 |
| 根包 **不得 import** `gofytest`、`examples`、`cmd/gofy` | 测试/工具不能进入运行时依赖 |
| `core` 只依赖 `context`、`time`、`zap`、`gorm` | 它是共享叶子，必须最小 |
| `logging` 不依赖任何 gofy 子包 | 可被业务独立使用 |
| `support/mysql` 不依赖 `logging` | 通过参数接收 `*zap.Logger`，避免耦合 |

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
    "github.com/bingcool/gofy/logging"
    "github.com/bingcool/gofy/support/mysql"
    "github.com/bingcool/gofy/transport"
    "go.uber.org/zap"
)

type Runtime = core.Runtime
type VersionInfo = core.VersionInfo
type ModuleFactory = transport.ModuleFactory
type Middleware = transport.Middleware
type Responder = transport.Responder
type CORSConfig = transport.CORSConfig
type DatabaseConfig = mysql.Config
type LogConfig = logging.Config

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
| `WithPIDFile(string)` | `""`（关闭） | pid 文件路径；相对路径基于 `WithWorkDir` | 路径非空；启动阶段校验父目录 |
| `WithPIDFilePerm(os.FileMode)` | `0o644` | pid 文件权限 | 必须包含写权限 |
| `WithPIDRefreshInterval(time.Duration)` | `0`（关闭） | 周期性重写 pid；`0` 表示只写一次 | `>= 0` |
| `WithWorkDir(string)` | 进程当前目录 | pid、相对路径的基准目录 | 启动阶段校验存在且是目录 |
| `WithDatabase(DatabaseConfig)` | 未配置 | App 创建、持有并关闭 DB | DSN 非空、连接池参数合法 |
| `WithDB(*gorm.DB)` | 未配置 | 注入已有 DB；**App 不 Ping、不关闭** | 与 `WithDatabase` 互斥 |
| `WithModuleFactories(...ModuleFactory)` | 空 | 启动时构建模块 | 不允许 nil；名称在构建阶段去重 |
| `WithModules(...transport.Module)` | 空 | 直接注入模块实例（测试/简单场景） | 名称去重 |
| `WithScripts(...script.Script)` | 空 | 注册脚本 | 名称唯一、Run 非 nil、保留名检查 |
| `WithCommands(...command.Command)` | 空 | 注册自定义命令 | 名称唯一、不与内置冲突 |
| `WithShutdownTimeout(time.Duration)` | `10s` | HTTP 优雅关闭 + 模块停止的总预算 | `> 0` |
| `WithResponder(transport.Responder)` | `transport.NewResponder(transport.ResponderConfig{})` | 统一响应格式 | 非 nil |
| `WithRecovery(bool)` | `true` | 是否安装 panic 恢复中间件 | — |
| `WithAccessLog(bool)` | `true` | 是否安装访问日志 | — |
| `WithCORS(CORSConfig)` | 关闭 | 跨域配置 | credentials + wildcard 冲突时报错 |
| `WithHealthEndpoints(bool)` | `false` | 是否注册 `/healthz`、`/readyz` | — |
| `WithMaxBodyBytes(int64)` | `4 << 20` | 请求体上限 | `> 0` |
| `WithTrustedProxies(...string)` | 空（不信任代理） | 设置 gin 的 trusted proxies | CIDR 合法 |
| `WithAlwaysOK(bool)` | `false` | 兼容旧行为：所有响应 HTTP 200 | — |
| `WithServerConfigurator(func(*http.Server) error)` | nil | HTTP Server 逃生口（TLS、ConnState 等） | 执行错误会中止启动 |
| `WithVersionInfo(VersionInfo)` | 从 `debug.ReadBuildInfo` 推导 | 构建信息 | — |

多次调用 `WithModuleFactories`、`WithModules`、`WithScripts`、`WithCommands` 是**累加**的，不是覆盖。构建顺序固定为：

```text
ModuleFactory（按注册顺序）
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

### 3.4 `transport`

#### 3.4.1 Module 与生命周期

```go
package transport

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
package transport

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
package transport

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

func (m *Module) Register(r *transport.Router) error {
    h := handler.New(m.svc)
    transport.GET(r, "/orders/:id", h.Get)
    transport.POST(r, "/orders", h.Create)
    transport.GET(r, "/orders", h.List)
    transport.NoContent(r, http.MethodDelete, "/orders/:id", h.Cancel)
    return nil
}
```

为什么不是 `r.GET(path, transport.Handle(h.Get))`：

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
package transport

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

框架中间件位于 `transport/middleware`，全部以"显式接收依赖"的方式实现，不使用包级 logger。

```go
func RequestID(header string) gin.HandlerFunc
func AccessLog(logger *zap.Logger, opts AccessLogOptions) gin.HandlerFunc
func Recovery(logger *zap.Logger, responder transport.Responder) gin.HandlerFunc
func CORS(cfg transport.CORSConfig) (gin.HandlerFunc, error)
func BodyLimit(maxBytes int64) gin.HandlerFunc
```

安装顺序（由外到内）：

```text
1. RequestID
2. AccessLog
3. CORS（可选）
4. Recovery
5. BodyLimit
6. 业务全局中间件（WithMiddleware）
7. 模块/路由中间件
8. 业务 handler
```

顺序理由：

- `RequestID` 最外层，后续所有日志都能拿到 `req_id`；
- `AccessLog` 必须在 `Recovery` 外层，它的 `defer` 才能记录到 panic 后的 `500`；
- `CORS` 在 `Recovery` 外层，保证 panic 响应也带跨域头；
- `Recovery` 在业务中间件外层，业务 panic 不会击穿进程；
- `BodyLimit` 在业务之前，避免大请求进入业务逻辑。

`AccessLogOptions`：

```go
type AccessLogOptions struct {
    SkipPaths      []string      // 默认 ["/healthz", "/readyz"]
    SlowThreshold  time.Duration // 默认 500ms，超过打 warn
    LogRequestID   bool          // 默认 true
    LogUserAgent   bool          // 默认 true
    LogClientIP    bool          // 默认 true
    MaxPathLength  int           // 默认 1024，防止超长 URL 污染日志
}
```

访问日志字段：

```json
{
  "level": "info",
  "msg": "http access",
  "req_id": "...",
  "method": "GET",
  "path": "/api/v1/order/orders/1",
  "status": 200,
  "bytes": 123,
  "latency_ms": 3.1,
  "client_ip": "10.0.0.1",
  "user_agent": "curl/8.0"
}
```

级别规则：`5xx -> error`，`4xx -> warn`，其余 `info`。

`Recovery` 规则：

- 记录 `req_id`、method、path、panic 值、堆栈；
- 响应固定 `500` + `code 50000` + `"internal server error"`；
- 不把 panic 值或堆栈返回给客户端；
- `WithRecovery(false)` 时不安装，panic 会按 gin 默认行为冒泡（不推荐）。

`RequestID` 规则：

- 优先读取请求头 `X-Request-ID`；
- 只接受 `^[A-Za-z0-9._-]{1,128}$`，否则重新生成；
- 生成 UUID v4；
- 写回响应头，并放入 `gin.Context`，key 为 `gofy.request_id`。

#### 3.4.7 CORS

```go
type CORSConfig struct {
    AllowOrigins     []string
    AllowMethods     []string
    AllowHeaders     []string
    ExposeHeaders    []string
    AllowCredentials bool
    MaxAge           time.Duration
}
```

默认（`WithCORS` 不调用时完全不启用）：

```go
CORSConfig{
    AllowOrigins:  []string{},
    AllowMethods:  []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
    AllowHeaders:  []string{"Origin", "Content-Type", "Authorization", "X-Request-ID"},
    MaxAge:        12 * time.Hour,
}
```

校验：

- `AllowCredentials == true` 且 `AllowOrigins` 包含 `"*"` -> `New` 返回错误；
- `AllowOrigins` 为空 -> 不启用；
- 要允许任意来源但不带凭证，显式写 `"*"`。

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

### 3.6 `logging`

```go
package logging

type Config struct {
    Level        string            `yaml:"level" default:"info"`        // debug|info|warn|error
    Encoding     string            `yaml:"encoding" default:"json"`     // json|console
    Outputs      []string          `yaml:"outputs"`                     // stdout|stderr|文件路径
    ErrorOutputs []string          `yaml:"error_outputs"`               // 仅 error 及以上
    TimeLayout   string            `yaml:"time_layout" default:"2006-01-02 15:04:05"`
    Rotation     RotationConfig    `yaml:"rotation"`
    BaseFields   map[string]string `yaml:"base_fields"`
}

type RotationConfig struct {
    Enabled   bool `yaml:"enabled"`
    MaxSizeMB int  `yaml:"max_size_mb" default:"100"`
    MaxAgeDays int `yaml:"max_age_days" default:"30"`
    MaxBackups int `yaml:"max_backups" default:"7"`
    Compress   bool `yaml:"compress" default:"true"`
    LocalTime  bool `yaml:"local_time" default:"true"`
}

func New(cfg Config) (*zap.Logger, error)
func Nop() *zap.Logger
func Sync(l *zap.Logger) error
func (c Config) Validate() error
```

行为：

- 不修改 `time.Local`；时间编码使用 `TimeLayout`。
- 不创建"按天目录"（当前 `ParseDayLogPath` 的行为被移除）；按大小/天数轮转由 lumberjack 负责。
- `Outputs` 为空时默认 `["stdout"]`。
- 文件输出且 `Rotation.Enabled == true` 时使用 lumberjack；否则直接 `os.OpenFile` 追加。
- `ErrorOutputs` 非空时，error 级别同时写入 `Outputs` 与 `ErrorOutputs`。
- `BaseFields` 会作为固定字段附加到每条日志（如 `service`、`env`、`version`）。
- 返回的 logger 由调用方负责 `Sync`；App 内建的 logger 由 App 在关闭时 `Sync`。
- 无 `init()`、无包级 logger、无后台切日 goroutine。

`gorm.go` 提供可选的 GORM 适配：

```go
func GormLogger(l *zap.Logger, cfg GormLoggerConfig) gormlogger.Interface
```

### 3.7 `support/mysql`

```go
package mysql

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
```

行为：

- `New` 内部执行 `PingContext`，失败返回错误，不 panic。
- `logger` 为 nil 时使用 `zap.NewNop()`。
- `DSN` 为空时返回 `ErrDSNRequired`。
- 连接池参数为 0 时使用默认值。
- `Close` 是 `sqlDB.Close()` 的薄封装。
- 不在 `New` 中打印 DSN；错误信息中的 DSN 需脱敏（至少隐藏密码段）。
- 不使用 `sync.Once`、不提供包级 `GetDb()`。

> v1 把 GORM 作为主依赖。若未来要支持"不用 DB 的用户零 GORM 依赖"，可把 `support/mysql` 拆成独立子 module；这属于 v2 的开放问题（见 §15.2）。

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
- 保留名：`start`、`stop`、`script`、`version`、`help`、`completion`；
- 名称唯一；
- 自定义命令挂在根命令下，例如 `order-service migrate`。

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
- 强制 `WithPIDFile("")`、`WithAccessLog(false)`、`WithRecovery(true)`、`WithLogger(logging.Nop())`；
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

`stop` 参数：

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--pid-file` | `WithPIDFile` 的值 | 覆盖 pid 路径 |
| `--timeout` | `WithShutdownTimeout` | 等待进程退出的时间 |
| `--force` | `false` | 超时后发送 SIGKILL（危险，显式开启） |

### 4.2 参数解析规则

- `ExecuteArgs(ctx, args)` 只解析传入的 `args`，不读 `os.Args`；`Execute` 等价于 `ExecuteArgs(ctx, os.Args[1:])`。
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
  ├─ 5. 若配置 DB：mysql.New -> Ping
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
// order-service/app/config/config.go
package config

import (
    "context"

    "github.com/bingcool/gofy"
    gofyconfig "github.com/bingcool/gofy/config"
    "github.com/bingcool/gofy/logging"
    "github.com/yourorg/order-service/app/module/order"
)

type Config struct {
    Env     string              `yaml:"env" default:"dev"`
    HTTP    gofy.HTTPConfig     `yaml:"http"`
    Log     logging.Config      `yaml:"log"`
    DB      gofy.DatabaseConfig `yaml:"db"`
    PIDFile string              `yaml:"pid_file"`
    Order   order.Config        `yaml:"order"`
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
  outputs:
    - stdout
    - /var/log/order-service/app.log
  error_outputs:
    - /var/log/order-service/error.log
  rotation:
    enabled: true
    max_size_mb: 100
    max_age_days: 30
    max_backups: 7
    compress: true
  base_fields:
    service: order-service
    env: prod

db:
  dsn: ${ORDER_DB_DSN}
  max_idle_conns: 10
  max_open_conns: 50
  conn_max_lifetime: 1h
  ping_timeout: 5s

pid_file: /var/run/order-service.pid

order:
  payment_timeout: 3s
```

`${ORDER_DB_DSN}` 在加载时展开；缺失时启动失败（除非显式允许）。

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
- `mysql.New` 的错误信息必须脱敏 DSN；
- 配置加载错误只输出文件路径与解析位置，不输出文件内容；
- 生产配置建议使用环境变量注入 secret，而不是把密码写进 YAML。

---

## 7. HTTP 请求与响应契约

### 7.1 请求处理流水线

```text
RequestID
  -> AccessLog
  -> CORS
  -> Recovery
  -> BodyLimit
  -> 业务全局中间件
  -> 模块/路由中间件
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
| `apperr.Error` | 其 `Status` | 其 `Code` | 其 `Message` |
| `context.Canceled` | 499 | 49900 | client closed request |
| `context.DeadlineExceeded` | 504 | 50400 | request timeout |
| 请求体超限 | 413 | 41300 | request entity too large |
| 其他 error | 500 | 50000 | internal server error |

`WithAlwaysOK(true)` 时，以上所有响应 HTTP 状态统一为 `200`，仅保留包络中的 `code`（兼容当前仓库行为）。新项目不建议开启。`transport.NoContent` 成功时固定返回 `204`，不受该选项影响。

### 7.4 日志与隐私

- 访问日志默认不记录请求体、响应体、Header；
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
| `*transport.Router` | 只在 `Register` 阶段使用，不保证并发安全 |
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

## 9. order-service 完整接入示例

### 9.1 module 与目录

```go
// order-service/go.mod
module github.com/yourorg/order-service

go 1.24

require github.com/bingcool/gofy v1.0.0

// 本地联调：
// replace github.com/bingcool/gofy => ../gofy
```

```text
order-service/
├── go.mod
├── go.sum
├── main.go
├── config.yaml
├── Makefile
├── Dockerfile
├── app/
│   ├── config/
│   │   └── config.go
│   ├── middleware/
│   │   └── auth.go
│   └── module/
│       └── order/
│           ├── module.go
│           ├── dto/
│           │   └── order.go
│           ├── handler/
│           │   └── order_handler.go
│           ├── service/
│           │   └── order_service.go
│           ├── repository/
│           │   └── order_repository.go
│           └── model/
│               └── order.go
├── script/
│   ├── register.go
│   └── fix_order.go
└── migrations/
```

### 9.2 `main.go`

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
    "github.com/yourorg/order-service/app/config"
    "github.com/yourorg/order-service/app/module/order"
    appscript "github.com/yourorg/order-service/script"
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

    app, err := gofy.New(
        gofy.WithName("order-service"),
        gofy.WithEnv(cfg.Env),
        gofy.WithLogConfig(cfg.Log),
        gofy.WithHTTP(cfg.HTTP),
        gofy.WithAPIPrefix("/api/v1"),
        gofy.WithPIDFile(cfg.PIDFile),
        gofy.WithDatabase(cfg.DB),
        gofy.WithModuleFactories(
            order.NewModuleFactory(cfg.Order),
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
        app.Logger().Error("order-service exit", zap.Error(err))
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
    "github.com/bingcool/gofy/logging"
    "github.com/yourorg/order-service/app/module/order"
)

type Config struct {
    Env     string              `yaml:"env" default:"dev"`
    HTTP    gofy.HTTPConfig     `yaml:"http"`
    Log     logging.Config      `yaml:"log"`
    DB      gofy.DatabaseConfig `yaml:"db"`
    PIDFile string              `yaml:"pid_file"`
    Order   order.Config        `yaml:"order"`
}

func Load(ctx context.Context, path string) (*Config, error) {
    return gofyconfig.Load[Config](ctx, path)
}
```

### 9.4 `app/module/order/module.go`

```go
package order

import (
    "errors"
    "net/http"
    "time"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/transport"
    "github.com/yourorg/order-service/app/module/order/handler"
    "github.com/yourorg/order-service/app/module/order/repository"
    "github.com/yourorg/order-service/app/module/order/service"
)

type Config struct {
    PaymentTimeout time.Duration `yaml:"payment_timeout" default:"3s"`
}

type Module struct {
    svc *service.OrderService
}

func NewModuleFactory(cfg Config) gofy.ModuleFactory {
    return func(rt *gofy.Runtime) (transport.Module, error) {
        if rt.DB == nil {
            return nil, errors.New("order module requires database")
        }
        repo := repository.New(rt.DB)
        svc := service.New(repo, rt.Logger, service.Config{
            PaymentTimeout: cfg.PaymentTimeout,
        })
        return &Module{svc: svc}, nil
    }
}

func (m *Module) Name() string { return "order" }

func (m *Module) Register(r *transport.Router) error {
    h := handler.New(m.svc)
    transport.GET(r, "/orders/:id", h.Get)
    transport.POST(r, "/orders", h.Create)
    transport.GET(r, "/orders", h.List)
    transport.NoContent(r, http.MethodDelete, "/orders/:id", h.Cancel)
    return nil
}
```

最终路由：

```text
GET    /api/v1/order/orders/:id
POST   /api/v1/order/orders
GET    /api/v1/order/orders
DELETE /api/v1/order/orders/:id
```

### 9.5 `app/module/order/dto/order.go`

```go
package dto

type GetOrderRequest struct {
    ID uint64 `uri:"id" validate:"required,min=1" message:"required:订单ID不能为空,min:订单ID非法"`
}

type GetOrderResponse struct {
    ID     uint64 `json:"id"`
    UserID uint64 `json:"user_id"`
    Amount int64  `json:"amount"`
    Status string `json:"status"`
}

type CreateOrderRequest struct {
    UserID uint64 `json:"user_id" validate:"required,min=1" message:"required:用户ID不能为空,min:用户ID非法"`
    Amount int64  `json:"amount" validate:"required,min=1" message:"required:金额不能为空,min:金额必须大于0"`
}

type ListOrderRequest struct {
    UserID uint64 `form:"user_id" validate:"required,min=1" message:"required:用户ID不能为空,min:用户ID非法"`
    Page   int    `form:"page" default:"1" validate:"min=1" message:"min:页码必须大于0"`
    Size   int    `form:"size" default:"20" validate:"min=1,max=100" message:"min:每页至少1条,max:每页最多100条"`
}

type ListOrderResponse struct {
    Items []GetOrderResponse `json:"items"`
    Total int64              `json:"total"`
}
```

### 9.6 `app/module/order/handler/order_handler.go`

```go
package handler

import (
    "context"
    "errors"

    "github.com/bingcool/gofy/apperr"
    "github.com/yourorg/order-service/app/module/order/dto"
    "github.com/yourorg/order-service/app/module/order/service"
)

type OrderHandler struct {
    svc *service.OrderService
}

func New(svc *service.OrderService) *OrderHandler {
    return &OrderHandler{svc: svc}
}

func (h *OrderHandler) Get(ctx context.Context, req *dto.GetOrderRequest) (*dto.GetOrderResponse, error) {
    o, err := h.svc.Get(ctx, req.ID)
    if err != nil {
        if errors.Is(err, service.ErrOrderNotFound) {
            return nil, apperr.NotFound("order not found")
        }
        return nil, apperr.Internal("get order failed").WithCause(err)
    }
    return &dto.GetOrderResponse{
        ID:     o.ID,
        UserID: o.UserID,
        Amount: o.Amount,
        Status: o.Status,
    }, nil
}

func (h *OrderHandler) Create(ctx context.Context, req *dto.CreateOrderRequest) (*dto.GetOrderResponse, error) {
    o, err := h.svc.Create(ctx, req.UserID, req.Amount)
    if err != nil {
        return nil, apperr.Internal("create order failed").WithCause(err)
    }
    return &dto.GetOrderResponse{
        ID:     o.ID,
        UserID: o.UserID,
        Amount: o.Amount,
        Status: o.Status,
    }, nil
}

func (h *OrderHandler) List(ctx context.Context, req *dto.ListOrderRequest) (*dto.ListOrderResponse, error) {
    items, total, err := h.svc.List(ctx, req.UserID, req.Page, req.Size)
    if err != nil {
        return nil, apperr.Internal("list orders failed").WithCause(err)
    }
    res := make([]dto.GetOrderResponse, 0, len(items))
    for _, o := range items {
        res = append(res, dto.GetOrderResponse{
            ID:     o.ID,
            UserID: o.UserID,
            Amount: o.Amount,
            Status: o.Status,
        })
    }
    return &dto.ListOrderResponse{Items: res, Total: total}, nil
}

func (h *OrderHandler) Cancel(ctx context.Context, req *dto.GetOrderRequest) error {
    if err := h.svc.Cancel(ctx, req.ID); err != nil {
        if errors.Is(err, service.ErrOrderNotFound) {
            return apperr.NotFound("order not found")
        }
        return apperr.Internal("cancel order failed").WithCause(err)
    }
    return nil
}
```

`Cancel` 使用 `transport.NoContent` 注册：

```go
transport.NoContent(r, http.MethodDelete, "/orders/:id", h.Cancel)
```

### 9.7 `app/module/order/service/order_service.go`（节选）

```go
package service

import (
    "context"
    "time"

    "github.com/yourorg/order-service/app/module/order/model"
    "github.com/yourorg/order-service/app/module/order/repository"
    "go.uber.org/zap"
)

type Config struct {
    PaymentTimeout time.Duration
}

var ErrOrderNotFound = repository.ErrNotFound

type OrderService struct {
    repo   *repository.OrderRepository
    logger *zap.Logger
    cfg    Config
}

func New(repo *repository.OrderRepository, logger *zap.Logger, cfg Config) *OrderService {
    return &OrderService{
        repo:   repo,
        logger: logger.With(zap.String("component", "order.service")),
        cfg:    cfg,
    }
}

func (s *OrderService) Get(ctx context.Context, id uint64) (*model.Order, error) {
    return s.repo.FindByID(ctx, id)
}

func (s *OrderService) Create(ctx context.Context, userID uint64, amount int64) (*model.Order, error) {
    return s.repo.Create(ctx, &model.Order{UserID: userID, Amount: amount, Status: "created"})
}

func (s *OrderService) List(ctx context.Context, userID uint64, page, size int) ([]model.Order, int64, error) {
    return s.repo.ListByUser(ctx, userID, (page-1)*size, size)
}

func (s *OrderService) Cancel(ctx context.Context, id uint64) error {
    return s.repo.UpdateStatus(ctx, id, "cancelled")
}
```

### 9.8 `app/module/order/repository/order_repository.go`（节选）

```go
package repository

import (
    "context"
    "errors"
    "fmt"

    "github.com/yourorg/order-service/app/module/order/model"
    "gorm.io/gorm"
)

var ErrNotFound = errors.New("order not found")

type OrderRepository struct {
    db *gorm.DB
}

func New(db *gorm.DB) *OrderRepository {
    return &OrderRepository{db: db}
}

func (r *OrderRepository) FindByID(ctx context.Context, id uint64) (*model.Order, error) {
    var o model.Order
    err := r.db.WithContext(ctx).First(&o, id).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        return nil, ErrNotFound
    }
    if err != nil {
        return nil, fmt.Errorf("find order %d: %w", id, err)
    }
    return &o, nil
}

func (r *OrderRepository) Create(ctx context.Context, o *model.Order) (*model.Order, error) {
    if err := r.db.WithContext(ctx).Create(o).Error; err != nil {
        return nil, fmt.Errorf("create order: %w", err)
    }
    return o, nil
}

func (r *OrderRepository) ListByUser(ctx context.Context, userID uint64, offset, limit int) ([]model.Order, int64, error) {
    var (
        items []model.Order
        total int64
    )
    q := r.db.WithContext(ctx).Model(&model.Order{}).Where("user_id = ?", userID)
    if err := q.Count(&total).Error; err != nil {
        return nil, 0, fmt.Errorf("count orders: %w", err)
    }
    if err := q.Order("id DESC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
        return nil, 0, fmt.Errorf("list orders: %w", err)
    }
    return items, total, nil
}

func (r *OrderRepository) UpdateStatus(ctx context.Context, id uint64, status string) error {
    res := r.db.WithContext(ctx).Model(&model.Order{}).Where("id = ?", id).Update("status", status)
    if res.Error != nil {
        return fmt.Errorf("update order %d: %w", id, res.Error)
    }
    if res.RowsAffected == 0 {
        return ErrNotFound
    }
    return nil
}
```

### 9.9 `script/register.go` 与 `script/fix_order.go`

```go
package script

import (
    gofyscript "github.com/bingcool/gofy/script"
    "github.com/yourorg/order-service/app/config"
)

func Register(cfg *config.Config) []gofyscript.Script {
    return []gofyscript.Script{
        FixOrder(cfg),
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
    "github.com/yourorg/order-service/app/config"
    "go.uber.org/zap"
)

func FixOrder(cfg *config.Config) gofyscript.Script {
    return gofyscript.Script{
        Name:  "fix-order",
        Short: "fix inconsistent order data",
        Flags: func(fs *pflag.FlagSet) {
            fs.Uint64("order-id", 0, "order id")
            fs.Bool("dry-run", true, "only print what would be changed")
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
                zap.Duration("payment_timeout", cfg.Order.PaymentTimeout),
            )
            // 使用 rt.DB 执行数据修复
            return nil
        },
    }
}
```

执行：

```bash
./order-service script fix-order --order-id=1001 --dry-run=false
./order-service script --list
```

### 9.10 业务中间件

```go
// app/middleware/auth.go
package middleware

import (
    "github.com/bingcool/gofy/transport"
    "github.com/gin-gonic/gin"
)

func Auth(verify func(token string) error) transport.Middleware {
    return func(c *gin.Context) {
        token := c.GetHeader("Authorization")
        if err := verify(token); err != nil {
            // 注意：中间件里不能直接调用 Responder；
            // 这里使用 gin 的 abort + 由业务自行写响应，
            // 或者把校验放到 handler 内返回 apperr。
            c.AbortWithStatusJSON(401, gin.H{
                "code": 40100,
                "msg":  "unauthorized",
            })
            return
        }
        c.Next()
    }
}
```

> 中间件的错误响应有两种做法：一是像上面直接写；二是把错误放进 `c`，在统一中间件里交给 `Responder`。v1 推荐把认证失败收敛到 handler 返回 `apperr.Unauthorized`，避免每个中间件重复响应格式。

### 9.11 CLI 行为

| 命令 | 行为 |
| --- | --- |
| `./order-service` | 等价于 `./order-service start` |
| `./order-service start` | 前台启动，阻塞到信号 |
| `./order-service stop` | 读取 pid，SIGTERM，等待退出 |
| `./order-service stop --force` | 超时后 SIGKILL |
| `./order-service version` | 输出版本/commit/构建时间/Go 版本 |
| `./order-service version --json` | JSON 输出 |
| `./order-service script --list` | 列出脚本 |
| `./order-service script fix-order --order-id=1` | 执行脚本 |
| `./order-service <custom>` | 自定义命令 |

---

## 10. 脚手架 `gofy new`

### 10.1 安装与用法

```bash
go install github.com/bingcool/gofy/cmd/gofy@latest

gofy new order-service \
  --module github.com/yourorg/order-service \
  --dir ./order-service \
  --with-db \
  --with-scripts \
  --with-docker
```

### 10.2 参数

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--module` | 必填 | 生成 `go.mod` 的 module path |
| `--dir` | `./<name>` | 输出目录 |
| `--with-db` | `false` | 生成 `support/mysql` 接入代码与 `db` 配置 |
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
created order-service
next steps:
  cd order-service
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
| 中间件测试 | RequestID 透传/生成、AccessLog 字段、Recovery 不泄漏堆栈、CORS 校验 |
| 生命周期测试 | 启动顺序、Stop 逆序、Shutdown 幂等、端口占用错误 |
| 进程测试 | pid 写入/读取/stale 处理、`stop` 超时（用子进程） |
| 依赖测试 | 公共包 import 方向、公共包无 `init()` |
| 示例测试 | `examples/order-service` 编译 + handler 测试 |

测试约束：

- 不读取工作目录下的 `config.yaml`；
- 不监听固定端口（用 `httptest` 或 `net.Listen("tcp", "127.0.0.1:0")`）；
- 不依赖真实 MySQL；需要 DB 的测试用 `sqlmock` 或业务注入的 fake；
- 所有测试支持 `-race` 与 `-count=1`。

### 11.2 业务方测试

```go
package handler_test

import (
    "testing"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/gofytest"
    "github.com/yourorg/order-service/app/module/order/dto"
)

func TestGetOrder(t *testing.T) {
    app := gofytest.New(t,
        gofy.WithName("order-service-test"),
        gofy.WithModules(newFakeOrderModule(t)),
    )

    rr := app.GET("/api/v1/order/orders/1")
    gofytest.Status(t, rr, 200)

    body := gofytest.JSON[dto.GetOrderResponse](t, rr)
    if body.ID != 1 {
        t.Fatalf("want id=1, got %d", body.ID)
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

在 `examples/order-service` 中维护一组"行为契约"测试：

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
- `go.mod` 的 `go` 指令：`1.24`（最低支持版本）；CI 覆盖 `1.24` / `1.25`，业务可使用更高版本；
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
        go: ["1.24", "1.25"]
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
          cd examples/order-service
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
            *.go apperr core transport logging support script command config gofytest; then
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

pkgs=(apperr core transport logging support/... script command config)
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
5. 在 `examples/order-service` 中把版本改成新 tag 并跑一次 CI；
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
| `main.go` | `examples/order-service/main.go` | 改写为 `gofy.New` 接入；根目录删除 |
| `src/cmd/root.go` | `gofy.go` / `option.go` / `internal/commandline` | 拆分；删除 `init()`、`os.Args` 解析、全局 `rootCmd` |
| `src/cmd/start.go` | `run.go` / `internal/pidfile` | `gin.New` + 中间件 + `net.Listen` + `Shutdown`；daemon 分支删除 |
| `src/cmd/stop.go` | `stop.go` / `internal/process` | 改为返回 error；删除 `os.Exit` |
| `src/cmd/script.go` | `execute.go` + `script` | 改为 `script <name>` 子命令；删除 `--c=` |
| `src/cmd/version.go` | `internal/version` + `execute.go` | 使用 `debug.ReadBuildInfo` |
| `src/cmd/daemon-start.go` | 删除 | v1 不内置 daemon |
| `src/cmd/daemon-stop.go` | 删除 | 同上 |
| `src/cmd/command/cmd.go` | 删除 | 命令常量与运行模式不再需要 |
| `src/conf/yaml.go` | `config/load.go` | 去掉 viper、`init()`、全局配置 |
| `src/log/logger.go` | `logging/logging.go` | 去掉 `init()`、包级 logger、切日协程；修正 `Compress` 读取 `MaxAge` 的 bug |
| `src/request/requestLog.go` | `transport/middleware/accesslog.go` | 改为显式 logger；不再单独初始化 |
| `src/system/system_env.go` | 删除 | `Env` 放 `Runtime`；`WorkDir` 放 Option |
| `src/timelocal/timezone.go` | 删除 | 不在库内修改 `time.Local`；时区由 DSN `loc` 或业务 `main` 决定 |
| `src/utils` | `internal/` | 仅保留仍在使用的工具；删除 `IsNumber` 等无人调用的函数 |
| `src/console` | 删除 | 未被使用 |
| `src/http/route.go` | `transport/router.go` | 改写为 `Router` 薄封装 |
| `app/middleware/cors.go` | `transport/middleware/cors.go` | 内部实现；校验 credentials + wildcard |
| `app/middleware/recovery.go` | `transport/middleware/recovery.go` | 不再返回堆栈给客户端；不使用 `*gin.Context` 跨 goroutine |
| `app/middleware/requestid.go` | `transport/middleware/requestid.go` | 内部实现；Header 校验 |
| `app/middleware/response.go` | `transport/responder.go` | 包络结构保留，改为 `Responder` 接口 |
| `app/middleware/registermiddleware.go` | 删除 | 中间件顺序由 App 统一安装 |
| `app/middleware/validateLogin.go` | `examples/order-service/app/middleware/auth.go` | 示例业务中间件 |
| `app/middleware/test.go` | 删除 | 测试用中间件不属于框架 |
| `app/route/build/route.go` | `transport/handler.go` / `bind.go` / `validate.go` | 改为泛型 handler；删除 `panic`、`validator.New()`/请求、`gconv` |
| `app/route/init.go` | `examples/order-service/app/module/order/module.go` | 业务路由注册 |
| `app/route/demo/*` | `examples/order-service/app/module/order/*` | 改写为 order 示例 |
| `app/module/demo/controller/order_controller.go` | `examples/order-service/app/module/order/handler` | 改为 `context.Context` handler |
| `app/Io/db/mysql.go` | `support/mysql/mysql.go` | 配置驱动、可关闭、可 Ping、无 `sync.Once` |
| `app/config/db.go` | `examples/order-service/app/config/config.go` | 业务配置 |
| `app/repository/tool.go` | `examples/order-service/app/module/order/repository` | 改写为业务仓储 |
| `script/kernel.go` | `examples/order-service/script/register.go` | 改为返回 `[]script.Script` |
| `script/user/test.go` | `examples/order-service/script/fix_order.go` | 改为示例脚本 |
| `config.yaml` | `examples/order-service/config.yaml` | 业务自己维护；删除机器相关路径与明文密码 |
| `gen.yaml` | 业务方维护 | 不进入框架库 |
| `cron.sql` / `cron.yaml` | 删除 | 定时任务系统已移除 |
| `app/server.pid`、`app/storage/logs` | 删除并加入 `.gitignore` | 运行时产物 |

### 13.3 迁移阶段

#### Phase 0：冻结与基线

- 打 tag `v0.9.0-project`；
- 记录当前构建、启动、停止、脚本行为；
- 新建 `v1` 分支。

#### Phase 1：建立 library 骨架（不改变行为）

- 新建 `core`、`apperr`、`transport`、`logging`、`support/mysql`、`script`、`command`、`config`；
- 先实现最小可编译版本：`Runtime`、`Router`、`Responder`、`logging.New`、`mysql.New`；
- 保持 `examples/order-service` 能跑通；
- 此时旧 `src/` 仍在，CI 同时编译两者。

#### Phase 2：迁移示例

- 把 `app/`、`script/`、`main.go` 复制到 `examples/order-service`，改写为图书馆 API；
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
├── run.go
├── execute.go
├── stop.go
├── signal.go
├── apperr/
├── core/
├── transport/
├── logging/
├── support/mysql/
├── script/
├── command/
├── config/
├── gofytest/
├── internal/
├── cmd/gofy/
├── examples/order-service/
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
- [ ] 公共包（`apperr`、`core`、`transport`、`logging`、`support`、`script`、`command`、`config`、`gofytest`）中不存在 `func init()`；
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

- [ ] `examples/order-service` 可独立 `go build ./...`；
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
| GORM 强制依赖 | 不用 DB 的业务也要下载 GORM | v1 接受；v2 可拆 `gofy/mysql` 子 module |
| gin 版本耦合 | 业务可能与自有 gin 版本冲突 | gofy 统一管理版本；`Router.Gin()` 提供逃生口 |
| 响应格式变化 | 从"永远 HTTP 200"改为真实状态码 | 提供 `WithAlwaysOK(true)` 兼容开关 |
| 配置加载差异 | 业务已有 viper/koanf | `config.Load` 完全可选；gofy 不强制 |
| 移除 daemon | 依赖 daemon 的部署方式需要改造 | 迁移文档提供 systemd/容器方案 |
| 脚手架腐烂 | 模板与 API 不同步 | CI 中生成并编译模板 |
| 抽象过度 | 为了"框架感"包装过多 gin | Router 只做薄封装；保留 `Gin()` |

### 15.2 开放问题

| # | 问题 | 当前倾向 | 决策时间 |
| --- | --- | --- | --- |
| Q1 | 是否把 `support/mysql` 拆成独立子 module，减少非 DB 用户的依赖？ | v1 不拆；v2 评估 | v2 规划时 |
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
| 2026-11-03 | 泛型路由使用自由函数 `transport.GET(r, path, h)` | Go 不支持泛型方法；同时避免包级 validator/responder |
| 2026-11-03 | 移除 `viper` | 全局单例、隐式 env、配置所有权应归业务 |
| 2026-11-03 | 移除 `gogf/gf` | 仅为 `gconv.Map` 引入超大依赖 |
| 2026-11-03 | 移除 `go-daemon` | 库不接管进程守护；交给 systemd/容器 |
| 2026-11-03 | 移除 `robfig/cron` | 定时任务不属于框架；pid 刷新用 `time.Ticker` |
| 2026-11-03 | 框架不隐式注册信号 | 生命周期由调用方通过 `ctx` 控制；`SignalContext` 只是辅助 |
| 2026-11-03 | 错误响应使用真实 HTTP 状态码 | 更符合 HTTP 语义；`WithAlwaysOK` 兼容旧行为 |
| 2026-11-03 | 不提供依赖注入容器 | 业务依赖用闭包/构造函数注入；`Runtime` 只放框架依赖 |

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
func WithModules(...transport.Module) Option
func WithScripts(...script.Script) Option
func WithCommands(...command.Command) Option
func WithShutdownTimeout(time.Duration) Option
func WithResponder(Responder) Option
func WithRecovery(bool) Option
func WithAccessLog(bool) Option
func WithCORS(CORSConfig) Option
func WithHealthEndpoints(bool) Option
func WithMaxBodyBytes(int64) Option
func WithTrustedProxies(...string) Option
func WithAlwaysOK(bool) Option
func WithServerConfigurator(func(*http.Server) error) Option
func WithVersionInfo(VersionInfo) Option

type HTTPConfig
func (HTTPConfig) Validate() error
type CORSConfig = transport.CORSConfig
type VersionInfo = core.VersionInfo
type Runtime = core.Runtime
type ModuleFactory = transport.ModuleFactory
type Middleware = transport.Middleware
type Responder = transport.Responder
type DatabaseConfig = mysql.Config
type LogConfig = logging.Config
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

### A.3 `transport`

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

### A.4 `transport/middleware`

```text
func RequestID(string) gin.HandlerFunc
func AccessLog(*zap.Logger, AccessLogOptions) gin.HandlerFunc
func Recovery(*zap.Logger, transport.Responder) gin.HandlerFunc
func CORS(transport.CORSConfig) (gin.HandlerFunc, error)
func BodyLimit(int64) gin.HandlerFunc
type AccessLogOptions
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

### A.6 `logging`

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

### A.7 `support/mysql`

```text
type Config
func (Config) Validate() error
func New(context.Context, Config, *zap.Logger) (*gorm.DB, error)
func Close(*gorm.DB) error
var ErrDSNRequired
```

### A.8 `script`

```text
type Runtime = core.Runtime
type Script
func Validate([]Script) error
func Find([]Script, string) (Script, bool)
```

### A.9 `command`

```text
type Runtime = core.Runtime
type Command
func Validate([]Command, ...string) error
```

### A.10 `config`

```text
type Option
func Load[T any](context.Context, string, ...Option) (*T, error)
func WithExpandEnv(bool) Option
func WithAllowMissingEnv(bool) Option
func WithStrict(bool) Option
func WithApplyDefaults(bool) Option
func WithValidate(bool) Option
```

### A.11 `gofytest`

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

---

## 附录 B：与旧行为的差异清单（升级必读）

| 旧行为 | 新行为 | 迁移建议 |
| --- | --- | --- |
| `import src/log` 自动读配置、建 logger | `import logging` 无副作用；`logging.New(cfg)` 显式创建 | 在 `main` 中创建并 `WithLogger` |
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
    "github.com/bingcool/gofy/transport"
)

type pingModule struct{}

func (pingModule) Name() string { return "ping" }

func (pingModule) Register(r *transport.Router) error {
    transport.GET(r, "/ping", ping)
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
