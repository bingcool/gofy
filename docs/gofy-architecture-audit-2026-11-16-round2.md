# Gofy 最新代码架构复审与问题审计报告（第二轮）

> **修复状态：已修复**（修复时间：2026-11-16）

> 审计日期：2026-11-16  
> 审计范围：`bingcool/gofy`、`bingcool/gofy-interface-api`、`bingcool/gofy-adapter`  
> 审计方式：基于仓库最新代码、关键调用路径、配置默认值、相关测试与跨仓库依赖关系进行代码审查。  
> 目的：复核上一轮审计问题的修复状态，审查本轮新增改动，找出仍可能影响生产正确性、可用性和可维护性的具体问题。

---

## 1. 审计结论摘要

### 1.1 总体判断

本轮代码没有出现需要推倒重来的架构问题。Gofy 的核心职责边界仍然清晰：

- `gofy`：进程生命周期、HTTP Server、Worker Host、Runtime、Consumer、Claim、Lock 等框架运行能力。
- `gofy-interface-api`：独立于 Web 框架的接口契约、客户端调用、OpenAPI 文档生成与边界检查。
- `gofy-adapter`：把接口契约接入 Gofy，以及 Nacos Resolver、Kubernetes Lease 等框架或基础设施适配。

上一轮审计中的 Worker daemon 启动参数、进程重复启动、PID 复用、Consumer 永久失败落库失败后仍 ACK、Readiness 过早上报、ReadyGate 重复信号、重启时 Nacos 同步和订单状态乱序等问题，大部分已经有针对性修复，并且补充了对应测试或测试入口。

本轮最重要的新风险不是模块划分，而是**批量领取与逐条串行执行之间的租约时间模型不一致**：

1. `support/claim` 默认一次领取 100 条任务，租约从批量领取时统一开始计时，Runner 再逐条串行调用 Handler。批次后部任务可能尚未执行就已过期，其他副本回收后会导致两个 Handler 都执行，数据库完成写入的 fencing 不能撤销已经产生的业务副作用。
2. RocketMQ 批量 Receive 同样会让一批消息的不可见时长从批次接收时开始，而当前 Consumer 是逐条串行处理这批消息。批次尾部消息处理前可能已经重新可见。

此外还发现两个相对独立的边界问题：

- Kubernetes Lease `Renew` 对 Update Conflict 返回 `ErrLeaseLost`，但没有释放本地 `held` 集合中的 key；后续同一 Locker 再次 `Acquire` 可能一直得到本地 `ErrHeld`。
- Nacos Resolver 缓存过期时没有按 service 合并并发刷新，热点服务的缓存同时过期会产生 N 次 Nacos 查询。

### 1.2 问题分级

| 级别 | 数量 | 含义 |
|---|---:|---|
| P1 | 1 | 在可预期的批量/多副本运行条件下可能造成重复业务执行，应优先修复 |
| P2 | 4 | 明确的异常分支或容量边界问题，需要修复或明确约束 |
| P3 / 运行约束 | 3 | 不要求立即改架构，但需要在配置、文档或运维规则中明确 |

> 本报告中的 P1/P2 指由代码路径推导出的真实风险，不表示已经在真实生产集群中复现。未执行真实 Broker、数据库集群或 Kubernetes 环境的故障注入。

---

## 2. 本轮审计基线与范围

### 2.1 仓库版本

| 仓库 | 审计分支 | 本轮基线 Commit | 说明 |
|---|---|---|---|
| `bingcool/gofy` | `main` | `58a7abcce54c1b041fee98d1969fe86c9561bda4` | 2026-11-16 最新提交；包含 Claim Runner、处理耗时字段、Worker/Consumer/Lock 等更新 |
| `bingcool/gofy-interface-api` | `main` | `5e5e1c3c8fd69353585b0b3073b868d12ad07f66` | 与上一轮审计基线一致，本轮未见新提交 |
| `bingcool/gofy-adapter` | `main` | `c5ef2a90acda4c92dcd91c05bb53482af331f7a0` | 与上一轮审计基线一致，本轮未见新提交 |

Gofy 相较上一轮基线 `db271b562f0169ed27a9a0d275d4d2930a680be6` 增加了多个提交，改动覆盖 Worker 生命周期、PID/进程锁、Consumer、Claim、Redis Lock、RocketMQ 批量消费示例、订单事件状态机、Demo 配置启动计划与文档。

### 2.2 本轮重点查看的代码路径

**Gofy Core / 生命周期**

- `start.go`：HTTP Server 启动、PID 写入、Module Start、Nacos 注册、Ready、Shutdown。
- `stop.go`：PID 解析、进程身份验证、终止与超时升级。
- `worker_host.go`、`worker_notify.go`：Worker 启动、daemon、启动握手、Worker PID 锁、进程停止与重启。
- `internal/commandline/worker.go`：Worker 命令解析和 daemon 子进程参数重建。
- `internal/proclock/`、`internal/pidfile/`、`internal/process/`：互斥锁文件、PID 文件、进程身份 token 和 detach。
- `support/worker/runner.go`、`support/worker/ready.go`：Worker 实例监督、重启、Drain、Readiness。

**Consumer / MQ**

- `support/consumer/consume.go`：统一 Processor、幂等台账、成功/失败/永久失败收尾。
- `support/consumer/kafka.go`、`amqp.go`、`rocketmq.go`、`queues.go`、`delayamqp.go`：协议适配与 Ready 上报。
- `support/kafka/consumer.go`、`support/amqp/queue.go`、`support/rocketmq/consumer.go`：底层订阅、拉取、ACK 与消费循环。

**Claim / Lock**

