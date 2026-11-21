# support/health

`support/health` 是 gofy 的探针注册表：`/healthz`、`/readyz` 与诊断端口上的 `/health` 背后的检查执行、缓存与结果聚合都在这里。

它只依赖标准库与 `gorm.io/gorm`（数据库检查器用），不依赖根包、不引入 logger，因此可以单独测试，也能在没有 `gofy.App` 的场景里直接使用。

## 一、三类语义

| 端点 | 回答的问题 | 失败的影响 |
| --- | --- | --- |
| `GET /healthz` | 进程活着、监听没有关闭 | 与依赖无关；摘流期间仍然是 200（准入闸门按精确路径豁免） |
| `GET /readyz` | 所有 readiness 检查是否通过 | 503，负载均衡不再派新流量 |
| `GET /health`（诊断端口） | 每个检查的明细，含 readiness 与 diagnostic | 始终 200，`status` 字段说明当前是否可用 |

`ClassReadiness` 的失败会让 `/readyz` 返回 503；`ClassDiagnostic` 只进明细，永不摘流。

诊断 `/health` 的响应体固定是这个形状：

```json
{
  "status": "ok",
  "ready": true,
  "checked_at": "2026-11-21T13:30:00.123456789Z",
  "reason": "",
  "checks": [
    {"name": "database", "class": "readiness", "status": "healthy", "latency_ms": 3},
    {"name": "cache", "class": "diagnostic", "status": "unknown", "error": "redis ping: i/o timeout", "reason": "timeout", "latency_ms": 1000}
  ]
}
```

`status` 是给人看的一句话（`ok` / `unavailable`），机器判断用布尔 `ready`；`checks` 里同时有 readiness 与 diagnostic 两类检查，只有失败项才带 `error` 与 `reason`。`reason` 只在 `ready` 不是由检查结果算出时出现（`grace`、`canceled`）。

## 二、快速开始

```go
import "github.com/bingcool/gofy/support/health"

reg := health.New(health.Options{
	Timeout:       2 * time.Second,
	TotalTimeout:  3 * time.Second,
	Concurrency:   4,
	ReadinessTTL:  time.Second,
	DiagnosticTTL: 2 * time.Second,
})

// 关系型数据库：复用 PingContext，与旧 /readyz 的行为一致
_ = reg.Register(health.NewDB("database", gormDB))

// 下游 HTTP：默认 GET，2xx/3xx 视为健康
probe, err := health.NewHTTP("inventory", health.HTTPConfig{URL: "http://inventory.internal/healthz"})
if err != nil {
	return err
}
_ = reg.Register(probe)

// Redis：传一个 ping 函数，包本身不绑定任何客户端
pinger, err := health.NewRedis("cache", func(ctx context.Context) error {
	return rdb.Ping(ctx).Err()
})
if err != nil {
	return err
}
_ = reg.Register(pinger, health.WithClass(health.ClassDiagnostic))

// 任意函数
_ = reg.Register(health.Named("smtp", func(ctx context.Context) error {
	return smtp.Check(ctx)
}), health.WithCheckTimeout(500*time.Millisecond))
```

示例用 `_ =` 省掉了错误处理；生产装配应当把 `Register` 的错误返回出去，让非法注册在启动期就失败，而不是等到第一次探针。

读取结果：

```go
snap := reg.Ready(ctx)
if !snap.Ready {
	// Failed() 按注册顺序返回未通过的 readiness 检查名，可直接进日志
	log.Warn("not ready", zap.Strings("checks", snap.Failed()))
}

detail := reg.Diagnose(ctx) // 明细包含 readiness 检查，运维只需读一个端点
for _, r := range detail.Results {
	log.Debug("check", zap.String("name", r.Name), zap.String("status", string(r.Status)))
}
```

