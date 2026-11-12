# Gofy 与 gofy-interface-service 架构优化实施方案

- 方案版本：v1.0
- 编制日期：2026-11-12
- 评审对象：
  - `https://github.com/bingcool/gofy`（`main`）
  - `https://github.com/bingcool/gofy-interface-service`（`main`）
- 方案性质：基于当前源码的架构修正与可靠性优化
- 总体原则：**小步改造、保留现有架构、不为扩展而扩展、不新增与当前目标无关的功能**

---

## 1. 方案目标与范围

### 1.1 总体目标

Gofy 当前已经具备独立 Go 框架库的基本形态，本次不推翻 `App / Runtime / ModuleFactory / Module / Router` 体系，也不重新设计业务分层。改造重点是把现有设计中尚未闭合的资源生命周期、并发隔离和服务发现语义补齐，使框架在生产环境下具备可验证的行为。

本方案主要解决：

1. Reload 时新旧模块、请求和基础设施资源没有完整的代际隔离。
2. DB、Redis、PG 等 IO Registry 的核心调用仍依赖进程级全局指针。
3. Nacos Resolver 的负载均衡选择被 Client 侧二次地址缓存削弱。
4. 数据库慢 SQL 日志可能记录展开后的敏感参数。
5. Kafka 默认自动提交与默认错误处理描述的投递语义不一致。
6. `gofy-interface-service` 的 Go Module 路径与仓库正式导入路径不一致。
7. 契约中的认证声明、OpenAPI Optional Auth 语义和流式接口能力边界需要明确。
8. OTel 全局错误处理器、IO 并发初始化及 CI 质量门禁需要完善。

### 1.2 本次不做的事情

- 不重写 Gin，不替换当前 HTTP 框架。
- 不引入 Wire、Fx 等大型 DI 框架；继续使用显式构造和 Container。
- 不把 Gofy 改造成服务网格、服务治理平台或分布式事务框架。
- 不新增定时任务、工作流、分布式锁等与本轮问题无关的基础设施能力。
- 不强制所有 Repository 都声明接口。
- 不为了消除反射而立即重写整个 Contract Adapter。
- 不要求本轮一次性实现 SSE、Chunked、Download 全部服务端能力。
- 不改变 `endpoint.Route` 作为服务端绑定、Client 和 OpenAPI 共用契约源的基本设计。

### 1.3 风险分级说明

本方案的优先级按生产影响定义：

| 等级 | 定义 | 处理要求 |
|---|---|---|
| P0 | 已确认会导致数据错误、安全绕过或进程不可用的直接缺陷 | 修复前不建议进入生产 |
| P1 | 高并发、Reload、故障切换或特定配置下存在明显可靠性风险 | 下一轮优先修复 |
| P2 | 语义、性能、可维护性或工程化风险 | P1 完成后处理 |

本轮源码检查没有执行完整的 `go test -race`、压力测试或真实中间件故障注入，因此不将静态检查中发现的并发风险直接描述为已经在生产复现的事故。下文会区分“源码已确认的行为”和“需要动态测试验证的风险”。

---

## 2. 当前架构基线

### 2.1 Gofy 核心调用链

```text
业务 main.go
    |
    v
  gofy.New()
    |
    v
 App.Execute(ctx) / App.Run(ctx)
    |
    +--> Logger
    +--> Telemetry Provider
    +--> DB
    +--> core.Runtime
    +--> ModuleFactory
             |
             v
           Module
             |
      +------+------+
      |             |
      v             v
   Register       Start
      |             |
      v             v
   Gin Router    IO / Consumer / Goroutine
      |
      v
  HTTP Server
      |
      v
 Middleware -> Controller -> Service -> Repository -> IO
```

### 2.2 契约仓库调用链

```text
appinterface / endpoint.Route[Req, Res]
             |
      +------+----------------+
      |                       |
      v                       v
Gofy Adapter               Client
      |                       |
      v                       v
support.Router             Resolver
      |                       |
      v                       v
Gin Handler                 Nacos
      |
      v
Service Implementation

同一份 Route Descriptor
      |
      v
   OpenAPI DocGen
```

### 2.3 应保持的架构边界

- `gofy` 不依赖 `gofy-interface-service`。
- `apicore/contracts` 不依赖 Gin、GORM、Gofy 或具体业务包。
- `apicore/client` 不依赖 Gofy 服务端实现。
- `apicore/adapter/gofy` 是契约仓库中唯一需要依赖 Gofy 的适配层。
- 业务跨模块通过 `innerapi` 接口和领域视图协作，不直接依赖其他模块的 Repository、Model。
- `Module.Register` 只注册路由；`Starter.Start` 初始化后台资源；`Stopper.Stop` 释放后台资源。
- Repository 不拥有全局生命周期；连接由 App 或业务 IO 资源集合拥有。

---

## 3. P1-01：建立完整的 Reload 资源代际隔离

### 3.1 当前问题

相关文件：

- `gofy/reload.go`
- `gofy/start.go`
- `gofy/support/module.go`
- `gofy/support/db/transaction.go`
- `gofy/support/db/io.go`
- `gofy/support/redis/io.go`
- `gofy/examples/demo-service/app/io/io_runtime.go`
- `gofy/examples/demo-service/app/io/db/db.go`
- `gofy/examples/demo-service/app/io/redis/redis.go`
- `gofy/examples/demo-service/app/module/order/repository/order_repository.go`
- `gofy/examples/demo-service/app/module/product/repository/product_repository.go`

当前 Reload 的大致过程是：构建新 Logger/DB/Runtime/Modules，启动新模块，切换 HTTP Handler，再停止旧模块并释放旧资源。这个总体思路正确，但“Handler 切换”和“资源切换”还不是同一个代际操作。

