# Gofy 与 gofy-interface-service 架构优化实施方案

- 方案版本：v1.1
- 编制日期：2026-11-12
- 复核修订：2026-11-13
- 评审对象：
  - `https://github.com/bingcool/gofy`（`main`，复核基线 `43a7920`）
  - `https://github.com/bingcool/gofy-interface-service`（`main`，复核基线 `2a45feb`）
- 方案性质：基于当前架构的修正与可靠性优化
- 总体原则：**小步改造、保留现有架构、遵循 Go 语言习惯、不按其他框架的功能清单做对等扩展**
- 实施状态：**已落地**
- 实施完成：2026-11-13
- 实施提交：
  - `gofy`：`f992c8b`（2026-11-13 13:26）
  - `gofy-interface-service`：`864e50e`（2026-11-13 13:26）

---

## 0. 复核结论与设计基线

本版逐条对照两个仓库当前源码复核了方案中的结论。设计基线是：**现有 `App / Runtime / ModuleFactory / Module / Router / Container / Contract` 结构不变，本轮只做修正与收敛；吸收成熟框架在生命周期、模块、IO、连接池和组件管理上的思想，但 API 按 Go 习惯表达，不追求功能对等。**

### 0.1 复核后仍然成立的问题

| 问题 | 复核依据 |
|---|---|
| Reload 只原子替换 Handler，旧模块紧接着被 Stop，没有在途请求排空 | `reload.go`：发布新 engine 后直接 `stopModuleList(oldStarted)` 并关闭旧 DB |
| Client 侧对 Resolver 结果做 30 秒单地址缓存，削弱轮询 | `apicore/client/client.go`：`resolveTTL = 30 * time.Second` |
| 502/503/504 重试不清除 Client 地址缓存 | `apicore/client/call.go`：仅网络错误走 `invalidate()` |
| GORM 自定义 Logger 的 `parameterized` 字段从未被置位，慢 SQL 会记录展开参数 | `support/db/db.go`、`support/pg/pg.go` |
| Kafka `DisableAutoCommit` 默认 `false`（自动提交），与注释宣称的 at-least-once 不一致 | `support/kafka/consumer.go` |
| Contract Module 路径为 `gofy/gofy-interface-service`，依赖 `github.com/bingcool/gofy v0.0.0` + 本地 replace | `gofy-interface-service/go.mod` |
| `AuthRequired` 与 `AuthOptional` 生成相同的 OpenAPI Security | `docgen/operation.go`：`hasAuth := AuthRequired || AuthOptional` |
| OTel `SetErrorHandler` 不受 `Global` 控制 | `support/otel/provider.go` |
| Registry 首次连接在 Registry 全局 Mutex 内执行，不同实例名之间串行 | `support/db/io.go`、`support/redis/io.go`、`support/pg/io.go` |

### 0.2 复核后需要修正的判断

| 方案原文 | 实际情况 | 本版处理 |
|---|---|---|
| `gofy.New()` 后立即构建 Logger/DB/Runtime/Modules | `New` 只保存选项；构建发生在 `Handler` / `Run` / `Execute(start)` | 修正 §2.1 |
| DB/Redis/PG 的“核心调用”仍依赖进程级全局指针 | 框架默认 DB 用 `gofydb.New` 实例 API；全局指针主要在 Demo 的 `app/io/*.NewIO` 包装和 `support/uuidint` | 修正 §1.1、§4 |
| Demo 的每代 IO 资源集合尚未建立 | `app/io/*` 已有每代 `Set`、`Install()`、带锁写入与防御式 `Close()`；每次 Reload 也新建 Container | 修正 §3.6：缺的是排空与请求级固定 |
| 建议 Repository 构造函数注入本代资源 | 与已落地的“无状态 Repository + Service 持有依赖”决策冲突 | 改为请求级快照（Context 固定），仓库保持无状态 |
| SQL 日志修复方式是把 SQL 置空 | 置空会丢失 SQL 诊断信息 | 改为参数化 SQL 模板输出 |
| Kafka 默认值变更应放到下一主版本 | 两个仓库均无 release tag（pre-1.0） | 现在直接改默认值 + CHANGELOG |
| Gofy 主仓库应补齐 CI | GitHub Actions 已在 `49d929b`（2026-11-13）主动删除，门禁在 `make check` | 修正 §13：默认不恢复 CI |
| Recovery 位于内建链中部是缺陷 | 这是有意的顺序：AccessLog 在 Recovery 之前才能记录 5xx | 降级为顺序契约与测试 |

### 0.3 优先级调整

- `P1-06`（Contract Module 路径）保留编号，但性质是**发布阻塞项**，不是运行期可靠性风险。
- `P1-02` 的核心工作从“新建实例 API”改为“内部改用实例 API + 按实例名并发初始化”。
- `P2-06`（中间件顺序）从“缺陷修复”降级为“顺序契约与测试补齐”。

### 0.4 实施结果（2026-11-13）

本方案已按 §16 的阶段 A–E 实施并提交；除 P1-06 的发布动作（打 tag、固定正式版本、移除 `replace`）外全部落地，两个仓库的回归均通过。

| 事项 | 状态 | 落地说明 |
|---|---|---|
| P1-01 Reload 代际隔离 | 已落地 | HTTP Handler 按 Generation 发布；请求持有 lease，旧代在途请求排空后才 Stop/Close；新代启动失败不影响旧代；Reload 与 Shutdown 串行化 |
| P1-01 Demo 请求级 IO 快照 | 已落地 | `app/io` 用不可变 `Snapshot` + `atomic.Pointer` 发布；入口中间件把快照固定进 context，仓储与服务在一次请求内只使用同一代句柄 |
| P1-02 Registry 实例 API | 已落地 | DB/PG/SQLite/Redis/AMQP/Kafka/RocketMQ 内部统一走实例 API；包级入口仅作兼容；同名并发首次初始化合并为一次，不同名并行 |
| P1-03 Client 地址选择 | 已落地 | 删除 Client 侧 30 秒单地址缓存；每次调用与重试都重新 Resolve，502/503/504 同样触发重新选址；Nacos Resolver 支持并发刷新与 stale 列表 |
| P1-04 SQL 日志参数化 | 已落地 | GORM Logger 实现 `ParamsFilter`，慢查询与错误日志输出占位符模板，不展开真实参数 |
| P1-05 Kafka 提交语义 | 已落地 | 新增 `CommitMode`，默认 manual；`DisableAutoCommit` 标记弃用；补充分区、失败消息与提交失败测试 |
| P1-06 Contract Module 路径 | 部分落地 | 模块路径已改为 `github.com/bingcool/gofy-interface-service`，两仓库导入全部同步；打 tag、固定正式版本、移除 `replace`、独立下载验证属于发布动作，尚未执行 |
| P2-01 认证声明校验 | 已落地 | 契约显式声明 `auth` 中间件；`AuthValidator` 在启动期校验，缺失即报错 |
| P2-02 OpenAPI Optional Auth | 已落地 | `AuthOptional` 生成允许认证与匿名两种情况的 Security 结构 |
| P2-03 流式接口边界 | 已落地 | 文档化既有 `ResponseKind` 与服务端实现的边界，本轮不新增流式能力 |
| P2-04 OTel 全局副作用 | 已落地 | 默认 `Global=false` 不触碰进程级 Provider/Propagator/ErrorHandler；导出失败经注入 logger 去重上报 |
| P2-05 质量门禁 | 已落地 | `make check` 覆盖 fmt、vet、race、示例构建/测试、导入守卫与 cgo-free；示例模块增加 `go vet` |
| P2-06 中间件顺序契约 | 已落地 | `buildHTTPWith` 记录完整顺序与位置理由；测试固定可见顺序、panic 响应与访问日志、Recovery 覆盖边界，以及 Reload 失败/并发场景 |

