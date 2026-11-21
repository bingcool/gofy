# gofy 健康检查与 Worker/MQ 指标设计方案

> 基线：`github.com/bingcool/gofy`，HEAD `b33e823`（审计 P2/P3 修复后的提交）。
> 本文是设计文档，**不包含代码改动**；接口签名只表达形状，不代表可直接编译。
> 目标是把两项能力设计到能直接排期实现的程度：语义边界、API、接入点、默认值、验收标准。

## 0. 结论：方向合理，但需要 6 处修正（另有 8 处实现前必须补齐，见 §0.1）

两项能力都是真实缺口，优先级判断成立。但用户描述里有几处假设与 gofy 的进程模型不符，
如果照原样实现，会把「依赖抖动」放大成「全站摘流」。修正如下：

| 原提案 | 判断 | 修正 |
|---|---|---|
| 所有依赖统一纳入 `/readyz` | 方向对，范围过宽 | gofy 的 HTTP 与 worker 是**两个进程**；Kafka 消费连接属于 worker 进程，不能影响 HTTP `/readyz`。健康注册表必须按进程装配 |
| 依赖详情放在业务端口 | 不合理 | 明细与错误放 diagnostics 监听（默认 `127.0.0.1:6060`），业务端口只返回状态码与聚合状态，避免暴露拓扑与内部错误 |
| `/healthz` 只表示进程存活 | 对，但当前实现没做到 | 准入闸门对**所有路径**生效（`generation.go:221-241`），摘流时 `/healthz` 也会 503。需要为 liveness 提供精确路径豁免 |
| 检查项可按业务选择 | 对，但缺默认值 | 每条检查必须显式声明是否影响 readiness；框架 DB 检查默认影响（保持现状），用户检查默认 readiness、可用 `DiagnosticOnly` 退出 |
| 每次探针实时 Ping 所有依赖 | 不可行 | 需要 TTL 缓存 + singleflight + 并发上限 + 单检查超时 + 总预算；否则 N 副本 × M 依赖 × 探针频率会把健康检查本身变成故障源 |
| 指标直接加在 MQ 适配器 | 会漏也会重 | 计数落在 `Processor.Handle` 的分支（收到/重复/in-flight/解码失败/永久失败/可重试/成功），ack/nack 失败只在 broker 适配器计一次 |

另外两个事实需要先说明，它们决定了实施顺序：

1. **健康端点默认关闭**：`defaultOptions()` 不设置 `health`，只有 `WithHealthEndpoints(true)` 才注册
   `/healthz`、`/readyz`（`start.go:428`、`option.go:777-779`）。
2. **指标管道与诊断监听耦合**：`buildMetrics` 只在 `diagnostics.Enabled && !DisableMetrics` 时构建
   （`gofy.go:487-495`），worker 进程根本不构建指标。因此 worker/MQ 指标的第一步是解耦这两件事，
   而不是直接加指标。

### 0.1 代码复核（2026-11-21 19:10）：方向成立，8 处主项需要修正（6 处设计缺口 + 2 处与代码不符的论断）

复核方式：在 `b33e823` 上逐条对照源码。原稿的事实基础成立——健康端点确实过准入闸门、
`/readyz` 确实只 Ping DB、指标确实绑在 diagnostics、worker 确实无指标、消费链路分支齐全。
但下面 8 处照原样实现会踩坑：

| # | 位置 | 问题 | 修正 |
|---|---|---|---|
| 1 | 附录 A / Phase 1 | `Runner.Ready() bool` 与现有 `Ready() <-chan struct{}`（`worker/runner.go:143`）**同名冲突**，照抄编译不过 | 只读视图改名 `IsReady() bool`，`Ready()` 通道保持原样 |
| 2 | §3.9 / Phase 1 | diagnostics 的 `Validate` 在「enabled 且 metrics、pprof 都关」时返回 `ErrNothingEnabled`（`support/diagnostics/diagnostics.go:93-95`）；只加 `/health` 而不改这条判断，health-only 的诊断端口会被拒绝 | 规则改为「三个端点全关才报错」，并把 `Endpoints.Health` 纳入 |
| 3 | §4.1 / Phase 2 | worker 进程**根本没有诊断监听**（`worker_host.go` 全文无 diagnostics 引用），只构建 `MeterProvider` 不会产生 `/metrics` | Phase 2 补「worker_host 在指标开启时启动诊断监听（或独立 metrics 监听）」 |
| 4 | §3.4 / §3.6 | readyGate 用 `sync.Once` 关通道后**永不回退**（`runner.go:147-149`），只看 gate 的 `/readyz` 在 draining 期间仍返回 200 | `/readyz` 必须同时要求 `state == ready`；`groupReady` 检查不能单独表达 draining |
| 5 | §4.5 | 「沿用 `durationBuckets`，再额外加 `0.005…10` 一组」——这组值**就是**现有 `durationBuckets`（`support/otel/metrics.go:58`），不存在额外一组；且新直方图不注册 `WithView` 会落回 SDK 默认桶（数值边界 `0…10000`，与 HTTP 的 `0.005…10` 不是同一刻度） | 改为「复用同一组边界，并为每个新直方图注册 view」；写明 `_seconds` 后缀来自 `metric.WithUnit("s")` |
| 6 | §3.5 | 在 `handlerSwap` 直接放行会**跳过整条 gin 中间件链**：现在 `/healthz` 是 engine 路由，位于 `start.go:340-430` 的 `engine.Use` 链上，依次经过 RequestID、HeaderPropagation、CORS、SecurityHeaders、Recovery；但 AccessLog 与 Trace/Metrics 本就默认跳过探针（`middleware/accesslog.go:78,126-127`、`start.go:350,361`），所以代价不是丢日志，而是丢 `req_id` 与 CORS/SecurityHeaders 响应头 | 写清这个取舍，并给出「闸门层豁免 + 仍走 engine」的备选及代价 |
| 7 | §4.3 | 基数估算只数了 counter，漏掉直方图：一个标签组合的直方图 ≈ 桶数 + 2，`consumer.handle.duration` 6 个 outcome 就是每消费者约 84 条序列，30 个消费者已有 2500+ | 按「counter 序列 + histogram ×(边界数+2)」重算 |
| 8 | §3.8 | 框架**没有 `config.Config`**：`config/` 只有 YAML 加载器，示例里的 `health:` 段属于应用自己的配置结构 | 写明 YAML 由应用映射成 `health.Options`，框架只认 Option |

其余定位复核无误：`start.go:813-830`（两个端点的实现）、`generation.go:219-246`（闸门与 503）、
`worker_health.go:88-97`（只看 ready 状态）、`gofy.go:484-505`（`buildMetrics` 的 gating）、
`support/otel/metrics.go:235-310`（现有 instrument）、`worker/ready.go`（readyGate）。
只有 `gofy.go:487-495` 与 `runner.go:157-300,316-370` 的区间略窄，引用时以函数边界为准。

另有三处小修，已直接改进正文：