- `support/claim/claim.go`、`runner.go`、`claim.sql`：行级认领、租约、CAS、重试、Runner、处理时间记录。
- `support/lock/`：Redis 分布式互斥锁。
- `examples/demo-service/app/appio/redislock/`、`appio/io_runtime.go`、`appio/snapshot.go`：Redis Lock 与 IO Runtime Snapshot 的绑定。

**Demo 业务与跨仓库边界**

- `examples/demo-service/worker/group1/scan_claim.go`：Claim Runner 示例参数与真实调用。
- `examples/demo-service/worker/group1/handler_order.go`：订单事件状态迁移。
- `examples/demo-service/worker/group1/rocketmq_batch_order.go`、`rocketmq_batch_producer.go`：RocketMQ 批量拉取与生产示例。
- `examples/demo-service/config.go`、`main.go`：命令计划、配置加载、Nacos 同步。
- `gofy-interface-api/client/*`、`docgen/*`、`cmd/refcheck/*`。
- `gofy-adapter/adapter/gofy/*`、`adapter/lease/k8slease/*`、`client/nacos/resolver.go`。

---

## 3. 当前架构与关键调用链

### 3.1 模块依赖边界

当前依赖关系保持合理：

```text
业务服务
  ├── gofy-interface-api
  │     ├── contracts       业务接口、DTO、Endpoint 描述
  │     ├── client          HTTP 客户端、重试、SSE/下载等
  │     ├── docgen          OpenAPI 生成与验证
  │     └── docsui          Swagger UI
  │
  ├── gofy-adapter
  │     ├── adapter/gofy            契约路由绑定到 Gofy Router
  │     ├── adapter/lease/k8slease  Kubernetes Lease 实现
  │     └── client/nacos            Nacos 服务发现 Resolver
  │
  └── gofy
        ├── App / Runtime / HTTP lifecycle
        ├── Worker Host / Worker Runner
        ├── Consumer / MQ adapters
        ├── Claim
        ├── Lock / Lease interfaces
        └── DB / Redis / Nacos / OTel / middleware
```

`gofy-interface-api` 没有反向依赖 `gofy` 或 Gin；框架绑定与 Nacos 客户端适配由 `gofy-adapter` 承担。`gofy-adapter` 也没有把 Kubernetes `client-go` 传递到 Gofy 主模块中。这一拆分应继续保持，不建议把 adapter 代码重新合并回 `gofy-interface-api` 或 `gofy`。

### 3.2 Worker 启动与停止链路

```text
CLI: worker --group / worker restart
        ↓
planCommand / WorkerStartArgs
        ↓
selectWorkerGroups
        ↓
Worker Host 构建 Logger / Telemetry / DB / Runtime
        ↓
claimWorkerPID
        ├── acquire <pid-file>.lock
        ├── 检查旧 PID + process identity
        └── 写入 PID Record
        ↓
启动 Worker Health endpoint（初始未就绪）
        ↓
每个 Group 创建 Worker Runner
        ↓
每个 Worker Instance 执行 Run / ReportsReady
        ↓
Consumer 完成连接和订阅后 Ready
        ↓
所有 Group/Instance Ready
        ├── probe = ready
        └── daemon notify pipe -> parent 收到 ready
        ↓
处理任务 / 监督重启 / Drain
        ↓
关闭模块、DB、Telemetry、PID；释放进程锁
```

这条链路比上一轮完整。特别是进程锁、daemon 子进程参数重建、启动握手、PID identity token 都分别解决了之前的竞态或误报问题。

### 3.3 Consumer 通用处理链路

```text
Kafka / AMQP / RocketMQ / Redis Queue
        ↓
协议适配器拿到原始 Delivery
        ↓
统一 Processor.Handle
        ├── Decode Envelope
        ├── Claim 幂等台账（可选）
        ├── Handler
        ├── Succeed / Fail / Park
        └── 由最终返回值决定 ACK / Commit / NACK / Redelivery
```

当前已经将协议差异与业务 Handler 解耦。需要注意：不同 Broker 的 Delivery/ACK 语义仍然不同，统一 Processor 并不代表所有协议都具备相同的“批量、重投、顺序、可见性”保证。

### 3.4 Claim Runner 调用链

```text
worker ticker
   ↓
claim.Runner.Run(ctx, owner)
   ├── Queue.Reap
   ├── Queue.Claim(owner, batch)
   ├── for task in tasks（串行）
   │     ├── Handler(handleCtx, payload)
   │     ├── Succeed(ctx, task) / Fail(ctx, task, err)
   │     └── OnSuccess / OnFailure
   └── errors.Join
```

设计优点：业务只提供一个 Handler；Claim/Succeed/Fail 的 fencing 字段由 Runner 原样传递；处理耗时、业务 ID、行 ID、尝试次数、dead、leaseLost 都集中在 `Result` 中；结果钩子在持久化结果后运行。

但 Claim 的租约目前按“行被批量认领的时间”计算，而不是“行开始执行的时间”。这是本轮的主要风险，详见 P1-01。

---

## 4. 本轮新发现的问题

## P1-01：Claim 批量认领后逐条串行执行，批次尾部任务的租约可能在开始执行前过期

**优先修复**

**涉及文件**

- `support/claim/claim.go`
- `support/claim/runner.go`
- `examples/demo-service/worker/group1/scan_claim.go`

### 现状

`support/claim/claim.go` 默认值：

- `DefaultBatch = 100`
- `DefaultLease = 60s`
- `DefaultMaxAttempts = 5`

`Queue.Claim()` 在一个事务中选取一批任务，并统一更新每一行的 `start_handle_time` 与 `lease_until`。`lease_until` 以本次 Claim 执行时的数据库时间为基准写入。

