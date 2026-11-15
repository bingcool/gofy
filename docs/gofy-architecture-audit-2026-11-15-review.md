# 《Gofy 最新架构复审与第二轮优化建议》复核

> 复核日期：2026-11-15
> 状态：本文的修正已于 2026-11-15 22:20 合并进原报告
> [`gofy-architecture-audit-2026-11-15.md`](gofy-architecture-audit-2026-11-15.md)；
> 本文保留为逐条核对的证据与过程记录；文中引用的“报告原文”均指合并前版本。
> 复核对象：`docs/gofy-architecture-audit-2026-11-15.md`
> 复核基线：gofy `main` @ `0859044`；`gofy-interface-api`、`gofy-adapter` 用本地
> 相邻 checkout 当前版本对照。
> 方法：逐条读源码与调用链，只对可验证的结论下判断；未在真实 MySQL / MQ /
> Kubernetes 环境复现实测，涉及并发行为的结论按代码语义推断并标注。

## 1. 总体判定

报告的架构判断成立：分层、契约拆仓、Worker 进程边界、Consumer 统一链路、
Claim/Lease 分离、Query 只读层这些结论都与代码一致，不建议大改的方向也对。

问题清单需要做少量修正：

| 项目 | 数量 | 说明 |
|---|---:|---|
| P1 完全成立 | 5 | P1-01、P1-03、P1-04、P1-05、P1-06 |
| P1 部分成立，建议降级 | 1 | P1-02：泄漏成立但被默认 Recovery 挡住；影响是 drain 超时不是永久泄漏 |
| P2 成立 | 7 | P2-02、P2-03、P2-04、P2-06、P2-07、P2-08、P2-09 |
| P2 与 P1 重复 | 1 | P2-01 是 P1-03 的另一面，应合并 |
| P2 细节需修正 | 1 | P2-05 对“已关闭 consumer”的描述不准确 |
| 事实错误 | 1 | 报告 §3.4 写 `--group=all`，实际是 `--group='*'` |
| 修复方案需修正 | 2 | P1-01 的方案违反依赖方向；P1-04 的 skip 语义会丢消息 |
| 报告遗漏 | 2 | restart/exit 失败后永久摘流；drain 期间 Worker readiness 仍为 200 |

## 2. P1 逐条核对

| 编号 | 结论 | 关键证据 | 需要的修正 |
|---|---|---|---|
| P1-01 | **成立** | `io_runtime.go:172` 在 `Start` 里 `Publish`；`reload.go:240-301` 先启动模块、后 `handlerRef.Store`；`generation.Set.Middleware` 每次请求读 `Current()`；失败时 `io_runtime.go:182` CAS 成 `nil`，旧快照不恢复 | 修复方案要改，见 §4.3 |
| P1-02 | **部分成立，降级 P2** | `generation.go:181-182` 确实没有 `defer`；但 `option.go:164` 默认开启 Recovery，`start.go:370` 安装了它 | 定级与影响描述，见 §4.2 |
| P1-03 | **成立** | `claim.go:244-253` 三条收尾 SQL 只比较 `id + owner + status`；`identity.go:36-38` 的 `String()` 不含 `Host` | 修复顺序，见 §4.4 |
| P1-04 | **成立，且比报告更严重** | `consume.go:641-648` 的更新条件是 `status <> 'done'`，`processing` 也会命中；`consume.go:654-681` 的 `succeed/fail/park` 只按 `(consumer, message_id)` 写回 | 修复方案有丢消息风险，见 §4.5 |
| P1-05 | **成立** | `worker_host.go:162-165` 先 `setReady`，`174-178` 才 `go runner.Run` | 需同时覆盖 drain，见 §5.2 |
| P1-06 | **成立** | `client/call.go:53` 只初始化 `headers`；`client/options.go:62` 直接 `o.query.Add` | 无 |

### 2.1 P1-01 的补充证据

失败回滚的链路是完整可复现的：

