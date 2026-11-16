# support/worker

把常驻任务（MQ 消费、扫表、对账）从 HTTP 进程里拆出来的最小运行时：一个分组内的
worker 在同一进程里同生共死，进程由 `app worker --group=<g>` 启动，HTTP 服务不会
拉起任何一个。

本包只关心三件事：**谁在跑**（`Group` / `Identity`）、**失败了怎么办**（`Policy`）、
**怎么停**（两级 context + drain 预算）。业务循环本身、以及"多副本如何不重复处理"，
分别属于业务代码与 `support/claim`、`support/lease`、消费者适配器。

> 部署方式、k8s 清单、排障表见 [`docs/worker-guide.md`](../../docs/worker-guide.md)；
> 设计取舍见 [`docs/worker-daemon-design.md`](../../docs/worker-daemon-design.md)；
> 可直接跑的分组示例见 [`examples/demo-service/worker/README.md`](../../examples/demo-service/worker/README.md)。

## 与其它生命周期的边界

| | `support.Starter` | `script.Script` | `worker.Worker` |
| --- | --- | --- | --- |
| 宿主 | HTTP 进程 | 独立进程（一次性） | 独立进程（常驻） |
| 生命周期 | 跟随 HTTP 监听就绪/关闭 | 跑完即退 | 跟随 SIGTERM，带 drain 预算 |
| 多副本 | 每个 POD 都跑一份 | 手动指定一次 | 分区型/抢占型安全，全局型需 lease |
| 典型用途 | nacos_watch、注册心跳、连接预热 | 数据修复、一次性迁移 | MQ 消费、扫表、对账 |

判定规则：任务与"这个 HTTP 实例"强相关（本地缓存、注册、监听配置）→ `Starter`；
可以脱离 HTTP 且需要常驻 → `worker`。

## 快速开始

### 1. 在 main 里注册分组

```go
group, err := worker.NewGroup("order",
	worker.Func("order-scan", 2, func(ctx context.Context) error {
		return scanLoop(ctx) // 与 HTTP handler 调用同一个 service 方法
	}),
	worker.Func("order-consumer", 1, func(ctx context.Context) error {
		return consumeLoop(ctx)
	}),
)
if err != nil {
	return err
}

app, err := gofy.New(
	gofy.WithName("demo-service"),
	gofy.WithWorkerGroups(group),
	gofy.WithWorkerDrainTimeout(20*time.Second),
)
```

### 2. 运行

```bash
app worker --list                     # 列出分组、worker 与并发数，不连数据库
app worker --group=order              # 跑整组
app worker --group=order --name=order-scan
app worker --group=order --concurrency=4
app stop --worker=order               # 按 pid 文件停掉这一组，不碰 HTTP
```

### 3. 进程产物

| 产物 | 位置 | 由谁决定 |
| --- | --- | --- |
| pid 文件 | `<pid_file>` 加 `-worker-<group>` 后缀，如 `app/demo-service-worker-order.pid` | 框架（未配置 `pid_file` 则不写） |
| 日志目录 | 示例落在 `storage/log/worker/<date>/{info,error}/<worker>.log` | 应用自行分支，用 `systemenv.IsWorkerService()` / `WorkerNames()` 判断；整组启动没有唯一 worker 名时用 `worker` |
| 端口 | 默认不监听；配了 `gofy.WithWorkerHealth(addr)` 才有 `/healthz` | 框架 |
| 退出码 | 正常停机 0，致命错误按 `gofy.ExitCode` 映射 | 框架 |

## 核心概念

### Worker

```go
type Worker interface {
	Name() string
	Run(ctx context.Context) error
}
```

`Name` 在分组内唯一，遵循 `^[a-z][a-z0-9-]{0,62}$`（与模块名、脚本名同一套规则）。
`Run` 拥有循环本身，必须**阻塞到 ctx 被取消**，然后释放资源并返回 `nil`。