`Runner.Run()` 随后使用普通 `for` 循环串行处理返回的 `tasks`。每条任务调用 `process()`，在其中用 `context.WithTimeout` 建立 Handler context，Handler 返回后再执行 Succeed 或 Fail。

也就是说，批次中所有任务的租约同时开始计时，但任务不是同时开始处理。

### 风险推导

假设使用默认参数：

- Batch = 100
- Lease = 60 秒
- 每条 Handler 平均需要 1 秒
- Runner 串行执行

第一条任务很快执行，后面的任务却要排队。第 61 条任务开始前，租约可能已经过期；第 100 条任务可能在原租约到期数十秒后才真正进入 Handler。

此时另一个 Worker 副本执行 `Reap()`，会把已过期的 Claimed 行重新放回 Pending。随后它可以再次 Claim 并执行同一条任务。原 Runner 并不知道任务已在排队期间被回收，仍会按旧 Task 继续执行 Handler。

`owner + attempts` 的 fencing 只保护最终 Succeed/Fail 的数据库更新，**不能撤销旧 Handler 已经发出的支付、库存、HTTP、消息发送等业务副作用**。旧 Handler 即使最后收到 `ErrLeaseLost`，重复副作用也已经发生。

这不是只有“极慢 Handler”才会触发的问题，而是当前默认批大小与串行处理模型没有被放进同一个租约计算公式。

### 为什么 Demo 参数暂时不容易触发

`examples/demo-service/worker/group1/scan_claim.go` 的示例配置使用：

- Batch = 3
- Lease = 30 秒
- HandlerTimeout = 5 秒
- Concurrency = 2

示例任务主要是一次数据库查询/更新，批次较小且租约余量较大。因此示例设置比框架默认值安全。但这并不能证明通用 `claim.Config` 默认值适合任意生产 Handler。

### 建议修复方向

优先采用简单、能证明租约正确性的方案，不建议先增加复杂的任务调度层：

1. **短期最稳妥：降低默认 Batch，或让 Runner 明确限制批次大小。** 对 Handler 执行耗时不可控的工作，默认按小批甚至单条 Claim，避免领取大量尚未开始的任务。
2. **让批次与执行并发数形成约束。** 如果后续需要并行执行，Runner 应显式配置并发数，并保证 `ceil(Batch / Concurrency) × Handler 最大合法耗时 + DB 收尾余量 < Lease`。不要让一个批次 100 条却只有一个串行执行槽。
3. **需要保留大批次时，增加租约续期或排队期间的租约保护。** 续期必须通过 owner/attempts/status 做 CAS，不能只按 id 延长租约；丢失租约后，尚未开始执行的任务必须跳过，不能继续跑 Handler。
4. **修正文档条件。** 当前“Lease 至少为 p99 Handler 的 3 倍”不足以覆盖批量串行排队。必须把批大小与并发度加入条件，或者明确该约束只适用于 Batch=1。

建议先完成第 1、4 项，并增加慢 Handler + 双 Runner 的集成测试，再决定是否需要续租机制。

### 必须增加的回归测试

- 批量 Claim 3 条任务，Handler 每条阻塞到接近租约时间，第二个 Runner 周期性 Reap；验证批次尾部任务不会在旧 Runner 启动 Handler 前被另一副本接管。
- 第一个 Runner 的 Handler 开始执行后让租约过期，第二个 Runner 接管；验证旧 Handler 的业务副作用不会被当成“数据库 fencing 已经解决”。测试应明确这个边界，方案修复后必须保证只有一个 Handler 进入副作用区。
- Batch 大于 1、HandlerTimeout 小于 Lease，但总批处理时间大于 Lease 的情形。
- 处理失败、Succeed 写库失败、进程中断与 Reap 交错时，owner/attempts fencing 仍然有效。

---

## P2-01：HandlerTimeout 是协作式取消，不是强制执行时限

**涉及文件**

- `support/claim/runner.go`
- `support/claim/claim.go`
- `examples/demo-service/worker/group1/scan_claim.go`

`Runner.process()` 直接在当前 goroutine 调用 `r.handler(handleCtx, task.Payload)`，并在 Handler 返回后调用 `cancel()`。`context.WithTimeout` 只能向 Handler 发出 Deadline；它不能强制终止忽略 context 的 Go 函数、CPU 死循环、未传递 ctx 的阻塞 IO 或第三方库内部的不可取消调用。

因此 HandlerTimeout 的实际语义是：**向遵守 context 的业务代码提供超时信号**，而不是保证 Handler 一定在该时长内退出。Handler 如果忽略 ctx，Runner 会一直卡在该 Handler 上，后续批次任务不会继续；任务租约最终到期后还可能被其他副本接管。

### 建议

- 把注释和 README 中“限制 Handler 耗时”的措辞明确为“协作式超时 / deadline”，避免用户理解成强制 kill。
- Handler 必须把 ctx 继续传给 DB、HTTP、Redis、MQ 和下游 API；避免在业务内部 `context.Background()` 重建调用链。
- 不要简单把 Handler 放进 goroutine 后超时返回：那只会让 goroutine 继续运行，带来未受控的副作用和泄漏，并不能实现真正取消。
- 对不可取消的外部命令或进程执行，必须由外部进程边界实现终止能力，而不是依赖 Go context。

这是 API 语义/使用约束，不意味着当前 context 用法本身错误，但应在文档里写清楚。

---

## P2-02：Kubernetes Lease Renew 的 Update Conflict 分支没有释放本地 held 占用

**涉及文件**

- `gofy-adapter/adapter/lease/k8slease/k8slease.go`
- `gofy-adapter/adapter/lease/k8slease/k8slease_test.go`