1. 新 io 模块 `Start` 成功 → `Publish(new)` 把进程级 current 覆盖为新快照；
2. 后续某个模块 `Start` 失败 → `reload.go` 停止已启动的新模块；
3. io 模块 `Stop` 执行 `CompareAndSwap(new, nil)` → current 变成 `nil`；
4. 旧 handler 仍在服务，请求进入后 `appio.WithContext(ctx)` 找不到快照，直接 panic
   （被 Recovery 转成 500）。

旧快照在步骤 1 已被覆盖，没有任何恢复路径。报告对症状的描述准确。

### 2.2 P1-04 的补充证据

报告说“`processing` 时第二次投递可以再次进入 Handler”，代码确认。额外两点：

- 代码注释“只有其中一个能把行改回 processing”是**错的**：两个并发更新都满足
  `status <> 'done'`，第二个同样返回 `RowsAffected = 1`；
- `fail()` 在旧 Handler 晚到时可以把已经是 `done` 的行改成 `failed`
  （`consume.go:662-681` 没有 status 条件），导致已完成消息被重新处理。这比报告
  写的“succeed 覆盖新结果”更严重。

## 3. P2 逐条核对

| 编号 | 结论 | 说明 |
|---|---|---|
| P2-01 | 成立但与 P1-03 重复 | `support/worker/README.md:138` 声称 `owner = id.String()` 能追溯到 Pod，但 `String()` 不含 Host。应并入 P1-03，不单独计数 |
| P2-02 | 成立 | `sse.go:57` 在重连循环外解析 `base`，`sse.go:91` 重连复用；普通 Call 在 `call.go:80-86` 每次重试都重新解析 |
| P2-03 | 成立 | `client.go:207/226` 用 `sync.Map` 按 `connectTimeout` 缓存，仓库内无 `Close` / `CloseIdleConnections` |
| P2-04 | 成立 | `k8slease.go:183-193` 在 holder 等于本实例时刷新并沿用同一 token，同 Identity 的并发 Acquire 都会成功 |
| P2-05 | **部分成立，措辞需修正** | `consumerWorker.Run` 只在 `ctx.Err() != nil` 时 `Close`；而 `Runner.supervise` 在 ctx 取消时直接返回、不再重试。准确说法是：**context 仍存活时的错误会让 Runner 重跑一个已经 `Consume` 过的 consumer**；“复用已关闭的 consumer”只发生在不会重试的那条路径上 |
| P2-06 | 成立（本轮新代码） | `order_queries.go:153/182` 用 `MAX(id) AS last_order_id`，列名带时间语义但实际是最大主键 |
| P2-07 | 成立 | `watch.go` 的 `prepare` 是固定时长 `pause`，不 join 请求或 Handler |
| P2-08 | 成立 | `start.go:168` 启动模块（watcher 随之异步起来）→ `197` Serve → `204` 注册；`StopAccepting` 在 registrar 为 nil 时直接返回 nil，不阻断启动 |
| P2-09 | 成立 | `gofy-adapter/README.md` 无 `k8slease` 相关内容 |

## 4. 需要修正的报告内容

### 4.1 事实错误：`--group=all`

报告 §3.4 的部署段落写 `--group=all`。实际取值是 `--group='*'`
（`internal/commandline/worker.go:13`，`WorkerGroupAll = "*"`）；`all` 只是
`--group='*'` 进程的 pid 文件后缀（`-worker-all.pid`，见 `docs/worker-guide.md:74`）。
命令写错会直接导致本地冒烟测试跑不起来。

### 4.2 P1-02 应降为 P2

两个子问题都成立，但影响被高估：

- **release 没有 defer**：默认 `recovery: true`（`option.go:164`），gin 的
  Recovery 在 `start.go:370` 安装，普通业务 panic 会被捕获并正常 release。真实
  触发面只剩 `http.ErrAbortHandler`、Recovery 被显式关闭、或 panic 发生在
  Recovery 自身。
