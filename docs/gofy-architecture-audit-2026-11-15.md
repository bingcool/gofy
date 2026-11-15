# Gofy 最新架构复审与第二轮优化建议

> 审计日期：2026-11-15  
> 审计范围：`bingcool/gofy`、`bingcool/gofy-interface-api`、`bingcool/gofy-adapter`，以及 Gofy 仓库中的 `examples/demo-service` 与 `examples/interface-service`  
> 主要审计基线：Gofy `main` 最新提交 `0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf`  
> 方式：按源码与调用链进行静态审查；没有在目标仓库环境中执行完整 `make check-all`、`go test -race`、真实 MySQL / Redis / MQ / Kubernetes 集成测试。
>
> 2026-11-15 修订：按 [`gofy-architecture-audit-2026-11-15-review.md`](gofy-architecture-audit-2026-11-15-review.md)
> 的逐条代码复核合并修正——P1-02（Lease）降级为 P2-10，原 P1-03～P1-06 上移一位，
> 新增 P2-11（restart/exit 失败后永久摘流），修正 P1-01 / P1-02 / P1-03 的修复方案，
> 并订正 `--group=all` 等事实错误。

---

## 1. 总体结论

**Gofy 当前的总体架构方向是成立的，不需要推倒重来。** 这轮改动已经从一个以 HTTP 为中心的 Gin 框架，逐步形成了一个同时支持 HTTP 服务、常驻 Worker、跨模块业务装配、消息消费和分布式协调的应用运行时。

值得保留的核心结构包括：

1. `App → Runtime → ModuleFactory → Module → Router` 的应用启动与模块注册链路。
2. `Container` 作为业务组合根；每次装配建立独立的 Repository / Service 对象，跨模块能力通过 `innerapi` 接口传递。
3. 请求通过 Context 固定资源快照，Repository / Query 从 Context 解析连接并复用事务。
4. Worker 以分组作为部署单位，使用独立进程、PID、健康探针和优雅停止链路。
5. `support/consumer` 统一处理消息信封、追踪、错误分类和消费台账，底层 Kafka / AMQP / RocketMQ / Redis Queue 适配器专注协议。
6. `claim` 与 `lease` 分别承担数据库任务行认领和分布式互斥协调，不把两种机制混成一个抽象。
7. `gofy-interface-api` 作为中立契约 / Client / OpenAPI 基础库，`gofy-adapter` 承接 Gofy/Gin 和 Nacos 等框架适配。

但当前仍有需要修正的正确性问题，其中 5 项为 P1：

- Reload 时 HTTP Handler 与业务 IO Snapshot 仍不是一次原子发布，旧 Handler 存在使用新 IO 的窗口，失败回滚也可能撤掉旧请求所需的 Snapshot。
- 数据库 Claim 使用的 owner 不是每次认领唯一的 fencing token；旧执行者可能在租约过期并被重新认领后，仍以相同 owner 写回任务结果。
- 消费幂等台账将 `processing` 记录再次认领时，没有阻止并发处理中消息的第二个执行者；完成、失败、dead 写回也没有绑定某一次 claim 的 attempt/token。
- Worker `/healthz` 在 Runner 真正启动前就置为 Ready，可能把尚未建立 broker 订阅的进程报告为可用。
- `gofy-interface-api/client.WithQuery()` 当前仍会因为 nil map 触发 panic。

另有 2 项 P2 与上述问题相邻，建议一并排期：

- HTTP Generation Lease 的释放没有用 `defer`，摘流闸门与 Acquire 之间也有竞争窗口；默认 Recovery 会挡住多数 panic，影响是 drain 超时而非永久泄漏（P2-10）。
- Nacos `restart` / `exit` 的进程动作失败后，摘流闸门不会恢复，进程会永久保持 503 且已从 Nacos 注销（P2-11）。

这些问题都可以在现有架构内小步解决，不需要新增一个大型框架子系统。

### 本轮结论分级

| 级别 | 含义 | 本轮数量 | 建议 |
|---|---|---:|---|
| P1 | 可能破坏资源代际一致性、任务互斥或消息可靠性，或导致运行时明确 panic / 健康状态误报 | 5 | 先处理，再扩大 Worker / MQ 上线范围 |
| P2 | 可用性、契约一致性、资源增长或运维文档风险 | 11 | 按影响逐项修正 |
| 保留 | 当前实现合理，或是明确且可接受的能力边界 | 多项 | 不要为“看起来完整”而扩功能 |

### P1 修复状态

| 编号 | 状态 | 落地内容 |
| --- | --- | --- |
| P1-01 | ✅ 已修复 | 框架新增 `support.RequestBinder`：`generation` 持有本代 binder，`handlerSwap` 在拿到 lease 后、进入 handler 前执行；`start` / `Reload` 从本代已启动模块组合。demo `appio.Module` 实现该接口绑定本代快照，脚本入口显式固定自己启动的那一代，`generation.Set.Middleware` 不再覆盖已有固定值。 |
| P1-02 | ✅ 已修复 | `Succeed` / `Fail` / dead 的 UPDATE 增加 `attempts = ?`，旧执行者一律 `ErrLeaseLost`，手工 task 直接 `ErrInvalidTask`；新增 `worker.Identity.Owner()`（带 host）并在文档中作为 claim owner 的推荐值。 |
| P1-03 | ✅ 已修复 | 台账按状态机推进：`processing` 且租约有效返回 `ErrInFlight`（可重试、不 ACK），租约过期或 `failed` 才允许带新 attempts 接管；`succeed` / `fail` / `park` 以 attempts + status 做 CAS，`done` / `dead` 不可重开；租约复用 `updated_at`，由 `ProcessorConfig.ProcessingLease` 配置。 |
| P1-04 | ✅ 已修复 | 新增 `worker.ReadyReporter` + `worker.Ready(ctx)`，四个消费者在建连/订阅后上报，`Runner.Ready()` 汇总；宿主等全部实例上报后才置 `/healthz` 为 200，启动失败永不就绪；drain 一开始立即回到 503。 |
| P1-05 | ✅ 已修复 | `gofy-interface-api` 的 `newCallOptions` 初始化 `query`，普通调用、重复 key 与流式调用均不再 panic。 |

---

## 2. 仓库边界与依赖关系

### 2.1 当前 Go 仓库分工

原先的 `bingcool/gofy-interface-service` 已不再是本轮可访问的 Go 契约仓库。当前需要按拆分后的三个 Go 仓库理解：