Demo 的 IO Set 通过 `Install()` 修改包级句柄，例如 `app/io/db.Db`、`OrderDb`、`ProductDb`；Redis 也采用类似方式。写入侧有锁，但 Repository 读取这些包级变量时没有统一使用同一把锁，存在数据竞争风险。

同时，Reload 的新旧模块短时间并存，并不自动意味着旧请求继续使用旧资源。普通 Repository 查询如果每次都读取当前包级变量，可能在一个请求的两次操作之间跨越资源代际。使用 `support/db.Transaction` 的请求通常会从 Context 复用同一个事务连接，但这不能覆盖未开启事务的查询，也不能解决 Redis、MQ 等其他资源的代际隔离。

另外，HTTP Handler 原子替换只保证新请求看到新 Handler，并不代表已经进入旧 Handler 的请求全部结束。若旧模块在旧请求尚未完成时被 Stop，旧请求可能遇到被关闭的连接、Consumer 或其他资源。

### 3.2 目标语义

必须实现以下不变量：

1. 每次启动或 Reload 创建一个独立 Generation。
2. Generation 拥有自己的 Runtime、Container、Modules、Middleware Registry、DB/Redis/MQ Registry 和其他生命周期资源。
3. 新 Generation 完整构建并启动成功后，才允许对新请求发布。
4. 一个请求从进入 Handler 到完成响应，始终绑定同一个 Generation。
5. 旧 Generation 发布为 Retired 后，不再接收新请求，但允许已经取得租约的请求继续执行。
6. 旧请求全部完成、旧后台任务停止之后，才关闭旧 Generation 的 IO 资源。
7. 新 Generation 构建或启动失败时，旧 Generation 继续服务，且新一代已经打开的资源全部回收。
8. 不能通过修改进程级包变量来实现 Generation 切换。

### 3.3 目标结构

建议在 Gofy 内部增加一个 Generation 聚合对象。它是 App 内部实现，不需要直接作为稳定公共 API 暴露。

```go
type generation struct {
    id uint64

    runtime *core.Runtime
    handler http.Handler

    modules []support.Module
    started []support.Module

    // 本代拥有的资源及其关闭逻辑。
    resources []io.Closer

    // 当前正在处理请求的数量和退休状态。
    lease *generationLease

    logger *zap.Logger
    db     *gorm.DB
}
```

`generationLease` 可以采用小型引用计数实现：

```text
Acquire:
    若 generation 已 Retired：拒绝获取
    否则 active++，返回 release 函数

Release:
    active--
    若 Retired 且 active == 0：关闭 drained 信号

Retire:
    标记 Retired
    若 active == 0：立即关闭 drained 信号
```

所有状态变更必须通过同一个锁或经过验证的原子状态机完成，不能用“先读 Retired、再无锁增加引用数”的方式留下竞态窗口。

### 3.4 Handler 发布方式

将当前只保存 `http.Handler` 的 `handlerSwap` 改为保存 Generation 指针，或让 HandlerSwap 持有一个 `atomic.Pointer[generation]`。

请求入口逻辑：

```text
HTTP Request
     |
     v
读取当前 Generation
     |
     v
Acquire Generation Lease
     |
     +---- Acquire 失败（Generation 已退休）
     |          |
     |          v
     |      重新读取当前 Generation
     |
     v
执行该 Generation 的 Handler
     |
     v
Release Lease
```

需要特别处理这个竞态：请求可能已经读取旧 Generation 指针，但还没有成功 Acquire 时，Reload 就完成了发布。旧 Generation 的 Acquire 必须失败，请求重新读取当前 Generation，而不能进入一个已经开始销毁的代际。

### 3.5 Reload 新流程

```text
Reload(ctx)
    |
    v
校验 ReloadSpec
    |
    v
创建 Generation B
    |
    +--> 构建 Logger B
    +--> 构建 DB B
    +--> 构建 Runtime B
    +--> 创建独立 IO Registry B
    +--> 创建 Middleware Registry B
    +--> 构建 Module B
    +--> 注册路由 B
    |
    v
启动 Generation B 的模块
    |
    +---- 任一 Start 失败
    |          |
    |          v
    |      逆序 Stop 已启动模块 B
    |      关闭 B 的全部资源
    |      保留 Generation A
    |      返回错误
    |
    v
原子发布 Generation B
    |
    v
Generation A 标记 Retired
    |
    v
等待 A 的请求租约归零
    |
    v
逆序停止 A 的后台模块
    |
    v
关闭 A 的 IO / DB / Logger 等资源
```

#### 关于 Reload 超时

Reload 的调用 Context 不能成为强制提前关闭旧资源的理由。

建议定义以下行为：

- 新代发布成功代表 Reload 已生效。
- 旧代排空尚未完成时，不得强行关闭旧代资源。
- 如果等待超过 Reload 的等待期限，记录 `generation drain delayed` 日志和指标，旧代继续处于 Retired 状态。
- 旧代最后一个请求释放租约后，由唯一的回收路径 Stop 旧模块并关闭资源。
- 回收逻辑必须幂等，不能由 Reload 超时路径和 Release 路径重复 Stop/Close。
- 需要避免每次 Reload 超时都创建无法跟踪的后台回收 Goroutine；回收任务应归属于 Generation，并能在 App Shutdown 时统一等待。

如果现有 `Reload(ctx, spec) error` API 不适合表达“新代已经发布、旧代仍在排空”，第一阶段不必直接更改公共 API。可以保持 Reload 成功返回，但通过结构化日志、Generation 状态和指标明确说明旧代清理延迟。后续确实需要调用方感知时再考虑增加结果类型。

### 3.6 Demo Container 和 IO 的改造

建议在业务 Demo 的 `app/io` 中引入本代资源集合，例如：