只要一个函数时用 `worker.Func(name, concurrency, fn)`，第二个参数就是这个 worker 默认要跑的实例数，不必为单个字段定义结构体，也不必再写一条宿主选项。

### Group

`NewGroup(name, workers...)` 校验并构造：分组名合法、至少一个 worker、worker 非 nil、
名字不重复、名字合法——非法集合在构造阶段就失败，不会等到进程跑起来才发现。

- `Name()` / `Workers()` 返回副本，调用方改名切片不会影响分组；
- `Subset(names)` 按名字裁剪，`worker --name` 就是这样实现的；名字不存在返回
  `ErrUnknownWorker`，空切片返回原分组；
- `ValidName(name)` 导出给 CLI 校验（`stop --worker` 会把它拼进文件名）。

### 多个分组

`WithWorkerGroups` 可累加，分组名必须唯一；**一个进程仍然只运行一个分组**，`--group`
在启动时选一个：

```go
// orderScan、reportSummary 由 worker.Func(name, n, fn) 构造，计数跟着 worker 走
order, err := worker.NewGroup("order", orderScan, orderHeartbeat)
report, err := worker.NewGroup("report", reportSummary)

app, err := gofy.New(
	gofy.WithWorkerGroups(order, report),
)
```

| 维度 | 多分组时 |
| --- | --- |
| `worker --list` | 列出全部组，不启动、不连数据库 |
| `worker --group=x` | 只启动 x；x 不存在时在构建 logger/DB 之前就返回 `ErrWorkerGroupNotFound` |
| pid 文件 | 每组独立：`<pid_file>` 加 `-worker-<group>` 后缀 |
| 日志 | 每组独立进程写自己的文件；文件名主干是 worker 名，建议带分组前缀保证全局唯一 |
| 并发数 | 由各自的 `Func(name, n, fn)` 声明；`WithWorkerConcurrency` / `--concurrency` 可按 `group/worker` 覆盖 |
| 探针端口 | **进程级**：`WithWorkerHealth` 一个进程一个地址，同机跑多个分组要换端口；k8s 里每组一个 Pod 则无冲突 |

不提供"一个进程跑多个分组"的开关：分组的语义是组内同生共死、一个分组一个部署单位。
混跑会让 pid 文件、`stop --worker`、就绪探针的粒度同时失效，一个组崩溃也会拖走另一个组。

### Identity

```go
type Identity struct {
	Group string // 分组名
	Name  string // worker 名
	Index int    // 0 起的实例序号
	Total int    // 该 worker 的实例总数
	Host  string // hostname
}

func (i Identity) String() string // "order/order-scan#1"，日志与指标标签
func (i Identity) Owner() string  // "order/order-scan#1@pod-a"，claim 的 owner
```

Runner 把身份注入 context，业务用 `worker.IdentityFrom(ctx)` 取。认领任务行时用
`id.Owner()`：它带上 hostname，两个 Pod 跑同名、同序号的实例也能区分开，一行因此
能追溯回具体 Pod 的具体实例。`String()` 故意不含 host，保持日志与指标序列稳定。

`Owner()` 只回答"哪个实例"，不回答"该实例的哪一次认领"：同一实例在租约过期、
行被 Reap 之后重新认领同一行时，owner 字符串完全不变。所以 `claim` 的收尾语句还会
比较 `task.Attempts`——每次认领必然自增的那个值，见 [`support/claim`](../claim/claim.go)。

### Policy

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `Mode` | `RestartOnError`（零值） | `ExitOnError` 用于重启修不好的失败：配置错误、订阅不存在 |
| `Backoff` | `1s` | 首次重启延迟 |
| `MaxBackoff` | `30s` | 指数增长上限 |
| `MaxRestarts` | `0`（不限） | 单个实例允许重启的次数，超过后整组退出 |
| `Jitter` | `true` | ±20% 抖动，避免 broker 重启后整批 Pod 同时重连 |

`worker.DefaultPolicy()` 给出框架默认值。`MaxRestarts` 只在临时故障持续不断时才有意义：
如果连不上 broker 是永久性的，重启只是噪音，这时该用 `ExitOnError` 让进程退出、由
调度器暴露问题。

