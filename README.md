# gofy

`gofy` 是一个基于 [gin](https://github.com/gin-gonic/gin) 的轻量、约定优先的 Go 应用框架。
它提供应用生命周期、类型化 HTTP handler、请求绑定与校验、统一响应包络、分类日志、
MySQL 协议 / PostgreSQL / SQLite（GORM）接入、维护脚本和 CLI，同时没有全局状态、
`init()` 副作用和固定配置文件。

## 安装

```bash
go get github.com/bingcool/gofy@latest
```

要求 Go 1.26+。

## 快速开始

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
)

type helloRequest struct {
	Name string `form:"name" default:"world" validate:"required,max=32" message:"required:名称不能为空,max:名称过长"`
}

type helloResponse struct {
	Message string `json:"message"`
}

func main() {
	ctx, stop := gofy.SignalContext(context.Background())
	defer stop()

	app, err := gofy.New(
		gofy.WithName("hello"),
		gofy.WithAPIPrefix("/api/v1"),
		gofy.WithHealthEndpoints(true),
		gofy.WithModules(helloModule{}),
	)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if err := app.Execute(ctx); err != nil {
		os.Exit(gofy.ExitCode(err))
	}
}

type helloModule struct{}

func (helloModule) Name() string { return "hello" }

func (helloModule) Register(r *support.Router) error {
	support.GET(r, "/greet", func(_ context.Context, req *helloRequest) (*helloResponse, error) {
		return &helloResponse{Message: "hello " + req.Name}, nil
	})
	return nil
}
```

```bash
go run .                     # 等价于 go run . start
go run . version
go run . stop                # 配置了 pid 文件时可用，需输入 yes 确认
go run . stop --force=1      # 跳过确认
```

路由挂载在 `/api/v1/hello/greet`（这里显式设置了 `WithAPIPrefix("/api/v1")`；
该选项默认不追加前缀，契约路由通常把完整前缀写在路由分组上）：

```bash
curl 'http://127.0.0.1:8080/api/v1/hello/greet?name=gofy'
# {"code":0,"msg":"success","data":{"message":"hello gofy"},"req_id":"..."}
```

校验失败时 `message` 标签的文案直接作为 `msg` 返回，同时保留字段级 `details`：

```bash
curl 'http://127.0.0.1:8080/api/v1/hello/greet?name='
# {"code":40001,"msg":"名称不能为空","data":null,"req_id":"...","details":{"name":"名称不能为空"}}
```

## HTTP 安全与限流

三个 option 都是可选的，只作用于业务模块路由，`/healthz`、`/readyz` 和未匹配路径不受影响：

```go
parser, err := jwtauth.NewParser(jwtauth.Config{
	Secret:        []byte(os.Getenv("JWT_SECRET")),
	Issuer:        "demo",
	RequireExpiry: true,
})
if err != nil {
	return err
}