```go
type Resources struct {
    defaultDB *gorm.DB
    dbs       *gofydb.Registry
    redis     *gofyredis.Registry
    // AMQP / Kafka / RocketMQ / UUID 等本代资源
}
```

该对象由每次 `container.NewContainer()` 同时创建，并被本代 `app/io.Module` 和业务模块工厂共同捕获。

建议流程：

```text
reloader.factories(cfg)
       |
       +--> resources := appio.NewResources()
       |
       +--> ioFactory(resources, cfg)
       |
       +--> ctr := container.NewContainer(resources)
                    |
                    +--> ProductModuleFactory
                    +--> OrderModuleFactory
```

`app/io.Module.Start()` 打开全部需要启动即用的资源，并将句柄写入本代 `Resources`。这些句柄在本代开始接收请求之前完成初始化；运行期不再修改。懒加载实例由该代持有的 Registry 管理。

Repository 改为显式持有本代资源访问器：

```go
type OrderRepository struct {
    resources *appio.Resources
}

func (r *OrderRepository) conn(ctx context.Context) *gorm.DB {
    return gofydb.WithContext(ctx, r.resources.DefaultDB())
}
```

Service 开启事务也从同一个本代资源集合获取默认 DB：

```go
err := gofydb.Transaction(ctx, s.resources.DefaultDB(), func(ctx context.Context) error {
    // Product API 和 Order Repository 都从 ctx 复用这一个事务。
    return nil
})
```

Redis、Kafka、RocketMQ 等同理：业务模块不再从 `app/io/redis.Cache` 这类包级变量获取当前连接，而是从构造时注入的本代资源集合获取。

> 注意：上面的结构是方向示例，不要求所有底层 Client 都包装成新的复杂接口。优先让每代 Container 持有现有 `Registry` 实例，并通过构造函数向 Service/Repository 注入即可。

### 3.7 IO Registry 去全局化的配套要求

`support/db`、`support/redis`、`support/pg` 保留实例 API：

```go
registry, err := db.NewRegistry(configs, logger)
conn, err := registry.NewIO(ctx, "order")
err = registry.Close()
```

框架内部和 Demo Container 只使用实例 API。

现有包级便捷入口：

```go
Configure(...)
NewIO(ctx, name)
CloseAllIO()
```

建议在兼容周期内保留，但标注为单 App / 单 Registry 的便捷模式，不再供框架内部使用。可在后续主版本中考虑迁移到独立的 `global` 或 `legacy` 入口；不要在本轮无必要地直接删除公开 API。

### 3.8 Shutdown 流程

正常进程关闭也应复用 Generation 回收逻辑：

1. Nacos 下线注册，停止接收新流量。
2. HTTP Server 开始 Shutdown，不再接收新连接并等待在途请求。
3. 当前 Generation 标记 Retired，等待请求租约归零。
4. 按启动顺序逆序 Stop 后台模块。
5. 关闭该代持有的命名 DB、Redis、MQ、UUID 等资源。
6. 关闭框架拥有的默认 DB。
7. 最后 Flush/Shutdown Telemetry。
8. Sync 自有 Logger 并清理 PID 等文件资源。

模块 Stop 必须保证自身后台 Goroutine 已停止，不能只发出取消信号就立即关闭它依赖的资源。

### 3.9 验收标准

- 同一请求跨越 Reload 前后，访问到的 DB/Redis 资源身份不改变。
- 新请求在 Generation B 发布后只进入 B。
- 旧请求未结束时，Generation A 的资源不能被 Close。
- B 的 Start 失败时，A 仍能正常处理请求，B 创建的资源均被关闭。
- 多次连续 Reload 不出现旧 Registry 覆盖新 Registry 的情况。
- 同一进程创建两个 App 时，两个 App 的命名 IO 不串用。
- `go test -race ./...` 不再报告 IO 句柄读写竞态。
- Shutdown 与 Reload 并发执行时，不出现重复 Stop、重复 Close 或资源泄漏。

---

## 4. P1-02：IO Registry 以实例为主，包级全局仅保留兼容入口

### 4.1 当前实现

当前 DB、Redis、PG Registry 都有类似代码：

```go
var current atomic.Pointer[Registry]
```

`Configure()` / `Install()` 把某个 Registry 设置为当前进程级实例，包级 `NewIO()` 再从该指针取 Registry。

这种做法可以支持单进程单 App 的便利用法，但与 App 级资源隔离目标不一致：同一进程内两个 App 安装不同 Registry 时，后安装者覆盖前者。

### 4.2 目标 API

以实例 API 为主：

```go
reg, err := db.NewRegistry(configs, logger)
if err != nil {
    return err
}

orderDB, err := reg.NewIO(ctx, "order")
if err != nil {
    return err
}

// Generation.Stop 时
return reg.Close()
```

业务模块只能持有自己的 Registry：

```text
App A -> Generation A -> DB Registry A
App B -> Generation B -> DB Registry B
```

### 4.3 实施步骤

1. 为 DB、Redis、PG 分别补齐 Registry 实例 API 的并发测试。
2. 确保 `NewIO()` 只操作当前 Registry 实例，不读取包级 `current`。
3. 将 Demo 的 `app/io` 改为保存每代 Registry 指针。
4. 业务仓库和模块从 Container/Resources 获取 Registry，不通过包级 `NewIO()`。
5. 现有全局 API 暂时保留并标记兼容用途。
6. 文档明确：包级入口不承诺多 App 隔离，不应用于 Reload 资源切换。
7. 在新版本发布说明中列出将来可能废弃的全局入口。

### 4.4 Registry 并发初始化

当前 `Registry.NewIO()` 在持有 Registry 全局 Mutex 时执行连接创建与 Ping。一个实例首次连接慢，会阻塞同 Registry 其他名称的首次连接。

建议改为按实例名称单独协调初始化：