### 现状

`Locker.Acquire()` 会先把 key 放进本地 `held` map，避免当前进程内两个 goroutine 对同一个 key 同时 Acquire。

`heldLease.Renew()` 在以下情况会调用 `unreserve(key)`：

- Lease 对象 NotFound；
- holderIdentity 或 fencing token 已被其他持有者修改。

但是在最后执行 `api.Update()` 时，如果返回 `apierrors.IsConflict(err)`，当前代码直接返回 `lease.ErrLeaseLost`，没有调用 `unreserve(key)`。

因此本地 `held` map 仍保留该 key。后续同一 `Locker` 再次 `Acquire(key)` 会在访问 Kubernetes API 之前直接得到 `ErrHeld`。如果调用方因 Renew 返回 LeaseLost 而放弃原 Lease 句柄、又没有机会 Release，这个 key 可能在当前进程内永久无法重新 Acquire。

### 建议

先明确 Update Conflict 的处理语义：

- 如果冲突后重新 GET 发现 holder 或 token 已变化，则确认 LeaseLost 并释放本地 reservation。
- 如果重新 GET 发现 holder/token 仍属于当前 Lease，只是 resourceVersion 被其他更新推进，可以有限次数重试 Renew；不要把普通资源版本竞争误判为已经被接管。
- 如果最终选择将 Conflict 统一视为 LeaseLost，则必须与所有其他 LeaseLost 分支保持一致，释放本地 reservation；调用方收到 LeaseLost 后必须停止受保护的业务写入。

### 测试缺口

`fakeAPI` 已经具备 `conflictNext`，但当前测试主要覆盖 holder/token 已被替换后 Renew 发现租约丢失的路径。应增加：

1. Acquire 成功；
2. 令下一次 Update 返回 Conflict，但不改变 holder/token；
3. 调用 Renew；
4. 验证 Renew 的语义与本地 held map 一致；
5. 释放/重新获取同一 key，确认不会永久陷入 `ErrHeld`。

另外，`Identity` 的说明要求“每进程唯一”，但 README 示例写 Pod 名。一个 Pod 若运行多个应用进程，仅 Pod 名并不保证每进程唯一。建议明确要求 `Pod UID + 进程/实例标识`，只有每 Pod 一个进程时才可直接使用 Pod 名。

---

## P2-03：Nacos Resolver 缓存同时过期时缺少 per-service 单飞刷新

**涉及文件**

- `gofy-adapter/client/nacos/resolver.go`
- `gofy-adapter/client/nacos/resolver_test.go`

### 现状

`Resolver.Resolve()` 的流程是：

1. 加锁读取 service 缓存；
2. 缓存有效则本地挑选实例并返回；
3. 缓存过期或不存在时释放锁；
4. 调 `ListInstances()` 查询 Nacos；
5. 查询成功后再次加锁更新缓存。

这段逻辑没有 singleflight，也没有按 service 的刷新互斥。假设某个热点服务的 CacheTTL 到期，100 个请求同时调用 Resolve，它们可能同时看到缓存过期、同时访问 Nacos，最终产生 100 次相同的实例列表查询。

Nacos 正常时这是控制面请求放大；Nacos 有故障、各请求都尝试回源时，还会让 Resolver 的缓存失效行为加重注册中心压力。StaleTTL 虽然允许失败时使用最近一次成功的实例列表，但它不能阻止并发请求一起回源。

### 建议

- 为每个 service 增加 singleflight/刷新状态，而不是对所有 service 用一把锁串行等待网络请求。
- 有 stale cache 时，刷新中的并发调用可以立即使用 stale 实例，或者等待同一个刷新结果；选择哪一种应由明确的 stale-while-revalidate 语义决定。
- 无缓存时，允许同一 service 只有一个 Nacos 请求在途，其他请求共享结果。
- 增加并发测试：同时让缓存过期并启动 N 个 Resolve，验证 ListInstances 只发生一次或有限次；同时测试刷新失败时 stale cache 在 StaleTTL 内可用、超出 StaleTTL 后必须报错。

---

## P2-04：RocketMQ 批量 Receive 的不可见时长覆盖整批，但批次按消息串行处理

**涉及文件**

- `support/rocketmq/consumer.go`
- `examples/demo-service/worker/group1/rocketmq_batch_order.go`
- `examples/demo-service/worker/group1/rocketmq_batch_producer.go`

### 现状

当前 RocketMQ Consumer 的批量语义是：一次 `Receive()` 最多拉回 BatchSize 条，然后一个 poll worker 对返回结果逐条执行 Handler，并对成功消息逐条 Ack。失败消息不 Ack，等待 InvisibleDuration 后再次投递。

Demo 的 BatchSize 为 10、PullWorkers 为 1、InvisibleDuration 为 15 秒。示例代码也明确说明批次内消息按顺序处理，Handler 看不到批次边界。

### 风险

Broker 对这批消息设置不可见时长时，计时从 Receive 返回/消息被投递时开始，不会因为当前 Consumer 正在处理批次前面的消息而自动为后面的消息延长时间。

如果前面几条 Handler 较慢，批次尾部的消息可能在排队期间已经超过 InvisibleDuration，重新对其他 Consumer 可见。其他副本可能开始处理它，而原批次稍后仍会处理该消息，产生重复执行。

因此这里的租约公式与 Claim 类似：不能只按“单条 Handler 最长耗时”设置不可见时间，还需要包含批次内部排队时间。

### 建议