app, err := gofy.New(
	gofy.WithSecurityHeaders(support.SecurityHeadersConfig{
		HSTS: 24 * time.Hour, // 仅在确认全站 HTTPS 后开启
	}),
	gofy.WithRateLimit(support.RateLimitConfig{
		Store:  ratelimit.NewMemoryStore(),
		Limit:  100,
		Window: time.Minute,
	}),
	gofy.WithJWTAuth(support.JWTAuthConfig{Parser: parser}),
	// ...
)
```

- **JWT**：失败固定返回 `401 / 40100`，成功后可 `support.JWTClaims(c)` 读取 claims；
  校验后的身份同时写入 `frameworkcontext`，Service 层用
  `frameworkcontext.UserID(ctx)` 读取；`Optional: true` 时无 token 也放行，由业务自己判断。
- **限流**：滑动窗口，按 `客户端 IP + 方法 + 路由` 分桶；响应带 `X-RateLimit-*`，
  超限返回 `429 / 42900` 和 `Retry-After`。单进程用
  `ratelimit.NewMemoryStore()`；多实例用 `support/redis` +
  `ratelimit/redisstore` 共享计数。存储故障默认返回 `503 / 50300`，
  `FailOpen: true` 可放行。
- **安全响应头**：默认 `nosniff`、`DENY`、`strict-origin-when-cross-origin`，
  可追加 HSTS、CSP、Permissions-Policy 和自定义头；含换行的值在启动时报错。
- **404 / 405**：未匹配路径和错误方法统一走响应包络（`40400` / `40500`），405 带
  `Allow` 头。
- **日志脱敏**：访问日志记录 query 时，`password`、`token`、`authorization`、
  `api_key`、`signature` 等值替换为 `***`；无法解析的 query 整体记为 `***`。

JWT 只做校验，不签发 token；签发由业务或独立认证服务完成。

### 中间件顺序

全局链的安装顺序是对外契约，从外到内固定为：

```text
RequestID → Telemetry Trace → HeaderPropagation → SecurityHeaders
→ AccessLog → CORS → Recovery → BodyLimit → WithMiddleware
→ /healthz、/readyz → 模块路由（RateLimit → JWTAuth）
```

- `RequestID` 在最外层，后续日志、span 和透传头都依赖 `req_id`；
- `Telemetry Trace` 紧跟 `RequestID`，`HeaderPropagation` 与 `AccessLog` 才能读到 OTel trace id；
- `AccessLog` 在 `Recovery` 外层，panic 被转成 500 后访问日志记录的仍是最终状态码；
- `Recovery` 在 `CORS` 内层，panic 响应仍带跨域头；
- `BodyLimit` 必须先于任何读取 body 的 handler；
- `WithMiddleware` 与模块路由都在 `Recovery` 内层，业务 panic 会被捕获；`RateLimit`、`JWTAuth` 只作用于模块路由。

### 中间件别名

契约路由里的 group/route 中间件只写名字，App 在装配期把「别名 → 实例」注册进
本代注册表，adapter 绑定时按名字解析。框架按已启用的选项预先登记 `request-id`、
`access-log`、`cors`、`recovery`、`body-limit`、`rate-limit`、`jwt-auth`、
`security-headers`、`header-propagation` 等内建别名，应用层用
`WithMiddlewareRegistrar` 补充：

```go
app, err := gofy.New(
    gofy.WithMiddlewareRegistrar(func(reg gofy.MiddlewareRegistration) error {
        return reg.Registry.Register("audit", auditMiddleware(reg.Runtime.Logger))
    }),
    // ...
)
```

别名不能为空或重复，重复注册直接让启动/Reload 失败而不是静默覆盖。注册函数在启动和
每次 Reload 时都面对全新的 `Runtime`、`Responder` 和空注册表，因此喂给这些应用层
中间件的配置无需重启即可生效；`ReloadSpec.MiddlewareRegistrars` 可以替换这组函数。
访问日志是全局中间件，不要把它登记成契约别名，否则同一请求会被记录两次。

## 服务间请求头透传

A → B → C 的调用链中，gofy 在入口把白名单请求头写入 request context，
`gofy-interface-api/client` 的客户端发起下游调用时从同一个 context 读取并注入，业务代码不需要手工搬运。

```go
app, err := gofy.New(
	gofy.WithHeaderPropagation(true),
	// 只有来自这些代理的地址才采信身份头；公网直连一律丢弃。
	gofy.WithTrustedProxies("10.0.0.0/8"),
	// ...
)
```

白名单（大小写不敏感）：

| Header | 含义 |
| --- | --- |
| `X-Trace-Id` | 分布式 trace id；缺失时用 RequestID 生成的 UUID 补齐 |
| `X-User-Id` | 已认证用户 ID |
| `X-User-Code` | 业务用户编号 |
| `X-Tenant-Id` | 租户 ID |
| `X-User-Name` | 用户名 |
| `X-Client-Ip` | 原始客户端地址 |
| `X-User-Agent` | 发起调用的 SDK / 网关标识 |

规则：

- `X-User-Id`、`X-User-Code`、`X-Tenant-Id`、`X-User-Name` 只在对端地址命中
  `WithTrustedProxies`（IP 或 CIDR）时保留；公网直连携带的同名头会被丢弃，
  并把 `X-Client-Ip` 覆盖为直连地址，避免客户端冒充其他用户或租户；
- `Authorization`、`Cookie` 等不在白名单内的头永远不会下发到下游；
- 网关转发的身份头应覆盖而不是追加，否则伪造值仍可能被下游采信；
- 本地认证身份优先：`JWTAuth` 校验成功后会把 token 身份写入统一身份上下文，
  并回写身份头，下游没有本地认证时才回落到透传头。

### 统一身份 FrameworkContext

网关应用在 JWT 中间件解析身份，下游应用从可信上游接收身份头，两者最终都进入
`support/frameworkcontext`。Service、Repository 等只拿到 `context.Context` 的层
用同一个入口读取，不需要感知身份来自哪里：

```go
import "github.com/bingcool/gofy/support/frameworkcontext"

userID := frameworkcontext.UserID(ctx)     // 本地认证优先，其次可信上游透传
tenant := frameworkcontext.TenantID(ctx)
traceID := frameworkcontext.TraceID(ctx)

user, ok := frameworkcontext.UserFromContext(ctx) // 只返回本地认证身份
```

优先级与写回规则：

- `UserID`、`TenantID`、`UserCode`、`UserName` 先读本地认证快照，没有时才读
  `X-User-Id` 等可信透传头；
- `WithUser` 会清掉 context 中残留的四个身份头，再写回本地非空字段，因此 token
  没有租户时不会把上游租户继续带给下游；trace、客户端地址等非身份头保持不变；
- `JWTAuth` 已自动接入，业务 handler 通常无需额外代码。

自定义认证（session、API Key 等）在认证中间件里显式写入：

```go
ctx = frameworkcontext.WithUser(ctx, frameworkcontext.User{
	ID:       "1001",
	TenantID: "tenant-a",
})
```

只需要原始透传头时，也可以直接读取：

```go
import "github.com/bingcool/gofy/support/headerpropagation"