- 同名实例并发首次调用只创建一个连接。
- 不同名称的连接初始化可以并行。
- 初始化失败后不缓存一个伪成功结果；后续调用可以重试。
- `Close()` 与正在初始化的 `NewIO()` 之间有明确的关闭语义。
- Close 开始后，不允许新的实例连接创建成功并遗留在已关闭 Registry 中。

可以采用每个实例一个初始化槽或 `singleflight`，不需要引入额外的连接池框架。

### 4.5 验收标准

- 两个 Registry 可在同一进程并行创建相同名称的实例，互不影响。
- 同一个名称并发 100 次首次访问，只发生一次实际连接初始化。
- 两个不同名称的初始化可以并发进行。
- Registry.Close 与 NewIO 并发时没有数据竞争、资源泄漏或关闭后仍返回新连接的问题。

---

## 5. P1-03：修正 Nacos Resolver 与 Client 的地址选择职责

### 5.1 当前问题

相关文件：

- `gofy-interface-service/apicore/client/client.go`
- `gofy-interface-service/apicore/client/call.go`
- `gofy-interface-service/apicore/client/retry.go`
- `gofy-interface-service/apicore/client/nacos/resolver.go`

Nacos Resolver 已经缓存实例列表，并在每次 `Resolve()` 时按 random / round_robin / weight 选择实例。

但 Client 还额外缓存 Resolver 返回的单个 BaseURL：

```go
const resolveTTL = 30 * time.Second
```

因此一个长期复用的 Client 在地址缓存有效期内会一直请求同一个实例，Resolver 的轮询策略无法按每次请求发挥作用。

此外，对 502、503、504 的响应重试当前不会清除 Client 地址缓存，所以重试可能继续访问刚刚返回错误的实例。

### 5.2 职责划分

目标是只保留一层“实例列表缓存”：

```text
Client.Call
   |
   v
Resolver.Resolve(service)
   |
   +--> Resolver 内部实例列表未过期：直接使用缓存列表
   |
   +--> 列表过期：向 Nacos 查询并刷新
   |
   v
按 random / round_robin / weight 选择实例
   |
   v
返回本次请求的 BaseURL
   |
   v
Client 发起 HTTP 请求
```

具体要求：

- Client 不长期缓存 Resolver 返回的某一个实例地址。
- Nacos Resolver 继续负责实例列表 CacheTTL 和 StaleTTL。
- 固定 BaseURL 模式仍然直接访问固定地址，不引入 Nacos 查询。
- 每次重试都重新执行 Resolve，以便重新进行实例选择。
- 网络错误、502、503、504 等符合当前重试规则的故障，重试时不能被 Client 的旧地址缓存固定到同一实例。
- 不承诺 random 策略每次都选到不同节点；如要排除失败节点，需要独立、明确的排除机制，不能靠随机结果碰运气。

### 5.3 Client 修改

建议：

1. 删除 `Client.cache`、`resolved`、`resolveTTL`、`invalidate()` 这层单实例地址缓存。
2. `base(ctx)` 在使用 Resolver 时直接调用 `Resolver.Resolve(ctx, service)`。
3. 保留固定 BaseURL 的快速路径。
4. 对 Resolver 返回的 BaseURL 做 `url.Parse` 和 Scheme/Host 校验，只允许 `http` / `https`，避免错误的 Nacos Metadata 形成不可用目标地址。
5. 对重试路径进行检查，确认每次重试确实重新执行 Resolver。
6. Nacos Resolver 的实例列表缓存继续维持原有职责。

### 5.4 Nacos Resolver 并发刷新

当前 TTL 过期时，多个并发请求可能同时执行 `ListInstances()`。

这不是当前功能错误，但在高 QPS 服务中会在 CacheTTL 边界形成 Nacos 查询尖峰。

P2 可增加按 ServiceName 的刷新合并：

- 同一 ServiceName 同一时间最多一次刷新。
- 其他请求可使用仍在 StaleTTL 范围内的旧列表，或等待刷新结果，策略需要明确。
- 刷新失败时保留上一次成功实例列表，直到超过 StaleTTL。
- Nacos 明确返回“没有健康实例”时，不应无条件把不健康旧列表当作正常可用节点继续返回。

### 5.5 重试语义

当前 Client 对 GET、HEAD、OPTIONS、DELETE 自动允许重试；POST、PUT、PATCH 只有请求携带 `Idempotency-Key` 时才允许重试。

需要补充一条文档约束：**请求头存在 Idempotency-Key，并不自动代表服务端已经实现幂等去重。**

建议：

- 只有服务端对该 OperationID 实现了幂等键记录/去重，契约才将其标记为可安全重试。
- 在现有 API 不破坏兼容性的前提下，后续可增加明确的 `Retryable` / `Idempotent` 契约元数据。
- 本轮先完善文档和测试，不要把所有 POST 默认纳入重试。
- `Retry-After` 当前对 429 主要按整数秒解析；HTTP-date 格式支持可以作为后续 P2，且需要限制最大等待时间。

### 5.6 验收标准

通过可控 Resolver 单测验证：

- round_robin 下，同一 Client 连续调用能够依次命中多个实例。
- Client 不再把第一个解析结果固定 30 秒。
- 固定 BaseURL 模式完全不调用 Resolver。
- 首次实例连接失败后重试会重新 Resolve。
- 502/503/504 触发重试时会重新 Resolve。
- Resolver 返回非法 Scheme 或空 Host 时立即返回明确错误。
- Nacos 列表未过期时不会重复访问 Nacos。
- Nacos 故障时在 StaleTTL 内可使用最后一次成功列表，超过 StaleTTL 后返回错误。

---

## 6. P1-04：数据库日志必须默认避免记录展开后的 SQL 参数

### 6.1 当前问题

相关文件：

- `gofy/support/db/db.go`
- `gofy/support/pg/pg.go`