- **后果不是永久泄漏**：`retireGeneration` 在 `wait` 超时后仍会继续停模块、关资源
  （`generation.go:197-207` 只记 warning）。所以最坏情况是**每次 Reload 多等一个
  drain 超时（默认 10s）**，不是资源永久不释放。
- **摘流竞争**：`generation.go:168` 只在 Acquire 前检查一次，确实存在“读到 true
  之后被置 false”的窗口；但这类请求本来就与 `StopAccepting` 并发进入，且
  restart/exit 后面还有固定 grace period，影响远小于 P1。

建议：保留两条修复（`defer release`、Acquire 后二次检查 accepting），定级 P2。

### 4.3 P1-01 的修复方案违反依赖方向

报告建议：

```go
type generation struct {
    snapshot *appio.Snapshot
}
```

`generation` 在框架包 `gofy` 里，`appio` 是 demo 的应用包。这样写会让框架 import
业务代码，**无法编译**，也违反三个仓库的依赖方向。

两个可行替代：

**方案 A（业务侧，零框架改动）**：把快照挂到“每代一个”的 `Container` 上。demo 的
`reloader.factories()` 每次 Reload 都新建容器，中间件注册也按代重建；让该代中间件
闭包读本代容器的快照，而不是读进程级 `currentSnapshot`。失败回滚时旧容器仍持有旧
快照，天然不受影响。全局 `Publish` 只保留给 script / worker / 测试。

**方案 B（框架侧，通用扩展点）**：框架不持有具体类型，只提供一个 binder 钩子：

```go
type RequestBinder func(ctx context.Context) context.Context
// handlerSwap.ServeHTTP: acquire 成功后 ctx = binder(ctx) 再进 handler
```

app 在构造每代模块时把“这一代的快照”闭包进 binder。框架仍然不依赖 `appio`。

报告“Start 只构建、不发布”“旧代 drain 后才 Stop 模块”两条约束是对的，应保留。

### 4.4 P1-03 应优先用 `attempts`，而不是新增 `claim_token`

`claim_token` 是更干净的 fencing，但有一项现实成本报告没提：**表由应用维护，
框架只提供 DDL**（`claim.go:5-10`）。新增列意味着所有使用方都要改表。

`Task.Attempts` 已经在返回值里，且每次 `Claim` 必然自增
（`claim.go:241`、`326-330`）。收尾 SQL 加上 `AND attempts = ?` 即可获得同样的
fencing 效果，**不需要改表**：

```sql
WHERE id = ? AND owner = ? AND status = 1 AND attempts = ?
```

`attempts` 理论上的弱点是可被运维手工重置；可作为后续可选升级为 token，而不是
第一版就要求迁移。报告已把 attempts 列为备选，建议把顺序倒过来。

### 4.5 P1-04 的“Skip”会丢消息

报告建议的状态机写：

```text
processing 且 lease 有效 → Duplicate / Skip
```

但当前 `Handle` 在 deduper 返回 `false` 时是 `return nil`（`consume.go:394-401`），
也就是 **ACK**。如果第二次投递在第一次仍在处理时被 ACK，而第一个 Handler 随后
崩溃，这条消息就永久丢失——备份副本已经被确认掉了。

正确的接管语义应是：

- `processing` 且 lease 未过期 → **不 ACK**，返回可重试错误或以延迟方式重新入队，
  让 broker 稍后再投；
- `processing` 且 lease 已过期 → 新 token 接管，旧 token 的写回全部失败；
- `done` / `dead` → 可以 ACK（终态）。

另外，不加列的接管方案是复用 `updated_at` 作为隐式 lease：读出后与当前时间比较，
再用 `updated_at` 做 CAS。这比新增 `processing_until` 更容易落地，也兼容 SQLite
测试。

### 4.6 P2-05 的措辞

见 §3 表格。建议改为：“低层 Adapter 的 consumer 是一次性对象；Runner 在 context
仍存活时重试会复用已经 `Consume` 过的实例。生产使用高层 `NewRocketMQ`，或把低层
Adapter 改成接收 Consumer Factory。”