### Runner

`NewRunner(group, RunConfig)` 负责把分组的每个实例拉起来、监督重启、在停机时 drain。

| `RunConfig` 字段 | 默认 | 说明 |
| --- | --- | --- |
| `Policy` | `DefaultPolicy()` | 零值即默认策略 |
| `Concurrency` | 每项 1 | `worker 名 → 实例数`，小于 1 在 `NewRunner` 报错 |
| `DrainTimeout` | `20s` | ctx 取消后在途任务还能跑多久，必须小于进程停机总预算 |
| `Logger` | 丢弃 | 生命周期事件 |

应用通常不直接调用 `NewRunner`：`gofy worker` 子命令会在启动时按选项构造它。自行实现宿主
（不经 `gofy` 根包）时才需要自己调。

## 生命周期

### 启动

```
worker --group=order
├─ 建 logger / DB / telemetry / Runtime     与 HTTP 同源，不同实例
├─ 解析 --name / --concurrency
├─ 抢 .lock 文件（同组已有进程 → ErrAlreadyRunning）
├─ 写 pid 文件（pid 还活着 → ErrAlreadyRunning）
├─ 可选：启动 /healthz（先 503）
└─ Runner.Run
   ├─ 每个 worker × concurrency 个 goroutine
   ├─ 等所有"自行上报就绪"的实例上报 → /healthz 转 200
   └─ 阻塞，直到停机信号、致命错误或全部实例退出
```

### 进程所有权

`<pid 文件>.lock` 是真正的互斥点：内核把 `flock` / `LockFileEx` 只授予一个打开的文件
描述，进程退出（含 SIGKILL）时自动释放。pid 文件继续作为 `worker stop` 的地址，但"先检查
再写入"之所以安全，靠的是这把锁——两个启动不会都读到"没有活着的 pid"然后都写。

pid 文件本身也从裸 pid 升级成了一条 JSON 记录（pid / executable / claimed_at / token）：
pid 会被内核回收，崩溃后留下的文件可能指向一个无关进程，只凭"存活"发信号就会误杀。token 由
进程启动时刻派生（Linux 用 boot id + `/proc/<pid>/stat` 的 starttime，macOS 用 `kern.proc.pid`，
Windows 用创建时间），被复用的 pid 无法复制它。`stop` 在发信号前校验，不匹配就当作"未运行"
并删掉陈旧文件；旧格式（纯数字）仍然可读，但只能退回存活检查，并在日志里记一条警告。

范围限定：锁在本地文件系统上，**只挡同一主机上的两个进程**。Kubernetes 里同一分组的两个
Pod 各有文件系统，这把锁对它们无效；那种重复要靠副本数约束或 `support/lease` 之类的跨进程
选举。HTTP 的 `start` 不需要它：端口绑定本身已经是互斥的。

锁文件**不会被删除**，这是刻意的。在释放时 `unlink` 会引入经典竞态：已经打开旧 inode 的
进程能在释放后锁住这个没有名字的 inode，而之后的进程会新建文件并锁住新的 inode——两个进程
同时"独占"同一个分组，而且都不报错。文件是空的，既不会过期也不占空间；归属在内核的打开文件
描述上，进程结束（含 SIGKILL）就消失。所以进程停掉后残留 `*.pid.lock` 是正常状态，它不挡
下次启动，也已被 `.gitignore` 忽略。想工作树干净，正确做法是把 `--pid` 指到运行时目录
（如 `/var/run/demo-service.pid`），锁会跟着 pid 文件走，而不是去删锁文件。

### 就绪上报

`Run` 被启动不等于实例能接活：消息消费者在 `Run` 内部才建连、声明队列、加入消费组。
这类 worker 实现 `worker.ReadyReporter`，并在**订阅真正建立之后**调用 `worker.Ready(ctx)`；
`Runner.Ready()` 在**每个分组、每个实例**都上报后才关闭，宿主据此才把 `/healthz` 置为 200。
闸门按实例身份记账，同一实例重复上报（重连或重启）不会替其他实例扣数。