MySQL 和 PG 的自定义 GORM Logger 均包含 `parameterized` 字段，但当前构造路径没有看到它被设置为 `true`。

`Trace()` 使用 GORM 的 SQL 回调拿到 SQL 文本，再将 `sql` 写入 zap 字段。当前默认慢 SQL 阈值为 200ms，日志级别为 warn；慢 SQL 可能因此记录实际展开的参数值。

例如查询条件中的手机号、邮箱、身份证号、业务 Token 等可能进入日志。

### 6.2 目标策略

生产默认日志不应包含展开后的参数值。

建议按以下规则处理：

1. MySQL 与 PostgreSQL 默认使用参数化 SQL 日志策略。
2. 自定义 Logger 不将带参数的完整 SQL 写入默认日志。
3. 慢 SQL 保留耗时、影响行数、数据库实例名、调用组件等诊断信息。
4. 如需要 SQL 文本，必须明确使用不含参数值的模板，不能误将展开 SQL 当作模板。
5. 错误日志不打印 DSN 原始密码。

### 6.3 实施步骤

- 先编写日志捕获测试，确保给定敏感参数不会出现在日志中。
- 检查 GORM 当前版本自定义 Logger 的 Trace 行为。
- 对 MySQL、PG 的 Logger 统一默认策略，不要一个修好另一个遗漏。
- 如果现有 `parameterized` 字段仅表示隐藏 SQL，可统一命名和注释，避免误导为“保留 SQL 占位符”。
- 避免在日志中输出完整请求参数或凭证。

### 6.4 验收标准

使用类似下面的参数执行慢查询和数据库错误：

```text
phone = 13800000000
email = secret@example.com
token = test-sensitive-token
```

断言完整日志中不包含这些真实值，但仍包含 SQL 慢查询耗时、错误类型、实例名称等必要诊断字段。

---

## 7. P1-05：明确 Kafka Consumer 的提交语义

### 7.1 当前问题

相关文件：`gofy/support/kafka/consumer.go`

当前 `DisableAutoCommit` 默认 `false`，意味着默认打开 Auto Commit。代码注释也说明 Auto Commit 可能提交已 Poll 的 Offset，即便对应记录处理失败，也可能发生提交。

但 Consumer 的默认错误处理文档又将失败不提交、重启后重投描述为 At-least-once。两者的默认语义不一致。

### 7.2 目标语义

建议把“消息消费失败是否允许丢失”明确为一个可见配置，而不是让使用者从 `DisableAutoCommit` 的反向布尔值猜测。

建议下一主版本优先采用：

```text
默认：Manual Commit
成功处理：提交成功记录的 Offset
处理失败：不提交失败记录及其后续未完成记录
显式启用 Auto Commit：由调用方接受可能提交未完成记录的风险
```

### 7.3 兼容策略

因为从 Auto Commit 改为 Manual Commit 会改变默认行为，不能不加说明地放入一个已有版本的补丁更新。

可采用以下任一种方式：

**方式 A：增加明确枚举**

```go
type CommitMode string

const (
    CommitModeManual CommitMode = "manual"
    CommitModeAuto   CommitMode = "auto"
)
```

配置未指定时，在下一主版本默认 Manual；旧配置可通过兼容读取规则维持原行为，并输出弃用提示。

**方式 B：保留旧布尔值并改变下一主版本默认值**

保留 `DisableAutoCommit`，但文档明确默认值在主版本切换时调整。此方式改动较少，但表达能力和可读性不如枚举。

更推荐方式 A，避免双重否定布尔配置。

### 7.4 必须覆盖的测试

- 一个 Partition 内前两条成功、第三条失败：只提交第二条成功记录的 Offset。
- `DiscardOnError=false`：失败记录不提交，Consume 返回错误。
- `DiscardOnError=true`：按明确策略跳过毒消息，并提交允许跳过的 Offset。
- 多个 Partition 同时返回数据：各 Partition 的提交互不污染。
- Commit 失败：通过 OnError 暴露错误；不得误报提交成功。
- Context 取消与 Rebalance 期间不提交已经失效的记录。

---

## 8. P1-06：修正 gofy-interface-service 的 Go Module 发布路径

### 8.1 当前问题

当前 `gofy-interface-service/go.mod` 的 Module 声明是：

```go
module gofy/gofy-interface-service
```

但仓库地址是：

```text
https://github.com/bingcool/gofy-interface-service
```

同时 go.mod 使用本地 replace：

```go
replace github.com/bingcool/gofy => ../gofy
```

仓库内部也大量使用 `gofy/gofy-interface-service/...` 作为导入路径。

这适合本地并排开发，但与业务项目通过 GitHub Module 路径独立依赖的目标不一致。

### 8.2 目标

正式发布的 Module 路径与业务项目导入路径统一：

```go
module github.com/bingcool/gofy-interface-service
```

框架依赖使用正式版本：

```go
require github.com/bingcool/gofy vX.Y.Z
```

开发时的本地依赖通过 `go.work` 或开发者本地 replace 管理，不将个人目录关系作为正式发布依赖。

### 8.3 实施步骤

1. 确认正式仓库地址和未来是否使用自定义 Vanity Import Domain。
2. 统一修改 `go.mod` Module 路径。
3. 全仓更新内部 import 路径。
4. 将 Gofy 依赖固定到已经发布、经过 CI 验证的版本。
5. 删除正式发布 go.mod 中的本地相对路径 replace。
6. 本地开发使用 `go.work` 同时引用两个仓库。
7. CI 分别验证：独立下载依赖构建，以及 Workspace 本地联调构建。
8. 发布首个正式版本标签，并在业务 Demo 中用真实版本依赖验证。

### 8.4 验收标准