| 仓库 | 职责 | 是否依赖 Gofy |
|---|---|---|
| `bingcool/gofy` | 框架核心、Runtime、HTTP、Worker、DB/Redis/MQ、Claim/Lease、Demo | 自身 |
| `bingcool/gofy-interface-api` | 框架无关的 endpoint 契约、类型化 Client、OpenAPI/docgen | 否 |
| `bingcool/gofy-adapter` | Gofy/Gin Adapter、Nacos Resolver、Kubernetes Lease Adapter | 是 |

推荐依赖图：

```text
                 业务项目自己的契约模块
                          │
                          ▼
              gofy-interface-api
          （endpoint / client / docgen）
                          ▲
                          │
                    gofy-adapter
             （Gofy Adapter / Nacos Resolver /
                  Kubernetes Lease Adapter）
                          │
                          ▼
                         gofy
        （App / Runtime / HTTP / Worker / support）
```

其中 `gofy-interface-api` 与 `gofy` 没有直接依赖边，这一点非常重要：纯契约库可以被其他框架的服务端或客户端复用，不必把 Gin 或 Gofy 带入依赖树。

### 2.2 Demo 契约模块与通用契约库不是一回事

Demo 项目当前使用 `examples/interface-service` 作为业务契约模块，Go Module 路径为 `github.com/yourorg/interface-service`。它包含项目自己的 `appinterface`、DTO、枚举和路由定义；它不是 `gofy-interface-api` 的替代品。

两层职责应保持清晰：

- `gofy-interface-api`：定义通用的契约原语和通用消费方式。
- `interface-service`：定义某一个业务系统自己的接口、请求响应类型、业务错误码与路由。
- `gofy-adapter`：把上述业务契约适配到 Gofy。

这次拆分是合理的。后续如果业务服务要独立发布契约模块，应发布项目自己的 `interface-service` 模块版本，而不是把业务 DTO 挪进通用 API 仓库。

### 2.3 发布与本地开发边界

当前 `gofy-adapter/go.mod` 与 Demo `go.mod` 仍有本地 `replace`，README 已说明需要相邻 checkout，也说明发布时应删除 replace 并固定依赖版本。这适合本地联调，但发布前必须把替换关系清理掉。

建议的发布顺序：

1. `gofy` 与 `gofy-interface-api` 分别发布版本；
2. `gofy-adapter` 固定二者版本后发布；
3. 业务项目的 `interface-service` 按自己的版本发布；
4. Demo / 业务服务固定上述版本构建。

不要让正式服务依赖本地路径 `replace`，也不要让 `gofy` 反向依赖某个业务契约模块。

---

## 3. 当前整体运行架构

### 3.1 HTTP 服务运行链路

```text
main
  │
  ├─ 加载配置 / Nacos 启动同步
  │
  ├─ appworker.Register（构造 Worker 分组定义，不启动 Worker）
  │
  └─ gofy.New(options...)
        │
        └─ App.Execute / Run
              │
              ├─ Logger
              ├─ OTel Provider
              ├─ Runtime DB
              ├─ core.Runtime
              ├─ ModuleFactory 构建模块
              ├─ Module.Register 注册路由
              ├─ Module.Start 启动资源
              ├─ listener / HTTP Server
              ├─ Nacos 注册
              └─ handlerSwap → generation → Gin Handler
```

这条链路的结构比较清楚。框架核心通过 `ModuleFactory` 构造模块，业务模块只负责路由注册和模块自身生命周期；HTTP Listener 由 App 持有，Reload 只替换 Handler，不重新绑定端口。

### 3.2 业务模块装配链路

```text
ModuleFactory
    │
    ▼
app/container.NewContainer()
    │
    ├─ RepositoryDeclare（零值构造的无状态 Repository）
    │
    ├─ ProductService（按需构建）
    │
    ├─ OrderConfigService
    │
    └─ OrderService
          │
          ├─ OrderRepository
          ├─ ProductAPI（InnerAPI 接口）
          └─ Logger
```

这比把所有依赖都塞进模块工厂、或依赖包级单例更容易追踪。跨模块访问通过 `innerapi` 发布接口与领域视图，避免消费者直接导入对方的 Service / Repository / Model。

### 3.3 请求数据访问链路

```text
HTTP Request
    │
    ▼
appio.Middleware
    │  固定当前 Snapshot 到 Request.Context
    ▼
Controller
    │
    ▼
Service
    │
    ├─ Repository ─┐
    │               ├─ appio.WithContext(ctx)
    └─ Query ───────┘        │
                             ├─ DB / Redis / MQ Set
                             └─ 当前请求代的资源
```

这条路径的设计目标正确：一个请求应该固定使用进入时的连接资源，不能在处理到一半时随着 Reload 切换连接。新增加的 `query` 只读层也遵守了这条访问路径，事务 Context 可以在 Repository 和 Query 之间复用。

但当前 Snapshot 仍由 IO Module 直接发布到全局指针，未纳入 `generation` 本身。这是本轮最重要的架构缺口，详见 P1-01。

### 3.4 Worker 运行链路

```text
main
  │
  └─ app.Execute → worker 子命令
        │
        ├─ 解析 --group / --name / --concurrency
        ├─ Logger / OTel / Runtime DB
        ├─ worker.Runner
        │     │
        │     ├─ 每个 Worker 创建 N 个实例
        │     ├─ Identity 注入 Context
        │     ├─ supervise / restart policy
        │     ├─ consume context：停止继续拉取
        │     └─ drain context：允许在途处理完成
        │
        ├─ worker PID
        ├─ worker health endpoint
        └─ Runner 退出 → App cleanup
```

Worker 与 HTTP 使用同一个 Runtime 组件构建逻辑，但启动路径相互隔离：Worker 不注册 HTTP 路由、不监听业务 HTTP 端口、不向 Nacos 注册业务服务。这是合理的进程职责切分。

部署上，一个进程运行一个 Worker Group 是清晰的边界；`--group='*'` 适合本地开发和冒烟测试，不适合作为正常生产部署形态（`all` 只是该进程 pid 文件的后缀，不是 `--group` 的取值）。这样业务变更只需滚动重启对应 Worker Group 的 Deployment。

### 3.5 消息消费者链路

```text
Worker Runner
    │
    ▼
support/consumer.NewKafka / NewAMQP / NewRocketMQ / NewQueues
    │
    ├─ Run 时建连接、订阅
    ├─ 转换为通用 Message
    ▼
Processor.Handle
    │
    ├─ 注入 / 提取 Trace
    ├─ 去重台账 Claim
    ├─ Decode Envelope
    ├─ 调用业务 Handler
    ├─ 标记 done / failed / dead
    └─ 返回 ACK / NACK 结果
    │
    ▼
底层协议适配器
```