- 对串行处理模式，要求 `InvisibleDuration > BatchSize × Handler p99 + ACK/网络余量`，并在配置文档中给出计算示例。
- 如果不能给出明确的处理耗时上界，降低 BatchSize 或采用逐条拉取。
- 如果后续引入批内并发，必须控制并发数、ACK 生命周期和顺序语义，不能只增大 PullWorkers 就认为问题解决。
- 检查 SDK 是否支持单消息 ChangeInvisibleDuration/ChangeVisibility；若支持，评估对尚未开始处理的批次尾部消息续期。不要在没有 SDK 支持的情况下假设已有自动续期。
- 增加慢 Handler、多 Consumer 副本的集成测试，确认一条批次尾部消息不会在原 Consumer 开始处理前就被第二个副本接管。

当前 Demo Handler 是轻量数据库状态更新，参数在演示场景下基本合理；这里列为生产配置边界，不表示 Demo 当前必然重复处理。

---

## 5. 运行约束与 P3 加固项

## P3-01：PID IdentityUnknown 仍允许按 PID 存活状态继续发送信号

**涉及文件**：`internal/pidfile/pidfile.go`、`internal/process/token_*.go`、`stop.go`。

新 PID Record 会保存进程 token；Linux、Darwin、Windows 都有各自的身份读取实现。`stopPIDFile()` 在 IdentityMismatch 时不发送信号，而是删除过期 PID 文件，这解决了大部分 PID 被系统复用后的误杀风险。

但旧格式 PID 文件或当前平台无法读取进程 token 时，`Verify()` 返回 `IdentityUnknown`。`stop.go` 会警告并继续按存活 PID 发送信号，以保持旧 PID 文件兼容。

这属于兼容性折中，不是新代码路径完全无保护。运维需要知道：**只有 IdentityMatch 才能证明目标进程就是写入 PID Record 的进程；IdentityUnknown 不是 Match。**

建议后续考虑：

- 在支持身份 token 的平台上，对新格式 PID 文件要求 Match；
- legacy PID 文件继续兼容，但明确提示风险；
- 提供显式的强制停止选项，而不是让 IdentityUnknown 和 IdentityMatch 在结果上过于接近。

## P3-02：daemon 启动握手超时后，子进程会继续运行

**涉及文件**：`worker_notify.go`、`internal/process/detach_unix.go`。

当前父进程等待子进程写出 `ready` 或 `error`。默认启动等待 30 秒。超时后父进程返回“尚未 Ready”的错误，但有意不杀子进程，以免把一个慢启动的 Broker 连接误杀成故障。

这个选择可以接受，但运维侧必须把它理解为“不确定启动结果”，而不是“子进程已失败并退出”。子进程可能在父进程报错后继续 Ready。此时再次启动会被 `.lock` 阻止，这一点是正确的；但操作手册应指导用户先检查 PID、Worker 日志与 health endpoint，不能直接把超时当成子进程已死。

建议在超时错误里继续保留 PID，并提供明确检查指引；无需仅为消除“不确定性”就自动 Kill 子进程。

## P3-03：Redis Lock 不是 fencing lease，TTL 到期后的旧持有者不能继续写共享资源

**涉及文件**：`support/lock/`、`support/lease/`、`examples/demo-service/app/appio/redislock/`。

新 `support/lock` 的职责划分是合理的：

- `support/lock`：Redis 短临界区互斥，使用 token + TTL，支持 Unlock 与 Extend。
- `support/lease`：长期归属/选主，提供 fencing token。

`support/lock/README.md` 已说明 TTL 必须长于临界区、Extend 失败后不能继续当作持锁者写入、Unlock 错误不能无条件吞掉，且单 Redis 客户端模式的可用性取决于该 Redis 实例。

生产业务仍需遵守一个底层约束：Redis lock 的随机 token 用于校验解锁对象，不是资源侧可单调比较的 fencing token。锁 TTL 过期后，旧 Handler 即使还在运行，也可能与新持有者并发。对强一致写入应采用数据库条件更新、版本号或 `support/lease` 的 fencing token，而不是仅依赖 Redis Lock。

当前文档已经点出 TTL 约束，建议在关键业务示例或 README 再强调一次：锁丢失后必须停止后续写入；真正的并发正确性最终仍要落在共享资源的 CAS / fencing 上。

---

## 6. 上一轮审计问题复核

以下按上一轮报告的主要问题逐项核验。本节状态是根据本轮代码路径判断，不是仅根据 changelog 或提交说明判断。

| 上一轮问题 | 本轮状态 | 代码依据 / 复核意见 |
|---|---|---|
| Worker daemon 子进程复用 `worker restart` 参数，导致递归 restart / 无终端确认 | **已修复** | `WorkerStartArgs()` 重建纯启动 argv，子进程只运行 `worker --group=... --daemon=false` |
| Worker PID check/write 竞态导致同组可启动两个进程 | **已修复** | `claimWorkerPID()` 先 acquire 持久 `.lock` 文件，进程退出由内核释放锁；锁文件本身不删除 |
| Consumer 永久失败时 dead 记录写库失败仍 ACK | **已修复** | Processor 的 `park()` 错误向上返回；低层 Adapter 不会把它视作成功处理 |
| Consumer 在真正建立订阅前就 Ready | **已修复** | Kafka 在 consumer poll/session 已成功后上报；AMQP 在 declare/QoS/Consume 建立后上报；RocketMQ 在 SDK Start 后上报；Redis Queue 在首次成功消费操作后上报 |
| ReadyGate 重复 signal 消耗其他实例的 Ready 名额 | **已修复** | `readyGate` 使用 `map[Identity]struct{}` 按实例记账，重复/迟到信号忽略 |
| daemon 父进程仅凭创建子进程就报告启动成功 | **已修复** | `DetachWithNotify` + startup pipe；子进程所有实例 Ready 后上报 `ready`，失败时上报 error |
| Worker restart 不执行 Nacos Sync | **已修复** | Demo `planWorker()` 将 restart 设置为 `LoadNacos + Sync`，先同步配置再停止旧进程 |
| PID 文件可能因 PID reuse 杀错进程 | **部分修复 / 兼容风险保留** | 新 Record 保存 platform token，Mismatch 阻止 signal；Legacy / IdentityUnknown 仍有 liveness-only 兼容路径 |
| help/version/stop 因无关配置文件损坏而无法执行 | **已修复** | `planCommand()` 先分类命令，再按 LoadNacos/LoadConfig/BuildServices 决定加载哪些依赖 |
| Consumer 幂等租约使用本机时间，跨节点时钟偏差可提前/延后接管 | **已修复（支持方言）** | `dbNow()` 写 DB 时间；MySQL/Postgres/SQLite 的 `leaseExpiredPredicate()` 在数据库端比较；未知方言仍回退本机时间并有注释 |
| 订单事件乱序可能把 cancelled 更新回 paid | **已修复** | `orderEventRules` 声明允许的来源状态，UPDATE 在数据库内按 status IN sources 条件迁移；测试覆盖 cancelled 不回 paid |