headers := headerpropagation.HeadersFromContext(ctx) // 返回副本，可安全修改
```

经过 HTTP 入口的调用把 handler 的 `ctx` 交给强类型客户端即可自动透传：

```go
res, err := orders.GetOrder(ctx, &orderrequest.GetOrderRequest{ID: 42})
```

客户端的合并顺序是「context 透传头 < Request 结构体的 `header` tag < `CallOption`」，
因此单个接口仍可用 `client.WithHeader` 覆盖；SSE、下载和分块请求同样生效。
`X-User-Agent` 没有透传值时默认写 `gofy-api-sdk`。

> 两个仓库通过固定的 context key `gofy.header_propagation.headers` 对接：
> 服务端用 `support/headerpropagation`，客户端用 `client.ContextWithHeaders` /
> `client.HeadersFromContext`，双方都不需要 import 对方模块。

不在 App 上启用中间件时，也可以手动构造：

```go
ctx = headerpropagation.ContextWithHeaders(ctx, http.Header{
	headerpropagation.HeaderTenantID: {"tenant-a"},
})
```

## OpenTelemetry 追踪

`WithTelemetry` 接入 OTLP 追踪，默认关闭；完整配置、采样策略和独立使用方式见
[support/otel/README.md](support/otel/README.md)。

```go
app, err := gofy.New(
	gofy.WithName("order-service"),
	gofy.WithTelemetry(gofy.TelemetryConfig{
		Enabled:     true,
		Endpoint:    "otel-collector:4317",
		Protocol:    "grpc",
		Compression: "gzip",
		SampleRatio: 0.1,          // 根 trace 采样 10%，上游已采样的继续采样
		Timeout:     10 * time.Second,
	}),
	// ...
)
```

启用后框架负责：

- 安装 Gin 服务端 span 中间件，提取上游 W3C `traceparent`，`/healthz`、`/readyz`
  不采样；
- 为 `WithDatabase` 托管的连接池安装 GORM 插件，记录 SQL 模板、表名和影响行数，
  不记录绑定参数；
- 通过 `Runtime.TracerProvider`、`Runtime.Propagator`、`Runtime.Tracing` 暴露
  追踪器，供 Redis、出站 HTTP 客户端等自行埋点；
- 关停时 flush 并关闭导出器；collector 不可达只记录限流后的日志，不影响退出。

默认使用 TLS（`insecure: false`），`endpoint` 默认 `localhost:4317`（gRPC）或
`localhost:4318`（HTTP）。访问日志和 panic 恢复日志自动带 `trace_id`、`span_id`；
启用 OTel 时 `X-Trace-Id` 优先使用 OTel trace id。追踪配置在启动时固定，
修改后需要重启进程。

`global` 默认为 `false`：provider 只持有自己的 tracer provider、exporter 和
propagator，不改动 `otel.SetErrorHandler` 等进程级状态，因此可以和其它
instrumentation 共用进程；导出失败经注入的 logger 上报并按一分钟去重。需要让
`otel.Tracer()` 这类全局入口也返回本 Provider 时显式设置 `global: true`，
此时同一进程只应有一个 App 打开它。

## 热更新

`App.Reload` 在运行中的进程内重建运行时资源，不重启进程，也不重新监听端口：

```go
err := app.Reload(ctx, gofy.ReloadSpec{
    Env:                  cfg.Env,      // 空字符串表示保持当前值
    Log:                  &cfg.Log,     // 重建主、system、access 三个 logger
    Database:             &cfg.DB,      // 重新连接数据库
    ModuleFactories:      factories,    // 重新注册路由与模块
    MiddlewareRegistrars: registrars,   // 重建应用层中间件别名表
})
```

执行顺序：校验新配置 → 构建新 logger / system logger / access logger 与 DB →
构建并启动新模块 → 原子发布新 handler 与 `Runtime` → 旧代在途请求排空 →
停止旧模块、关闭旧 DB 与旧 logger。新模块启动失败时丢弃已构建的资源，旧服务继续可用。

发布后 handler 立刻指向新一代：新请求进入新代，已在旧代上处理的请求持有租约，
旧模块和资源要等这些请求结束（或达到 `WithShutdownTimeout` 预算）后才真正释放，
因此切换期间不会有请求落到已关闭的连接上。`Reload` 只能在服务已启动后调用，
否则返回 `ErrNotRunning`；并发 `Reload` 之间以及与 `Shutdown` 之间都会串行化。

HTTP 监听地址与超时、pid 文件不在 `ReloadSpec` 中，修改这些内容需要重启进程。
OpenTelemetry 的 provider 和导出器也在启动时固定，`Reload` 复用同一个实例；
修改 telemetry 配置需要重启。
通过 `WithModules` 注入的实例不会被重建：它们继续运行，只在新路由上重新注册，
因此仍持有创建时捕获的 `Runtime`；需要跟随配置变化的模块请改用 `WithModuleFactories`。
通过 `With*` 选项安装的全局中间件（CORS、限流、JWT、安全头）与 responder 沿用启动
配置，`Reload` 不能更换；应用层中间件别名可以随 `MiddlewareRegistrars` 重建。

## 脚手架

```bash
go install github.com/bingcool/gofy/cmd/gofy@latest

gofy new demo-service \
  --module github.com/yourorg/demo-service \
  --dir ./demo-service \
  --with-db \
  --with-docker
```

本地开发时加 `--replace /path/to/gofy` 指向本地仓库。生成的项目不包含密钥，
也不预生成 `go.sum`；执行 `go mod tidy` 后再运行 `go run . start`。

## 配置

gofy 自身从不读取配置文件。配置由业务项目负责加载，再通过 option 传入。
可选的配置加载器会应用默认值、展开 `${ENV_VAR}` 并做校验：

```go
type Config struct {
	HTTP gofy.HTTPConfig     `yaml:"http"`
	Log  gofy.LogConfig      `yaml:"log"`
	DB   gofy.DatabaseConfig `yaml:"db"`
}

cfg, err := gofyconfig.Load[Config](ctx, "config.yaml")
```

```yaml
http:
  port: 8080
db:
  dsn: ${ORDER_DB_DSN}
```

密钥应通过环境变量注入，不要提交到 YAML。

如果只需要读取零散配置，可以按路径直读，不必定义完整结构体：

```go
doc, err := gofyconfig.LoadDocument(ctx, "config.yaml")
if err != nil {
    return err
}
dsn, ok := doc.GetString("db.default.dsn")
if !ok {
    return errors.New("db.default.dsn is required")
}
port, _ := doc.GetInt("http.port")          // 数字、字符串形式的数字都可读取
timeout, _ := doc.GetDuration("http.read_timeout") // 1h、30s、200ms
```

`Document` 支持 `Get`、`Has`、`GetString`、`GetInt`、`GetInt64`、`GetFloat64`、
`GetBool`、`GetDuration`、`GetStringSlice`；路径用点号分隔，序列用数字下标
（如 `servers.0.host`），同样会展开 `${ENV_VAR}`。它不做结构体默认值和校验，
需要这些能力时继续使用 `Load[T]`。

日志用 `logger`（zap）：`log.color` 控制终端着色，`auto`（默认）只在 TTY 上
生效，`always` 供 IDE 这类非 TTY 控制台强制开启，`never` 关闭，非法值启动即报错。
着色生效时 `error` 及以上渲染成红色多行块：首行是时间、级别、caller、消息和
其余字段，`error` 单独成行，`stacktrace` 逐帧展开；低于 `error` 的条目和所有
文件日志仍按 `log.encoding` 输出，`NO_COLOR` 与 Windows 始终关闭着色。

```yaml
log:
  encoding: json
  color: always   # IDE 控制台不是 TTY，需要彩色分行错误时显式开启