## 三、默认值

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `Timeout` | 2s | 单检查超时；注册时的 `WithCheckTimeout` 与检查器自带的 `TimeoutProvider` 优先 |
| `TotalTimeout` | 3s | 一整轮的预算；到期时未完成的检查记 `reason=budget` |
| `Concurrency` | 4 | 同时执行的检查数；抢不到槽位的检查不排队，直接记 budget |
| `ReadinessTTL` | 1s | readiness 快照缓存；把同一副本的重复探测合并成一轮 |
| `DiagnosticTTL` | 2s | 诊断快照缓存；明细允许比接活结论稍旧 |
| `StartupGrace` | 0 | 创建后的宽限期内 readiness 直接为真 |

几个非直觉的约定：

- **零值取默认，负值表示关闭**：`ReadinessTTL`、`DiagnosticTTL` 设为负数会关掉缓存（每次探测都跑一轮），`0` 则回落到默认值。
- **预算与并发是绑定的**：`4 × 2s = 8s > 3s`，默认配置只保证不超过 4 个 readiness 检查能在一轮内跑完。使用 gofy 框架时，自动注册的 `database` 检查也占一个槽位，因此默认最多再加 3 个 readiness 检查。调小并发或调大单检查超时，必须在同一个改动里复核总预算。
- **`StartupGrace` 只影响 readiness**：宽限期内的快照 `Ready=true`、`Reason=grace`、`Results` 为空，`/health` 的明细不受影响。

## 四、执行模型

- **缓存**：TTL 内直接复用快照；过期才触发一轮。
- **singleflight**：同一类别下同时到达的探针共享一轮，100 个并发探针只调用一次检查函数。
- **并发与超时**：一轮用信号量限制并发；每个检查有独立的 `context.WithTimeout`，超时为注册值 → `TimeoutProvider` → `Options.Timeout` 依次回退。
- **预算独立于调用方**：整轮预算挂在 `context.Background()` 派生的 context 上。探针超时放弃只是不再等待，已经发出的检查不会被取消，结果仍写入缓存供后续探针复用。
- **panic 隔离**：检查函数被 `recover` 包住，只让该检查失败（`reason=panic`），不中断整轮。
- **调用方取消**：等待快照期间 `ctx` 被取消时返回 `Ready=false`、`Reason=canceled`（fail-closed）。

状态与原因的组合规则：

| 情况 | readiness 检查 | diagnostic 检查 |
| --- | --- | --- |
| 返回 `nil` | `healthy` | `healthy` |
| 返回错误 | `failed` | `unknown` |
| 单检查超时 | `failed` + `reason=timeout` | `unknown` + `reason=timeout` |
| panic | `failed` + `reason=panic` | `unknown` + `reason=panic` |
| 预算耗尽 | `failed` + `reason=budget` | `unknown` + `reason=budget` |

`Result` 的字段：`Name`、`Class`、`Status`、`Error`、`Reason`、`Latency`、`CheckedAt`。`Latency` 量的是检查函数自身的耗时，从拿到并发槽位起算，排队等槽位的时间不计入；`CheckedAt` 是检查真正开始的时刻。

### 注册表自身的 API

| 方法 | 用途 |
| --- | --- |
| `New(Options) *Registry` | 构造空注册表，再用 `Register` 逐个加入 |
| `Register(Checker, ...CheckOption) error` | 加入检查；非法注册在启动期失败，不会变成探针里的意外 |
| `Ready(ctx) Snapshot` | readiness 结论，探针处理器只需要这一个方法 |
| `Diagnose(ctx) Snapshot` | readiness + diagnostic 明细，供 `/health` 使用 |
| `Entries() []Entry` | 已注册检查的副本（`Name` / `Class` / 生效 `Timeout`），适合打进启动日志 |
| `Options() Options` | 应用默认值之后真正生效的配置，同样适合启动日志 |
| `Invalidate()` | 丢掉两个类别的快照；状态翻转后让下一次探测重新算，而不是复用之前的结论 |

## 五、内置检查器

| 构造器 | 依赖 | 行为 |
| --- | --- | --- |
| `NewDB(name, *gorm.DB)` | `gorm.io/gorm` | 调 `PingContext`；默认 1s 超时（`DefaultPoolTimeout`） |
| `NewHTTP(name, HTTPConfig)` | 标准库 | 默认 GET、2xx/3xx 健康；URL 非 http/https 时构造期报错 |
| `NewRedis(name, RedisPingFunc)` | 无（只收函数） | 调传入的 ping；nil 函数在构造期报错 |