- 一个全新目录中的 Go 项目只通过 `go get github.com/bingcool/gofy-interface-service/...` 即可下载和编译。
- 不需要仓库旁边存在 `../gofy`。
- `go mod tidy` 不会重新写入本地 replace。
- Gofy 和 Contract 的版本组合有明确的兼容测试记录。

---

## 9. P2-01：完善契约认证声明与服务端绑定校验

### 9.1 当前约定

`endpoint.Spec.Auth` 目前主要影响 OpenAPI 文档，不会自动安装认证中间件。实际认证必须通过 `Group.Use("auth")` 或路由级 Middleware 显式绑定。

将认证实现与 Contract 解耦是正确的，但只靠开发者记忆容易产生“文档说需要认证，实际没有安装认证”的安全配置错误。

### 9.2 优化方案

在 `BindOptions` 中增加可选的认证校验回调，而不是硬编码 JWT 或某个别名：

```go
type AuthValidator func(
    spec endpoint.Spec,
    declaredMiddleware []string,
) error
```

BindGroup/BindAll 完成中间件声明解析前后，调用 AuthValidator：

- `AuthNone`：不强制要求认证中间件。
- `AuthRequired`：必须满足应用配置的认证策略。
- `AuthOptional`：必须满足应用配置的“可选认证”策略，不能简单等同 Required。

业务可以通过回调判断分组和路由声明中是否存在认可的认证策略，也可以根据不同 OperationID 使用不同规则。

默认是否强制检查需要考虑兼容：第一阶段可在生产配置中启用严格模式，并给未通过的路由明确启动错误；后续主版本再考虑默认严格。

### 9.3 验收标准

- AuthRequired 但没有认证策略时，严格模式启动失败。
- 分组级认证和路由级认证均可识别。
- 自定义认证中间件可通过回调认可，不强制依赖 `jwt-auth`。
- AuthNone 不被误判。
- OpenAPI Security 与实际服务端策略之间有集成测试。

---

## 10. P2-02：修正 OpenAPI AuthOptional 的语义

### 10.1 当前问题

`docgen/operation.go` 对 `AuthRequired` 和 `AuthOptional` 都生成同一种 Security Requirement。

但 Optional Auth 的语义应当是：提供凭证时进行校验，没有凭证也允许访问。当前单独的 Security Requirement 会让 OpenAPI 使用者误认为必须携带凭证。

### 10.2 目标

- AuthRequired：Security 中必须存在对应认证方案。
- AuthOptional：既允许使用认证方案，也允许匿名访问。
- AuthNone：不生成认证要求。

OpenAPI Optional Auth 应表达为“有认证方案的 Security Requirement”与“空 Security Requirement”二选一，具体结构以 OpenAPI 3.0.3 规范验证为准。

### 10.3 验收标准

- 分别为 Required、Optional、None 生成 OpenAPI。
- 使用 kin-openapi Validate 验证文档合法。
- 对生成的 JSON 做语义断言，而不是只比较文件文本。

---

## 11. P2-03：明确流式接口的实现边界

### 11.1 当前现状

Contract 已经定义 `KindSSE`、`KindChunked`、`KindDownload`，Client 也有对应消费能力，但当前 Gofy Adapter 的 `BindAll` 明确只支持 JSON，非 JSON 类型会在启动绑定时返回错误。

这不是 JSON API 的运行故障，而是契约声明范围大于当前服务端 Adapter 的实现范围。

### 11.2 本轮处理

本轮不要求一次性补齐所有流式接口。首先做清晰的能力声明：

1. 文档明确当前 Gofy Adapter 仅支持 JSON 和 NoContent。
2. 对 SSE、Chunked、Download 继续在启动期 fail fast，不允许静默注册成 JSON。
3. Client 的 StreamSSE/Download/Chunked 测试继续保留，不能将 Client 能力误写成服务端端到端支持。
4. 未来实现某种 ResponseKind 时，必须同时补齐 Adapter、请求取消、写超时、响应头、错误处理和集成测试。

### 11.3 将来增加流式支持的验收点

- 客户端取消时服务端写流能够退出。
- 服务端错误不会在已经开始写流后尝试写第二个 JSON Envelope。
- WriteTimeout 和长连接配置有明确约定。
- Trace Span、RequestID 和 AccessLog 能记录流式请求的结束原因与持续时间。
- Download 的 Content-Disposition、文件名和 Content-Type 有明确安全策略。

---

## 12. P2-04：消除 OTel 非预期全局副作用

### 12.1 当前问题

`support/otel/provider.go` 的 `New()` 会调用全局 `otel.SetErrorHandler()`；而 `Config.Global` 主要控制全局 TracerProvider 和 Propagator 的安装。

因此 `Global=false` 时仍会修改进程级 ErrorHandler，与“默认不使用全局状态”的配置意图不完全一致。

### 12.2 优化方案

- `Global=false` 时 Provider 不修改全局 ErrorHandler、TracerProvider 或 Propagator。
- `Global=true` 时才安装所有进程级 OTel 默认项。
- 优先用 Provider 自己持有的错误处理回调记录导出问题，减少全局错误处理器覆盖。
- 不承诺在一个进程里安全地同时安装多个互相争夺全局 OTel Provider 的 App；该限制要写入文档。
- Shutdown 只关闭本 Provider 拥有的 SDK Provider/Exporter，不错误关闭其他 Provider。

### 12.3 验收标准

- Global=false 创建 Provider 前后，进程全局 ErrorHandler/TracerProvider/Propagator 不发生变化。
- 两个独立 Provider 可以在同一进程内独立创建和 Shutdown。
- Global=true 行为有单测覆盖，并明确全局所有权规则。

---

## 13. P2-05：Gofy 主仓库补齐 CI 质量门禁

### 13.1 目标

参考 `gofy-interface-service` 现有 CI，Gofy 主仓库应建立独立、稳定的质量门禁。至少覆盖：