### 6.1 对 Readiness 修复的补充说明

Readiness 现在更接近“可以接收工作”而不是“goroutine 已启动”。需要继续保持以下原则：

- Consumer 只有在连接和订阅建立后调用 Ready；
- 重连过程不应重复消耗其他实例的 Ready 名额；
- Worker 没有实现 ReadyReporter 时仍走兼容路径，默认在 Run 启动时满足 Ready；
- 如果某个分组启动失败，Host 不应报告全局 Ready。

当前 `readyGate` 和 Worker Host 的聚合逻辑符合上述目标。

---

## 7. 本轮新增能力与设计评价

### 7.1 Worker 进程治理

本轮新增/完善的进程治理是正确方向：

- 独立 `.lock` 文件承担原子进程互斥；PID 文件只承担定位职责；
- lock 文件不删除，避免一个进程持有旧 inode、另一个进程在新 inode 上获得锁；
- PID Record 存储进程身份 token，避免仅凭 PID 判断身份；
- daemon child argv 不再依赖原始命令行，restart 与普通 start 使用一致的 Worker 运行入口；
- 父子进程通过通知管道确认真正 Ready，而不是仅确认 `exec.Start()` 成功。

需要特别注意：当前 `claimWorkerPID()` 主要保护 Worker 进程路径；HTTP App 的 PID/监听仍有自身控制方式，不能把 Worker 的 `.lock` 保护直接理解为所有进程入口都使用同一锁机制。

### 7.2 Claim Runner 与处理时间字段

这轮把 Claim 的业务调用集中到 `Runner`，并新增/使用：

- `start_handle_time`
- `finish_handle_time`
- `fail_handle_time`
- `Result.Elapsed`
- `TaskID` / `RowID` / `Attempts` / `Dead` / `LeaseLost`
- `OnSuccess` / `OnFailure`

这是很有价值的封装：业务 Worker 不再重复写领取、完成、失败、重试和 dead 状态的样板代码。`claim.sql` 与运行时代码生成的 DDL 有测试对照，能降低业务复制迁移 SQL 后与实际查询不一致的风险。

需要把 P1-01 的“批次排队计时”问题修好后，Runner 才能把统一生命周期语义完整闭环。

### 7.3 Redis Lock 与 IO Snapshot

新 `support/lock` 与 `support/lease` 的区分合理，且 `appio/redislock` 根据当前 IO Runtime/Snapshot 解析客户端，避免在模块热更新后继续持有上一代 Redis 客户端。

这一设计保持了业务层不直接管理 Redis 连接池的边界。业务仍需要显式处理 `ErrAlreadyHeld`、`ErrUnavailable`、`Extend` 和 `Unlock` 错误；不要用无条件 `_ = Unlock()` 把锁丢失信号隐藏掉。

### 7.4 RocketMQ 批量拉取示例

当前示例展示的是“批量拉取 + 逐条 Handler + 逐条 Ack”，不是“批量业务事务处理”。文档对此区分得比较清楚：Handler 不知道批次边界，也不能把一批消息作为一次业务写入。

建议继续保持这种准确表达，不要将其简单宣传成“批量消费等于批量处理”。真正的批量业务写入需要 API 层明确接收整个 batch，并定义批次内部分失败、逐条 ACK 与事务原子性策略。

### 7.5 订单业务状态机

订单事件现在把状态迁移规则定义为 `Target + Sources`，并在 UPDATE where 条件内限制来源状态。这比先 SELECT 再 UPDATE 更可靠，因为状态合法性在数据库原子更新中完成。

零行结果再用 SELECT 区分：

- 订单尚不存在：返回可重试错误；
- 已经处于目标状态：按幂等成功处理；
- 当前状态不允许本次迁移：记录旧事件并忽略。

该实现修复了上一轮的真实状态回退问题，且有专门测试覆盖取消后收到迟到 paid 事件的场景。

---

## 8. `gofy-interface-api` 审查结果

### 8.1 契约层依赖边界

`contracts` 保持轻量，不依赖 Gofy 或 Gin。`cmd/refcheck` 通过 AST 扫描依赖规则，并禁止 `init()`，把架构约束变成可在 CI 执行的检查。

这比依赖开发者记住“这个目录不能 import 框架”更可靠。建议继续把 `make check` 作为发布前必须执行的检查。

### 8.2 Client 调用与重试

Client 当前具备：