几个实现细节：

- HTTP 检查器的错误里不放完整 URL：`url.Error` 只保留 `Op` 与底层错误；scheme 不是 http/https 时用 `safeURL` 渲染目标地址，去掉 query、fragment 与 userinfo，避免把凭据写进日志或 `/health` 明细。
- 三者都实现 `TimeoutProvider`，默认 1 秒（HTTP 用 `HTTPConfig.Timeout`，零值同样回落到 1 秒）——连接池要么一秒内应答，要么就是不可用。`WithCheckTimeout` 优先于检查器自报的值，也优先于 `Options.Timeout`。
- `health.Named` 包装的函数**不**实现 `TimeoutProvider`：自定义检查只能靠 `WithCheckTimeout` 或 `Options.Timeout` 定预算。

## 六、与框架的集成

| 选项 | 作用 |
| --- | --- |
| `WithHealthEndpoints(true)` | 在业务端口注册 `/healthz`、`/readyz` |
| `WithHealthChecks(...)` | 注册 readiness 检查（默认类别） |
| `WithDiagnosticChecks(...)` | 注册 diagnostic 检查，进明细但不摘流 |
| `WithHealthCheckOptions(health.Options{...})` | 覆盖本节的默认值 |
| `WithDatabaseHealthCheck(false)` | 关掉自动注册的 `database` 检查 |
| `WithWorkerHealth(addr)` | 在 worker 进程的 `addr` 上暴露 `/healthz` 与 `/readyz`；不设置就没有探针监听 |
| `WithWorkerHealthChecks(...)` | 为 worker 进程的 `/readyz` 注册检查 |

框架行为：

- 存在连接池时自动注册名为 `database` 的 readiness 检查，保持旧 `/readyz` 的结论与响应体。
- `/readyz` 在进程 markReady 之前直接回 503，且不跑检查：listener 已经能接受连接，但 Nacos 注册还没完成，此时把每个依赖 ping 一遍既不真实也浪费。
- Reload 会重建注册表，检查不会去 ping 已经被替换掉的连接池。
- worker 进程的 `/healthz` 只表示存活（drain 期间仍 200），`/readyz` 要求 `state == ready` 且本包的检查通过。`worker_host` 会自动注册名为 `group` 的 readiness 检查，并在分组建连完成时调用 `Invalidate()`，把之前缓存的 503 丢掉。
- 诊断端点的 `/health` 由 `diagnostics.Config` 的 `DisableHealth`、`HealthPath` 控制；只开 health 的监听是合法的，三个端点全关才会报 `ErrNothingEnabled`。

## 七、边界与注意事项

- **没有检查就认为就绪**：空注册表 `Ready()` 为 `true`。开启端点却没注册检查不会摘流，但也没有任何保护。
- **注册即表态**：默认类别是 readiness——放进去就意味着"这个依赖不可用就别接活"。报表库、搜索集群这类非关键依赖请用 `WithClass(health.ClassDiagnostic)` 或 `WithDiagnosticChecks`。
- **`Register` 会拒绝**：`nil` 检查器、空名字、重名、未知类别、负超时（包括 `TimeoutProvider` 返回负值）。这些都会在启动期失败，而不是在探针里暴露。
- **检查函数要快速失败**：不要在检查里做重试或重连——重试会把本轮预算吃光，让其他依赖拿不到结论。
- **`Named` 传 nil 函数等于恒健康**：`funcChecker.Check` 在函数为 nil 时返回 nil，`Register` 拦不住它，只能靠审查。
- **`WithWeight` 目前只记录**，不参与聚合；它是为将来的加权判定预留的。

## 八、测试

```bash
go test ./support/health/...
go test -race ./support/health/...
```

用例覆盖：空注册表、非法注册、readiness fail-closed、diagnostic 不摘流、TTL 缓存、100 并发单飞、panic 隔离、单检查超时原因、预算截断、类别映射、HTTP 检查器的 URL 脱敏，以及 `TimeoutProvider` 返回负值的拒绝。