- `worker.run.duration{outcome}` 的 `drained` 不是 `Run` 的返回值：`Run` 的返回只有 `nil`、`fatal`
  与 `ErrDrainTimeout` 三种，`drained` 本身从不外传（`runner.go:274-306`）。drain 超时是否变成
  error 取决于路径：ctx 取消后的停机里预算耗尽只 `warn` 并置 `abandoned`，返回 `nil`
  （`:295-300`）；ctx 未取消而某个实例没退完才返回 `ErrDrainTimeout`（`:302-304`）。`abandoned`
  是组级标记，仍用 `worker.abandoned` 表达；`outcome` 保持 `ok | error`，因为 `error` 已覆盖
  「致命失败」与「异常停机超时」两类，不需要再切分。
- `topic` 标签的来源按 broker 不同：Kafka 是 `record.Topic`、RocketMQ 是 `message.Topic`，AMQP 是
  `delivery.RoutingKey`（`consumer/amqp.go:341`，路由键，**不是**队列名），Redis queues 是队列
  `key`（`consumer/queues.go:277`）；保留这个标签名并在标签表里注明各 broker 的实际来源（改名
  会破坏与 Kafka 的一致）。
- `WithMetrics` 打开却未挂任何 handler 时，Prometheus reader 会持续累积序列却无人抓取；要求与
  diagnostics 或自挂 `Handler()` 配套。

---

## 1. 现状基线

| 能力 | 现状 | 缺口 |
|---|---|---|
| HTTP liveness | `GET /healthz` 返回 `{"status":"ok"}`（`start.go:813-816`） | 经过准入闸门，摘流期间也 503 |
| HTTP readiness | `GET /readyz` 只 Ping `rt.DB`，1s 超时；无 DB 直接 Ready（`start.go:817-830`） | 只有 DB；无法注册 Redis/下游 HTTP 等检查；无缓存与并发控制 |
| Worker 健康 | 可选监听，只有 `/healthz`；状态 starting/ready/stopping（`worker_health.go:19-97`） | 没有 `/readyz`；不能表达「哪个 worker 未就绪」；liveness 与 readiness 混在一个端点 |
| 诊断监听 | 可选，独立端口，默认回环；提供 `/metrics` 与 pprof（`support/diagnostics/diagnostics.go:43-95`） | 没有依赖健康明细端点 |
| 指标 | 只有 `http.server.request.duration`、`http.server.active_requests` 与 Go runtime 指标（`support/otel/metrics.go:235-310`） | Worker、MQ、Claim、Lease 侧全空 |
| 指标装配 | 与 diagnostics 绑定；worker 进程无指标（`gofy.go:487-495`、`worker_host.go`） | worker 进程无法暴露自身指标 |
| Runtime | `TracerProvider`/`Propagator`/`Tracing`；**无 Meter**（`core/runtime.go`） | 子包拿不到指标入口 |

现有 worker 生命周期已经具备可观测的语义，只是没有暴露出来：`Runner` 内部有 readyGate、
重启计数、drain 预算与 `abandoned` 标记（`worker/runner.go:157-300,316-370`），
`ReadyReporter` 已经让消费者在建连后才上报就绪（`worker/ready.go`）。

消费链路同样已经有完整分支：claim / in-flight / duplicate / 解码失败 / 永久失败 / 可重试 / 成功，
以及 `OnLedgerError` 钩子（`support/consumer/consume.go:333-345,475-640`）。认领链路有
`Result{Dead, LeaseLost, Elapsed}` 与 `OnSuccess/OnFailure`（`support/claim/runner.go:120-190`），
租约侧有 `Renew`/`ErrLeaseLost`（`support/lease/lease.go:191-260`）。这些都是天然的埋点位置。

---

## 2. 目标与非目标

**目标**

1. 三种语义严格分层：liveness 永不依赖外部、readiness 只包含「本进程当前能否接活」的必要条件、
   diagnostic 只描述依赖明细且不触发任何动作。
2. 业务可以按自己的依赖关系注册检查，框架不 import 任何第三方 SDK。
3. 探针有缓存、并发与超时上限，健康检查不能成为依赖的压力源。
4. Worker 与 MQ 的指标复用现有 OTel/Prometheus 管道，维度有界、命名统一。
5. 默认行为不变：不开新选项时，现有 `/healthz`、`/readyz` 的语义与响应体保持不变。

**非目标**

- 不做自动重启、自动摘流之外的任何自愈动作（依赖恢复由编排层决定）。
- 不把业务指标（订单量、消息积压）纳入框架；只做生命周期与消费链路指标。
- 不要求根包 import Redis/Kafka/RocketMQ SDK；内置检查器只覆盖通用协议（DB、HTTP、Redis Ping）。
- 不替换 Prometheus exporter，不引入第二套指标框架。

---

## 3. 健康检查设计

### 3.1 语义分层与端点矩阵

| 端点 | 语义 | 检查内容 | 失败行为 | 监听 |
|---|---|---|---|---|
| `GET /healthz`（HTTP） | Liveness | 不跑任何依赖检查，只看进程状态 | 仅 fatal 时 503；**正常摘流期间保持 200**，监听关闭后自然不可达 | 业务端口 |
| `GET /readyz`（HTTP） | Readiness | `admission` 已关闭 → 503；本进程 readiness 检查（DB 默认纳入） | 503，响应体保持 `{"status":"unavailable"}` | 业务端口 |
| `GET /healthz`（worker） | Liveness | 进程是否还活着 | 停止后 503；starting/draining 期间 200 | worker health 端口 |
| `GET /readyz`（worker） | Readiness | 所有 runner 的 readyGate 已满足 + worker 侧 readiness 检查 | 503 | worker health 端口 |
| `GET /health`（diagnostics） | Diagnostic | 全部检查（readiness + diagnostic），逐项状态与耗时 | **始终 200**，状态写在 JSON 里 | diagnostics 端口 |

关键点：

- **Liveness 绝不因为依赖失败而失败**，否则一次 Redis 抖动会让所有副本一起重启。
- **Readiness 只回答「本进程现在能不能接活」**；HTTP 进程不检查 worker 的 Kafka 连接。
- **Diagnostic 不驱动任何动作**，只给人和看板看；返回 200 是为了让抓取端拿到明细，而不是把
  「某个依赖坏了」变成一次 HTTP 失败。

### 3.2 注册机制与 API

新增独立包 `support/health`（不依赖根包，符合 `shell/check-imports.sh` 的分层约束）：