- 轮询任务（`Func`、`Ticker`）没有可等的东西，不实现该接口，`Run` 开始即就绪；
- 启动失败的分组永不关闭 `Runner.Ready()`，等待它的调用方不会把"启动失败"当成"已就绪"；
- 停机（含致命错误）一开始 `/healthz` 立即回到 503，而不是等 drain 结束。

消费者侧的上报时点由各传输层的 `ConsumeOptions.OnReady` 决定，都是"订阅可用"的那一刻：

| 传输 | 上报时点 |
| --- | --- |
| Kafka | 第一次无致命错误的 `PollRecords` 之后：此时消费组已加入、分区已分配 |
| AMQP（含延迟） | channel 打开、拓扑声明完成、`ConsumeWithContext` 返回投递流之后 |
| RocketMQ（含延迟） | SDK `Start` 成功之后：路由已解析、订阅已注册 |
| Redis 队列 / 延迟队列 | 第一次成功 `Pop` / `Pop` 到期集合之后（空结果也算） |

Kafka 仍会在进入消费循环前 `Ping` 一次 broker，但那只是让不可达快速失败，不是就绪声明。

### 失败与重启

一个实例 `Run` 返回非 nil 且 `Mode == RestartOnError` 时，Runner 按退避重启**该实例**，
其余实例不受影响。以下情况会**让整组退出**：

| 情况 | 原因 |
| --- | --- |
| `Mode == ExitOnError` | 重启修不好 |
| 重启次数超过 `MaxRestarts` | 持续失败，退出比空转更有信息量 |
| `Run` 在 ctx 取消前返回 `nil` | 违反阻塞约定（`ErrWorkerExited`） |

最后一条是刻意的：一个 worker 静默返回会让"进程活着但少了一个消费者"这件事没人发现。

### 停机：两级 context

```
SIGTERM / app stop --worker=order
├─ runCtx 取消         → 所有 Run 收到 ctx.Done，停止拉取新工作
├─ 等待 drain（默认 20s）→ 已在途的处理用 drainCtx 继续跑完
└─ drainCtx 取消       → 预算耗尽，进程退出（只告警，不算失败）
```

`drainCtx` 在停机之前始终有效：它由 `context.WithoutCancel(parent)` 派生，去掉取消传播、保留
value，预算只在 `runCtx` 被取消之后才开始计时。**这一点很关键**——如果预算从 `Run` 开始就计时，
一个跑得比预算久的 worker 会拿到已经过期的 context，每个在途 handler 都会返回
`context.DeadlineExceeded`。顺序固定为"先停拉取、再等 drain、最后关连接"，反过来会中断仍有
预算的在途任务。

**drain 预算耗尽不返回错误**：进程仍以 0 退出，只写一条 warn 日志。否则 k8s 会把一次
正常的 SIGTERM 记成失败，触发无意义的重启与告警。

### Run 的契约

写 `Run` 时逐条对照：

- [ ] 阻塞到 `ctx.Done()`，不要提前返回 `nil`；
- [ ] 收到取消后释放自己持有的资源（连接、文件、临时锁）；
- [ ] **必须可并发调用**：同一实例会被 `concurrency` 个 goroutine 共享（见下节）；
- [ ] 在途处理用 `worker.DrainContext(ctx)`，不要用 `ctx`；
- [ ] 不要把错误吞掉：返回错误才会触发重启策略与日志。

## context 里有什么

Runner 给每个实例的 `Run` 注入三样东西：

```go
func (w *scanWorker) Run(ctx context.Context) error {
	id, _ := worker.IdentityFrom(ctx) // 我是谁：owner、日志字段
	drain := worker.DrainContext(ctx) // 停机动作用它，在途处理跑完再退

	rt, ok := worker.RuntimeFrom(ctx) // logger / DB / tracer
	if !ok {
		return errors.New("worker runtime is missing from the context")
	}

	rt.Logger.Info("scan started", zap.String("worker", id.String()))
	_ = drain
	return nil
}
```