```

### 日志分流

`logger.Config` 把一次写入按用途拆到四个输出列表，同一进程可以同时写多个目标：

| 字段 | 接收范围 | 为空时 |
| --- | --- | --- |
| `outputs` | 当前 `level` 及以上的全部日志（运行态日志） | 默认 `stdout` |
| `error_outputs` | `error` 及以上，便于快速筛出失败请求 | 所有 error 仍写入 `outputs` |
| `system_error_outputs` | 框架自动捕获的系统错误（目前是 HTTP recovery 记录的 panic） | App 回落到主 logger；直接调用 `NewSystem` 得到 nop |
| `access_outputs` | 只接收 HTTP 访问条目 | App 回落到主 logger；直接调用 `NewAccess` 得到 nop |

- 访问日志由 `middleware.AccessLog` 产生，`msg` 固定为 `http access`，字段包含
  `method`、`path`、`status`、`bytes`、`latency_ms`，可按需附带 `req_id`、
  `trace_id`/`span_id`、`client_ip`、`user_agent` 和脱敏后的 `query`；
  默认跳过 `/healthz`、`/readyz`。
- 访问条目按状态码和耗时分流：5xx → `error`，4xx 或超过慢请求阈值 → `warn`，
  其余 → `info`。`logger.NewAccess` 因此忽略 `level`、始终接收 `info` 及以上，
  也不附带 `stacktrace`（5xx 记录的是请求结果，不是出错位置）。
- `access_outputs` 为空时行为与以前一致：访问条目混在主 logger 的
  `outputs` / `error_outputs` 里；设置后运行态日志和访问日志各写各的文件。
- 滚动方式对两类 info 流一致：`rotation.mode`（`size` / `hourly`）同时作用于
  `outputs` 和 `access_outputs`，`rotation.date_dir` 与 `{date}` 占位符同样适用；
  `error_outputs` / `system_error_outputs` 固定按大小滚动。
- `WithAccessLogOptions` 不做字段级合并：布尔开关按字面生效，只想改一项时要把
  其余需要的开关一并设为 `true`（nil 切片和 0 值数值仍由中间件补默认）；
  `ReloadSpec.Log` 会同时重建主 logger、system logger 和 access logger。

demo-service 的分流结果：HTTP 模式写 `storage/log/http/<date>/`（`info` /
`error` / `system_error`）和 `storage/log/access/<date>/access.log`；脚本模式写
`storage/log/script/<date>/{info,error}/`，没有访问日志。这些目录由进程在运行时
自行创建，整个 `examples/demo-service/storage/` 都是运行产物，不纳入版本库
（见根 `.gitignore`）。

数据库连接用 `support/db`（GORM，兼容 MySQL 协议的服务器均可：MySQL、
MariaDB、TiDB、OceanBase、SelectDB 等）：`WithDatabase` 交给 App 托管连接池，
`WithDB` 注入已有连接。慢查询和失败查询默认只记录 SQL 模板（`?` / `$n` 占位符）
与耗时、行数、错误，不展开参数值；`log_parameter_values: true` 只用于本地调试。
配置项、默认值和日志行为见 [support/db/README.md](support/db/README.md)。

PostgreSQL 连接用 `support/pg`（GORM + `gorm.io/driver/postgres`，底层
`jackc/pgx/v5`）：API 与 `support/db` 对齐（`New`/`Close`、命名实例
`Configure`/`NewIO`），DSN 支持 URL 和 keyword/value 两种 pgx 形式，错误中的
密码段两种形式都会脱敏。配置与示例见
[support/pg/README.md](support/pg/README.md)。

SQLite 连接用 `support/sqlite`（GORM + `github.com/glebarez/sqlite`，纯 Go 的
modernc 实现，无 cgo）：API 与 `support/db`、`support/pg` 对齐；默认单连接
（SQLite 单写多读），WAL / `busy_timeout` 通过 DSN 的 `_pragma` 参数开启。
配置与示例见 [support/sqlite/README.md](support/sqlite/README.md)。

三个包共用同一套 SQL 日志行为：`log_level`、`slow_threshold` 与
`log_parameter_values` 语义一致，慢查询与错误日志同样只输出占位符模板。

Redis 连接用 `support/redis`；`support/cache` 和 `support/queues` 是在注入
客户端之上的缓存与队列门面。AMQP（RabbitMQ）用 `support/amqp`：支持多 host
回退、`Wrap` 注入已有连接、发布确认、手动 ack/nack、断线重连，以及基于
TTL + 死信交换机的延迟队列。配置与示例见
[support/amqp/README.md](support/amqp/README.md)。

Kafka 用 `support/kafka`（纯 Go 的 franz-go，无 cgo）：`New` 连接 seed brokers
并由 metadata 发现集群，`Wrap` 注入已有客户端；`Producer` 同步发布，同一个 Key
落到同一分区从而保序；`Consumer` 按消费组订阅，`CommitMode` 默认 `manual`：
只提交 handler 处理成功的记录，失败消息不提交、重启后重投（at-least-once）。
`CommitMode: "auto"` 让 franz-go 按 `AutoCommitInterval` 提交已拉取的位点，
可能丢掉失败消息，必须显式开启；`DisableAutoCommit` 已弃用并让位于 `CommitMode`。
earliest/latest 重置位点、rebalance 回调与 `ConsumeOptions.DiscardOnError` 跳过毒消息
仍然可用。配置与示例见 [support/kafka/README.md](support/kafka/README.md)。

RocketMQ 用 `support/rocketmq`（官方 apache/rocketmq-clients/golang v5，走 gRPC
proxy，默认 8081，不需要 sidecar）：`New` 启动 producer，可选配置
`StartupTopics` 做启动探测；`Client.Publish` / `PublishJSON` 同步发送，支持
Tag、业务 Key、自定义属性、绝对时间的延时消息和 FIFO 的 `MessageGroup`；
消费者用同一份 `Config` 调用 `NewConsumer` 创建，内部是拉取型的
SimpleConsumer（v5 没有 push consumer），支持 Tag / SQL92 过滤、逐条 ack 和
失败重投（at-least-once）。配置与示例见
[support/rocketmq/README.md](support/rocketmq/README.md)。

`support/db`、`support/pg`、`support/sqlite`、`support/redis`、`support/amqp`、
`support/kafka`、`support/rocketmq` 还支持命名
实例：启动时用 `Configure(map[string]Config)` 注册配置别名，之后任意位置用
`NewIO(ctx, "default")` 按需建连并缓存。首次建连在注册表锁外执行（不同别名
并行、同名合并），失败不缓存；每次 `Configure` 生成一代注册表，`Close` 只关闭
本代连接，因此热更新可以先启动新模块再关闭旧模块。包级 `NewIO` /
`CloseAllIO` 是单 App 的便捷入口，同一进程内多个 App 需要隔离时持有
`*Registry` 用 `reg.NewIO` / `reg.Close`：

```go
reg, err := redis.Configure(map[string]redis.Config{
    "default": {Addr: os.Getenv("REDIS_ADDR")},
})
if err != nil {
    return err
}
defer reg.Close()