- 固定 BaseURL 或 Resolver；
- 每次调用解析服务实例；
- 默认 Timeout、最大响应体限制；
- Retryable HTTP 方法与状态码判定；
- Idempotency-Key 保护非幂等写请求重试；
- Request ID、Header/Token 透传；
- SSE、下载、Chunked stream 独立 API；
- `WithConnectTimeout` 派生 Transport 池缓存上限与 Close 清理。

之前 `WithQuery` 初始化、connect-timeout transport 缓存上限、Client Close、SSE 重连重新解析 Resolver 等问题已在现有代码中体现为修正。

没有发现需要升级为 P1/P2 的客户端调用缺陷。当前重试策略是保守策略：只对安全方法，或显式通过 CallOption 配置 Idempotency-Key 的写请求执行重试；应保持这一原则，不能为了“提高成功率”扩大到任意 POST。

一个 API 使用注意点：如果 Idempotency-Key 仅通过请求 DTO 的 Header tag 设置，它虽然会被发送，但重试判定需要在请求构造前读取 CallOption，因此不能默认将 DTO Header tag 当成重试授权。建议文档明确：需要允许写请求重试时，通过明确的 CallOption/调用配置传入 Idempotency-Key。

### 8.3 OpenAPI 生成

`docgen.Build()` 对 operationID、路径、重复路由做检查；Schema 生成支持命名类型、指针 nullable、递归结构、JSON tags、required/default/example/validate 等字段元数据；Validate 禁止外部 `$ref`，避免验证期间隐式网络访问。

没有发现必须改变当前实现的重大问题。需要持续保证生成物由工具生成，不手动修改，并让生成文件在 CI 中做稳定 diff。

---

## 9. `gofy-adapter` 审查结果

### 9.1 Gofy Router Adapter

`BindGroup` / `BindAll` 将契约的 group prefix、route metadata、业务实现方法签名、中间件 alias 与 Gofy Router 绑定。实现了：

- 路由方法、路径、OperationID、URI 字段存在性检查；
- 实现方法签名校验；
- Middleware map / Registry / App registry 按优先级解析；
- 对缺失中间件在启动时失败；
- RateLimit、Global、命名中间件、Routes 的固定装配顺序；
- 可选 AuthValidator 校验契约声明与真实认证中间件策略是否匹配。

需要注意 `Spec.Auth` 是元数据，adapter 不会根据 `AuthRequired` 自动附加认证中间件；调用方必须显式声明 auth middleware，或配置严格的 `AuthValidator`。这是一个刻意的责任边界，不是自动鉴权功能。

此外 adapter 当前明确拒绝 SSE、Download、Chunked 等非 JSON ResponseKind。虽然 interface-api Client 支持流式调用，但服务端 Gofy Adapter 并不因此自动支持流式响应。必须在接口服务文档里说明两侧能力范围，不应从 Client 能力推断 Adapter 已实现。

### 9.2 Kubernetes Lease Adapter

适配器只依赖 typed coordination/v1 client，没有引入完整 Kubernetes Clientset；Lease key 做 DNS-1123 校验，不对不合法 key 静默字符替换；通过 resourceVersion 做更新竞争；使用 `leaseTransitions` 作为 fencing token；本地 `held` 集合防止同一进程内并发 goroutine 以相同 identity 重入获取。

关键运维规则：

- `Identity` 必须唯一到进程维度；
- 不要随意删除 Lease 对象，否则 fencing token 可能重置；
- RBAC 不应授予无必要的 delete 权限；
- 业务资源必须真正校验 fencing token，只有 Lease 自身有 token 并不自动保护外部 DB/缓存写入。

除 P2-02 Renew Conflict 分支外，没有发现需要改变适配器整体设计的问题。

### 9.3 Nacos Resolver

当前支持随机、轮询、权重选择；CacheTTL 缓存；StaleTTL 在 Nacos 不可用时使用最后一次成功的实例列表；默认过滤不健康实例；可按 namespace/group/clusters 限定服务列表。

功能范围合理。主要改进点是 P2-03 的并发刷新单飞；此外应增加刷新耗时、刷新失败、命中 stale cache 等可观测指标，便于判断服务发现退化是否发生。

---

## 10. 建议修复顺序

### 第一阶段：先收敛批量任务的租约语义

1. 修复 Claim 批量领取与串行执行的租约窗口问题（P1-01）。
2. 明确 HandlerTimeout 是协作式 Deadline（P2-01），修订字段注释与 README。
3. 增加双 Runner、慢 Handler、批次尾部任务过期的集成测试。

这一步优先级最高，因为它关系到业务副作用是否可能被重复执行。

### 第二阶段：补齐适配器异常分支

1. 修复 Kubernetes Lease Renew Conflict 的本地 reservation 处理（P2-02）。
2. 为 Renew Conflict 增加测试，不只是覆盖 holder/token 已变化的情况。
3. Nacos Resolver 增加 per-service refresh singleflight（P2-03）。
4. 对 Nacos 缓存过期并发请求、stale fallback 增加并发测试。

### 第三阶段：约束 MQ 批量配置

1. 文档说明 RocketMQ `BatchSize`、`PullWorkers`、`InvisibleDuration` 与 Handler 耗时之间的关系。
2. 先通过配置约束/文档降低风险，不要立刻引入复杂的可见性续期机制。
3. 增加慢 Handler 与多实例消费者的批次可见性测试。

### 第四阶段：运维可观测与边界文档

1. 明确 daemon startup timeout 后 child 仍运行的状态与排查步骤。
2. 明确 IdentityUnknown PID 的兼容风险。
3. 对 Redis Lock 的使用补充“锁丢失后不得继续写”与“强一致资源需要 fencing/CAS”约束。
4. 把 Worker、Claim、MQ、Nacos 的关键指标纳入现有日志或 Metrics，而非新建一套独立诊断子系统。

