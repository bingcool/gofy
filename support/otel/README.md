# support/otel

生产可用的 OpenTelemetry 追踪接入：带校验的配置、OTLP 导出器、批处理 span
处理器、W3C TraceContext + Baggage 传播，以及 Gin、`net/http`、GORM、Redis
的埋点助手。包本身不持有全局状态，除非显式设置 `Config.Global`；调用方拿到
`*Provider` 并自己决定何时关闭。

`App` 通过 `gofy.WithTelemetry` 使用这一层：启用后自动安装 Gin 服务端 span
中间件、为 App 托管的 MySQL 连接池埋点，并在关停时 flush 导出器。

## 在 App 中使用

```go
import (
    "time"

    "github.com/bingcool/gofy"
)

app, err := gofy.New(
    gofy.WithName("order-service"),
    gofy.WithTelemetry(gofy.TelemetryConfig{
        Enabled:     true,
        Endpoint:    "otel-collector:4317",
        Protocol:    "grpc",
        Compression: "gzip",
        SampleRatio: 0.1,
        Timeout:     10 * time.Second,
    }),
)
```

`gofy.WithTelemetry` 在 `New` 时校验配置，非法值直接返回错误。启用后框架负责：

- 在 `RequestID` 之后、`HeaderPropagation` 之前安装 `otelgin` 中间件，
  提取上游 `traceparent` 并创建服务端 span；`/healthz`、`/readyz` 不采样，
  避免探针刷满 span 流；
- 对 `WithDatabase` 创建的连接池安装 GORM 插件；`WithDB` 注入的外部连接
  归调用方所有，框架不动；
- `Runtime.TracerProvider`、`Runtime.Propagator` 和 `Runtime.Tracing` 可被业务
  读取，用于给额外组件（如 Redis、下游 HTTP 客户端）埋点；
- 关停时先停模块、再 flush 并关闭导出器；导出器不可达只记录日志，不会让
  进程退出失败。

服务名、版本和环境默认取 `WithName`、构建版本和 `Env`，配置中的
`service_name`、`service_version`、`environment` 非空时以配置为准。

## 配置

```yaml
otel:
  enabled: true
  endpoint: otel-collector:4317   # 带 scheme 时按 URL 解析，否则视为 host:port
  protocol: grpc                  # grpc | http
  insecure: false                 # true 关闭 TLS，仅限内网 collector
  compression: gzip               # gzip | none
  timeout: 10s                    # 单次导出超时
  sampler: parentbased_traceidratio
  sample_ratio: 0.1               # 根 trace 采样比例，0 表示默认值 1.0
  service_name: order-service
  service_version: 1.4.0
  environment: prod
  attributes:                     # 额外资源属性
    team: commerce
  global: false                   # true 时同时安装进程级 provider/propagator
  batch:
    timeout: 5s
    max_queue_size: 2048
    max_export_batch_size: 512
    export_timeout: 30s
  tls:                            # insecure 为 false 时生效
    ca_file: /etc/ssl/otel-ca.pem
    cert_file: ''                 # 双向 TLS：cert 与 key 必须同时设置
    key_file: ''
    server_name: otel-collector
    insecure_skip_verify: false   # 仅开发环境使用
  shutdown_timeout: 5s
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `Enabled` | 总开关；关闭时不建导出器、不装中间件 | `false` |
| `Endpoint` | OTLP collector 地址 | `localhost:4317`（gRPC）/ `localhost:4318`（HTTP） |
| `Protocol` | `grpc` 或 `http` | `grpc` |
| `Insecure` | 关闭 TLS | `false` |
| `Headers` | 每次导出附带的头，如 API token；值不会写日志 | 空 |
| `Compression` | `gzip` 或 `none` | `gzip` |
| `Timeout` | 单次导出超时 | `10s` |
| `Sampler` | `parentbased_traceidratio`、`parentbased_always_on`、`parentbased_always_off`、`traceidratio`、`always_on`、`always_off` | `parentbased_traceidratio` |
| `SampleRatio` | 根 trace 采样比例，取值 `[0,1]`；`0` 按默认 `1.0` 处理 | `1.0` |
| `ServiceName` | `service.name` | App 名称，再缺省为 `unknown_service` |
| `ServiceVersion` | `service.version` | App 构建版本 |
| `Environment` | `deployment.environment.name` | App 环境 |
| `Attributes` | 额外资源属性 | 空 |
| `Global` | 安装进程级 provider/propagator | `false` |
| `Batch` | 批处理参数 | 见上表 |
| `TLS` | 导出连接 TLS | 系统根证书，最低 TLS 1.2 |
| `ShutdownTimeout` | 关停时最终 flush 的上限 | `5s` |

`Validate()` 在建立连接前检查，不读证书文件、不发起网络请求：协议、压缩、采样器
枚举合法；`SampleRatio` 在 `[0,1]`；带 scheme 的 endpoint 只能是 `http`/`https`
且有 host；header 名非空；时长和批量参数不为负；`max_export_batch_size` 不超过
`max_queue_size`；`cert_file` 与 `key_file` 必须成对；`insecure` 不能与任何
TLS 设置同时出现。

零值表示使用默认值。`New` 会先 `Validate` 再补齐默认值，因此
`Config{Enabled: true}` 就是一套完整的本地配置。

## 独立使用

```go
import (
    "context"

    "github.com/bingcool/gofy/support/otel"
)