client, err := redis.NewIO(ctx, "default")
```

追踪用 `support/otel`：`WithTelemetry` 是框架级接入，负责 Gin 中间件和 App
托管连接池；Redis、出站 HTTP、额外 GORM 连接用包内的埋点助手接入。配置与
隐私约定见 [support/otel/README.md](support/otel/README.md)。

对称加解密用 `support/encryption`：AES-128/256-CBC（HMAC-SHA256）与
AES-128/256-GCM（AEAD），密文格式与 swoolefy PHP Encryption 逐字节兼容，
`EncryptString`/`DecryptString` 可跨语言互解。用于保护配置、Token、Cookie
一类可逆数据，不用于密码哈希。见
[support/encryption/README.md](support/encryption/README.md)。

分布式自增 ID 用 `support/uuidint`：Lua 把 Redis `TIME` 的毫秒前缀与段内序号
拼成 `int64`（趋势递增、同毫秒冲突重试），与 swoolefy PHP Uuid 共用同一套
Lua 和 ID 布局。`New` 可用 `WithClient` 注入本代客户端，或用
`WithInstance("cache")` 走 `support/redis` 命名实例（每次取号经包级 `NewIO`
懒解析，适合单 App 脚本）；还支持备用连接降级、批量取号，以及可选的本地预取池
（`WithPool` 配合 `StartPool`/`StopPool`，降低 Redis QPS）。见
[support/uuidint/README.md](support/uuidint/README.md)。

```go
gen, err := uuidint.New("uuid-key", uuidint.WithInstance("cache"))
id, err := gen.Get(ctx)
ids, err := gen.GetBatch(ctx, 10)
```

Excel 导出/导入用 `support/phpoffice`：移植自 swoolefy PHP `PhpOffice\Excel`，
底层换成纯 Go 的 excelize（无扩展、无 cgo）。`Export` 支持声明式表头/列宽/
冻结/筛选与生命周期钩子，`ExportStream` 用 StreamWriter 流式写大数据；
`Read` 全量读取、`Import` 流式逐行处理。小数据用常规 API，大数据必须用流式
接口，注意内存占用与 excelize 要求的 Go 版本。导出文件名安全校验且绝不覆盖
已有文件。见 [support/phpoffice/README.md](support/phpoffice/README.md)。

图像处理用 `support/image`：移植自 swoolefy PHP `Image` 组件，基于标准库与
`golang.org/x/image`（纯 Go、无 GD/Imagick）。`Open`/`Create` 返回请求级
`Editor`，支持缩放（contain/cover/scale/stretch）、缩略图、裁剪、旋转、灰度、
图片/文字水印、命名预设与 jpeg/png/gif 编码；`max_pixels` 与读取白名单防止
超大图和路径穿越。见 [support/image/README.md](support/image/README.md)。

跨模块依赖只有一个实现时，在装配根（demo 的 `app/container`）构造实例、按对方
`innerapi` 的接口注入消费方即可。需要按名字索引或多份同类型实例时，
再用 `support/registry`：显式的「名字 → 实例」注册表，启动装配时登记
client/service，按名字取用（`Get[T]` 带类型检查）；空名字、nil 实例、重复注册
都直接报错，每次启动/热更新新建一份，不做包级全局。见
[support/registry/README.md](support/registry/README.md)。

持有代际资源（DB/Redis 连接等）的服务用 `support/generation` 保证一次请求只
使用同一代：入口（HTTP 中间件、脚本、守护进程）发布并固定当前代，共享代码只
调用 `WithContext(ctx)`；Reload 期间固定值优先，旧模块停止时用
`CompareAndSwap` 只撤回自己那一代。见
[support/generation/README.md](support/generation/README.md)。

## Nacos 配置中心

`support/nacos` 直接调用 Nacos Open API（不引入 SDK）：启动时把远端配置写成
`config.yaml`，运行期间通过长轮询保持同步。示例见
`examples/demo-service/main.go`；完整 API 见
[support/nacos/README.md](support/nacos/README.md)。

```go
// swoolefy 风格：连接信息与配置中心项分开；没有 nacos.yaml 时可用 nacos.FromEnv()
nx, err := nacos.Load("nacos.yaml", "application.yaml")
if err != nil {
    return err
}
if nx.Enabled {
    if _, err := nacos.Sync(ctx, nx); err != nil {
        log.Fatalf("sync nacos config: %v", err)
    }
}
cfg, err := gofyconfig.Load[Config](ctx, nx.File) // 之后照常加载
// ...
if nx.Enabled && nx.Watch {
    go nacos.Watch(ctx, nx, nacos.WithLogger(logger),
        nacos.WithOnChange(func(ctx context.Context, _ nacos.Result) error {
            cfg, err := gofyconfig.Load[Config](ctx, nx.File)
            if err != nil {
                return err
            }
            // 进程内热更新：监听端口不变，重建日志、数据库与模块
            return app.Reload(ctx, gofy.ReloadSpec{
                Env:             cfg.Env,
                Log:             &cfg.Log,
                Database:        &cfg.DB,
                ModuleFactories: factories,
            })
        }))
}
```

**两文件布局（与 swoolefy 一致）**

`nacos.yaml` 只放服务器连接：

```yaml
nacos:
  host: 127.0.0.1
  port: 8848
  username: ''
  password: ''
  namespace: dev          # 配置中心命名空间 ID；public 或空表示默认命名空间