```go
package health

type Status uint8
const (
    StatusUnknown Status = iota
    StatusHealthy
    StatusUnhealthy
    StatusDegraded // 仅 diagnostic：可用但降级
)

type Class uint8
const (
    ClassReadiness Class = iota // 影响 /readyz
    ClassDiagnostic             // 只在 /health 展示
)

type Checker interface {
    Name() string
    Check(ctx context.Context) error
}

type CheckFunc func(ctx context.Context) error
func Named(name string, fn CheckFunc) Checker

type Options struct {
    Timeout        time.Duration // 单检查，默认 2s
    TotalTimeout   time.Duration // 单轮总预算，默认 3s
    Concurrency    int           // 默认 4
    ReadinessTTL   time.Duration // 默认 1s
    DiagnosticTTL  time.Duration // 默认 2s
    StartupGrace   time.Duration // 可选：启动后宽限期内 readiness 不判定
}

type CheckOption func(*checkOptions)
func WithClass(c Class) CheckOption
func WithCheckTimeout(d time.Duration) CheckOption
func WithWeight(w int) CheckOption // 预留：未来按权重聚合

type Registry struct{ /* ... */ }
func New(opts Options) *Registry
func (r *Registry) Register(c Checker, opts ...CheckOption) error

type Result struct {
    Name     string
    Class    Class
    Status   Status
    Err      error
    Duration time.Duration
}
type Snapshot struct {
    Status    Status
    CheckedAt time.Time
    Stale     bool
    Results   []Result
}

func (r *Registry) Ready(ctx context.Context) Snapshot
func (r *Registry) Diagnose(ctx context.Context) Snapshot
```

App 层新增选项（与现有 `WithHealthEndpoints` 并存）：

```go
func WithHealthChecks(checkers ...health.Checker) Option        // 默认 readiness
func WithDiagnosticChecks(checkers ...health.Checker) Option    // 只诊断
func WithHealthCheckOptions(opts health.Options) Option
func WithDatabaseHealthCheck(enabled bool) Option               // 默认 true（保持现状）
func WithWorkerHealthChecks(checkers ...health.Checker) Option  // worker 进程用
```

`WithHealthChecks` 注册的检查默认 `ClassReadiness`，但文档必须写明：
**只有「本进程不满足它就不能接活」的依赖才该放进来**。非核心报表 Redis 应使用
`WithDiagnosticChecks` 或 `health.WithClass(health.ClassDiagnostic)`，否则一次抖动会把整个服务摘流。

框架自动注册的 DB 检查名固定为 `database`，行为与现状一致（无 DB 不注册）。
用户想完全接管 readiness 时用 `WithDatabaseHealthCheck(false)`。

### 3.2.1 默认 readiness 的评审结论（2026-11-21）

保留默认 readiness，不改「默认 diagnostic、显式声明才生效」。理由：注册检查本身就是表态「我在意
这个依赖」，默认不生效会让检查静默失效——比误摘流更难发现。改成 diagnostic 后，漏写 `WithClass`
的用户会得到一个永远绿的 `/readyz`，问题被藏起来而不是被暴露。

代价用三条约束压低，而不是靠改默认值：

- **槽位预算**：框架自动注册的 `database` 检查同样占 readiness 与并发槽位。默认并发 4 意味着
  用户最多再注册 **3 个** critical 检查，加上 DB 才凑满 4 个；第 5 个（含 DB）跑不完，落到
  `budget` 截断规则。这个数字要写进 godoc，否则用户按「4 个自定义检查」规划就会踩线。
- **godoc 与 README 的硬性措辞**：放进 readiness 的检查必须满足「不满足就不能接活」且「失败快、
  不重试、不重连」；业务健康（报表 Redis、搜索集群、第三方额度）一律走 `WithDiagnosticChecks`。
- **注册日志**：`WithHealthChecks` 为每个检查记一条 debug 日志，写明 `name`、`class=readiness`、
  `timeout`（默认 2s）。误注册时看启动日志即可定位，不必读代码。

不做的事：不提供「默认 diagnostic」的反向开关，也不提供静默降级——类别只能显式选择。
`WithDatabaseHealthCheck(false)` 保留，用于完全接管 readiness。

### 3.3 执行模型：缓存、并发与超时

以下五个默认值**已确认**（2026-11-21），作为 Phase 1 的落地值；改动需同步本表与 §6 的验收门槛。

| 参数 | 默认值 | 理由 |
|---|---|---|
| 单检查超时 | 2s | 超过 2s 的 Ping 已经不能代表「当前可用」；与现有 `/readyz` 的 1s 同量级 |
| 单轮总预算 | 3s | 探针自身通常 5s 超时；总预算必须小于探针超时，否则探针先失败、拿不到诊断信息 |
| 并发上限 | 4 | 避免 20 个依赖同时 Ping 打满连接池；依赖检查应快速失败而不是排队 |
| Readiness 缓存 TTL | 1s | 探针频率通常 5–10s；1s 缓存足以合并同一副本的重复探测，又不会让状态滞后太久 |
| Diagnostic 缓存 TTL | 2s | 明细允许稍旧；避免人工刷新与抓取同时打依赖 |
| 并发与预算的关系 | 4 × 2s > 3s | 默认值只保证**不超过 4 个 critical 检查**能在预算内全部跑完；检查数超过 4 时第二轮只剩 1s，必须按下一行的规则截断，不排队等待 |
| 预算耗尽 | 已完成的结果照用 | readiness：未完成或未开始的检查视为失败（503），与 `fail-closed` 一致；diagnostic：未完成记 `unknown` 并标注 `budget` |
| 单检查结果 | 缓存到下一轮 | 同一轮内所有请求共享一次执行结果 |
| 超时/panic | fail-closed | readiness 返回 503，diagnostic 标记 `unknown`；panic 恢复并记录，不中断整轮 |
| singleflight | 按 `(registry, kind)` 合并 | 100 个并发探针只触发一轮检查 |

表里最容易漏掉的是「并发 × 单检查 > 总预算」这条边界：调小并发、调大单检查，或者注册第 5 个
critical 检查，都会让部分检查永远跑不完。因此改任一默认值时，必须在同一个 PR 里复核总预算
与检查数量，否则 readiness 会长期失败而看不出原因。

执行规则：

- `/healthz` 不触碰 registry；只读进程状态。
- `/readyz` 读 `Ready` 快照；缓存过期才触发一轮。
- `/health` 读 `Diagnose` 快照；同样 singleflight，但 TTL 独立。
- 检查在**独立 goroutine** 中带超时执行；请求 ctx 取消只影响等待，不取消已发出的检查（否则
  探针超时会制造「检查永远 incomplete」）。**结果 channel 必须带缓冲**（容量 1）：等待方被
  探针超时放弃后，检查 goroutine 仍要能写回并退出，否则每次超时漏一个 goroutine。
- 检查返回的 error 只在 diagnostics 里出现，且必须经过脱敏：去掉 DSN、密码、URL query 中的
  token；业务自定义检查的错误文本由业务自己保证不含敏感信息。
- 启动期：App 尚未 ready 时 `/readyz` 直接 503，不跑检查；避免启动瞬间把依赖打一遍。
- 摘流：`StopAccepting` 关闭 admission 后 `/readyz` 503（现状由闸门保证），`/healthz` 仍 200。

### 3.4 进程边界与装配

```
HTTP 进程                                worker 进程
├─ /healthz  (liveness)                  ├─ /healthz  (liveness)
├─ /readyz   (HTTP readiness)            ├─ /readyz   (group readiness + worker checks)
│   ├─ database (critical, auto)          │   ├─ group/<name> (readyGate, auto)
│   └─ downstream inventory (critical)    │   └─ kafka/consumer (ReadyReporter, auto)
└─ diagnostics /health (detail)           └─ (可选) diagnostics /metrics, /health
```