回归结果：

- `gofy`：`make check` 退出码 0（含 race 测试、demo 构建与 cgo-free 构建）。
- `examples/demo-service`：`go build ./...`、`go vet ./...`、`go test -race ./...` 通过。
- `gofy-interface-service`：`make check` 的 fmt、vet、test-race、refcheck 通过；`golden` 重新生成的 OpenAPI 产物与提交版本无差异。

---

## 1. 方案目标与范围

### 1.1 总体目标

Gofy 当前已经具备独立 Go 框架库的基本形态，本次不推翻 `App / Runtime / ModuleFactory / Module / Router` 体系，也不重新设计业务分层。改造重点是把现有设计中尚未闭合的资源生命周期、并发隔离和服务发现语义补齐，使框架在生产环境下具备可验证的行为。

本方案主要解决：

1. Reload 时新旧模块、请求和基础设施资源没有完整的代际隔离：Handler 已原子替换，但在途请求没有排空，旧模块紧接着就被 Stop。
2. DB、Redis、PG 的包级便捷入口仍通过进程级全局指针解析实例；框架内部的 `support/uuidint` 和 Demo 的 `app/io` 包装仍在使用该入口。
3. Nacos Resolver 的负载均衡选择被 Client 侧二次地址缓存削弱。
4. 数据库慢 SQL 日志可能记录展开后的敏感参数。
5. Kafka 默认自动提交与默认错误处理描述的投递语义不一致。
6. `gofy-interface-service` 的 Go Module 路径与仓库正式导入路径不一致（发布阻塞项）。
7. 契约中的认证声明、OpenAPI Optional Auth 语义和流式接口能力边界需要明确。
8. OTel 全局错误处理器、IO 并发初始化及质量门禁需要完善（质量门禁为主仓库本地 `make check`，不是恢复 CI）。

### 1.2 本次不做的事情

- 不重写 Gin，不替换当前 HTTP 框架。
- 不引入 Wire、Fx 等大型 DI 框架；继续使用显式构造和 Container。
- 不把 Gofy 改造成服务网格、服务治理平台或分布式事务框架。
- 不按其他框架的能力清单补齐功能；定时任务、工作流、分布式锁等只有出现真实业务缺口时才单独评估。
- 不强制所有 Repository 都声明接口。
- 不为了消除反射而立即重写整个 Contract Adapter。
- 不把流式 ResponseKind 当作本轮新增能力；只明确既有契约声明与服务端实现之间的边界。
- 不改变 `endpoint.Route` 作为服务端绑定、Client 和 OpenAPI 共用契约源的基本设计。

### 1.3 风险分级说明

本方案的优先级按生产影响定义：

| 等级 | 定义 | 处理要求 |
|---|---|---|
| P0 | 已确认会导致数据错误、安全绕过或进程不可用的直接缺陷 | 修复前不建议进入生产 |
| P1 | 高并发、Reload、故障切换或特定配置下存在明显可靠性风险 | 下一轮优先修复 |
| P2 | 语义、性能、可维护性或工程化风险 | P1 完成后处理 |

本轮源码检查没有执行完整的 `go test -race`、压力测试或真实中间件故障注入，因此不将静态检查中发现的并发风险直接描述为已经在生产复现的事故。下文会区分“源码已确认的行为”和“需要动态测试验证的风险”。

### 1.4 设计原则：在现有架构上做 Go 原生的修正

当前的 `App / Runtime / ModuleFactory / Module / Router`、`support` 组件、Contract 适配层和 Demo 分层已经是既定基线。本方案不重新设计这些结构，只修正已经确认的缺陷和语义缺口。

具体约束：

1. **不按其他框架的功能清单做对等扩展。** 吸收生命周期、模块化、IO 资源、连接池和组件管理的思想即可；不因为别处有某个能力，就在 Gofy 里增加对应入口。
2. **API 形态遵循 Go 习惯。** 显式构造与依赖注入、`context` 传递取消与超时、`error` 返回值、接口在使用方定义、组合优先于继承；不使用隐式全局状态或运行时魔法。
3. **新增能力必须有真实调用方。** 每项改动对应已确认的缺陷、可验证的并发/故障风险，或现有抽象无法表达的语义；没有调用方的“预留能力”不进入本轮。
4. **优先修正而不是新增。** 能用现有 `Module`、`Registry`、`Set`、`Transaction`、`Resolver` 表达的问题，不引入新抽象层。P1-01 的 Generation/Lease 是解决在途请求排空的最小内部状态机，Demo 的 Snapshot 只是既有 `Set` 的只读视图。
5. **框架边界保持单向。** `gofy` 不依赖 Contract，Contract 只有 Adapter 依赖 `gofy`；业务模块之间通过 `innerapi` 协作。
6. **可验证。** 每个 P1 项都必须有并发、故障或集成测试；没有测试证明的行为不写入完成标准。

---

## 2. 当前架构基线

### 2.1 Gofy 核心调用链