```

`application.yaml` 放配置中心项和监听开关：

```yaml
nacos:
  service_config:
    data_id: gofy-service
    group: GO_GROUP
    tenant: ''            # 仅当 nacos.yaml 未配置 namespace 时生效
  enable_nacos_config_change_restart: true
  monitor_config_change:
    listener_timeout_ms: 30000
    failed_wait_ms: 3000
```

规则：

- `nacos.Load` 合并两个文件，优先级为 文件 > 环境变量 > 默认值；
- 该布局默认启用 Nacos（与 swoolefy 一样，文件存在即视为配置由远端托管），
  `nacos.enabled: false` 或 `NACOS_ENABLED=0` 可关闭；
- 命名空间优先级：`nacos.yaml` 的 `namespace`（或 `NACOS_NAMESPACE`）→
  `service_config.tenant`（或 `NACOS_TENANT`）→ public；
- `enable_nacos_config_change_restart` 对应 `Config.Watch`，控制启动后是否继续监听；
  配置变更后由回调调用 `App.Reload` 做进程内热更新，不再重启进程（键名沿用 swoolefy）；
- `monitor_config_change` 覆盖 `NACOS_LISTENER_TIMEOUT_MS` / `NACOS_LISTENER_FAILED_MS`；
- `nacos.yaml` 不存在时 `Load` 返回的错误满足 `errors.Is(err, fs.ErrNotExist)`，
  可回退到 `nacos.FromEnv()`（示例见 `examples/demo-service/main.go`）。

**单文件写法**

所有键也可以写进同一个 `nacos.yaml`（需显式 `enabled: true` 或 `NACOS_ENABLED=1`）：

```yaml
nacos:
  enabled: true
  addr: http://127.0.0.1:8848
  namespace: dev          # 命名空间 ID，public 或空表示默认命名空间
  data_id: gofy-service
  group: GO_GROUP
  file: config.yaml
```

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `NACOS_ENABLED` | 是否启用，`1`/`true` 启用 | `false`（两文件布局下默认 `true`） |
| `NACOS_ADDR` | 服务地址，优先级高于 `NACOS_HOST`/`NACOS_PORT` | `http://127.0.0.1:8848` |
| `NACOS_HOST` / `NACOS_PORT` | 服务地址与端口 | `127.0.0.1` / `8848` |
| `NACOS_USERNAME` / `NACOS_PASSWORD` | 开启鉴权的集群才需要 | 空 |
| `NACOS_NAMESPACE` | 命名空间 ID（不是显示名称） | 空（public） |
| `NACOS_TENANT` | 命名空间 ID 的兜底值，仅当 `NACOS_NAMESPACE` 为空时生效 | 空（public） |
| `NACOS_DATA_ID` / `NACOS_GROUP` | 配置项标识 | — / `DEFAULT_GROUP` |
| `NACOS_FILE` | 生成的本地配置文件 | `config.yaml` |
| `NACOS_TIMEOUT_MS` | 单次请求超时 | `10000` |
| `NACOS_LISTENER_TIMEOUT_MS` | 长轮询挂起时间 | `30000` |
| `NACOS_LISTENER_FAILED_MS` | 监听或应用失败后的重试间隔 | `3000` |

```bash
NACOS_ENABLED=1 NACOS_ADDR=http://127.0.0.1:8848 NACOS_NAMESPACE=dev \
NACOS_DATA_ID=gofy-service NACOS_GROUP=GO_GROUP go run . start
```

行为说明：

- 远端内容必须是合法 YAML，否则拒绝写入，本地文件保持不变；
- 文件通过临时文件 + rename 原子替换，权限 `0600`（可能包含密钥），建议加入 `.gitignore`；
- 内容未变化时不重写文件；监听失败按 `NACOS_LISTENER_FAILED_MS` 重试；
- 配置变更后的推荐处理是 `App.Reload`：在同一个进程里重建日志、数据库连接和模块，
  先发布新 handler，等旧代在途请求排空后再停止旧模块，监听 socket 保持不变；
- 需要在变更时自定义动作（或不使用 `App.Reload`）时用 `nacos.WithOnChange`；
  旧版本示例里的 `WithRestartOnChange` 会发送 SIGTERM，仍然保留但不再是推荐做法；
- 热更新范围有限：HTTP 地址与超时、pid 文件、`With*` 选项安装的全局中间件和
  responder 不会重建；
  通过 `WithModules` 注入的模块只重新注册、不重启；Nacos 配置里改了 host/port
  只会打警告，需要真正重启进程才会换监听地址；