装配规则：

- HTTP 进程的 registry 只包含 HTTP 进程自己的依赖（DB、下游服务、Redis 等）。
- Worker 进程的 registry 包含 `groupReady`（由 host 自动注册，读所有 runner 的 readyGate）与
  `WithWorkerHealthChecks` 注册的检查。**Kafka 连接状态通过 `ReadyReporter` 表达，不写成静态
  checker**：连接由 `Run` 建立，checker 拿不到那个连接；强行 Ping 一个未连接的 client 只会得到误导结果。
- worker 的 `/readyz` 与 HTTP 的 `/readyz` 语义一致，但检查集合不同；两者共用同一个 `health.Registry` 实现。

### 3.5 Liveness 必须豁免准入闸门

现状：`handlerSwap.ServeHTTP` 在 gate 关闭时对所有路径返回 503（`generation.go:227-231`）。
`StopAccepting` 的注释也承认「readiness probe 看到 503」，但 `/healthz` 同时被影响：
滚动更新时 Pod 处于 Terminating，kubelet 仍会探 liveness，一次 503 可能触发不必要的重启。

设计：`handlerSwap` 增加精确路径豁免：

```go
type handlerSwap struct {
    // ...
    livenessPath    string        // "/healthz"
    livenessHandler http.Handler  // 不经过 generation/lease
}
```

`ServeHTTP` 首行判断 `r.Method` 为 GET/HEAD 且 `r.URL.Path` **精确等于** `livenessPath` 时直接
交给 `livenessHandler`。不做前缀匹配、不做 `Clean` 之后的宽松比较，避免 `/healthz/../admin`
一类路径借道绕过准入。豁免只给 liveness；`/readyz` 继续走闸门，摘流即 503。

**代价必须写清**：现在 `/healthz` 是 engine 路由，位于 `start.go:340-430` 的 `engine.Use` 链上，
依次经过 RequestID、HeaderPropagation、CORS、SecurityHeaders、Recovery。日志不是代价：AccessLog
默认 `SkipPaths` 就含 `/healthz`、`/readyz`（`middleware/accesslog.go:78,126-127`），Trace 与
Metrics 也用 `SkipPaths` 排除了探针（`start.go:350,361`），探针本来就不写访问日志。在 `handlerSwap`
直接放行会**跳过整条中间件链**，真实代价是丢 `req_id`（报障排查对不上号）和 CORS /
SecurityHeaders 响应头。若不能接受，改成「闸门层豁免 + 仍走当前 engine」：`ServeHTTP` 只跳过
`gate.admit`，不跳过 `gen.handler`；两种方案都能让摘流期间 liveness 保持 200，区别是后者保留
`req_id` 与安全头，但要接受请求进入正在退役的一代（healthz 无状态，实际无风险）。

### 3.6 worker `/healthz` 与 `/readyz` 的语义调整

现状 `/healthz` 在 starting/stopping 时 503（`worker_health.go:88-97`），把 liveness 与 readiness
混在一起。方案：

- `/healthz`：进程存活即 200；starting、ready、draining 都 200；监听关闭后自然不可达。
- `/readyz`：`state == ready` **且**所有 runner 的 readyGate 满足、worker readiness 检查通过 → 200；
  starting/draining → 503。只判 gate 不够：`markReady` 用 `sync.Once` 关通道（`runner.go:147-149`），
  drain 开始后通道仍然是关的，单看 gate 会把 `/readyz` 一直报成 200。
- 这是行为变更，**不提供 legacy 开关**（评审结论 2026-11-21）。`WithWorkerHealthLegacyLiveness`
  会把「路径叫 healthz、语义是就绪」的错配固化成两个可选项，并把两套语义一起拖到 v2；而迁移成本
  只是一行 YAML：`readinessProbe` 指 `/readyz`、`livenessProbe` 指 `/healthz`。worker 不接业务
  流量，探针误判不会把流量打到坏实例，风险窗口很小。
- 替代开关的交付项：`CHANGELOG.md` 的 `Changed` 段写明「worker `/healthz` 由就绪语义改为存活性
  语义」，`docs/worker-guide.md:96` 的「只报就绪」描述同步改写，README 的探针章节补迁移示例。
- 连带影响：`status` 命令的 `health` 列探测的就是 `/healthz`（`README.md:1070`），语义变更后该列
  只表示「进程活着」，不再表示「就绪」。表格列名与说明需同步修正，否则运维会把 `alive` 误读成
  `ready`。
- 若发布后确有用户在升级中受阻，再在 v1.x 追加兼容开关；届时语义已稳定，开关只承担兼容职责。

### 3.7 内置检查器

| 检查器 | 放置位置 | 说明 |
|---|---|---|
| DB | `support/health/db.go`（接收 `*gorm.DB`） | 复用现有 `PingContext`；默认 1s 超时，可覆盖 |
| Redis | `support/health/redis.go`（接收 `goredis.Cmdable`） | `PING`；不 import gofy 内部 redis 包 |
| HTTP 下游 | `support/health/http.go` | GET，2xx/3xx 视为健康；默认 1s；用于 inventory-service 这类同步依赖 |
| 自定义 | `health.Named(name, fn)` | 业务自己实现，框架不接管错误文本 |
| Kafka/AMQP/RocketMQ | **不提供静态检查器** | 连接状态由 worker 的 `ReadyReporter` 表达；HTTP 进程不应检查 worker 的 broker |
| Nacos | 可选 `support/health/nacos.go` | 只在「注册失败即不能接流」时使用；默认 diagnostic |

所有内置检查器都只接受接口/句柄，不引入新的第三方依赖到根包。

### 3.8 配置示例

框架只认 Option：`config/` 不是应用配置模型，只是一个 YAML 加载器，下面的 `health:` 段属于
应用自己的配置结构，由应用映射成 `health.Options` 再传给 `WithHealthCheckOptions`。不要让它
和 Option 形成两套真值——YAML 解析出来的值只在这一个入口出现一次。

```yaml
health:
  enabled: true
  # 探针执行参数；零值用默认
  timeout: 2s
  total_timeout: 3s
  concurrency: 4
  readiness_ttl: 1s
  diagnostic_ttl: 2s
  # 框架 DB 检查，默认 true
  database: true
  # 每条检查是否影响 readiness 在代码里声明，不在 YAML 里猜
```

```go
app, err := gofy.New(
    gofy.WithHealthEndpoints(true),
    gofy.WithHealthCheckOptions(health.Options{Concurrency: 4}),
    gofy.WithHealthChecks(
        health.Named("inventory", inventoryChecker),
    ),
    gofy.WithDiagnosticChecks(
        health.Named("report-redis", reportRedisChecker),
    ),
)
```

### 3.9 兼容性

- 不开 `WithHealthEndpoints(true)`：行为与现在完全一致（没有端点）。
- 开启但未注册检查：`/readyz` 仍只 Ping DB，响应体保持 `{"status":"ok"}` / `{"status":"unavailable"}`。
- diagnostics 新增 `/health`：默认路径 `/health`，`DisableHealth` 可关；不影响 `/metrics` 与 pprof。
  但必须同步放宽 `Config.Validate` 的 `ErrNothingEnabled`（`diagnostics.go:93-95`）：现在
  「enabled 且 metrics、pprof 都关」直接报错，加上 `/health` 后规则要改成「三个端点全关才报错」，
  否则 health-only 的诊断端口根本起不来。