| 取值 | 来源 | 拿不到时 |
| --- | --- | --- |
| `IdentityFrom(ctx)` | Runner 注入 | 返回零值与 `false`；自行实现 Runner 的宿主需调 `WithIdentity` |
| `DrainContext(ctx)` | Runner 注入 | 返回 `ctx` 本身，因此调用方不必判空 |
| `RuntimeFrom(ctx)` | 宿主 `WithRuntime` 注入 | 返回 `false`；`gofy worker` 下一定有 |

`Runtime` 是脚本收到的 `*script.Runtime` 同源对象（`core.Runtime`）：`Logger`、`DB`、
`Version`、`TracerProvider` 都在里面。业务**不要**自己再开一个数据库连接池。

## 并发模型

`concurrency` 是**实例数**，每个实例一个 goroutine，共享同一个 `Worker` 值：

```go
// 反例：状态挂在接收者上，两个实例会互相覆盖
type badWorker struct{ processed int }

func (w *badWorker) Run(ctx context.Context) error {
	w.processed++ // 数据竞争
	return nil
}

// 正例：状态在 Run 内部
func (w *scanWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// 只读配置 + 局部变量
		}
	}
}
```

实例数默认由 worker 自己声明（`Func` 的第二个参数）；宿主用
`WithWorkerConcurrency` 或 `worker --concurrency` 覆盖，覆盖优先于声明。并发该开多少
按协议不同：

| 类型 | 建议 | 原因 |
| --- | --- | --- |
| Kafka | 保持 1，靠分区与副本扩容 | 单个 `Consumer` 只对应一个拉取 goroutine |
| AMQP | 1 个实例 + `prefetch 32~128` | 单 channel 上的预取比多实例更有效 |
| RocketMQ | 1 个实例 | SDK 内部已有多 pull worker |
| Redis 队列 / claim 扫表 | 按数据库与下游能力配 2~8 | 每个实例独立 `Pop`/`Claim`，天然可并行 |

## 轮询：Ticker

扫表、汇总、巡检这类"每隔一段时间做一次"的循环，不必每次手写 ticker + select 和错误分支：

```go
return worker.Ticker(ctx, 5000, func(ctx context.Context) error { // 5000 毫秒 = 5s
	queryCtx, cancel := context.WithTimeout(worker.DrainContext(ctx), 3*time.Second)
	defer cancel()
	return scanOnce(queryCtx)
})
```

三条语义：

- **失败不中断**：回调返回 error 按 warn 记录（带 `worker` 身份），下一个 tick 照常执行。
- **panic 不外溢**：单次 panic 恢复后按 error 记录（含 stack），循环继续。
- **取消即退出**：ctx 取消返回 `nil`，Runner 记为一次正常停机，不会触发重启策略。

`intervalMS` 是毫秒数（`uint32`），不是 `time.Duration`：轮询节奏通常按"每 5000 毫秒"配，
裸数字不会被人读成秒，也不用在调用处写一长串乘法。0 与空回调都会在第一个 tick 之前返回错误——
把配置错误挡在启动阶段，而不是让循环空转。回调内部要自己决定用不用 `DrainContext`：已经在链路上
的查询应该用它，停机才不会被切断。

## 消费者适配器

`support/worker/consumer/` 下四个子包把现成的消费者接到 worker 生命周期上：

| 子包 | 包装 | 构造 |
| --- | --- | --- |
| `kafkaconsumer` | `*kafka.Consumer` | `New(name, consumer, Config{Handler, Options})` |
| `amqpconsumer` | `*amqp.Queue` | 同上 |
| `rocketmqconsumer` | `*rocketmq.Consumer` | `New(...)` 一次性；`NewFactory(name, factory, cfg)` 每次 Run 新建 |
| `queuesconsumer` | `queues.Queue`（或实现 `Consume` 的等价类型） | 同上 |