- 启动时 Nacos 不可达或配置非法会让进程启动失败，与 swoolefy 的行为一致；
- 两文件布局是 demo-service 的默认方式：`nacos.yaml` 管连接，`application.yaml`
  管 `service_config` 和监听开关，详见上文的规则；
- 只在 `start` 路径调用 `Sync`，避免 `stop` / `version` 也依赖 Nacos
  （示例见 `examples/demo-service/main.go` 的 `isStartInvocation`）。

## Nacos 服务注册与发现

`support/nacos` 同时实现了 Naming API：`App` 在监听端口可用后把自身注册到 Nacos，
退出时注销。客户端侧（`gofy-adapter/client/nacos`）按服务名解析实例并
做负载均衡。

**服务注册**

`application.yaml` 沿用 swoolefy 的键：

```yaml
nacos:
  enable_nacos_register: true
  service_register:
    ip: ${POD_IP}          # NACOS_SERVICE_REGISTER_HOST → POD_IP → 自动探测
    service_name: demo-service
    namespace_id: dev
    group_name: swoolefy-group
    weight: 1
    heartbeat_interval: 5
    metadata:
      version: 1.0.0
  discovery_service_client:
    load_balancer: random  # random | round_robin | weight
    cache_ttl: 60
    stale_ttl: 180
    healthy_only: true
    namespace_id: dev
    group_name: swoolefy-group
```

把合并后的配置交给 `App`：

```go
nx, err := nacos.Load("nacos.yaml", "application.yaml")
if err != nil {
    return err
}
app, err := gofy.New(
    gofy.WithHTTP(httpCfg),
    gofy.WithNacosRegistration(nx), // 端口默认取实际 HTTP 监听端口
    // ...
)
```

行为说明：

- 监听端口开始接受连接后才注册，避免流量打到尚未就绪的实例；端口优先级为
  `NACOS_SERVICE_REGISTER_PORT` > `service_register.port` > 实际监听端口；
- 实例固定为临时实例（ephemeral），按 `heartbeat_interval` 发送心跳，Nacos 可在
  心跳响应里调整间隔（限制在 1s–30s）；连续失败后先补一次完整心跳，仍失败则重新注册；
- `App.Shutdown` / `stop` 先注销实例再关闭监听，注销失败只记录警告；
- 注册对象挂在 `App` 上，`App.Reload` 不会重复注册或注销，热更新期间服务发现保持连续；
- `WithNacosRegistration` 在 `service_register.service_name` 为空时直接返回错误。

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `NACOS_SERVICE_REGISTER_HOST` | 注册 IP，优先级最高 | 空 |
| `POD_IP` | Kubernetes Downward API 提供的 Pod IP | 空 |
| `NACOS_SERVICE_REGISTER_PORT` | 注册端口 | 实际监听端口 |
| `NACOS_SERVICE_NAMESPACE_ID` / `NACOS_SERVICE_GROUP_NAME` | 注册命名空间 / 分组 | 连接命名空间 / `DEFAULT_GROUP` |
| `NACOS_DISCOVERY_LOAD_BALANCER` | 客户端负载均衡策略 | `random` |
| `NACOS_DISCOVERY_CACHE_TTL` / `NACOS_DISCOVERY_STALE_TTL` | 实例缓存时间（秒） | `60` / `max(3×cache, 180)` |
| `NACOS_DISCOVERY_HEALTHY_ONLY` | 是否只返回健康实例 | `true` |
| `NACOS_DISCOVERY_CLUSTERS` | 只发现指定集群，逗号分隔 | 空（全部） |

**客户端服务发现（gofy-adapter）**

`client/nacos.Resolver` 实现了 `client.Resolver`：按服务名调用 `instance/list`，
在本地缓存实例列表；Nacos 不可达时在 `stale_ttl` 内继续使用最后一次成功的结果；
请求失败会清空缓存，重试时重新解析，从而切换到其他实例。

```go
import (
    "github.com/yourorg/interface-service/appinterface/demoservice/demoserviceclient"
    "github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
    resolvernacos "github.com/bingcool/gofy-adapter/client/nacos"
)

resolver, err := resolvernacos.NewFromFiles("nacos.yaml", "application.yaml")
if err != nil {
    return err
}
orders, err := demoserviceclient.NewWithResolver(resolver) // serviceName = demo-service
if err != nil {
    return err
}
res, err := orders.GetOrder(ctx, &orderrequest.GetOrderRequest{ID: 1})
```

`demoserviceclient.NewWithResolver` 固定使用 `demoservice.ServiceName`（`demo-service`）。
需要自定义客户端配置时用
`client.New(client.Config{Service: demoservice.ServiceName, Resolver: resolver})`
再交给 `demoserviceclient.New`。

不使用 Nacos 时用固定地址（域名或 IP，省略 scheme 默认 http）：

```go
orders, err := demoserviceclient.NewWithBaseURL("https://orders.example.com")
```

## CLI

| 命令 | 行为 |
| --- | --- |
| `./service` / `./service start` | 前台运行，直到收到 SIGINT/SIGTERM |
| `./service start --daemon=1` | 派生后台子进程后父进程退出，日志与标准输出重定向到 `/dev/null`；Windows 不支持，启动时直接 panic |
| `./service stop [--pid-file path] [--timeout 10s] [--force=1]` | 发送 SIGTERM，超时后可发送 SIGKILL；默认要求输入 `yes` 确认 |
| `./service restart [--force=1]` | 先停止 pid 文件对应的实例（如有），再前台启动；默认要求输入 `yes` 确认 |
| `./service script <name> [flags]` | 执行维护脚本 |
| `./service script --list` | 列出已注册脚本 |
| `./service version [--json]` | 输出构建信息 |
| `./service <name>` | 通过 `WithCommands` 注册的自定义命令 |