- worker `/healthz` 语义变更需要发布说明，不提供 legacy 开关（理由见 §3.6）；`/readyz` 是纯新增。

---

## 4. Worker/MQ 指标设计

### 4.1 先解耦指标与诊断监听

现状指标只在 diagnostics 开启时构建。方案：

- 新增 `WithMetrics(otel.MetricsConfig) Option`：独立于 diagnostics 构建 `*otel.Metrics`；
  diagnostics 只负责挂载 handler，不再决定指标是否可用。
- `otel.Metrics` 增加 `MeterProvider() metric.MeterProvider`：禁用时返回 no-op provider，
  调用方无需判空。
- `core.Runtime` 增加 `MeterProvider metric.MeterProvider`（第三方类型，`core` 仍是叶子）。
- worker host 与 HTTP 路径都构建指标；worker 进程不再「没有指标」。

  但**只有 MeterProvider 不会产生抓取端点**：`worker_host.go` 目前没有任何 diagnostics 引用，
  指标构建完也没有人挂 `Handler()`。Phase 2 必须同时让 worker host 在指标开启时启动诊断监听
  （复用 `diagnostics.Config`）或独立的 metrics 监听，否则 `/metrics` 不存在，验收条件无法成立。
  落地上复用 `App.startDiagnostics`：只有配置了 `WithDiagnostics` 才监听，避免凭空绑定 6060；
  没有配置时启动日志给出告警，由调用方决定挂到哪里。
- 反向注意：`WithMetrics` 打开却没有任何 handler 挂载时，Prometheus reader 会持续累积序列
  而无人抓取，属于内存泄漏式的误配置。文档要求它与 diagnostics 或自挂 `Handler()` 配套使用。
- `WithDiagnostics` 保持现状：开启诊断且未禁用 metrics 时，等价于同时开启指标（向后兼容）。

### 4.2 指标清单

命名采用 OTel 点号；Prometheus 侧由 exporter 转成下划线。

**健康检查**

| 指标 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `gofy.health.ready` | Gauge | — | 1=Ready，0=NotReady；驱动摘流告警 |
| `gofy.health.check.status` | Gauge | `check`, `class` | 1=健康，0=失败，-1=unknown（Gauge 支持负值） |
| `gofy.health.check.duration` | Histogram | `check`, `class` | 单检查耗时；发现问题依赖变慢 |

**Worker 生命周期**

| 指标 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `gofy.worker.ready` | Gauge | `group`, `name` | 该 worker 的所有实例是否就绪（1/0） |
| `gofy.worker.instances.ready` | Gauge | `group`, `name` | 就绪实例数 |
| `gofy.worker.instances.total` | Gauge | `group`, `name` | 配置的实例总数 |
| `gofy.worker.restarts` | Counter | `group`, `name` | Runner 重启次数（`supervise` 每次 +1） |
| `gofy.worker.run.duration` | Histogram | `group`, `name`, `outcome` | 单次 `w.Run` 的持续时间；`outcome=ok/error`。`Run` 返回 nil、fatal 或 `ErrDrainTimeout`（`runner.go:274-306`）：drain 超时只在 ctx 未取消的路径上成为 error（`:302-304`），ctx 取消后的预算耗尽仅 `warn` + 置 `abandoned`（`:295-300`），因此 `error` 覆盖「致命失败」与「异常停机超时」两类，不再细分 |
| `gofy.worker.tick.duration` | Histogram | `group`, `name` | 轮询 worker 单次 tick 耗时 |
| `gofy.worker.tick.errors` | Counter | `group`, `name` | tick 报错与 panic 次数 |
| `gofy.worker.abandoned` | Counter | `group` | drain 超时、实例未退出的次数 |

**消费链路**

| 指标 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `gofy.consumer.received` | Counter | `consumer`, `topic`, `broker` | 每条进入 `Handle` 的投递计一次 |
| `gofy.consumer.acked` | Counter | `consumer`, `topic`, `broker`, `reason` | `reason=success/duplicate/permanent/decode`；决策为 ACK 时计一次 |
| `gofy.consumer.nacked` | Counter | `consumer`, `topic`, `broker`, `reason` | `reason=retry/inflight/decode/ledger`；决策为 NACK/重投时计一次 |
| `gofy.consumer.dead` | Counter | `consumer`, `topic`, `broker`, `reason` | 成功落为 dead 的消息；`reason=permanent/decode` |
| `gofy.consumer.handle.duration` | Histogram | `consumer`, `topic`, `outcome` | 从 `Handle` 开始到返回的耗时（不含 broker ack） |
| `gofy.consumer.ledger.errors` | Counter | `consumer`, `stage` | `stage=done/failed/claim`；对应 `OnLedgerError` |
| `gofy.consumer.ack.errors` | Counter | `broker`, `op` | `op=ack/nack`；只在适配器里计一次 |
| `gofy.consumer.connect.errors` | Counter | `broker`, `reason` | 建连/订阅失败；`reason=connect/subscribe/timeout` |

不变量：`received == acked + nacked`（每条投递恰好一个确认决策）；
`dead ≤ acked{reason=permanent|decode}`（落 dead 只在 park 成功时计）。
这样「接收 / 确认 / 重试 / 永久失败」四个问题各自有独立计数器，不会出现同一条消息在多个
`result` 里重复累加。

**认领与租约**

| 指标 | 类型 | 标签 | 说明 |
|---|---|---|---|
| `gofy.claim.claimed` | Counter | `job` | 一轮认领到的行数 |
| `gofy.claim.outcomes` | Counter | `job`, `outcome` | `outcome=succeeded/failed/dead/lease_lost` |
| `gofy.claim.handler.duration` | Histogram | `job`, `outcome` | `Result.Elapsed` |
| `gofy.lease.acquire.failures` | Counter | `lease` | 获取失败（不含 `ErrHeld` 的正常等待） |
| `gofy.lease.renew.failures` | Counter | `lease` | 续租失败；`ErrLeaseLost` 单独计 `outcome=lost` |

### 4.3 标签与基数约束

允许的标签（全部来自配置或固定枚举）：

| 标签 | 来源 | 取值约束 |
|---|---|---|
| `group` | `worker.Group.Name` | 配置期校验，≤63 字符 |
| `name` | `worker.Name` | 同上 |
| `consumer` | `ConsumerConfig.Name` | 同上 |
| `topic` | 投递元数据 | 只允许配置里声明的值；空值记为 `unknown`。来源按 broker：Kafka `record.Topic`、RocketMQ `message.Topic`、AMQP `delivery.RoutingKey`（按配置声明的 binding 白名单化，未命中记 `unknown`）、Redis queues 队列 `key`；与 Kafka 共用同一个标签以免同一 consumer 换 broker 时改名 |
| `broker` | 构造器决定 | 固定枚举：`kafka/amqp/rocketmq/queues` |
| `reason` | 分支 | 固定枚举：`success/duplicate/permanent/decode/retry/inflight/ledger` |
| `outcome` | 分支 | 固定枚举：`success/duplicate/permanent/decode/retry/inflight/succeeded/failed/dead/lease_lost` |
| `job` | `claim.Config.Job` | 配置期校验 |
| `lease` | `lease.RunConfig.Name`（新增） | 可选；为空时只记聚合指标 |