- Go 格式检查。
- `go build ./...`。
- `go vet ./...`。
- `go test -race -count=1 ./...`。
- 示例服务独立 module 构建和测试。
- Contract/Adapter 本地联调兼容检查。

### 13.2 推荐矩阵

根据实际支持的 Go 版本设置 matrix，不必为了矩阵数量牺牲构建速度。至少覆盖 Linux；如果项目明确支持 macOS 开发和运行，则保留 macOS CI。

### 13.3 Reload 专项测试

除了普通单测，增加以下并发集成测试：

1. 一个请求在旧 Generation 执行期间暂停。
2. 触发 Reload，发布新 Generation。
3. 新请求确认进入新 Generation。
4. 旧请求继续执行并确认仍使用旧资源。
5. 旧请求释放租约后确认旧模块 Stop、旧 Registry Close。
6. `go test -race` 检查整个过程。

---

## 14. P2-06：中间件顺序和 Recovery 覆盖范围审计

当前 `buildHTTPWith()` 的内建链路包括 RequestID、Telemetry、HeaderPropagation、安全响应头、AccessLog、CORS、Recovery、BodyLimit，以及应用自定义 Middleware。

Recovery 是否能够覆盖前置中间件中的 Panic，取决于它在 Gin 链中的实际位置。当前 Recovery 安装在一部分内建中间件之后，因此它不能自动恢复在它之前执行的中间件 Panic。

建议：

1. 明确每个内建中间件的“必须在谁之前/之后”约束。
2. 保证 Recovery 覆盖绝大多数可能 Panic 的下游中间件和业务 Handler。
3. 保持 AccessLog 能记录 Recovery 处理后的最终状态码。
4. 对请求 ID 和 Trace ID 的可观测性做回归测试。
5. 不要只凭注释调整顺序，必须通过 Gin 中间件顺序测试验证实际执行栈。

此项不需要大规模重构，只需整理 `buildHTTPWith()` 的中间件装配顺序并补测试。

---

## 15. 其他暂不优先重构的事项

### 15.1 Adapter 反射调用

`gofy-interface-service/apicore/adapter/gofy/bind.go` 在启动绑定阶段反射查找方法、校验签名，并将 Method 缓存在 Handler 闭包中。请求时使用 `reflect.Value.Call()`。

当前反射查找主要发生在启动阶段，请求时仍有反射调用开销，但尚无基准测试证明它是当前瓶颈。

建议：

- 保留当前实现。
- 补充 `Benchmark`，覆盖普通泛型 Handler 与 Contract Adapter。
- 只有当反射成本达到实际 QPS 的显著比例时，再考虑生成静态 Binding Code。
- 不在本轮为减少少量反射而增加复杂代码生成维护成本。

### 15.2 Repository 接口

当前 Demo Repository 使用具体类型，并通过 SQLite 运行真实 SQL 测试。该方向可以保留。

不要求所有 Repository 都定义接口。需要替换的外部边界或跨模块调用才定义接口，例如 `innerapi.ProductAPI`。

### 15.3 事务语义

`support/db.Transaction()` 通过 Context 复用外层事务，符合当前模块共享一个数据库连接的业务模型。

需要在文档中说明：

- 嵌套 Transaction 不创建 Savepoint。
- 内层错误必须返回到外层，否则外层调用方可能选择继续并最终提交。
- 不同 DB 实例、Redis、Kafka、RocketMQ 不会自动加入同一个 GORM Transaction。
- 不要将该能力描述为分布式事务。

### 15.4 缓存和队列等其他组件

本轮不对 `support/cache`、`support/queues` 等组件进行无目标扩展。只有在资源代际管理、Race 测试或实际故障用例暴露问题时才进行针对性修改。

---

## 16. 推荐实施顺序

### 阶段 A：建立测试基线

1. 固定两个仓库的审计 Commit SHA。
2. 运行并记录 `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...`。
3. 单独运行 Demo Service 的构建和测试。
4. 为 Reload、IO Registry、Nacos Client、GORM Logger、Kafka Consumer 补当前行为测试。
5. 记录现有行为，避免修复时把原本正确的功能一起改变。

交付物：测试基线报告、失败项列表。

### 阶段 B：先修正资源代际设计

1. 确认 Generation 的所有权边界。
2. 为 Handler 加入 Generation Lease。
3. 将 Demo Container 和 IO Module 改成每代独立资源集合。
4. Repository/Service 改为注入资源访问器。
5. 去掉框架内部对包级 Registry 当前指针的依赖。
6. 实现旧 Generation 排空后再 Stop/Close。
7. 增加 Reload 成功、失败、超时、Shutdown 竞争测试。

交付物：Generation 资源隔离实现、Race 测试报告。

### 阶段 C：修复服务发现与消息提交语义

1. 删除 Client 对单个 Resolver BaseURL 的 30 秒缓存。
2. 确认每次调用和重试都会执行 Resolver 选择。
3. 为 Nacos Resolver 补轮询和故障测试。
4. 确定 Kafka CommitMode 的兼容方案。
5. 补齐 Partition、失败消息、提交失败等测试。

交付物：Client/Resolver 测试报告、Kafka 提交语义说明。

### 阶段 D：安全与契约发布

1. MySQL/PG GORM Logger 默认隐藏展开参数。
2. 修正 `gofy-interface-service` Module 路径。
3. 用真实版本依赖构建一个独立业务 Demo。
4. 加入 AuthValidator 严格校验入口。
5. 修正 AuthOptional 的 OpenAPI Security 结构。
6. 明确流式 ResponseKind 的支持边界。

交付物：正式可下载的 Contract Module、OpenAPI 验证报告、安全日志测试报告。

### 阶段 E：质量门禁与收尾