把通用处理链路放到 `support/consumer`，让每种 broker 的代码只负责连接、订阅和消息转换，是这轮值得保留的抽象。尤其新消费者在每次 `Run()` 中重新建立协议客户端或队列订阅，能够配合 Runner 的重启策略，而不把一次性 Consumer 对象永久挂在 Worker 接收者上。

### 3.6 多副本协调

```text
数据库任务行认领：support/claim
    SELECT ... FOR UPDATE SKIP LOCKED
        → Claim
        → Handler
        → Succeed / Fail
        → Reap 过期任务

分布式互斥：support/lease
    Locker.Acquire
        → fencing token
        → Renew
        → Run callback
        → Release

可选 Kubernetes 实现：gofy-adapter/adapter/lease/k8slease
```

这两类机制的职责是不同的：`claim` 面向一张待处理任务表，`lease` 面向一个逻辑资源的互斥协调。当前目录划分没有必要合并。

---

## 4. 相比上一轮已经落地、应保留的改进

以下内容已在当前代码中看到实质落地，不再重复列为待办：

| 项目 | 当前状态 | 评审意见 |
|---|---|---|
| 通用契约与 Gofy Adapter 拆仓 | 已拆为 `gofy-interface-api` 和 `gofy-adapter` | 正确，继续保持单向依赖 |
| Client 重试逐次 Resolve | 普通 `Call` 的 `send()` 每次 attempt 都重新解析地址 | 正确；SSE 重连仍有单独问题，见 P2-02 |
| Nacos `reload / restart / exit` 策略 | 已有策略枚举、校验、摘流、grace period | 方向合理，策略差异需要在运维文档中保持明确 |
| Worker 作为独立进程 | 有单独命令、PID、Runtime、探针、分组 | 正确，是服务部署的清晰边界 |
| Worker consume / drain 双 Context | 已实现并由 Runner 统一控制 | 正确，避免停机时直接取消在途业务处理 |
| Broker 消费者按 `Run()` 重建连接 | `support/consumer` 各类 Worker 在 Run 中建连、退出时释放 | 可以安全配合 restart policy；继续优先使用高层消费者构造器 |
| Kafka Manual Commit | 已将手动提交作为业务事件的安全默认，并提供示例 | 正确；不能把“手动”误解为 Handler 自己调用 CommitRecords |
| 消息通用 Processor | 已统一信封、trace、永久失败分类与台账 | 方向正确，但台账并发认领语义还需要修正 |
| DB Claim `SKIP LOCKED` | 已有事务内选择和更新、lease_until、Reap | 基本模型合理，当前 owner fencing 不完整 |
| Redis / Kubernetes Lease | 已提供 Redis 核心和独立 K8s Adapter | 分层合理；同一 Identity 的并发 Acquire 需要约束 |
| 只读 Query 层 | 已增加 JOIN / EXISTS / 聚合示例与 SQLite 测试 | 合理，继续与 Repository 写职责分离 |
| Request-level IO Snapshot | Repository / Query 通过 appio.Context 取句柄 | 方向正确，但 Snapshot 还未和 Handler Generation 一起提交 |
| Makefile 质量门禁 | 根仓库 `check`、`check-all` 覆盖 race、example、contract 生成检查 | 本地门禁有实质内容；GitHub 自动化状态仍需单独确认 |

---

## 5. P1：需要优先处理的正确性问题

### P1-01：Handler Generation 与 IO Snapshot 仍然是两次独立发布

**涉及文件**

- [`generation.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/generation.go)
- [`reload.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/reload.go)
- [`examples/demo-service/app/appio/io_runtime.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/examples/demo-service/app/appio/io_runtime.go)
- [`examples/demo-service/app/appio/snapshot.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/examples/demo-service/app/appio/snapshot.go)

**现状**

`Module.Start()` 在新 IO 资源全部创建完成后，依次安装各个 Set，再执行 `Publish(m.snapshot)`。而 Gofy 的 Reload 流程是：

1. 构建新 logger / DB / Runtime / Handler；
2. 构建并启动新 Module；
3. 全部模块启动成功后，才执行 `handlerRef.Store(newGen)`；
4. 再退役旧 Generation。

这代表新 Snapshot 的可见时刻早于新 Handler 的可见时刻。`appio.Middleware()` 在请求进入时读取全局 Snapshot，因此旧 Handler 在这个窗口里仍可能固定新 Snapshot。

更严重的是失败回滚：假设 IO Module 已经 Start 并发布新 Snapshot，但后续某个模块 Start 失败，框架会停止已启动的新模块。新 IO Module Stop 会 CAS 撤回新 Snapshot；旧 Handler 仍在服务，但先前覆盖掉的旧 Snapshot 不会自动恢复。

**影响**

- 旧路由可能使用新一代数据库 / Redis / MQ 连接；
- Reload 中途失败后，旧请求入口可能拿不到 Snapshot；
- 资源释放顺序和请求实际使用的资源代际不再严格一致。

**建议修正**

不要让业务 IO Module 自己决定进程级 Snapshot 的发布时刻，也不要让框架反向依赖业务包：`generation` 属于 `gofy` 包，`appio` 属于 demo，直接写 `snapshot *appio.Snapshot` 无法编译，也违反三仓依赖方向。两种可行方案：

**方案 A（业务侧，零框架改动）**：把快照挂到“每代一个”的 `Container`。demo 的 `reloader.factories()` 每次 Reload 都新建容器，让该代中间件读本代容器的快照，而不是读进程级 `currentSnapshot`；全局 `Publish` 只保留给脚本、Worker 与测试。Reload 失败时旧容器仍持有旧快照，不需要恢复逻辑。

**方案 B（框架侧，通用扩展点）**：框架只提供 binder 钩子，不持有业务类型：

```go
type RequestBinder func(ctx context.Context) context.Context
// handlerSwap.ServeHTTP: acquire 成功后 ctx = binder(ctx) 再进入 handler
```

app 在构造每代模块时把该代快照闭包进 binder。两条方案都满足“新代发布失败时旧代从未被覆盖”。

`handlerSwap.ServeHTTP()` 成功 Acquire 某一代后，应把同一代的快照注入 Request.Context，再进入该代 Handler。

**重要约束**