```text
业务 main.go
    |
    v
  gofy.New()            // 只保存选项，不构建资源
    |
    +--> WithXXX(...)
    |
    v
 App.Execute(ctx) / App.Run(ctx) / App.Handler()
    |
    v
（首次构建：Logger / Telemetry / DB / Runtime / Modules）
    |
    +--> Logger
    +--> Telemetry Provider
    +--> DB（gofydb.New 实例 API）
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

`New` 之后、`Execute`/`Run`/`Handler` 之前，App 不持有 Logger、DB、Runtime 或 Module；“启动期构建”与“`New` 期构建”必须区分。

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
- `apicore/client` 的核心（Client、Call、Retry）不依赖 Gofy 服务端实现；`apicore/client/nacos` 作为可选 Resolver 实现会引用 `github.com/bingcool/gofy/support/nacos`，这是有意的复用。
- `apicore/adapter/gofy` 是契约仓库中唯一需要依赖 Gofy Web/Router 适配能力的适配层。
- 业务跨模块通过 `innerapi` 接口和领域视图协作，不直接依赖其他模块的 Repository、Model。
- `Module.Register` 只注册路由；`Starter.Start` 初始化后台资源；`Stopper.Stop` 释放后台资源。
- Repository 不拥有全局生命周期，也不持有连接；连接由当前装配代（App/Demo IO Set）拥有，仓库按调用解析。

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

当前 Reload 的大致过程是：构建新 Logger/DB/Runtime/Modules，启动新模块，切换 HTTP Handler，随后立即 `stopModuleList(oldStarted)` 并关闭旧 DB。模块层面的重建已经按代进行（每次 Reload 都新建 Container 与 Module 实例），但 **Handler 切换和旧代回收不是同一个生命周期操作**，中间没有等待在途请求的环节。

Demo 的 IO 并非没有代际概念：`app/io/db`、`redis`、`amqp`、`kafka`、`rocketmq`、`uuidint` 都已有每代 `Set`，`Start` 时构造并 `Install()`，`Close()` 只清理仍指向本代的句柄。这一层可以保留。实际缺口是：

1. `Set.Install()` 在互斥锁内写包级句柄，而 Repository 与 Service 直接无锁读取 `appdb.Db`（例如 `ProductRepository.conn`、`OrderService` 的事务入口），存在数据竞争；需要补一个并发 Reload 测试并用 `go test -race` 确认。
2. 读取发生在每次查询时，一个请求先后执行多次非事务查询时，两次查询可能落在不同的资源代际。
3. 旧模块在新 Handler 发布后立即 Stop，旧 Handler 内尚未完成的请求可能在旧连接被关闭后继续查询，或在旧 Consumer 停止后继续投递。

使用 `support/db.Transaction` 的请求会从 Context 复用同一个事务连接，这能保证事务内部的一致，但不能覆盖未开启事务的查询，也不能解决 Redis、MQ 等其他资源。

因此本节改造分两层：**框架负责 Handler / Runtime / Module / DB / Logger 的租约与排空**；**Demo 负责把每代 IO Set 固定到请求上**。两者都不需要推翻现有结构。

### 3.2 目标语义

必须实现以下不变量：

1. 每次启动或 Reload 创建一个独立 Generation。
2. Generation 拥有自己的 Runtime、Modules、Middleware Registry、框架默认 DB、Logger 和本代需要关闭的资源句柄；业务 IO 的每代 Registry/Set 由业务模块持有并随该代 Module 停止而关闭。
3. 新 Generation 完整构建并启动成功后，才允许对新请求发布。
4. 一个请求从进入 Handler 到完成响应，始终绑定同一个 Generation。
5. 旧 Generation 发布为 Retired 后，不再接收新请求，但允许已经取得租约的请求继续执行。
6. 旧请求全部完成、旧后台任务停止之后，才关闭旧 Generation 及其模块拥有的 IO 资源。
7. 新 Generation 构建或启动失败时，旧 Generation 继续服务，且新一代已经打开的资源全部回收。
8. Generation 切换不能通过修改进程级包变量完成；包级句柄只能作为兼容读取入口，不能作为发布机制。

### 3.3 目标结构

建议在 Gofy 内部增加一个 Generation 聚合对象。它是 App 内部实现，不需要直接作为稳定公共 API 暴露。

```go
type generation struct {
    id uint64

    runtime *core.Runtime
    handler http.Handler

    modules []support.Module
    started []support.Module

    // 框架在本代直接拥有的资源：默认 DB、Telemetry、Logger 等。
    // 业务命名 IO 由模块在 Stop 中关闭，不在这里重复登记。
    db     *gorm.DB
    logger *zap.Logger

    // 当前正在处理请求的数量和退休状态。
    lease *generationLease
}
```

Generation 不持有 Demo 的 Container：Container 是业务装配对象，由业务 ModuleFactory 自行创建；框架只需要保证 Handler、Runtime、Module 与框架资源的代际一致。

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
    +--> 创建 Middleware Registry B
    +--> 构建 Module B（业务模块自行创建本代 Container 与 IO Set）
    +--> 注册路由 B
    |
    v
启动 Generation B 的模块
    |
    +---- 任一 Start 失败
    |          |
    |          v
    |      逆序 Stop 已启动模块 B
    |      关闭 B 的默认 DB / Logger
    |      保留 Generation A
    |      返回错误
    |
    v
原子发布 Generation B（Handler + Generation 指针）
    |
    v
Generation A 标记 Retired
    |
    v
等待 A 的请求租约归零
    |
    v
逆序停止 A 的后台模块（业务 IO Set 在 Stop 中关闭）
    |
    v
关闭 A 的默认 DB / Logger 等框架资源
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

### 3.6 Demo IO 的改造：保留每代 Set，增加请求级固定

Demo 目前的每代 `Set` 已经是正确方向，不需要重写：

```text
app/io/db.Set   { defaultDB, registry, order, product }
app/io/redis.Set{ ... }
app/io/amqp.Set / kafka.Set / rocketmq.Set / uuidint.Set
```

每次 `app/io.Module.Start()` 构造全部 Set，全部成功后统一 `Install()`；`Close()` 只清理仍指向自己的句柄。每次 Reload 也会新建 Container 和 Module 实例。需要补的是**读取方式**，不是集合结构。

目标读取语义分两步：

1. **消除数据竞争**：包级句柄的发布改为不可变快照，用 `atomic.Pointer[Snapshot]` 整体替换，而不是在锁内改多个独立变量。所有读取走快照，不再直接读 `appdb.Db`。
2. **请求级固定（推荐）**：Demo 增加一个应用中间件，在请求进入时读取一次当前快照并写入请求 Context；Repository 与 Service 通过 `appio.FromContext(ctx)` 取连接。这样一次请求内的多次查询始终使用同一代资源，Reload 不会在请求中途切换。

示意（`app/io` 聚合各子包 Set，不引入新的资源框架）：

```go
type Snapshot struct {
    DB     *gorm.DB
    Redis  *redis.Client
    // AMQP / Kafka / RocketMQ / UUID 等本代句柄
}