## 5. 报告遗漏的两项

### 5.1 restart / exit 动作失败后，进程会永久停在“已摘流、已注销”状态

这条比 P2-08 更实际，建议新增为 P1 或 P2 首项：

1. `prepare()` 先调 `WithDrain` → `App.StopAccepting()` 把 `accepting` 置 false，
   并把 `a.registrar` 取走、置 nil（`start.go:737-758`）；
2. 随后 `syscall.Exec` 或发信号失败；
3. `watch.apply` 的错误只记日志，watcher 继续轮询（`watch.go:184-186`），
   **没有任何路径把 `accepting` 改回 true**（全仓库只有 `gofy.go:146` 在初始化时
   写成 true）。

结果是：进程还活着、已从 Nacos 注销、所有请求 503；如果 k8s 的 liveness 探测走
`/healthz`（同一 gin 引擎，`start.go:700`），最终会被重启；如果 liveness 是 TCP
或没有 liveness，就是一个长期占着资源的僵尸进程。

建议：动作失败时要么恢复闸门（新增显式的 resume 语义），要么把失败当作致命错误
退出交给监管者；至少要有 error 级别日志和指标。

### 5.2 drain 期间 Worker 的 readiness 不会翻转

`worker_host.go:191-196` 的 `health.setStopping()` 在所有 runner 都退出之后才调用。
也就是说 SIGTERM 之后、drain 进行中的整段时间里 `/healthz` 仍是 200。报告 P1-05
的验收标准写着“进入 drain 后立刻回到 503”，与当前实现不符。

建议把 `setStopping()` 移到收到停止信号、`cancelRunners` 之后立即执行，而不是等
`errCh` 收完。

## 6. 报告中成立、无需改动的部分

以下结论已逐条对照代码，保持原样：

- 三仓依赖方向（`gofy-interface-api` 不依赖 `gofy`；`gofy-adapter` 依赖两者）；
- `gofy-adapter` 与 demo `go.mod` 的本地 `replace` 现状；
- Worker 不注册 HTTP 路由、不向 Nacos 注册；Worker 独立进程 / PID / 探针；
- `support/consumer` 统一封装信封、trace、错误分类与台账；Kafka 默认手动提交
  （`kafka.go` 的 `CommitMode` 零值落到 manual）；
- Redis Queue “弹出即删除、崩溃会丢”的能力边界（`queues.go:16-23`）；
- Processor 在 `succeed()` 标记失败时仍 ACK 的取舍（`consume.go:419-429`）；
- Claim 与 Lease 分开、`SKIP LOCKED` 事务内认领、`Reap` 双状态；
- Query 只读层的定位与访问路径；
- 根 `make check` / `check-all`，以及 `gofy-interface-api`（fmt/vet/race/refcheck）
  与 `gofy-adapter`（fmt/vet/race/guards）的检查目标。

## 7. 修正后的优先级

| 顺序 | 项目 | 变化 |
|---|---|---|
| 1 | P1-01 快照与 Handler 同代发布 | 保留，修复方案改为 A 或 B |
| 2 | P1-04 消费台账并发与写回 CAS | 保留，skip 不得 ACK |
| 3 | P1-03 Claim fencing | 保留，先用 attempts 落地 |
| 4 | P1-05 Worker readiness | 保留，同时修 drain 期 200 |
| 5 | **新增：restart/exit 失败后的永久摘流** | 建议 P1 或 P2 首项 |
| 6 | P1-06 `WithQuery` nil map | 保留（interface-api） |
| 7 | P1-02 降级为 P2 | 定级调整 |
| 8 | P2-02 / P2-03 / P2-04 / P2-06 / P2-07 / P2-08 / P2-09 | 保留 |

报告“不要新增更复杂的调度/工作流抽象，先把 Generation、Claim、Consumer Ledger
的并发与生命周期语义闭合”的最终建议成立，本次复核不改变这个判断。