RocketMQ 的 Consumer 只能 `Consume` 一次再 `Close`，因此 `New` 接现成消费者时只能当
一次性 Worker 用：Runner 在失败后重启它会从一个已经消费过的客户端继续拉取。放在
Runner 后面必须用 `NewFactory` —— 它在每次 `Run` 开始时调用工厂建新消费者（失败重启
因此是真的重连），退出时无条件 `Close`。高层 `support/consumer.NewRocketMQ()` 本来
就在 `Run` 内建消费者，没有这个问题。

```go
consumer, err := kafkaconsumer.New("order-consumer", client, kafkaconsumer.Config{
	Handler: func(ctx context.Context, record *kgo.Record) error {
		return svc.Handle(ctx, record.Value)
	},
	Options: kafka.ConsumeOptions{ /* 组、topic、提交策略 */ },
})
if err != nil {
	return err
}

group, err := worker.NewGroup("order", consumer)
if err != nil {
	return err
}
```

每个适配器只做三件事，恰好是各项目自己写时最容易错的三件：

1. **把投递 context 换成 drain context** —— 三种 MQ 都把 `Consume` 的 ctx 派发给 handler，
   SIGTERM 一到就取消，在途处理半路失败 → 未 ack → 重投 → 重复处理；
2. **只在停机时 `Close`** —— 出错时关掉消费者，重启策略就没法复用它，一次网络抖动会把
   worker 变成永久死的；
3. **错误原样返回** —— 交给 `Policy` 决定重启还是退出。

各客户端的选项按原样透传，不做统一抽象：AMQP 的 `AutoAck`（关闭手动确认）与 Kafka 的
`DiscardOnError`（提交失败位点并继续）是不同契约，硬套一张结构体只会把差异藏起来。

适配器只管协议。往上一层是 [`support/consumer`](../consumer/README.md)：一个订阅一个文件，
信封、数据库幂等台账、永久/可重试分类都在里面，`consumer.NewKafka(conns, cfg)` 返回的值直接
满足 `Worker`，所以业务分组通常跳过本节的适配器、直接用那四个构造器。两者可以分开用——
只要 worker 生命周期就只用本包的适配器；要完整的消费语义，就用 `support/consumer`。

## 选型：先看工作形态

| 工作的形态 | 用什么 | 为什么安全 |
| --- | --- | --- |
| 消息 | `consumer/*` | 消费组把分区/队列分给成员 |
| Redis 队列 | `support/queues` + `queuesconsumer` | 出队即删除，Lua 原子完成 |
| 表里 `status=0` 的待办行 | `support/claim` | `FOR UPDATE SKIP LOCKED` 行级抢占 + owner/attempts CAS |
| 定时轮询（扫状态、汇总、巡检） | `worker.Ticker` | 单次失败只记日志，循环不退出 |
| 绝不能执行两次（对账、日切） | `support/lease` | 选主 + fencing token，旧持有者写入被拒 |
| 跟随本 HTTP 实例的循环 | `support.Starter` | 本来就该每个副本各跑一份 |

用 worker 包住 `lease.Run` 是最常见的全局型写法：

```go
w := worker.Func("settle", 1, func(ctx context.Context) error {
	return lease.Run(ctx, locker, "settle-reconcile", lease.RunConfig{
		TTL:      15 * time.Second,
		Interval: 5 * time.Minute,
	}, func(ctx context.Context, token int64) error {
		return settle(ctx, token)
	})
})
```

## 与宿主的集成

`gofy` 根包的选项：

| 选项 | 作用 |
| --- | --- |
| `WithWorkerGroups(groups...)` | 注册分组；非法分组让 `New` 失败，重名分组报错 |
| `WithWorkerPolicy(policy)` | 覆盖默认重启策略 |
| `WithWorkerConcurrency(map[string]int)` | 按 `group/worker` 覆盖 `Func` 声明的实例数；小于 1、或键对不上已注册的 worker 都让 `New` 失败 |
| `WithWorkerDrainTimeout(d)` | 覆盖 20s 的 drain 预算，必须为正 |
| `WithWorkerHealth(addr)` | 开启 worker 进程的 `/healthz`：启动中 503、全部实例上报就绪后 200、drain 一开始立刻 503 |