provider, err := otel.New(ctx, otel.Config{
    Enabled:  true,
    Endpoint: "otel-collector:4317",
}, otel.WithLogger(logger), otel.WithServiceName("order-service"))
if err != nil {
    return err
}
defer provider.Shutdown(ctx)
```

可选参数：

| Option | 作用 |
| --- | --- |
| `WithLogger` | 导出失败、关停失败的日志出口，缺省为 no-op |
| `WithServiceName` | `Config.ServiceName` 为空时的服务名 |
| `WithServiceVersion` | `Config.ServiceVersion` 为空时的版本 |
| `WithEnvironment` | `Config.Environment` 为空时的环境 |
| `WithSpanExporter` | 替换 OTLP 导出器，用于测试或导出到其他后端 |

`Provider` 的方法：`Enabled()`、`ServiceName()`、`TracerProvider()`、
`Propagator()`、`Tracer(name)`、`Config()`、`ForceFlush(ctx)`、`Shutdown(ctx)`。
`TracerProvider()` 和 `Propagator()` 永远非 nil；禁用时返回 no-op tracer 和
W3C 组合传播器，调用方可以直接使用，不必分支。

## 埋点助手

### Gin

```go
middleware := provider.GinMiddleware(otel.SkipPaths("/healthz", "/readyz", "/metrics"))
router.Use(middleware) // 禁用时为 nil
```

中间件从请求头提取 `traceparent`，调用方的 trace 会延续到本服务；`filter`
返回 `false` 的请求不建 span，也不参与导出。App 已自动安装，独立使用时才需要
手工挂载。

### 出站 HTTP

```go
client := provider.HTTPClient(&http.Client{Timeout: 3 * time.Second})
resp, err := client.Get(url) // 自动注入 traceparent，并创建 client span
```

`HTTPTransport(base)` 适合注入到已有 `http.Transport` 或第三方 SDK；
`HTTPClient(base)` 保留 `Timeout`、`CheckRedirect` 等字段并复制客户端。
禁用时原样返回传入值。

### GORM

```go
db.Use(otel.NewGORMPlugin(provider.TracerProvider()))
// 或对 App 托管的连接池：
err := provider.InstrumentGORM(db)
```

为 create、query、update、delete、row、raw 六类操作建立 client span：
`db.system`（dialector 名）、`db.operation`、`db.sql.table`；结束时补
`db.statement`（SQL 模板，最多 4096 字符）、`db.rows_affected`，除
`ErrRecordNotFound` 外的错误记录为 span 状态。绑定参数不写入 span。

同一个 `*gorm.DB` 重复安装会返回 GORM 的重复回调错误；禁用埋点时
`InstrumentGORM` 是 no-op。

### Redis

```go
err := provider.InstrumentRedis(rdb)
```

基于 `redisotel` 为每条命令建立 client span。命令参数与值不记录
（`WithDBStatement(false)`）：缓存 key 常带用户或租户标识，值可能是密钥。
持有 `Runtime` 而非 `Provider` 时可使用
`otel.InstrumentRedisWith(rdb, rt.TracerProvider)`。

### 日志关联

```go
logger := otel.Logger(ctx, baseLogger) // 附带 trace_id / span_id
traceID, spanID := otel.TraceIDs(ctx)
fields := otel.TraceFields(ctx)
```

context 中没有有效 span 时返回原 logger / 空字段，不会给日志增加空字段。
框架的访问日志和 panic 恢复日志已经输出 `trace_id`、`span_id`；启用 OTel 时
`X-Trace-Id` 优先使用 OTel trace id，否则回落到 RequestID 生成的 UUID。

## 采样与传播

- 采样默认是 `parentbased_traceidratio`：上游已决定采样的 trace 一定继续采样，
  根 trace 按 `SampleRatio` 抽样。生产建议从 `0.05`～`0.2` 起步，高流量服务
  再结合尾部采样 collector。
- `sample_ratio: 0` 表示默认值 `1.0`；要关闭采样请显式使用
  `sampler: always_off` 或 `parentbased_always_off`。
- 传播器固定为 W3C `traceparent` + `baggage`，与主流 collector、服务网格兼容；
  不注入 B3，避免同一请求出现两套 trace id。

## 生命周期、错误与全局状态

- `New` 不连接 collector；导出失败由 SDK 在后台重试。OTel 内部错误转到
  配置的 logger，并对相同错误做 1 分钟限流（附带 `suppressed` 计数），
  避免 collector 不可达时刷日志。
- `Shutdown` 在 `ShutdownTimeout` 内 flush 并释放导出器，可重复调用；
  `ForceFlush` 用于测试或关停前的主动上报。
- `Config.Global` 为 true 时把 provider 和 propagator 安装为进程级默认值，
  供未显式接线的第三方库使用。一个进程只承载一个 App 时才可以打开；
  `Shutdown` 不会反向清除全局 provider，因为其他组件可能仍持有引用。
- `Headers` 中的值（如 collector token）只随导出发送，不写日志、不出现在
  span 属性里。

## 热更新

追踪器的配置在启动时固定，`App.Reload` 复用同一个 provider 和导出器：
替换 logger、数据库或模块不会重建 span 管道，修改注册的 telemetry 配置需要
重启进程。`Reload` 创建的 `Runtime` 仍然携带同一个 `TracerProvider`、
`Propagator` 和 `Tracing` 值。

## 安全默认值

- 默认开启 TLS（`insecure: false`），最低 TLS 1.2；内网 collector 可显式
  `insecure: true`，此时不允许再配置任何 TCP/TLS 相关字段；
- `insecure_skip_verify` 只应在开发环境使用；
- 请求头、SQL 绑定参数、Redis 命令参数都不会进入 span；
- `db.statement` 只保存 SQL 模板并截断，不会因为一条超大语句撑爆导出批次。

## 测试

```bash
go test ./support/otel/
```

单元测试用 `tracetest.NewInMemoryExporter` 注入导出器，不依赖 collector；
覆盖默认值、配置校验、资源属性、Gin 中间件提取与路径过滤、出站
`traceparent` 注入、GORM DryRun span、Redis span 及日志字段。需要验证真实
collector 时，把 `endpoint` 指向本地 OTel Collector 再运行 demo-service。