- `Start()` 只构建和预热资源，不发布进程级当前代；
- Handler 与 Snapshot 在同一代内成对可见：方案 A 由中间件绑定本代容器，方案 B 由 App 统一发布完整 Generation；
- 旧 Generation 完成 drain 后才 Stop 模块和关闭资源；
- 脚本 / CLI / Worker 等非 HTTP 入口，使用显式的独立 Snapshot 生命周期，不应复用 HTTP 的进程级 Current 状态。

**验收测试**

1. 旧 Handler 阻塞时触发 Reload，新 Handler 的 IO 标识为 N+1，旧 Handler 从头到尾都只看到 N。
2. IO Module Start 成功、第二个 Module Start 失败，旧 Handler 仍能持续读取旧 DB / Redis。
3. 两个并发 Reload 与请求入口交错时，不允许出现 Handler N + Snapshot N+1。
4. Reload 成功后，旧资源只在旧 Generation 最后一个 Lease 释放后关闭。

### P1-02：Claim 的 owner 不是每次认领唯一 token，旧执行者可以写回已被重新认领的任务

**涉及文件**

- [`support/claim/claim.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/claim/claim.go)
- [`support/claim/claim_test.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/claim/claim_test.go)
- [`support/worker/identity.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/worker/identity.go)
- [`docs/worker-guide.md`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/docs/worker-guide.md)

**现状**

Claim 返回的 Task 包含 `ID / Owner / Attempts`。`Claim()` 更新任务时设置 `owner` 并递增 `attempts`。但是 `Succeed()`、`Fail()` 和 `Dead()` 的条件只检查：

```sql
WHERE id = ? AND owner = ? AND status = 1
```

并没有比较本次 Task 的 `Attempts`，也没有独立的 claim token。

另一方面，Worker 文档建议直接用 `Identity.String()` 作为 owner。当前 `Identity.String()` 返回 `group/name#index`，并不包含 `Host`，所以不同 Pod 的相同 Worker Group / Name / Index 会得到相同 owner。

更关键的是，即使把 Host 拼入 owner，也不足以解决同一个进程内同一 Worker 实例的旧执行者问题：同一个 owner 的任务租约过期、被 Reap、再次被同一实例 Claim 后，旧 Handler 仍有同样的 owner 字符串。

**风险场景**

```text
Worker A Claim task 100 → owner=A, attempts=1
    │
    ├─ Handler 卡住，租约过期
    │
Reap → Pending
    │
Worker A 或同名实例再次 Claim → owner=A, attempts=2
    │
旧 Handler 终于返回
    │
旧 Task.Succeed(owner=A) 仍满足 id + owner + status 条件
    │
错误地把新一轮执行的任务标记为完成
```

**建议修正**

首选不新增列的方案：`Task` 已经带回 `Attempts`，`Claim()` 每次认领必然自增，收尾 SQL 增加 `AND attempts = ?` 就能让上一次认领的写回全部失败：

- `id`；
- `status = CLAIMED`；
- `attempts = task.Attempts`；
- 必要时再校验 `lease_until > NOW(3)`。

更强的语义可以再引入 `claim_token` 列，但表由应用维护（框架只提供 DDL），新增列意味着所有使用方都要迁移，建议作为第二步而不是第一版。

`owner` 保留为可观测性字段，记录 Pod / Worker 身份；它不应充当每次认领的唯一 fencing token。

此外，应把 `Identity.String()` 是否包含 Host 与 claim token 明确分开：Host 解决“哪个实例”，attempts/token 解决“该实例哪一次认领”。当前 `String()` 只输出 `group/name#index`，与 `support/worker/README.md` 里“能追溯到具体 Pod”的描述不一致，建议把 Host 补进去或改用专门的 owner 构造函数，但这不能替代 fencing。

**验收测试**

- 同一个 Owner 对同一任务先后 Claim 两次，第一次 Task 的 Succeed / Fail 必须返回 `ErrLeaseLost`；
- 不同 Pod 的同名 Worker 并发 Claim 时，旧 Pod 的 Task 不能更新新 Pod 的 claim；
- Reap → Reclaim 后，之前的 Task 一律不能 Succeed / Fail / Dead；
- 任务已超时但尚未 Reap 时，旧 Task 也不能越过明确的 lease 有效期继续写回（若设计选择允许到 Reap 前完成，应在契约中明确，并在 SQL 中保持一致）。

### P1-03：消息幂等台账不能保证同一消息只有一个处理中执行者

**涉及文件**：[`support/consumer/consume.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/consumer/consume.go)

**现状**

`deduper.claim()` 对首次到达消息执行唯一键 Insert。重复消息到达时：

- `done` 返回 `false`，正确跳过；
- attempts 到达上限时标记 `dead` 并跳过；
- 其他状态，包括 `processing`，只要 attempts 还未耗尽，就执行更新：

```go
Where("id = ? AND status <> ?", existing.ID, consumedStatusDone).
Updates(map[string]any{
    "status": consumedStatusProcessing,
    "attempts": gorm.Expr("attempts + 1"),
})
```

也就是说，消息还处在 `processing` 时，第二次投递可以把 attempts 加一并再次进入 Handler。当前台账没有 `processing_until` / owner / claim token，也没有限定只有 `failed` 状态才允许重试。

另外：

- `succeed()`、`fail()`、`park()` 只按 `(consumer, message_id)` 写回，不与某一次 claim 的 attempts/token 绑定；
- `fail()` / `park()` 没有 status 条件，旧 Handler 可以把已经是 `done` 的行改成 `failed` / `dead`，导致已完成消息被重新处理；
- `dead` 没有在 `claim()` 中被优先识别；如果 attempts 尚未到上限，dead 记录再次投递仍可能被重置成 processing；
- 对同一消息并发执行时，旧 Handler 的 `succeed()` 可能覆盖新 Handler 的结果状态。

**影响**

在 Kafka rebalance、AMQP 连接断开后重投，或者 RocketMQ 不可见时长小于业务处理时长的情况下，可能出现同一消息并发处理。数据库唯一键只能保证“台账记录只有一行”，并不能保证“同一时刻只有一个 Handler 执行”。

**建议修正**

把消息台账的状态机明确为：

```text
不存在 → processing(attempts, lease_until)
processing 且 lease 有效 → 不 ACK，延迟重投
processing 且 lease 过期 → 使用新 attempts/token 接管
failed → 使用新 attempts/token 重试（attempts + 1）
done → Skip / ACK
dead → Skip / ACK
```

这里有一条容易踩空的语义：`Handle` 在 deduper 返回 `false` 时是 `return nil`，也就是 **ACK**。`processing` 未过期时如果直接 Skip + ACK，而第一个 Handler 随后崩溃，备份副本已经被确认，消息就永久丢失。因此“已有一个处理中执行者”必须返回可重试错误或以延迟方式重新入队，不能 ACK。

所有 `succeed / fail / park` 都必须带本次 claim 的 attempts 或 token，并以 status 做 CAS；`fail` / `park` 还要拒绝把 `done` 改回 `failed` / `dead`。

`processing` 的 lease 可以依赖业务最大处理时长和 MQ 可见性时长。不想新增列时，可以复用 `updated_at` 作为隐式 lease：读出后与当前时间比较，再用 `updated_at` 做 CAS，这样 SQLite 测试也能覆盖。

**验收测试**

1. 同一 message ID 并发 20 次 `Handle()`，只允许一个 Handler 进入业务代码。
2. 第一个 Handler 阻塞，第二次重投不应同时进入业务 Handler。
3. processing 超时后允许新 attempts/token 接管；旧 attempts/token 的 succeed/fail/park 必须被拒绝。
4. dead 状态重投必须直接跳过，不能重新变成 processing。
5. attempts 上限只能被一个竞争者推进，不能因并发读取造成多次越界。

### P1-04：Worker 健康探针过早进入 Ready

**涉及文件**：[`worker_host.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/worker_host.go)、[`worker_health.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/worker_health.go)

当前 `runWorker()` 的顺序是：

1. 构造 Runner；
2. 启动 health HTTP Server；
3. `a.markReady()`；
4. `health.setReady()`；
5. 才启动各 Runner goroutine。

因此探针 Ready 时，Worker 的 `Run()` 甚至可能还没有开始连接 broker、声明队列或加入 Kafka Consumer Group。

**影响**

Kubernetes / 调度系统可能在消费者实际上尚未准备好接收消息时，就把该 Pod 视为 Ready。特别是启动时 broker 连接失败、首次重试退避、队列拓扑声明失败时，外部状态可能短暂或持续误报。

**建议修正**

区分三个状态：

- `starting`：进程、Runtime、Runner 已创建；
- `ready`：所有配置为必需的 Worker 已完成启动/订阅；
- `stopping`：停止接收新任务，正在 drain。

可以为 Worker 增加可选的启动就绪协议（如 `Starter` / `Ready` 或每实例 ready channel），由 Runner 汇总全部实例状态；只有所选 Group 的必需 Worker 全部 Ready 后再设 `health.setReady()`。

不要简单地在 `Runner.Run()` 一进入就标 Ready，因为 `Run()` 本身可能还处于 broker connect / queue declare 阶段。

另外，当前 `health.setStopping()` 要等所有 Runner 都退出后才调用，SIGTERM 之后、drain 进行中的整段时间 `/healthz` 仍是 200，与“进入 drain 立刻 503”的预期不符。应把 `setStopping()` 移到收到停止信号、`cancelRunners` 之后立即执行，再等待 `errCh` 收完。

**验收测试**

- 延迟 broker 建连时，探针一直 503，直到订阅真正建立；
- 任一 Worker startup 返回错误，Ready 不得曾经变成 200；
- 所有 worker Ready 后，探针 200；进入 drain 后立刻回到 503。

### P1-05：`WithQuery()` 对 nil `url.Values` 调用 Add，触发 panic

**涉及文件**

- [`client/options.go`](https://github.com/bingcool/gofy-interface-api/blob/b77a198b05a2e5ac2c69a6fc055acf603efa2e82/client/options.go)
- [`client/call.go`](https://github.com/bingcool/gofy-interface-api/blob/b77a198b05a2e5ac2c69a6fc055acf603efa2e82/client/call.go)

当前 `newCallOptions()` 只初始化：

```go
options := callOptions{headers: make(http.Header)}
```

而 `WithQuery()` 直接调用：

```go
func WithQuery(key, value string) CallOption {
    return func(o *callOptions) { o.query.Add(key, value) }
}
```

`o.query` 为 nil，调用 `WithQuery()` 会 panic。普通 JSON Client 与 SSE / Download / Chunked 都通过 `newCallOptions()`，因此这个公共选项目前不可用。

**建议修正**

构造时同时初始化 `query: make(url.Values)`，并增加普通 Call、重复 query key 累加、流式调用三类测试。

---

## 6. P2：需要安排处理的其他问题

### P2-01：`Identity.String()` 未包含 Host，与文档所述 owner 唯一性不一致

本项与 P1-02 是同一问题的两面：即使补上 Host，也只能解决“哪个实例”，解决不了“该实例哪一次认领”，修复时应与 P1-02 一并处理。

**涉及文件**：[`support/worker/identity.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/worker/identity.go)、[`support/worker/README.md`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/worker/README.md)

Runner 创建 Identity 时确实填了 `Host`，但 `Identity.String()` 只输出 `group/name#index`。README 又把 `id.String()` 作为 Claim owner 的推荐值，并描述它能识别到 Pod 的具体实例。这两者不一致。

建议将 `String()` 改为稳定、可读且包含 Host 的观测身份；但再次强调：**Host 不能代替 P1-02 的每次认领 fencing**。一个 Pod 里的同一 Worker 可能在租约过期后重新 Claim，仍需用 `attempts`（或 token）防止旧执行者写回。

### P2-02：SSE 重连沿用初次解析到的 BaseURL

**文件**：[`client/sse.go`](https://github.com/bingcool/gofy-interface-api/blob/b77a198b05a2e5ac2c69a6fc055acf603efa2e82/client/sse.go)

`StreamSSE()` 在启动时解析一次 `base := c.base(ctx)`。如果 SSE 连接中断，重连循环调用 `openStream(ctx, base, ...)`，继续使用同一个 base。

普通 `Call` 的 `send()` 每次 retry 都重新 Resolve；SSE 重连没有沿用该语义。因此启用 Nacos Resolver 时，SSE 重连不能因为连接中断而自动切换到另一个实例。

建议：把 `base()` 移入重连循环，每次 reconnect 重新 Resolve；固定 `BaseURL` 情况自然仍返回同一地址。测试应覆盖初始实例 A 断开、Resolver 第二次返回 B、SSE 使用 Last-Event-ID 恢复。

### P2-03：`WithConnectTimeout` 派生 Transport 缓存没有上限或关闭接口

**文件**：[`client/client.go`](https://github.com/bingcool/gofy-interface-api/blob/b77a198b05a2e5ac2c69a6fc055acf603efa2e82/client/client.go)

Client 使用 `sync.Map` 按每个 distinct `connectTimeout` 缓存派生 `*http.Client` 和 Transport。相同值复用连接池的方向正确；但如果运行期允许任意 duration，缓存会持续增长，而且没有明确的 `CloseIdleConnections()` / Client.Close 生命周期。

建议：

- 若超时配置本应是有限枚举，限制允许值并文档化；
- 若确实允许任意值，提供有界缓存或将 Transport 复用责任移到 Client 构造期；
- 提供关闭入口，遍历派生 Transport 调用 `CloseIdleConnections()`。

### P2-04：Kubernetes Lease 同一 Identity 的并发 Acquire 会 Join 同一 Token

**文件**：[`adapter/lease/k8slease/k8slease.go`](https://github.com/bingcool/gofy-adapter/blob/03b4f1d52057f1ed0b53aed3cae2e7cc46778384/adapter/lease/k8slease/k8slease.go)

当前 `takeOrJoin()` 在 Lease 仍有效且 holder 等于本实例 `identity` 时，会刷新租期并返回同一个 token，而不是 `ErrHeld`。源码注释解释这是为了同名 Pod 重启后保留此前在途操作的 token。

这是一项明确的语义选择，但也意味着**同一个进程中两个并发 Acquire / Run，只要 Identity 一样，就可能同时被视为持有同一把锁**。

建议至少明确以下约束之一：

1. 文档明确每个 `(identity,key)` 同时只能有一个 Run 调用，调用方自行串行；
2. Adapter 内部按 `(key,identity)` 做本地单飞协调；
3. 引入每次 acquisition/session ID，使同一 Pod 的并发调用仍有独立所有权。

不要把“相同 Pod 的恢复续租”与“同一个 Pod 中允许任意多个并发临界区”混为一谈。

### P2-05：低层 `rocketmqconsumer` Adapter 是一次性 Consumer；Runner 的 restart 会复用 Worker

**文件**：[`support/worker/consumer/rocketmqconsumer/rocketmqconsumer.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/worker/consumer/rocketmqconsumer/rocketmqconsumer.go)、[`support/worker/runner.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/worker/runner.go)

低层 `rocketmqconsumer.New(name, consumer, ...)` 接收一个已经创建好的 Consumer，文档说明 Consumer 只能 Consume 一次然后 Close。Runner 的 `supervise()` 在错误后会再次调用同一个 Worker 的 `Run()`。

这意味着直接使用低层 Adapter 时，如果第一次 `Run` 是因 context 仍存活时的错误返回，consumer 不会被 `Close`（`Close` 只在 `ctx.Err() != nil` 时调用），Runner 重试会复用一个已经 `Consume` 过的实例；如果是 ctx 取消导致的退出，`supervise` 直接返回、不再重试。两条路径都得不到“真正重连”的效果。

这不影响当前 `support/consumer.NewRocketMQ()` 的主要路径：它在每次 Run 内重新 `NewConsumer()`，适合 Runner 重启。建议：

- 将低层 Adapter 定位为一次性 Worker，或调整为接收 Consumer Factory；
- README 明确生产项目优先用 `support/consumer.NewRocketMQ()`，不要把两个层级的生命周期契约混淆。

### P2-06：Query 的 `MAX(id)` 被用作 `last_order_id`，并不严格等于“最近一笔订单”

**文件**：[`examples/demo-service/app/module/order/query/order_queries.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/examples/demo-service/app/module/order/query/order_queries.go)

`UserOrderStat` 的 `last_order_id` 用 `MAX(id)` 计算。如果 ID 是自增主键且所有写入顺序与业务创建时间一致，通常可用；但并发插入、导入历史数据或分布式 ID 时，最大 ID 不一定是时间上最近的一笔。

建议明确它的语义是 `max_order_id`，或者按业务定义使用 `MAX(created_at)` 并通过二级排序获得对应订单 ID。不要让列名看起来表示时间语义，SQL 实际却只表示最大主键。

### P2-07：Nacos restart / exit 是固定 grace period，不是真正的在途任务 Join

**文件**：[`support/nacos/watch.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/nacos/watch.go)、[`support/nacos/strategy.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/nacos/strategy.go)

当前 `WithDrain` / `prepare()` 的流程是：停止接受新 HTTP 请求、等待固定 grace period，然后 restart 或 exit。代码注释明确说明：它不会 Join 实际请求、Worker tick 或 Consumer Handler；超过 grace period 的工作会被截断。

这是可接受的部署策略，但不能把它称为严格优雅关停。生产需要保证：

- `grace_period` 大于正常 HTTP / MQ Handler 的可接受最大执行时间；
- Kubernetes `terminationGracePeriodSeconds` 大于 grace period + 进程退出清理预算；
- readiness / endpoint 摘流和 Nacos 注销动作有明确顺序；
- 对不可中断任务，业务层仍需幂等、可恢复或使用 claim/lease fencing。

### P2-08：Nacos Watcher 在 HTTP Listener / 服务注册完成前启动，存在极早配置变更竞态

`run()` 中先启动模块，`nacos-watch` Module.Start 会异步启动 watcher，后续才创建 Listener、Serve 并注册 Nacos。若远端配置在 watcher 刚启动时就发生变更，`onDrain` 可先调用 `App.StopAccepting()`；此时 registrar 可能还没写入 `a.registrar`。随后启动路径仍可能完成 Listener / Nacos 注册，出现 accepting 已关闭但仍然被注册的短暂状态。

该窗口较窄，属于启动时序风险。建议增加 App lifecycle state gate：`StopAccepting` 如果发生在 listener/register 完成前，启动阶段不得继续标记 ready 或注册服务；或把 watcher 启动推迟到 App 完全 ready 后。

### P2-09：`gofy-adapter` README 尚未同步新增 Kubernetes Lease Adapter

当前 README 解释了 `adapter/gofy` 和 `client/nacos`，但没有把 `adapter/lease/k8slease` 加入仓库结构和依赖图说明。该 Adapter 的核心设计（使用 typed coordination client，减少 client-go 包面）值得记录，并补上 RBAC 最小权限、Lease object 命名、Identity 唯一性和 fencing token 删除约束。

### P2-10（原 P1-02，已降级）：HTTP Generation Lease 缺少 `defer`，摘流闸门存在竞争窗口

**涉及文件**：[`generation.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/generation.go)、[`option.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/option.go)、[`start.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/start.go)

当前 `ServeHTTP()` 是 `acquire → handler.ServeHTTP → release`，`release()` 没有 `defer`。以下两点使影响低于 P1：

- 默认 `recovery: true`（`option.go`），`engine.Use(recovery)` 已安装，普通业务 panic 会被捕获，release 正常执行；真实触发面只剩 `http.ErrAbortHandler`、Recovery 被显式关闭、或 panic 发生在 Recovery 自身；
- `retireGeneration` 在 drain 超时后仍会继续停模块、关资源，只记 warning。active 计数不释放的后果是**每次 Reload 多等一个 drain 超时（默认 10s）**，不是资源永久泄漏。

摘流闸门确实只在 Acquire 前检查一次，存在“读到 `accepting == true` 之后被置 false”的窗口；这类请求本来就与 `StopAccepting` 并发进入，且 restart/exit 后面还有固定 grace period，影响有限。

**建议修正**（不因降级而取消）：

- Acquire 成功后立刻 `defer gen.lease.release()`；
- Acquire 之后二次检查 accepting，或把 accepting 与 active/retired 收敛到同一个可同步的 admission gate；
- 已进入的请求仍允许持有 Lease 跑完，再关闭 server 连接。

**验收测试**

- `http.ErrAbortHandler` 不应泄漏 Lease；Recovery 关闭时 Handler panic，active 最终回到 0；
- 并发 `StopAccepting` 与请求进入，闸门关闭后不再新增请求进入 Handler。

### P2-11：restart / exit 动作失败后，进程永久停在“已摘流、已注销”

**涉及文件**：[`support/nacos/watch.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/support/nacos/watch.go)、[`start.go`](https://github.com/bingcool/gofy/blob/0859044f6e9890fb8d0bf5e0ff48bea9fb6988cf/start.go)

`prepare()` 先调 `WithDrain` → `App.StopAccepting()`：`accepting` 置 false，并把 `a.registrar` 取走、置 nil。随后 `syscall.Exec` 或发信号失败时，`apply` 的错误只记日志，watcher 继续轮询；全仓库没有把 `accepting` 改回 true 的路径（只有 App 初始化时写过一次 true）。

结果是：进程还活着、已从 Nacos 注销、所有请求 503。若 k8s 的 liveness 探测走 `/healthz`（同一个 gin 引擎），最终会被重启；如果 liveness 是 TCP 检查或没有 liveness，就是一个长期占着资源的僵尸进程。

建议：动作失败时恢复闸门（新增显式的 resume 语义），或把失败当作致命错误退出交给监管者；至少要有 error 级别日志和指标。

**验收测试**：让 exec / terminate 返回错误，进程要么恢复接客，要么退出；不允许停在“活着但永久 503”。

---

## 7. 当前合理且应避免过度改造的部分

### 7.1 不要将 Query 层重新塞回 Repository

目前 Query 的职责是复杂只读 SQL：JOIN、EXISTS、聚合、报表投影；Repository 负责单聚合写入和简单读取。这个边界清楚。Query 返回专属 Row / Read Model，不强迫复用 GORM Model，也不提供写方法，值得保留。

跨模块 JOIN 直接使用表结构而不是 import 对方的 Repository / Model，是一个数据库契约取舍。它会增加 SQL 对表结构的耦合，但减少 Go 包依赖耦合。只要通过迁移 fixture 和真实 Schema 集成测试保证列名变更能被发现，这个取舍是可接受的。

### 7.2 不要把所有 Repository 都接口化

单实现 Repository 保持具体类型，跨模块能力才通过 `innerapi` 接口暴露，符合当前架构目标。无需为了测试方便给每个 Repository 强行添加接口；真实 SQLite Repository 测试更能覆盖 SQL 与事务边界。

### 7.3 不要把契约库和业务契约模块混成一个仓库

`gofy-interface-api` 只负责通用 API 原语；项目自己的接口模块继续独立。不要将 Demo 的订单 DTO / 状态枚举迁到 Gofy 核心，也不要让 Gofy 核心依赖业务接口仓库。

### 7.4 不要把 Claim 与 Lease 合并成一个“分布式锁系统”

Claim 是一张任务表上的可恢复工作认领；Lease 是通用资源互斥。可以共用 fencing 原则，但不需要统一成同一个 API 或存储后端。

### 7.5 MQ 消费仍然是 at-least-once，不要宣传成 exactly-once

当前 Processor 的 `succeed()` 标记失败时，会记录 warning，但仍然 ACK，因为业务副作用可能已经完成。这样下一次重投仍有可能再执行一次。数据库台账提高了去重能力，但不能跨 DB 与 broker ACK 做原子提交。

对外语义应继续表述为：**至少一次投递 + 幂等台账降低重复执行 + 业务 Handler 必须具备幂等或可补偿语义**。不要宣称 Exactly Once。

### 7.6 Redis Queue 的可靠性边界已经正确写明

`support/consumer.QueuesConfig` 文档明确说明 Redis Queue Pop 后消息已从 Redis 移除，Handler 崩溃可能丢失；这不是 Kafka/AMQP 的同等级可靠性。保留这个清晰的能力边界，不要为了 API 对称而把 Redis Queue 说成可靠 MQ。

### 7.7 流式 Adapter 启动期拒绝是合理边界

`gofy-interface-api` / Client 与 OpenAPI 可以表达 SSE / Download / Chunked，但当前 Gofy Adapter 对非 JSON 响应仍 fail fast，要求业务自行注册流式路由。只要错误在启动时暴露、文档明确，就比静默降级成 JSON 更安全。当前阶段无需为了“能力齐全”把流式输出强行纳入 Adapter。

---

## 8. 建议修复顺序

### 阶段 A：先修代际原子性

1. P1-01：把快照纳入代际发布——按 §5 P1-01 的方案 A（业务侧 Container 作用域，零框架改动）或方案 B（框架 binder 钩子），不要引入框架到 `appio` 的依赖。
2. 补 Reload 成功、失败、并发、panic、摘流压力测试。

这是整个 HTTP 运行时正确性的基础，先修这里。

### 阶段 B：补齐多副本任务 Claim 的 fencing

1. P1-02：收尾 SQL 增加 `attempts = ?`；Owner 只用于实例标识和日志。
2. Claim / Reap / Reclaim / Succeed / Fail / Dead 全链路都比较同一次认领。
3. 把 Host 补进 `Identity.String()`，或另提供带 Host 的 owner 构造函数。
4. 增加过期执行者晚到写回测试。

### 阶段 C：修复消息幂等台账状态机

1. P1-03：`processing` 有有效 lease 时不得 ACK，也不得第二次进入执行；只有明确过期 / failed 才允许接管。
2. 每次认领生成新的 attempts/token，所有状态写回做 CAS；`fail` / `park` 拒绝覆盖 `done`。
3. `dead` 和 `done` 永远视为终态。
4. 增加多副本并发、重投、重平衡、ack 丢失、旧 Handler 晚到等测试。

### 阶段 D：完善 Worker readiness 和 API 小缺陷

1. P1-04：Runner startup readiness barrier；`setStopping()` 提前到 drain 开始。
2. P1-05：初始化 `url.Values`，补 WithQuery 测试。
3. P2-02：SSE reconnect 每次重新 Resolve。
4. P2-03：限制或回收连接超时派生 Transport。

### 阶段 E：补充 Adapter / 运维契约与门禁

1. P2-11：restart / exit 动作失败后恢复闸门或退出，不允许停在永久 503。
2. P2-10：Lease release 加 `defer`、Acquire 后二次检查 accepting。
3. P2-05：明确低层 RocketMQ Adapter 一次性生命周期；生产推荐 `support/consumer` 高层构造器。
4. P2-04：文档化 Kubernetes Lease 同 Identity join 行为和调用方单飞要求。
5. P2-07：完善 Nacos drain / grace period / restart 的部署预算文档。
6. P2-09：`gofy-adapter` README 加入 K8s Lease。
7. 对三个仓库统一启用 GitHub Actions 或等效 CI，且保护主分支必须通过 `make check`。

---

## 9. 建议增加的回归测试矩阵

| 测试范围 | 必测场景 | 验收重点 |
|---|---|---|
| Generation | 旧 Handler 与新 Snapshot 并发 Reload | Handler 与 Snapshot 永远同代 |
| Generation | 新 Module Start 后下一个 Module 失败 | 旧代仍可正常服务，旧资源不被撤回 |
| Generation Lease | Handler panic / ErrAbortHandler | active 计数必定归零 |
| Admission Gate | StopAccepting 与并发请求交错 | 闸门关闭后没有新 Handler 进入 |
| Claim | 同 Owner 任务租约过期并被重领 | 旧 Task 收尾失败，返回 LeaseLost |
| Claim | 两个 Pod 同 group/name/index | owner 可区分实例，attempts/token 可区分单次 claim |
| Consumer Ledger | 同一消息同时到达两个消费者 | 只允许一个 Handler 进入业务逻辑 |
| Consumer Ledger | processing 未过期时再次投递 | 第二次不进入 Handler，且不得 ACK |
| Consumer Ledger | processing lease 过期后新 token 接管 | 老 token 不可更新状态 |
| Consumer Ledger | dead / done 再次投递 | 直接跳过并 ACK |
| Worker | broker 建连慢 / subscribe 失败 | Ready 只在所有必需订阅成功后变 200 |
| Worker | SIGTERM 时有在途消息 | 先停拉取，再按 drain budget 完成在途处理 |
| Worker | SIGTERM 后 drain 进行中 | `/healthz` 立即变为 503，而不是等 Runner 退出 |
| Nacos | restart / exit 动作失败 | 进程恢复接客或退出，不允许永久 503 |
| Client | WithQuery 单值 / 同名多值 | 无 panic，query 编码与文档一致 |
| Client SSE | 实例 A 断开、Resolver 选到 B | 重连重新 Resolve 并传 Last-Event-ID |
| Client Transport | 多组连接超时值 / Close | 连接池数量可控，关闭后无 idle pool 泄漏 |
| K8s Lease | 同 Identity 并发 Acquire | 行为符合明确的 single-flight / join 契约 |
| OpenAPI | 生成全量与 service 文件 | 生成物稳定、无未跟踪生成文件 |
| Build | `make check-all` | race、example、guard、cgo-free 与契约生成一致性全部通过 |

---

## 10. 质量门禁与本次审计限制

### 10.1 已看到的质量门禁

Gofy 根 Makefile 的 `check` 当前包含：

- `fmt-check`
- `vet`
- `test-race`
- `example`（Demo build / vet / race）
- `guards`（import、IO access、init 检查、contract template）
- `cgo-free`

`check-all` 再增加 `example-contracts`，运行契约模块测试和 OpenAPI 生成一致性检查。

`gofy-interface-api` 的 `check` 覆盖 fmt / vet / race / refcheck；`gofy-adapter` 的 `check` 覆盖 fmt / vet / race / import guards。

这是比仅运行 `go test ./...` 完整得多的工程门禁，应当保留。

### 10.2 本次没有验证的内容

本轮通过 GitHub 源码读取和提交历史做了静态审查，没有在目标环境实际运行：

- `make check-all`；
- `go test -race ./...`；
- MySQL 8 的 `SKIP LOCKED` 真实并发测试；
- Redis Lease 的故障切换 / 网络分区测试；
- Kafka / RabbitMQ / RocketMQ 的 rebalance / ack 丢失 / 不可见时长测试；
- Kubernetes API Server 的资源版本冲突、租约续租、Pod 强杀测试。

GitHub 对本次检查的最新 commit status / pull-request workflow-run 查询没有返回可见 check records；这不等价于证明仓库没有 CI 文件或没有外部流水线，只能说明本次没有取得可验证的最新流水线结果。发布前仍需由 CI 给出实际通过记录。

---

## 11. 最终评审意见

**Gofy 的整体架构已经可以作为长期演进基础，当前不建议再进行大规模结构调整。** 业务分层、契约拆仓、Worker Group 部署边界、消息消费者统一处理链路、Query 只读层等方向都值得保留。

下一阶段不应继续以增加支持组件数量为目标，而应将可靠性语义做实：

1. HTTP Handler 和 IO Snapshot 真正成为同一代对象；
2. 每次任务认领具备独立的 fencing 条件（`attempts` 起步，必要时升级为 token）；
3. 消息处理中状态具备并发互斥和过期接管语义，且接管前不得 ACK 掉备份副本；
4. Worker Ready 真正表示所选工作负载已完成启动，drain 开始即退出 Ready；
5. 公共 Client 选项不 panic，流式重连具备与普通请求相同的发现语义。

这五个方向闭环后，再扩大多副本任务、消息消费和 K8s Lease 的生产使用范围，会更稳妥。另外，`restart` / `exit` 的动作失败必须让进程「恢复接客」或「退出」，不能停在永久 503。

**最终建议：以 P1-01 为第一项，按阶段 A → B → C → D 修复。不要新增更复杂的调度、工作流或资源管理抽象；先把现有 Generation、Claim、Consumer Ledger 的并发和生命周期语义闭合。**