CLI：

| 命令 | 说明 |
| --- | --- |
| `worker --list` | 打印 GROUP/WORKER/CONCURRENCY，不启动任何东西 |
| `worker --group=<g>` | 启动分组；缺 `--group` 返回 `ErrUsage`，未知分组返回 `ErrWorkerGroupNotFound` |
| `worker --name=<w>` | 可重复，缩小到组内指定 worker |
| `worker --concurrency=<n>` | 覆盖组内所有 worker 的实例数 |
| `stop --worker=<g>` | 停止该分组；组名非法在解析 pid 之前就报错（不带该旗标则停 HTTP） |

## 错误

| 错误 | 何时出现 | 怎么处理 |
| --- | --- | --- |
| `ErrNameRequired` / `ErrInvalidName` | 分组名或 worker 名为空/不合规 | 修名字 |
| `ErrNoWorkers` / `ErrNilWorker` / `ErrDuplicateWorker` | 分组构造非法 | 修注册代码 |
| `ErrUnknownWorker` | `Subset` 里的名字不存在 | 检查 `--name` |
| `ErrNilGroup` / `ErrInvalidConcurrency` | `NewRunner` 参数非法 | 修配置 |
| `ErrWorkerExited` | `Run` 在取消前返回 `nil` | 循环必须阻塞到 `ctx.Done()` |
| `ErrDrainTimeout` | 非停机退出路径上未能在预算内排空 | 少见，通常与 `ErrWorkerExited` 同源 |
| `ErrNilRun` | `Func` 的 run 为 nil | 补实现 |

`ErrDrainTimeout` 在**停机**路径上不会出现：预算耗尽只写 warn，进程照常以 0 退出。

## API 速查

| 符号 | 说明 |
| --- | --- |
| `Worker` / `Func(name, n, fn)` | 常驻任务的接口与函数适配，`n` 是默认实例数 |
| `Concurrent`（可选接口） | worker 自带默认实例数；`Func` 已实现，自定义 Worker 可实现 |
| `NewGroup(name, workers...)` | 校验并构造分组 |
| `(*Group).Subset(names)` | 按名字裁剪，返回新分组 |
| `ValidName(name)` | 名字是否符合分组/worker 规则 |
| `Identity` / `IdentityFrom(ctx)` | 实例身份 |
| `DrainContext(ctx)` | 取 drain context |
| `Ticker(ctx, intervalMS, fn)` | 常驻定时循环（毫秒）：失败/panic 记日志后继续，ctx 取消返回 nil |
| `WithRuntime(ctx, rt)` / `RuntimeFrom(ctx)` | 注入/读取宿主 Runtime |
| `Policy` / `DefaultPolicy()` / `FailureMode` | 重启策略 |
| `NewRunner(group, RunConfig)` / `(*Runner).Run(ctx)` | 自行托管分组时使用 |

## 测试

```bash
go test -race ./support/worker/...
```

本包测试不依赖外部服务：MQ 适配器用假消费者，`Runner` 用可计数的 `Worker`，覆盖
校验、`Subset`、并发实例身份、重启到成功、重启触顶、`ExitOnError`、提前返回、drain
有效性。`support/claim` 的集成测试需要真实 MySQL 8，未设置 `GOFY_MYSQL_DSN` 时跳过。

## 相关文档

- [`docs/worker-guide.md`](../../docs/worker-guide.md)：命令、四种循环写法、k8s 清单、排障
- [`docs/worker-daemon-design.md`](../../docs/worker-daemon-design.md)：决策与取舍
- `support/claim`：扫表抢占（`go doc github.com/bingcool/gofy/support/claim`）
- `support/lease`：全局单例与 fencing token（`go doc github.com/bingcool/gofy/support/lease`）