1. 建立 Gofy 主仓库 CI。
2. 增加多 App 同进程测试。
3. 增加 Reload 压力与长请求排空测试。
4. 更新架构文档和 Release Notes。
5. 明确弃用 API 和下一主版本行为变化。

交付物：CI、架构优化验收清单、版本发布说明。

---

## 17. 验收矩阵

| 模块 | 验收项目 | 通过标准 |
|---|---|---|
| Generation | Reload 中旧请求继续使用旧资源 | 资源身份稳定，无跨代访问 |
| Generation | 新请求进入新 Handler | 发布后新请求不再进入旧代 |
| Generation | 旧请求未结束时资源不关闭 | Close 发生在 Lease 归零之后 |
| Generation | 新代启动失败 | 旧代继续服务，新代资源回收 |
| Generation | Reload 与 Shutdown 并发 | 无重复 Stop/Close、无数据竞争 |
| Registry | 两个 App 使用不同 Registry | 不串用连接和配置 |
| Registry | 同名并发首次初始化 | 只创建一个连接 |
| Registry | 不同名并行初始化 | 不被单个慢连接串行阻塞 |
| Nacos Client | RoundRobin | 一个长期 Client 可按 Resolve 结果轮询多个实例 |
| Nacos Client | 502/503/504 重试 | 重试重新选址，不复用旧 Client 地址缓存 |
| Nacos Resolver | Nacos 暂时不可用 | StaleTTL 内按上一次成功列表服务 |
| SQL Logger | 慢 SQL 与 SQL 错误 | 敏感参数不进入日志 |
| Kafka | Manual Commit | 只提交成功处理的连续 Offset |
| Contract | Module 发布 | 无本地 replace 也可被独立项目下载构建 |
| Contract | AuthRequired | 严格配置下缺少认证策略则启动失败 |
| OpenAPI | AuthOptional | 允许认证和匿名访问两种情况 |
| OTel | Global=false | 不修改进程级 OTel 全局 Provider/Propagator/ErrorHandler |
| CI | 全仓质量门禁 | build、vet、race、示例编译全部通过 |

---

## 18. 风险控制与兼容策略

### 18.1 不一次性删除旧 API

包级 `Configure/NewIO`、`DisableAutoCommit` 等公开配置都有潜在业务使用者。本轮优先提供实例 API 和明确语义，再按主版本节奏废弃旧入口。

### 18.2 Reload 先做测试，再动资源发布

Generation 方案涉及 Handler、Runtime、模块、数据库和消息消费者的生命周期。不能只改包变量就宣称完成。必须先添加能复现跨代请求的集成测试，再逐步替换实现。

### 18.3 不让新功能扩大本轮范围

SSE、Chunked、Download、静态 Binding Code、更多负载均衡策略都不是本轮 P1。先保证现有 JSON/NoContent 和当前基础设施组件的行为可靠。

### 18.4 版本策略

- 不改变 Gofy 与 Contract 的单向依赖关系。
- 不把本地 `replace` 当作正式发布方案。
- 对默认行为变化（Kafka CommitMode）使用清晰的版本迁移说明。
- 对包级全局入口先 Deprecated，再在主版本中处理删除。
- 所有契约字段变更同步更新 Adapter、Client、DocGen 和兼容测试。

---

## 19. 最终架构目标

本轮完成后，希望 Gofy 的核心运行时形成以下结构：

```text
                         App
                          |
                          v
                  Atomic Generation
                          |
          +---------------+---------------+
          |               |               |
          v               v               v
      Runtime         HTTP Handler     ResourceSet
          |               |               |
          |               v               +--> DB Registry
          |          Acquire Lease        +--> Redis Registry
          |               |               +--> PG Registry
          |               v               +--> AMQP Client
          |          Module Routes         +--> Kafka Client
          |               |               +--> RocketMQ Client
          |               v               +--> Other Owned IO
          |          Controller
          |               |
          |               v
          |            Service
          |               |
          |               v
          +----------> Repository
                          |
                          v
                  Generation ResourceSet
```

Reload 时：

```text
Generation A 正在服务
          |
          v
完整创建 Generation B
          |
          v
B 启动成功
          |
          v
原子发布 B
          |
          +---------------------> 新请求使用 B
          |
          v
A 标记 Retired
          |
          v
A 在途请求排空
          |
          v
A 后台模块 Stop
          |
          v
A 资源 Close
```

契约体系保持独立：

```text
            gofy-interface-service
                       |
          +------------+-------------+
          |            |             |
          v            v             v
      Contracts      Client        DocGen
          |            |             |
          v            v             v
      Endpoint      Resolver       OpenAPI
          |
          v
      Gofy Adapter
          |
          v
         Gofy
```

## 20. 最终结论

Gofy 当前不需要推倒重来。现有 `App` 生命周期、模块工厂、Container、Service/Repository 分层、Context 事务传播以及独立 Contract 仓库都值得保留。

下一轮应该把精力集中在三个方面：

1. **资源生命周期闭环：** Reload 不只是替换 HTTP Handler，而是原子发布一个完整的资源 Generation，并在旧请求排空后释放旧资源。
2. **多实例与故障切换语义：** Registry 以实例为主，Nacos 每次解析选择实例，Kafka 提交策略与实际投递保证一致。
3. **契约发布和安全边界：** 正式 Module 路径、认证校验、OpenAPI 语义和 SQL 日志安全形成可验证约束。

建议严格按照“测试基线 → Generation → Registry → Nacos/Kafka → SQL/Contract → CI”的顺序实施。不要在第一阶段同时增加新组件或大幅调整目录结构。

**本方案的完成标准不是新增了多少 API，而是 Reload 后请求不会串用资源、资源不会提前关闭、故障切换符合预期、契约能够独立发布，并且这些行为都有自动化测试证明。**