禁止进入标签：`message_id`、`event_id`、`order_id`、`user_id`、`tenant_id`、`instance`（实例序号）、
`host`、原始 `error` 文本、`key`（租约业务键）。这些值只进日志/span，不进指标。

基数预算要分两类算，counter 的算法会严重低估：

- **counter**：`1 received + 4 acked + 4 nacked + 2 dead` = 11 条/消费者（不含 ledger/ack 错误）。
- **histogram**：一个标签组合的序列数 = 桶数 + `_sum` + `_count`。现有 11 个边界意味着 12 个桶
  （含 `+Inf`）+ 2 = **14 条**。`consumer.handle.duration` 有 6 个 `outcome`，即 84 条/消费者；
  若再加 claim 与 worker 的直方图，单个消费者的 histogram 序列就到 100 上下。

所以 30 个消费者（各 1 topic）不是 500 级，而是 **3000 级上下**——仍然可接受，但把预算写成
17 条会让人在扩容时低估一个数量级。真正要守的规则是：histogram 的 `outcome` 枚举不许再加，
`topic` 数由部署决定（文档要求「同一 consumer 的 topic 不超过 20 个」），实例序号绝不建标签
——副本级差异用 Prometheus 的 `instance` 目标标签表达。

### 4.4 接入点

| 事件 | 位置 | 指标 |
|---|---|---|
| Runner 启动/就绪/停机 | `worker/runner.go:157-300` + `readyGate` | `worker.ready`、`instances.*` |
| 重启 | `worker/runner.go:316-370`（`supervise`） | `worker.restarts`、`worker.run.duration` |
| drain 超时 | `runner.go:262-284`（`abandoned`） | `worker.abandoned` |
| 轮询 tick | `worker/ticker.go:62-89`（`runTick`） | `worker.tick.duration/errors` |
| 消息进入 | `support/consumer/consume.go:470`（`Handle` 开始） | `consumer.received` |
| ACK 决策 | `consume.go:520-640` 的 nil 返回路径 | `consumer.acked{reason=success/duplicate/permanent/decode}` |
| NACK 决策 | 同上的 error 返回路径 | `consumer.nacked{reason=retry/inflight/decode/ledger}` |
| 落为 dead | `consume.go` 的 `park` 成功路径 | `consumer.dead{reason=permanent/decode}` |
| handler 耗时 | `consume.go:534-640` | `consumer.handle.duration{outcome}` |
| 台账写失败 | `consume.go:333-345`（`OnLedgerError`）+ `ledgerFailed` | `consumer.ledger.errors{stage}` |
| ack/nack 失败 | `support/consumer/kafka.go`、`amqp.go`、`rocketmq.go`、`queues.go` | `consumer.ack.errors{broker,op}` |
| 认领 | `support/claim/runner.go:276-300` | `claim.claimed` |
| 认领结果 | `runner.go` 的 `OnSuccess/OnFailure` 调用点 | `claim.outcomes`、`claim.handler.duration` |
| 租约获取/续租 | `support/lease/lease.go:191-260` | `lease.*.failures` |

实现约定：

- `Processor` 在构造时创建 instrument 并缓存；`Handle` 里只做 `Add`/`Record`，不创建对象。
- `result` 只在一个地方决定：`Handle` 的出口；broker 适配器不重复计消息数，只计 ack 错误。
- `worker.ready` 的状态来源是 readyGate，不轮询 `w.Run`；由 `setReady`/`setStopping` 触发更新。
- `instances.ready/total` 需要一个新数据源：readyGate 目前只有 `pending map` 与 `done` 通道
  （`worker/ready.go:54-80`），没有计数访问器。要么加 `reported()`/`registered()` 两个
  只读方法，要么在 Runner 里按 `instances` 长度与 gate 差值维护计数——不改 gate 就取不到值。
- 所有 `Meter` 在指标禁用时是 no-op；调用点无需判空。

### 4.5 命名与 Prometheus 映射

| OTel | Prometheus |
|---|---|
| `gofy.worker.restarts` | `gofy_worker_restarts_total` |
| `gofy.consumer.received` | `gofy_consumer_received_total` |
| `gofy.consumer.acked` | `gofy_consumer_acked_total` |
| `gofy.consumer.nacked` | `gofy_consumer_nacked_total` |
| `gofy.consumer.dead` | `gofy_consumer_dead_total` |
| `gofy.consumer.handle.duration` | `gofy_consumer_handle_duration_seconds` |
| `gofy.health.check.status` | `gofy_health_check_status` |

直方图桶直接复用 `support/otel/metrics.go:58` 的 `durationBuckets`——它就是
`0.005,0.01,0.025,0.05,0.1,0.25,0.5,1,2.5,5,10` 这组值，**不存在「额外加一组」**。落地时还有
两件事：

1. 每个新直方图都要落到同一套 view。现在只有 `http.server.request.duration` 有 view
   （`metrics.go:164-171`），其余 instrument 会落回 SDK 默认桶——边界是
   `0,5,10,25,50,75,100,250,500,750,1000,2500,5000,7500,10000`（`_sum`/`_count` 之外 16 个计数桶）。
   问题不是「10s 以上挤进一个桶」：10s–10000 之间仍有 25/50/…/7500 多档。真实风险是同一份看板
   要面对两套边界——HTTP 用 `0.005…10`、消费耗时用 SDK 默认值，单位同为秒但刻度差三个数量级，
   分位数与阈值会随 SDK 版本变化而漂移。落地时改为一条按
   `(Kind=Histogram, Unit="s")` 匹配的 view：新增 histogram 自动继承同一组边界，不必再逐个登记
   名字；`worker.run.duration` 的回归用例会断言 `le=0.005` 存在、`le=0/50/10000` 不存在。
2. 映射表里的 `_seconds` 后缀来自 `metric.WithUnit("s")`（现有直方图已设置）；新直方图必须同样
   设置单位，否则 Prometheus 侧的名字是 `gofy_consumer_handle_duration` 而不是 `_seconds`。

### 4.6 看板与告警建议