退出码：`0` 成功，`1` 运行错误，`2` 用法错误。

`start`/`stop`/`restart` 会在终端输出彩色的状态信息和启动信息表格（非 TTY 或设置了 `NO_COLOR` 时自动关闭颜色）。`stop`/`restart` 默认要求输入 `yes` 才继续；非交互环境（systemd、容器、CI）请显式传入 `--force=1`，否则 stdin 读到 EOF 会按放弃处理。

## 测试

`gofytest` 在进程内构建 handler，不监听端口、不写 pid 文件、不连接真实数据库：

```go
app := gofytest.New(t, gofy.WithModules(orderModule{}))
rr := app.GET("/api/v1/order/orders/1")
gofytest.Status(t, rr, 200)
```

运行框架测试：

```bash
make check     # fmt-check + vet + test-race + example + guards + cgo-free
make test-race
make example   # 构建、vet 并测试 examples/demo-service
make guards    # import 守卫 + 公开包禁止 func init()
make cgo-free  # CGO_ENABLED=0 构建，并校验依赖图无 cgo 包
```

全仓库不使用 cgo：SQLite 用 `glebarez/sqlite`（modernc 纯 Go 实现），MySQL、
PostgreSQL、Redis、AMQP、Kafka、RocketMQ 客户端也都是纯 Go。`make guards`
检查导入规则与公开包里没有 `func init()`，`make cgo-free` 检查依赖图中没有
`CgoFiles`；需要 cgo 的 `-race` 只在 `test-race` 中启用。仓库不包含
GitHub Actions，`make check` 是唯一门禁。

`examples/demo-service` 通过 `replace` 指向本地的 `gofy`、`gofy-interface-api`、
`gofy-adapter` 与 `examples/interface-service`（见 `examples/demo-service/go.mod`）；
本地需要把 `gofy-interface-api` 与 `gofy-adapter` 两个仓库放在 `gofy` 的同级目录。
其中 `gofy-interface-api` 是框架无关的纯契约库（不含 gofy / gin），gofy 适配层由
`gofy-adapter` 单独提供，两者互不依赖。

MySQL 集成测试默认跳过，需显式开启：

```bash
GOFY_TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
  go test ./support/db -run Integration -v
```

PostgreSQL 集成测试同样默认跳过：

```bash
GOFY_TEST_PG_DSN='postgres://user:pass@127.0.0.1:5432/app?sslmode=disable' \
  go test ./support/pg -run Integration -v
```

## 目录结构

```text
gofy.go, option.go                          App 构造与选项
start.go, execute.go                        启动、脚本执行与命令入口
generation.go, reload.go                    代际切换、请求租约与热更新
stop.go, restart.go, daemon.go, signal.go   停止、重启、后台化与信号
config.go, errors.go, ui.go                 共享配置类型、退出码与终端输出
apperr/        业务错误模型
core/          共享 Runtime（叶子包）
core/systemenv/ 环境、系统与 goroutine 判定
support/       Router、类型化 handler、Responder、CORS/JWT/限流/安全头配置
logger/       zap 日志构建（主/system/access）、滚动策略与 GORM 适配
support/middleware/ 请求 ID、访问日志、恢复、CORS、限流、JWT、安全头、透传、契约别名注册表
support/registry/ 通用 name -> instance 注册表（可选；单实现由 app/container 注入）
support/generation/ 当前一代句柄的原子发布与请求级固定（跨 Reload 不换资源）
support/db/    GORM MySQL 协议连接管理（MySQL/MariaDB/TiDB/OceanBase/SelectDB）
support/pg/    GORM PostgreSQL 连接管理（gorm.io/driver/postgres + jackc/pgx/v5）
support/sqlite/ GORM SQLite 连接管理（glebarez/sqlite，纯 Go 无 cgo）
support/redis/ Redis 客户端（go-redis）
support/nacos/ Nacos 配置拉取、本地生成、变更监听与服务注册发现
support/jwtauth/ HMAC JWT 校验
support/headerpropagation/ 入口请求头白名单与服务间透传上下文
support/frameworkcontext/ 统一身份读取（本地认证优先，透传头兜底）
support/ratelimit/ 滑动窗口限流接口与内存实现
support/ratelimit/redisstore/  Redis 共享限流存储
support/cache/  基于注入 Redis 实例的键值缓存门面
support/queues/ 基于注入 Redis 实例的即时队列与延迟队列
support/amqp/   AMQP（RabbitMQ）连接、发布、消费与 TTL 延迟队列
support/kafka/  Kafka 连接、同步生产与消费组消费（franz-go，手动提交位点）
support/rocketmq/  RocketMQ 连接、同步生产与拉取消费（官方 v5 gRPC 客户端）
support/otel/   OpenTelemetry 追踪配置、OTLP 导出与 Gin/HTTP/GORM/Redis 埋点
support/encryption/  AES-CBC/GCM 对称加解密，与 swoolefy PHP 密文互通
support/uuidint/  基于 Redis 的分布式自增 int64 ID（与 PHP Uuid 布局互通）
support/phpoffice/ Excel .xlsx 导出/导入（excelize，纯 Go）
support/image/   图像缩放/裁剪/旋转/水印/编码（纯 Go，无 GD/Imagick）
script/        维护脚本描述
command/       自定义 CLI 命令描述
config/        可选 YAML 加载器
gofytest/      HTTP 测试辅助
internal/      pid 文件、进程控制、版本、cobra 装配
cmd/gofy/      脚手架 CLI（不被库本身 import）
examples/demo-service/  完整示例服务（独立 Go module）
docs/          设计与迁移文档
```

公共包不调用 `init()`、不保留包级可变状态、不调用 `os.Exit` 或 `panic`。
App 持有 logger、DB 和 HTTP server，并在关停时释放。