func FromContext(ctx context.Context) *Snapshot // 请求内固定值
func Current() *Snapshot                        // 带原子读取的兜底
```

Repository 保持无状态，不注入资源：

```go
func (r *ProductRepository) conn(ctx context.Context) *gorm.DB {
    return gofydb.WithContext(ctx, appio.FromContext(ctx).DB)
}
```

Service 开启事务同样从 Context 快照取默认 DB：

```go
err := gofydb.Transaction(ctx, appio.FromContext(ctx).DB, func(ctx context.Context) error {
    // Product API 和 Order Repository 都从 ctx 复用这一个事务。
    return nil
})
```

这样既满足“一个请求绑定一个资源代际”，又不破坏已经落地的“无状态 Repository + Service 持有依赖”决策，也不需要把 Container 类型塞进框架。

> 说明：请求级固定是严格语义；如果业务能接受“同一请求的两次非事务查询可能落在相邻两代”（例如只读展示场景），可以只做第 1 步的原子快照而不加中间件。两种方案都不能保留无锁读取包变量的现状。非 HTTP 路径（脚本、后台消费者）没有请求中间件，`FromContext` 应回退到 `Current()`，脚本模式仍可直接使用 `script.Runtime.DB`。

Demo 现有单测直接给 `appdb.Db` 赋值（`order_repository_test.go`、`product_repository_test.go`、`module_test.go` 等）。改造后这些测试应改为安装一个测试用 `Snapshot`，否则测试仍在无锁写包级变量，`-race` 会继续报告。

### 3.7 IO Registry 去全局化的配套要求

`support/db`、`support/redis`、`support/pg` 的实例 API **已经存在**：

```go
registry, err := db.NewRegistry(configs, logger)
conn, err := registry.NewIO(ctx, "order")
err = registry.Close()
```

本轮不需要新建实例 API，需要做的是把剩余的内部调用切到实例 API。复核结果：

- 框架默认 DB 走 `gofydb.New(ctx, cfg, logger)`，本身不读包级 Registry。
- Demo 的 `app/io/*.Open()` 使用实例 `NewRegistry`/`NewIO`，但 `app/io/*.NewIO()` 包装与 `appredis` 限流器仍调用包级 `NewIO`。
- `support/uuidint` 在每次取号时调用包级 `redis.NewIO` 解析别名，是框架内唯一仍依赖包级 Registry 的组件。

现有包级便捷入口：

```go
Configure(...)
NewIO(ctx, name)
CloseAllIO()
```

建议在兼容周期内保留，但标注为单 App / 单 Registry 的便捷模式，不再供框架内部使用。

`support/uuidint` 已经提供 `WithClient(*goredis.Client)` 和 `WithInstance(name)` 两种来源。Demo 的 `app/io/uuidint` 应改为把本代 Redis Set 的客户端通过 `WithClient` 传入，不再让每次取号都走包级别名解析。`WithInstance` 保留为兼容路径，但其注释中“Reload 后自动使用新连接”的表述应改为说明它依赖最后安装的包级 Registry。不要在本轮无必要地删除公开 API。

### 3.8 Shutdown 流程

正常进程关闭也应复用 Generation 回收逻辑：

1. Nacos 下线注册，停止接收新流量。
2. HTTP Server 开始 Shutdown，不再接收新连接并等待在途请求。
3. 当前 Generation 标记 Retired，等待请求租约归零。
4. 按启动顺序逆序 Stop 后台模块；模块在 Stop 中关闭自己这一代的 IO Set 与后台 Goroutine。
5. 关闭该代持有的命名 DB、Redis、MQ、UUID 等资源（通常已由上一步完成）。
6. 关闭框架拥有的默认 DB。
7. 最后 Flush/Shutdown Telemetry。
8. Sync 自有 Logger 并清理 PID 等文件资源。

模块 Stop 必须保证自身后台 Goroutine 已停止，不能只发出取消信号就立即关闭它依赖的资源。

### 3.9 验收标准

- 同一请求跨越 Reload 前后，访问到的 DB/Redis 资源身份不改变；请求内不会读到下一代句柄。
- 新请求在 Generation B 发布后只进入 B。
- 旧请求未结束时，Generation A 的模块与资源不能被 Stop/Close。
- B 的 Start 失败时，A 仍能正常处理请求，B 创建的资源均被关闭。
- 多次连续 Reload 不出现旧 Set 覆盖或清理新 Set 的情况。
- 同一进程创建两个 App 时，两个 App 的命名 IO 不串用（依赖 P1-02）。
- `go test -race ./...` 不再报告 IO 句柄读写竞态。
- Shutdown 与 Reload 并发执行时，不出现重复 Stop、重复 Close 或资源泄漏。

---

## 4. P1-02：IO Registry 内部统一使用实例 API，包级全局仅保留兼容入口

### 4.1 当前实现

DB、Redis、PG Registry 同时存在两套入口，实例 API 已经可用：

```go
// 实例 API（已存在）
reg, err := db.NewRegistry(configs, logger)
conn, err := reg.NewIO(ctx, "order")
reg.Install()   // 仅把本 Registry 设为包级 current
reg.Close()

// 包级便捷 API（已存在）
db.Configure(configs, logger)
db.NewIO(ctx, "order")
db.CloseAllIO()
```

包级入口内部通过 `var current atomic.Pointer[Registry]` 找到“当前 Registry”。这适合单进程单 Registry 的便利用法，但与装配代级隔离目标不一致：同一进程内两个 Registry 都调用 `Install()` 时，后安装者覆盖前者，包级 `NewIO()` 会解析到错误实例。

复核后的实际状况：

- 框架默认 DB 使用实例 API，不受影响。
- Demo 打开了每代 Registry（`app/io/db.Open` 等），但 `app/io/*.NewIO()` 包装、限流器的 Redis 解析仍走包级入口。
- `support/uuidint` 每次取号都调用包级 `redis.NewIO`，因此天然受“最后安装的 Redis Registry”影响。

### 4.2 目标 API

以实例 API 为主，包级入口只保留给单 App 脚本或兼容调用：

```go
reg, err := db.NewRegistry(configs, logger)
if err != nil {
    return err
}

orderDB, err := reg.NewIO(ctx, "order")
if err != nil {
    return err
}

// Module.Stop / Generation 回收时
return reg.Close()
```

业务模块只能持有自己的 Registry：

```text
App A -> Generation A -> DB Registry A
App B -> Generation B -> DB Registry B
```

### 4.3 实施步骤

1. 为 DB、Redis、PG 分别补齐 Registry 实例 API 的并发测试（同名初始化、Close 竞争）。
2. 保持 `Registry.NewIO()` 只操作自身实例；包级 `NewIO()` 保持现状并标注兼容边界。
3. Demo 的 `app/io/*.NewIO(ctx, name)` 包装改为从 `FromContext(ctx)` 取本代 Registry，缺失时回退 `Current()`，不再转发到包级入口。
4. `support/uuidint` 已有 `WithClient`；Demo 的 `app/io/uuidint` 改用本代 Redis 客户端构建 Generator，`WithInstance` 仅保留为兼容路径并修正注释。
5. 现有全局 API 暂时保留，文档标注“不承诺多 App 隔离，不用于 Reload 资源切换”。
6. 在发布说明中列出将来可能废弃的全局入口。

> 不要为了“去全局化”给每个底层组件再包一层复杂接口。Registry 实例本身就是边界。

### 4.4 Registry 并发初始化

`Registry.NewIO()` 当前在持有 Registry 全局 `r.mu` 的情况下执行连接创建与 Ping，一个实例首次连接慢会阻塞同一 Registry 内其他名称的首次连接（`r.mu.Lock()` 覆盖 lookup + connect）。

建议改为按实例名称单独协调初始化：

- 同名实例并发首次调用只创建一个连接。
- 不同名称的连接初始化可以并行。
- 初始化失败后不缓存“伪成功”结果，后续调用可以重试。
- `Close()` 与正在初始化的 `NewIO()` 之间有明确语义：Close 开始后不得再发布新连接。
- 可以采用每个实例一个初始化槽或 `singleflight`，不引入额外的连接池框架。

这一项是吞吐优化，不影响正确性；可以与 P2 一起排期，但测试必须与 4.3 同期补上。

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

重试循环本身会再次调用解析逻辑，但 30 秒缓存让“再次解析”退化为“再次命中同一地址”，因此问题不在重试次数，而在缓存层的位置。

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

MySQL 和 PG 的自定义 GORM Logger 均包含 `parameterized` 字段，`Trace()` 在该字段为 `true` 时会把整条 SQL 置空，但当前构造路径从未将它设置为 `true`，因此实际行为始终是记录展开后的 SQL。即使把它打开，置空 SQL 也会丢掉模板信息，属于过度处理。

`Trace()` 使用 GORM 的 SQL 回调拿到已经展开参数的 SQL 文本，再写入 zap 字段。当前默认慢 SQL 阈值为 200ms，日志级别为 warn；慢 SQL 因此可能记录实际参数值。

例如查询条件中的手机号、邮箱、身份证号、业务 Token 等可能进入日志。

### 6.2 目标策略

生产默认日志应保留 SQL 模板、去掉参数值。GORM 已经提供现成机制：

- `gorm/callbacks.go` 在写日志前会检测 `db.Logger` 是否实现 `gorm.ParamsFilter`：

  ```go
  // gorm/callbacks.go
  if filter, ok := db.Logger.(ParamsFilter); ok {
      sql, vars = filter.ParamsFilter(stmt.Context, stmt.SQL.String(), stmt.Vars...)
  }
  ```

- `ParamsFilter` 返回 `(sql, nil)` 时，后续 `Dialector.Explain` 无法展开参数，`Trace()` 收到的就是带占位符的模板。

因此不需要把 SQL 置空，也不需要重写 GORM Logger，只需要：

1. 自定义 Logger 实现 `ParamsFilter`，按配置返回 `(template, nil)` 或 `(template, params)`。
2. 默认 `ParameterizedQueries = true`（命名与 GORM 自身 `logger.Config` 对齐，替换当前从未生效的 `parameterized` 字段）。
3. 慢 SQL 日志保留耗时、影响行数、数据库实例名、调用组件，以及带占位符的 SQL 模板。
4. 错误日志不打印 DSN 原始密码。
5. 确实需要完整参数的调试场景，提供显式开关，并在文档中标注为不适用于生产。

### 6.3 实施步骤

- 先编写日志捕获测试：给定敏感参数执行慢查询与数据库错误，断言日志不含真实值、但包含 SQL 模板与诊断字段。
- 让 `support/db` 与 `support/pg` 的 Logger 同时实现 `Trace` 与 `ParamsFilter`，共用一个参数化开关。
- 删除或重命名当前无效的 `parameterized` 字段，避免“看起来已经生效”的误导。
- 检查 `support/otel` 的 GORM plugin 与会话日志路径不会绕过该开关。
- 避免在日志中输出完整请求参数或凭证。

> 说明：GORM 默认 logger 的 `ParameterizedQueries` 只对默认 logger 生效；自定义 Logger 必须自己实现 `ParamsFilter`，这正是当前字段“写了但没生效”的原因。

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

复核结论：**手工提交路径本身是正确的，问题只在默认值和配置表达方式。**

已确认的行为：

- `DisableAutoCommit` 默认 `false`，即默认启用 franz-go 的 Auto Commit。Auto Commit 按周期提交已 Poll 的 Offset，与 Handler 是否成功无关，因此默认配置下“处理失败即重投”并不成立。
- `commit()` 只在 `DisableAutoCommit=true` 且 `ctx` 未取消时调用 `CommitRecords`，所以 Auto Commit 模式下手工提交路径完全不会执行。
- `process()` 的手工提交逻辑已经符合 at-least-once：`DiscardOnError=false` 时先提交此前成功的最后一条记录（`last`）再返回错误；首条即失败时 `last == nil`，不提交；`DiscardOnError=true` 时把失败记录位点一并提交，避免毒消息反复阻塞。
- 每处理完一个 Partition 才提交该 Partition 的 `last`，不同 Partition 的位点互不影响。

不一致之处：

1. `Consume` 的注释把“失败不提交、重启后重投”描述为默认语义，而默认值实际是 Auto Commit。
2. `ConsumerConfig.DisableAutoCommit` 的注释以外部库的默认行为作为依据，没有直接描述本框架的投递语义。
3. 使用者必须通过双重否定的布尔值推断“失败是否可能丢消息”，配置可读性差。

### 7.2 目标语义

把投递语义做成一个正向枚举，配置里直接写明希望的行为：

```text
commit_mode: manual（默认）
  成功处理：提交本条成功记录的 Offset
  处理失败：提交此前的成功位点，失败记录及后续记录不提交
  DiscardOnError=true：跳过毒消息并提交其位点（此时该条按 at-most-once 处理）

commit_mode: auto（必须显式配置）
  由 franz-go 按周期提交已 Poll 的 Offset
  调用方明确接受“处理失败也可能已提交”的风险
```

副作用可重复执行的场景（例如缓存刷新、配置同步）可以选 `auto`；订单、支付等要求不丢消息的消费必须保持默认 `manual`。

### 7.3 实施步骤

两个仓库均未打 release tag（pre-1.0），默认值变更可以直接落地，但必须在 CHANGELOG 中作为行为变更说明。

1. `ConsumerConfig` 增加正向字段，保留旧字段一个周期：

   ```go
   type CommitMode string

   const (
       CommitModeManual CommitMode = "manual"
       CommitModeAuto   CommitMode = "auto"
   )

   type ConsumerConfig struct {
       // CommitMode 决定位点提交方式，零值按 manual 处理。
       CommitMode CommitMode `yaml:"commit_mode"`
       // DisableAutoCommit 已弃用：true 等价于 CommitModeManual。
       // 旧配置中的 false 不再意味着自动提交，自动提交需显式 commit_mode: auto。
       DisableAutoCommit bool `yaml:"disable_auto_commit"`
       // ...
   }
   ```

2. `withDefaults()` 归一化：`CommitMode` 为空时置为 `CommitModeManual`；`DisableAutoCommit == true` 且 `CommitMode` 为空时同样为 manual；两者同时出现且冲突时以显式 `CommitMode` 为准。非法值由 `validate()` 返回错误，沿用现有风格。
3. `clientOptions()` 只在 `CommitModeAuto` 时不追加 `kgo.DisableAutoCommit()`；其余情况追加。
4. `commit()` 的守卫条件从 `!c.cfg.DisableAutoCommit` 改为 `c.cfg.CommitMode != CommitModeAuto`。
5. 更新 `ConsumerConfig`、`Consume` 和 README 的注释：删除以外部库默认行为作依据的表述，改为直接描述 manual/auto 的投递保证；`disable_auto_commit` 标注 deprecated 并指向 `commit_mode`。
6. CHANGELOG 中记录：默认提交模式由 auto 变为 manual；显式写 `disable_auto_commit: false` 的配置需要改用 `commit_mode: auto` 才能保持原行为。

### 7.4 必须覆盖的测试

- 默认配置（不写 `commit_mode`）：前两条成功、第三条失败，Commit 只到第二条成功记录。
- `DiscardOnError=false`：失败记录不提交，`Consume` 返回错误。
- `DiscardOnError=true`：跳过毒消息，失败记录位点被提交。
- 多个 Partition 同时返回数据：各 Partition 的提交互不污染。
- Commit 失败：通过 `OnError` 暴露错误，不得误报提交成功。
- Context 取消与 Rebalance 期间不提交已经失效的记录。
- `commit_mode: auto` 时不调用 `CommitRecords`；非法 `commit_mode` 在 `NewConsumer` 阶段返回错误。

---

## 8. P1-06：修正 gofy-interface-service 的 Go Module 发布路径（发布阻塞项）

本节是发布卫生问题，不是运行期可靠性风险：本地并排开发可以正常工作，但一旦有业务项目按 Go Module 路径依赖 Contract，当前路径就会失败。两个仓库均未打 tag，现在修改的代价最低。

### 8.1 当前问题

当前 `gofy-interface-service/go.mod` 的 Module 声明是：

```go
module gofy/gofy-interface-service
```

但仓库地址是：

```text
https://github.com/bingcool/gofy-interface-service
```

同时依赖声明为：

```go
require github.com/bingcool/gofy v0.0.0

replace github.com/bingcool/gofy => ../gofy
```

仓库内部也大量使用 `gofy/gofy-interface-service/...` 作为导入路径。

这适合本地并排开发，但与业务项目通过 GitHub Module 路径独立依赖的目标不一致；`v0.0.0` 也不是可以被 `go get` 解析的版本。

### 8.2 目标

正式发布的 Module 路径与业务项目导入路径统一：

```go
module github.com/bingcool/gofy-interface-service
```

框架依赖使用正式版本：

```go
require github.com/bingcool/gofy vX.Y.Z
```

Gofy 需要先有首个 release tag。开发时的本地依赖通过 `go.work` 或开发者本地 replace 管理，不将个人目录关系写进正式发布的 go.mod。

### 8.3 实施步骤

1. 确认真实仓库地址，以及是否使用自定义 Vanity Import Domain（不使用则按 GitHub 路径）。
2. 先为 `github.com/bingcool/gofy` 打首个 tag，并确认该版本能让 Contract 通过构建与测试。
3. 统一修改 Contract 的 `go.mod` Module 路径，并全仓更新内部 import。
4. 将 Gofy 依赖从 `v0.0.0` 固定到上一步的 tag。
5. 删除正式发布 go.mod 中的本地相对路径 replace。
6. 本地开发使用 `go.work` 同时引用两个仓库；`go.work` 不提交到需要独立发布的仓库，或提交但保证 CI 不依赖它。
7. 在 Contract 已有的 `.github/workflows/ci.yml` 中增加一次“独立下载依赖”的构建：不检出 `../gofy`，也不使用 workspace。
8. 为 Contract 打首个 tag，并用一个全新的示例项目按 Module 路径依赖验证。

### 8.4 验收标准

- 一个全新目录中的 Go 项目只通过 `go get github.com/bingcool/gofy-interface-service/...` 即可下载和编译。
- 不需要仓库旁边存在 `../gofy`。
- `go mod tidy` 不会重新写入本地 replace。
- Gofy 与 Contract 都有可引用的 tag，版本组合有明确的兼容测试记录。

---

## 9. P2-01：完善契约认证声明与服务端绑定校验

### 9.1 当前约定

`endpoint.Spec.Auth` 目前主要影响 OpenAPI 文档，不会自动安装认证中间件。实际认证必须通过 `Group.Use("auth")` 或路由级 Middleware 显式绑定。

将认证实现与 Contract 解耦是正确的，但只靠开发者记忆容易产生“文档说需要认证，实际没有安装认证”的安全配置错误。

### 9.2 优化方案

在 `BindOptions` 中增加一个**启动期校验回调**，与现有 `BindOptions.RateLimit` 回调同构。它只做声明一致性检查，不实现认证、不新增认证中间件、不引入插件系统：

```go
type AuthValidator func(
    spec endpoint.Spec,
    declaredMiddleware []string,
) error
```

BindGroup/BindAll 完成中间件声明解析后调用 AuthValidator：

- `AuthNone`：不强制要求认证中间件。
- `AuthRequired`：必须满足应用配置的认证策略。
- `AuthOptional`：必须满足应用配置的“可选认证”策略，不能简单等同 Required。

业务通过回调判断分组和路由声明中是否存在自己认可的认证策略，同一回调可以按 OperationID 区分规则。校验失败直接让启动或 Reload 失败并给出具体路由，而不是等运行期才发现。

默认不启用（`AuthValidator == nil` 时跳过），避免影响现有项目；生产环境建议显式配置严格校验。

### 9.3 验收标准

- AuthRequired 但没有认证策略时，配置了 AuthValidator 的启动流程失败，并指出具体路由。
- 分组级认证和路由级认证均可识别。
- 自定义认证中间件可通过回调认可，不强制依赖 `jwt-auth`。
- AuthNone 不被误判。
- 未配置 AuthValidator 时行为与现状完全一致。
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

本轮不新增服务端流式能力，只把现有边界写清楚，这不是功能路线图：

1. 文档明确当前 Gofy Adapter 仅支持 JSON 和 NoContent。
2. 对 SSE、Chunked、Download 继续在启动期 fail fast，不允许静默注册成 JSON。
3. Client 的 `StreamSSE`、`StreamChunked`、`DownloadFile` 测试继续保留，文档不能把 Client 能力写成服务端端到端支持。

### 11.3 将来实现流式支持时的验收点（非本轮工作）

- 客户端取消时服务端写流能够退出。
- 服务端错误不会在已经开始写流后尝试写第二个 JSON Envelope。
- WriteTimeout 和长连接配置有明确约定。
- Trace Span、RequestID 和 AccessLog 能记录流式请求的结束原因与持续时间。
- Download 的 Content-Disposition、文件名和 Content-Type 有明确安全策略。

---

## 12. P2-04：消除 OTel 非预期全局副作用

### 12.1 当前问题

`support/otel/provider.go` 在 `cfg.Enabled` 为真时调用全局 `otel.SetErrorHandler()`；而 `Config.Global` 只控制 `SetTracerProvider` 和 `SetTextMapPropagator`。

也就是说：**启用 tracing 且 `Global=false` 时，Provider 仍然修改了进程级 ErrorHandler**。`support/otel/config.go` 的包注释写着“除非开启 `Global`，否则不拥有全局状态”，与实际行为不一致。

关闭 tracing（`Enabled=false`）时提前返回 noop Provider，不触碰全局状态，这部分没有问题。

### 12.2 优化方案

- `Global=false` 时 Provider 不修改全局 ErrorHandler、TracerProvider 或 Propagator。
- `Global=true` 时才调用 `SetErrorHandler`、`SetTracerProvider`、`SetTextMapPropagator`。
- 导出失败不再依赖全局 ErrorHandler：用一个薄包装的 `sdktrace.SpanExporter` 在 `ExportSpans` 返回错误时直接用 Provider 的 logger 记录（保留现有的一分钟去重逻辑），再把错误返回给 batcher。这样非全局模式下也能看到导出问题，而不用覆盖进程级处理器。
- 修正 `support/otel/config.go` 的包注释，使其与实际所有权一致。
- 不承诺同一进程内可以安全安装多个互相争夺全局 Provider 的 App；该限制写入文档。
- `Shutdown` 只关闭本 Provider 拥有的 SDK Provider/Exporter，不关闭全局 Provider（现有行为已经正确，补测试固定下来）。

### 12.3 验收标准

- `Enabled=true, Global=false` 创建与关闭 Provider 前后，进程全局 ErrorHandler/TracerProvider/Propagator 不发生变化。
- 同一进程内创建两个 `Enabled=true, Global=false` 的 Provider，两者都可用，导出失败分别记录到各自的 logger。
- `Global=true` 时四项全局设置（含 ErrorHandler）被安装，并有单测覆盖。
- 导出失败的日志保留一分钟去重，不因去除全局处理器而丢失。

---

## 13. P2-05：质量门禁以本地 `make check` 为准

### 13.1 现状

Gofy 主仓库没有 GitHub Actions：`.github/workflows` 已在 `49d929b`（2026-11-13）被有意删除。当前权威门禁是根目录 `make check`：

```text
fmt-check → vet → test-race → example → guards → cgo-free
```

`gofy-interface-service` 仍保留 `.github/workflows/ci.yml`。两个仓库的门禁形态不同是有意选择，不是缺陷。

本轮的结论是：**不恢复主仓库 CI；先把 `make check` 补齐并保证本地可重复执行。** 是否需要云端 CI 由维护者单独决策，不作为本方案的交付项。

### 13.2 `make check` 需要覆盖的内容

- Go 格式检查（`gofmt -l`，包含 `examples/demo-service`）。
- `go build ./...` 与 `go vet ./...`。
- `go test -race -count=1 ./...`。
- Demo 作为独立 Module 的 `go build`、`go vet`、`go test -race`；当前 `example` 目标只做了 build 和 test，需要补上 `vet`。
- Cgo-free 构建检查（`CGO_ENABLED=0`）。

如果新增 `support/kafka`、`support/db` 的测试需要外部服务，应默认跳过并用环境变量开启，保证 `make check` 无外部依赖即可通过。

### 13.3 Reload 专项测试

除了普通单测，增加以下并发集成测试：

1. 一个请求在旧 Generation 执行期间暂停。
2. 触发 Reload，发布新 Generation。
3. 新请求确认进入新 Generation。
4. 旧请求继续执行并确认仍使用旧资源。
5. 旧请求释放租约后确认旧模块 Stop、旧 Registry Close。
6. `go test -race` 检查整个过程。

---

## 14. P2-06：中间件顺序契约与 Recovery 覆盖范围

### 14.1 实际顺序

`buildHTTPWith()` 当前的装配顺序（均为可选，按 Options 启用）：

```text
RequestID → Telemetry Trace → HeaderPropagation → SecurityHeaders
→ AccessLog → CORS → Recovery → BodyLimit
→ App Middleware → healthz/readyz → Base Router(RateLimit → JWTAuth)
→ MiddlewareRegistrars → Module Routes
```

这个顺序是有意的，不是缺陷：

- AccessLog 必须在 Recovery 之前，Recovery 处理后的最终状态码才会被 AccessLog 记录。
- Recovery 在 CORS 之后，panic 响应仍带 CORS 头。
- BodyLimit 只包装 `Request.Body`，超限由绑定层映射成 413；它必须位于读取 body 的 handler 之前，当前顺序满足这一点。
- App Middleware 与契约中间件都在 Recovery 之后，因此业务 Handler 和业务中间件中的 panic 会被 Recovery 捕获。

### 14.2 已知边界

Recovery 只能捕获它之后的链路。RequestID、Trace、HeaderPropagation、SecurityHeaders、AccessLog、CORS 自身发生的 panic 不会被这个 Recovery 捕获，最终由 `net/http` 兜底：连接被关闭，客户端拿不到 Envelope。这些内建中间件本身不执行用户代码，风险很低，本轮不为此在最外层再加一个 Recovery——那会改变 AccessLog 的记录语义。

### 14.3 本轮工作

1. 在 `start.go` 的装配处写清楚顺序契约，说明每个中间件为什么在这个位置（现有注释只覆盖了 RequestID/Trace/HeaderPropagation）。
2. 增加一个顺序测试：用一组标记中间件断言实际执行顺序，防止后续调整被静默破坏。
3. 增加一个 panic 集成测试：业务 Handler panic 时返回统一 Envelope，AccessLog 记录最终 5xx，RequestID/Trace 字段仍在。
4. 增加一个边界测试：记录当前“Recovery 之前的中间件 panic 不被捕获”的行为，避免误以为全覆盖。

不移动 Recovery 到链首，也不新增第二层 Recovery。

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

> 本节顺序已按计划执行完毕，逐项结果见 §0.4。

### 阶段 A：建立测试基线

1. 固定审计基线：Gofy `43a7920`、Contract `2a45feb`。
2. 在根目录执行 `make check`，记录结果。
3. 在 `examples/demo-service` 执行 `go mod tidy -diff`、`go vet ./...`、`go test ./...`。
4. 在 `gofy-interface-service` 执行 `go build ./...`、`go vet ./...`、`go test ./...`。
5. 为 Reload、IO Registry、Nacos Client、GORM Logger、Kafka Consumer 补当前行为测试。
6. 记录现有行为，避免修复时把原本正确的功能一起改变。

交付物：测试基线记录、失败项列表。

### 阶段 B：先修正资源代际设计

1. 确认 Generation 的所有权边界（框架拥有的 DB/Logger/Telemetry 与业务 Module 拥有的资源分开）。
2. 为 Handler 加入 Generation Lease，实现旧代排空后再 Stop/Close。
3. 保留 Demo 现有的每代 `Set`，在其上增加不可变 `Snapshot` 与 `atomic.Pointer` 发布。
4. 在 HTTP 入口中间件把当次 `Snapshot` 固定进 `context`，Repository 经 `appio.FromContext(ctx)` 读取；不改为构造函数注入。
5. 把 Demo 的 `app/io/*.NewIO`、限流器和 `uuidint` 改为读取本代实例，内部不再走包级全局指针。
6. 增加 Reload 成功、失败、超时、Shutdown 竞争测试。

交付物：Generation 资源隔离实现、Race 测试报告。

### 阶段 C：修复服务发现与消息提交语义

1. 删除 Client 对单个 Resolver BaseURL 的 30 秒缓存。
2. 确认每次调用和重试都会执行 Resolver 选择；502/503/504 也触发重新选址。
3. 为 Nacos Resolver 补轮询和故障测试。
4. 引入 `CommitMode`，默认 manual，`DisableAutoCommit` 标记弃用。
5. 补齐 Partition、失败消息、提交失败等测试。

交付物：Client/Resolver 测试报告、Kafka 提交语义说明。

### 阶段 D：安全、契约与发布

1. MySQL/PG GORM Logger 使用 `ParamsFilter` 默认输出参数化 SQL 模板。
2. 加入 AuthValidator 启动期校验入口（默认关闭）。
3. 修正 AuthOptional 的 OpenAPI Security 结构。
4. 明确流式 ResponseKind 的支持边界。
5. 修正 `gofy-interface-service` Module 路径，先给 Gofy 打首个 tag。
6. 用真实版本依赖构建一个独立业务 Demo。

交付物：可独立下载的 Contract Module、OpenAPI 验证报告、SQL 日志测试报告。

### 阶段 E：质量门禁与收尾

1. 确认 `make check` 覆盖新增测试并通过。
2. 增加多 App 同进程测试与 Reload 压力/长请求排空测试。
3. 更新架构文档与 CHANGELOG。
4. 列出已弃用 API 与默认行为变化（Kafka 默认提交模式）。
5. 是否恢复主仓库云端 CI，单独评估，不作为本轮交付项。

交付物：架构优化验收清单、版本发布说明。

---

## 17. 验收矩阵

| 模块 | 验收项目 | 通过标准 |
|---|---|---|
| Generation | Reload 中旧请求继续使用旧资源 | 资源身份稳定，无跨代访问 |
| Generation | 新请求进入新 Handler | 发布后新请求不再进入旧代 |
| Generation | 旧请求未结束时资源不关闭 | Close 发生在 Lease 归零之后 |
| Generation | 新代启动失败 | 旧代继续服务，新代资源回收 |
| Generation | Reload 与 Shutdown 并发 | 无重复 Stop/Close、无数据竞争 |
| Demo IO | 请求级快照 | 单次请求从入口到结束只使用同一代 IO；Reload 发生在请求中途也不切换到新代句柄 |
| Registry | 两个 App 使用不同 Registry | 不串用连接和配置 |
| Registry | 同名并发首次初始化 | 只创建一个连接 |
| Registry | 不同名并行初始化 | 不被单个慢连接串行阻塞 |
| Nacos Client | RoundRobin | 一个长期 Client 可按 Resolve 结果轮询多个实例 |
| Nacos Client | 502/503/504 重试 | 重试重新选址，不复用旧 Client 地址缓存 |
| Nacos Resolver | Nacos 暂时不可用 | StaleTTL 内按上一次成功列表服务 |
| SQL Logger | 慢 SQL 与 SQL 错误 | 日志含占位符 SQL 与诊断字段，不含真实参数值 |
| Kafka | Manual Commit 默认 | 只提交成功处理的连续 Offset；自动提交必须显式配置 |
| Contract | Module 发布（发布阻塞项） | 无本地 replace 也可被独立项目下载构建 |
| Contract | AuthRequired | 配置 AuthValidator 时，缺少认证策略则启动失败 |
| OpenAPI | AuthOptional | 允许认证和匿名访问两种情况 |
| OTel | 启用 tracing 且 Global=false | 不修改进程级 OTel 全局 Provider/Propagator/ErrorHandler |
| 质量门禁 | `make check` | fmt、vet、race、示例编译、cgo-free 全部通过 |

---

## 18. 风险控制与兼容策略

### 18.1 不一次性删除旧 API

包级 `Configure/NewIO`、`DisableAutoCommit` 等公开入口都有潜在业务使用者。本轮保留它们并标记弃用：包级入口转发到实例 API，`DisableAutoCommit=true` 等价于 `CommitModeManual`；文档指向新入口，删除另排计划。

### 18.2 Reload 先做测试，再动资源发布

Generation 方案涉及 Handler、Runtime、模块、数据库和消息消费者的生命周期。不能只改包变量就宣称完成。必须先添加能复现跨代请求的集成测试，再逐步替换实现。

### 18.3 不让新功能扩大本轮范围

SSE、Chunked、Download、静态 Binding Code、更多负载均衡策略都不是本轮 P1。先保证现有 JSON/NoContent 和当前基础设施组件的行为可靠。

### 18.4 版本策略

- 两个仓库当前均未打 release tag（pre-1.0），本轮以 `v0.x` 发布；默认行为变化可以直接落地，但必须写入 CHANGELOG。
- 不改变 Gofy 与 Contract 的单向依赖关系。
- 不把本地 `replace` 当作正式发布方案。
- Kafka 默认提交模式由 auto 改为 manual，属于行为变更；`disable_auto_commit: false` 不再等价于自动提交。
- 包级全局入口的弃用标识在本轮完成，删除不设在本轮。
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
      Runtime         HTTP Handler     Framework Owner
          |               |               - db.default
          |               v               - Logger
          |          Acquire Lease        - Telemetry Provider
          |               |               - 框架本代其他资源
          |               v
          |          Module Routes
          |               |
          |               v
          |          Controller
          |               |
          |               v
          |            Service
          |               |
          |               v
          +----------> Repository
                          |
                          v
                 Request IO Snapshot
          （入口中间件固定在 context 的本代 Set）
                          |
        +-----------------+-----------------+
        |                 |                 |
        v                 v                 v
    DB instances     Redis instances    MQ / UUID
        |
        +--> 本次请求的查询与事务只落在同一代连接上
```

业务模块在 `Start()` 中打开本代 IO Set，在 `Stop()` 中关闭；请求不直接读包级句柄，而是读固定在本代 `Snapshot` 上的实例。

Reload 时：

```text
Generation A 正在服务
          |
          v
完整创建 Generation B（Logger/DB/Runtime/Modules/业务 IO Set/路由）
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
A 在途请求排空（Lease 归零）
          |
          v
A 后台模块 Stop（业务在此关闭本代 IO Set）
          |
          v
A 框架资源 Close（默认 DB/Logger/Telemetry）
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

**实施状态：本方案已于 2026-11-13 落地（提交见文首），以下为最终结论与后续演进方向。**

Gofy 当前不需要推倒重来。现有 `App` 生命周期、模块工厂、Container、Service/Repository 分层、Context 事务传播以及独立 Contract 仓库都值得保留。

下一轮应该把精力集中在三个方面：

1. **资源生命周期闭环：** Reload 不只是替换 HTTP Handler，而是原子发布一个完整的资源 Generation，并在旧请求排空后释放旧资源；业务侧用请求级 Snapshot 保证一次请求只使用同一代 IO。
2. **多实例与故障切换语义：** Registry 以实例为主，Nacos 每次解析选择实例，Kafka 提交策略与实际投递保证一致。
3. **契约发布和安全边界：** 正式 Module 路径、认证校验、OpenAPI 语义和 SQL 日志安全形成可验证约束。

实施顺序为“测试基线 → Generation → Client/Resolver 与 Kafka → SQL/契约发布 → 质量门禁收尾”。每个阶段先补测试再改实现，不要在同一阶段同时增加新组件或大幅调整目录结构。

本方案的目标是补齐可靠性缺口，不是把 Gofy 做成另一个框架的功能超集。所有改动都用 Go 的方式表达：显式依赖、`context` 传递、返回 `error`、小接口、组合优先；不引入隐式全局状态和运行时魔法。

**本方案的完成标准不是新增了多少 API，而是 Reload 后请求不会串用资源、资源不会提前关闭、故障切换符合预期、契约能够独立发布，并且这些行为都有自动化测试证明。**