| 告警 | 条件 | 说明 |
|---|---|---|
| NotReady | `gofy_health_ready == 0` 持续 2m | 区分 `check` 标签定位依赖 |
| Worker 抖动 | `rate(gofy_worker_restarts_total[5m]) > 0.2` | 持续重启说明连接或配置问题 |
| 消费停滞 | `rate(gofy_consumer_received_total[5m]) == 0` 且应有流量 | 与 topic 维度一起看 |
| 重投升高 | `rate(gofy_consumer_nacked_total{reason="retry"}[5m]) / rate(gofy_consumer_received_total[5m]) > 0.2` | 可能是下游故障或 handler 非幂等 |
| 永久失败 | `rate(gofy_consumer_dead_total{reason="permanent"}[5m]) > 0` | 需要人介入修数据 |
| 台账落后 | `rate(gofy_consumer_ledger_errors_total[5m]) > 0` | 去重失效风险 |
| 认领失败 | `rate(gofy_claim_outcomes_total{outcome="failed"}[5m]) + rate(gofy_claim_outcomes_total{outcome="dead"}[5m]) > 0` | 扫表任务健康 |
| 租约丢失 | `rate(gofy_lease_renew_failures_total{outcome="lost"}[5m]) > 0` | 长任务被接管 |

---

## 5. 分阶段实施

### Phase 1：健康检查（约 4–5 人日）

| 文件 | 变更 |
|---|---|
| `support/health/*.go`（新） | `Checker`/`Registry`/`Options`/`Snapshot`、缓存与 singleflight、DB/HTTP/Redis 检查器 |
| `option.go` | `WithHealthChecks`、`WithDiagnosticChecks`、`WithHealthCheckOptions`、`WithDatabaseHealthCheck`、`WithWorkerHealthChecks` |
| `start.go:813-831` | 用 registry 替换现有 `registerHealth`；DB 检查自动注册；响应体保持兼容 |
| `generation.go:221-241` | liveness 精确路径豁免 |
| `worker_health.go` | 新增 `/readyz`；`/healthz` 改为纯 liveness |
| `worker_host.go:202-208,300-301` | 注册 `groupReady` 检查；构造 worker registry |
| `worker/runner.go` | 暴露 `IsReady()`/`State()`（只读视图，不改调度逻辑；`Ready()` 通道保持原样） |
| `support/diagnostics/diagnostics.go` | `Config.HealthPath`/`DisableHealth`、`Endpoints.Health`，并把 `ErrNothingEnabled` 改成「三个端点全关才报错」（`:93-95`） |

验收：不注册检查时 `/readyz` 行为与响应体不变；摘流时 `/healthz` 200、`/readyz` 503；
100 并发探针只触发一轮检查；单检查 panic/超时不会拖垮整轮。

#### Phase 1 落地记录（2026-11-21）

代码已落地并通过验证：

| 验证项 | 命令 | 结果 |
|---|---|---|
| 格式 | `gofmt -l .` | 无输出 |
| 编译 / 静态检查 | `go build ./...`、`go vet ./...` | 通过 |
| 单元 + 集成 | `go test ./support/health/... ./support/diagnostics/... .` | 通过 |
| 并发 | `go test -race ./support/health/...` | 通过 |
| 依赖方向 | `make guards` | 通过 |

验证过程中发现并修掉的问题（均先复现再修）：

1. **`WithCheckTimeout` 静默吞掉负值**：选项层 `if d > 0` 让 `Register` 的负超时校验永远不可达，
   调用方以为检查运行在自己要求的预算下。改为记录非零值，由 `Register` 统一拒绝；
   `TimeoutProvider` 返回负值同样拒绝。
2. **Reload 复用注册表会 ping 已关闭的连接池**：`dbChecker` 捕获启动时的 `*gorm.DB`，
   而带 `Database` 的 Reload 会关闭旧池（`generation.go:339-343`），`/readyz` 此后永久 503。
   改为注册表随 runtime 重建（`atomic.Pointer` 保存，处理器按请求读取），`Handler()` 模式也构建注册表。
3. **启动期 `/readyz` 会跑检查**：listener 在 `markReady` 之前已接受连接，与 §3.3
   「尚未 ready 直接 503、不跑检查」不符。现在未 ready 时直接 503，不触发任何检查。
4. **HTTP 检查错误带出 URL query**：`http.Client.Do` 的 `*url.Error` 文本重复完整 URL，
   会把 `?token=...` 渲染进 `/health`。改为解包只保留操作与原因；构造期错误用去掉 query 与
   userinfo 的 URL。

测试口径也同步到新语义：`/healthz` 的摘流断言改为「liveness 200 + readiness 503」；
worker 宿主探针测试改探 `/readyz`（并断言 `/healthz` 在启动与 drain 期间始终 200）。

### Phase 2：指标管道解耦（约 3–4 人日）

| 文件 | 变更 |
|---|---|
| `support/otel/metrics.go` | `MeterProvider()` 访问器；view 改为覆盖所有秒级直方图 |
| `option.go` / `gofy.go` | 新增 `WithMetrics`；`buildMetrics` 显式配置优先，兼容旧的 diagnostics 规则 |
| `worker_host.go` | 指标开启时启动 diagnostics 监听（`diagnostics.New` + `Endpoints{Metrics: metrics.Handler()}`），否则 worker 的 `/metrics` 不存在 |
| `core/runtime.go` | `MeterProvider metric.MeterProvider` |
| `worker/runner.go`、`ticker.go` | `RunConfig.MeterProvider`（或从 Runtime 取）；状态/重启/run/tick 指标 |
| `worker_host.go` | 构建指标并注入 runner |

验收：指标关闭时所有调用为 no-op、无额外分配；worker 进程 `/metrics` 能看到 `gofy_worker_*`。

#### Phase 2 落地记录（2026-11-21）

代码已落地并通过验证：

| 验证项 | 命令 | 结果 |
|---|---|---|
| 格式 | `gofmt -l .` | 无输出 |
| 编译 / 静态检查 | `go build ./...`、`go vet ./...` | 通过 |
| 单元 + 集成 | `go test ./...` | 通过 |
| 并发 | `go test -race ./worker/... ./support/otel/... ./core .` | 通过 |
| 依赖方向 | `make guards` | 通过 |
| 示例模块 | `examples/demo-service` 的 `go build ./...`、`go vet ./...` | 通过 |

实现要点与验证过程中确认的问题：

1. **`WithMetrics` 独立构建**：显式配置优先，`Enabled:false` 能关掉 diagnostics 原本会打开的管道；
   未传该选项时保持「diagnostics 开启且未禁用 metrics 即构建」的旧行为。仅开 provider、没有
   diagnostics 的进程会在启动日志告警，而不是静默累积无人抓取的序列。
2. **provider 入口**：`Metrics.MeterProvider()` 在禁用或 nil 接收者时返回 no-op provider；
   `core.Runtime` 新增 `MeterProvider` 与 `Metrics` 两个字段，HTTP 的 run/Handler、Reload 与
   worker 进程都装配。脚本路径没有指标管道，沿用 no-op 默认值。
3. **worker 抓取端点**：`runWorker` 在指标开启且配置了 diagnostics 时调用 `App.startDiagnostics`。
   回归用例 `TestWorkerServesMetricsEndpoint` 在临时注释掉这段时因 `connection refused` 失败，
   恢复后通过——单有 MeterProvider 确实不会有 `/metrics`。
4. **直方图边界**：view 从「按 instrument 名」改为「按 `(Kind=Histogram, Unit="s")`」匹配，
   所有秒级耗时直方图共用 `0.005…10`。回归用例在旧 view 下拿到 SDK 默认桶（`le=0/50/10000`）
   而失败，改回后通过。