---

## 11. 回归测试清单

| 模块 | 测试场景 | 期望结果 |
|---|---|---|
| Claim | Batch 大于 1、串行 Handler、总耗时逼近/超过 Lease | 批次尾部任务不在开始执行前被其他 Runner 接管 |
| Claim | 两个 Runner 并发 Reap + Claim | 同一任务不会同时进入两个 Handler 的副作用区 |
| Claim | Handler 忽略 ctx Deadline | 文档/测试明确这是协作式取消，不误认为硬 kill |
| Claim | Handler 成功但 Succeed 写库失败 | 任务进入可恢复路径；业务 Handler 仍需幂等 |
| Claim | Lease 已被接管后旧 Handler Succeed/Fail | owner/attempts CAS 拒绝旧写入并回报 LeaseLost |
| K8s Lease | Renew Update Conflict，holder/token 未变 | 处理策略明确；本地 held 不出现永久残留 |
| K8s Lease | Renew 时 holder/token 已变化 | 返回 LeaseLost，不能清理新持有者的 Lease |
| Nacos | 同一 service 缓存过期时 100 个并发 Resolve | Nacos ListInstances 只发生一次或受控次数 |
| Nacos | 刷新失败但 StaleTTL 未过 | 继续使用最后一次成功缓存 |
| Nacos | StaleTTL 已过且刷新失败 | 返回错误，不使用过期过久的地址 |
| RocketMQ | BatchSize 大、Handler 慢、InvisibleDuration 较短 | 验证批次尾部消息不会被重复并发处理 |
| RocketMQ | 批次内一条失败，其余成功 | 每条消息独立 Ack；失败消息按可见性策略重投 |
| Worker | 同一 group 两个进程同时启动 | 只有一个获得 `.lock` 并写入有效 PID Record |
| Worker | daemon restart | Child argv 为启动形态；不递归 restart、不重新询问确认 |
| Worker | daemon child 建连失败 | Parent 收到 error，不报告启动成功 |
| Worker | daemon child 慢启动超过父进程等待时间 | Parent 返回明确“不确定/仍在运行”信息；child 不被误杀 |
| Worker | 两个 ReadyReporter，某实例重启并重复 Ready | ReadyGate 只按实例计一次，不误判全部 Ready |
| Consumer | dead 状态落库失败 | Processor 返回错误，协议层不 ACK/Commit |
| Consumer | 实际 Broker subscribe/topology 失败 | Worker health 不进入 ready |
| Order | cancelled 后收到迟到 paid | 状态保持 cancelled，消息被识别为过期迁移 |
| Order | 事件先于订单行到达 | Handler 返回可重试错误，而不是永久忽略 |
| API Client | POST 无 Idempotency-Key | 不自动重试写请求 |
| API Client | POST 显式 Idempotency-Key | 按配置重试并复用同一业务幂等键 |
| Adapter | AuthRequired 路由未声明 auth middleware | 严格 AuthValidator 下启动失败 |
| Adapter | SSE/Download/Chunked 路由绑定 | 启动阶段明确报不支持，不静默注册错误 Handler |

---

## 12. 测试与验证范围说明

本轮完成的是**源码级审计**：对仓库当前提交的关键实现、调用关系、默认值、配置和现有测试进行检查，并通过代码路径推导潜在竞态与失败场景。

本轮没有在真实环境执行以下项目，因此不能把报告中的风险称为“已在线上复现”，也不能声称整仓库测试全部通过：

- `go test ./...`、`go test -race ./...`、`go vet ./...`；
- Claim 在真实 MySQL 8 多副本下的并发故障注入；
- Kafka、RabbitMQ、RocketMQ、Redis Broker 的真实连接、重投和 ACK 测试；
- Kubernetes API Server 上的 Lease 并发与资源版本冲突测试；
- Nacos 故障、实例列表高并发刷新与 stale fallback 压测；
- 多进程 daemon/restart 在 Linux、macOS、Windows 的全矩阵实测。

仓库中已存在不少针对性单元测试和集成测试，例如 Worker PID/daemon、ReadyGate、Claim Runner、订单状态机、K8s Lease fake API、Nacos fake server 等。下一轮应优先补齐本报告指出的测试缺口，再运行完整 `make check` / 项目测试流水线。

---

## 13. 最终结论

Gofy 当前架构方向是正确的，上一轮列出的主要生命周期与 Consumer 问题已有真实代码修复，不需要重新设计 App、Worker、Consumer 或接口契约层。

当前优先级应集中在三个事实：

1. **Claim：批量领取的租约从统一 Claim 时刻开始，而 Runner 串行执行。默认 Batch=100 与 Lease=60s 不能只用单条 Handler 的 p99 来证明安全。** 这是本轮最需要先修的正确性问题。
2. **RocketMQ：批量 Receive 的 InvisibleDuration 包含批次内部排队时间。** 需要把 BatchSize 与 Handler 耗时一起纳入配置约束。
3. **Kubernetes Lease 与 Nacos Resolver：** 前者补齐 Renew Conflict 的本地状态清理，后者增加按 service 的并发刷新合并。

其余主要变化值得保留：Worker 进程锁与 PID identity、daemon 启动握手、按协议真实订阅 Ready、Consumer dead 落库失败不 ACK、DB 时钟 Lease、Redis Lock 与 Lease 分工、订单事件状态机、API 契约层依赖隔离。

建议以本报告的 P1-01 为下一轮代码修复入口。先把 Claim 批量与租约模型收敛，再处理两个 adapter 异常分支和 MQ 批量配置约束；不建议借此扩展成一轮无边界的框架功能开发。