5. **worker 埋点**：`worker/metrics.go` 缓存 8 个 instrument；Runner 记录 ready、instances、
   restarts、run 耗时与 abandoned，Ticker 经 context 取得同一组实例记录 tick 耗时与错误。
   禁用指标时所有方法在构造标签之前返回，`testing.AllocsPerRun` 断言每次调用 0 分配。
6. **并发修正（验证中发现）**：readyGate 最初在解锁后才发布 pending 快照，两个实例同时上报时，
   旧快照可能在闸门关闭之后覆盖 `instances.ready=2`，压测 50 次复现约 4 次。改为持锁按状态
   变化顺序发布，压测 50 次全过；`-race` 也通过。

### Phase 3：消费、认领、租约指标（约 4–5 人日）

| 文件 | 变更 |
|---|---|
| `support/consumer/consume.go` | `ProcessorConfig` 增加 `MeterProvider`；`Handle` 分支计数与耗时 |
| `support/consumer/{kafka,amqp,rocketmq,queues}.go` | ack/nack、connect 错误计数 |
| `support/claim/runner.go` | `RunnerConfig.MeterProvider`；claim/outcome/duration |
| `support/lease/lease.go` | `RunConfig.Name` 与 `MeterProvider`；acquire/renew 失败计数 |

验收：1000 条消息只产生固定序列数；`received == acked + nacked`，同一条消息只计一次终态；
日志与指标不重复计数。

### Phase 4：增强（可选）

- Kafka consumer lag：用 franz-go 的 `Client.Metrics()` 或 `DescribeGroups`，只在 `broker=kafka` 时暴露。
- 下游 HTTP 检查器支持 TLS/超时/方法配置。
- Startup probe：`/readyz` 已能承担；如需独立 `/startupz` 再加。
- 健康检查历史：diagnostics 里保留最近 N 次 `Snapshot`，用于排查间歇性失败。

---

## 6. 测试计划与验收

| 层级 | 用例 | 断言 |
|---|---|---|
| 单元 | registry：失败/超时/panic/并发上限 | fail-closed；panic 不扩散；并发不超过上限 |
| 单元 | 缓存与 singleflight：100 goroutine 同时探测 | 检查函数只被调用 1 次；两次请求间隔 < TTL 时复用 |
| 单元 | readiness vs diagnostic | diagnostic 失败不影响 `/readyz`；readiness 失败使 `/readyz` 503 |
| HTTP | 摘流期间 `/healthz`/`/readyz` | 200 / 503，liveness 不被闸门拦截 |
| Worker | starting → ready → draining | `/readyz` 503 → 200 → 503；`/healthz` 始终 200 |
| 指标 | manual reader 采集 | 指标名/标签符合清单；无禁止标签 |
| 指标 | 1000 条消息 | series 数不随消息数增长 |
| 指标 | 关闭指标 | no-op；无 panic；无额外分配（`testing.AllocsPerRun`） |
| Race | registry、runner 状态、指标 | `go test -race` 通过 |
| E2E | 假 checker 翻转 | `/readyz` 状态在 TTL 内翻转；diagnostics 明细同步 |

验收门槛：

1. 默认配置下行为与现状一致（端点开关、响应体、`/readyz` 的 DB 语义）。
2. 任意单个检查超时/panic 不会让 `/healthz` 失败。
3. 探针并发数不超过配置上限；同一 TTL 内检查函数调用次数为 1；总预算耗尽时，未完成或未开始的
   readiness 检查必须判失败（503），不得静默通过；diagnostic 对应项记 `unknown`（`budget`）。
4. 指标序列数与消息量解耦；禁用指标时无额外分配。
5. 不新增公共包对根包的依赖（`make guards` 通过）。

---

## 7. 风险与取舍

| 风险 | 影响 | 缓解 |
|---|---|---|
| 把非核心依赖设成 readiness | 一次抖动摘掉全部副本 | 默认只让显式声明的检查影响 readiness；DB 保持现状；文档给出反例 |
| 探针频率 × 副本数打满依赖 | 健康检查本身成为故障源 | 缓存 + singleflight + 并发上限 + 超时 |
| 详细健康信息泄露拓扑 | 安全风险 | 明细只在 diagnostics 端口（默认回环）；业务端口只返回状态码 |
| liveness 被闸门拦截 | 滚动更新时误重启 | 精确路径豁免，只放行 `/healthz` |
| 指标基数爆炸 | Prometheus 内存/查询成本 | 标签白名单 + 枚举 + topic 配置约束；禁止实例/消息 ID |
| worker `/healthz` 语义变更 | 现有探针配置失效 | 发布说明 + `CHANGELOG` 的 `Changed` 段 + worker-guide 改写；`/readyz` 为新增，不提供 legacy 开关 |
| 指标与诊断耦合未解开 | worker 无指标 | `WithMetrics` 独立构建；diagnostics 仅挂载 handler |
| 包依赖方向被破坏 | 循环依赖 | `support/health` 不 import 根包；`core` 保持叶子；`make guards` 守护 |

---

## 8. 附录

### A. 接口草图（仅形状）

```go
// support/health
type Checker interface {
    Name() string
    Check(context.Context) error
}

// option.go
func WithHealthChecks(checkers ...health.Checker) Option
func WithDiagnosticChecks(checkers ...health.Checker) Option
func WithHealthCheckOptions(opts health.Options) Option
func WithDatabaseHealthCheck(enabled bool) Option
func WithWorkerHealthChecks(checkers ...health.Checker) Option
func WithMetrics(cfg otel.MetricsConfig) Option

// support/otel
func (m *Metrics) MeterProvider() metric.MeterProvider

// core
type Runtime struct {
    // ...
    MeterProvider metric.MeterProvider
}

// worker
// Ready() 已被现有只读通道占用（runner.go:143），只读视图用另一个名字。
func (r *Runner) IsReady() bool
func (r *Runner) State() State // Starting/Ready/Draining/Stopped/Failed
```

### B. 与现有实现的差异清单

| 现有 | 方案 |
|---|---|
| `registerHealth` 内联 DB Ping | `health.Registry` + 内置 DB 检查器，响应体不变 |
| `/healthz` 经过准入闸门 | 精确路径豁免，liveness 在摘流期间保持 200 |
| worker `/healthz` 混用两种语义 | `/healthz` 纯 liveness，新增 `/readyz` |
| 指标只在 HTTP 路径构建 | `WithMetrics` 独立构建，worker 也有指标 |
| 无 Meter 入口 | `Metrics.MeterProvider()` + `core.Runtime.MeterProvider` |
| 消费/认领/租约无指标 | 按 §4.2 清单埋点，维度有界 |

### C. 估算

Phase 1 约 4–5 人日，Phase 2 约 3–4 人日，Phase 3 约 4–5 人日，Phase 4 按需。
测试与文档约占总量 30%。全部完成后，gofy 的健康与可观测能力覆盖「HTTP + worker + 消费 + 认领 + 租约」
五个面，且不引入新的运行时依赖。