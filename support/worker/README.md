# support/worker

把常驻任务（MQ 消费、扫表、对账）从 HTTP 进程里拆出来的最小运行时：一个分组内的
worker 在同一进程里同生共死，进程由 `app worker --group=<g>` 启动，HTTP 服务不会
拉起任何一个。

本包只关心三件事：**谁在跑**（`Group` / `Identity`）、**失败了怎么办**（`Policy`）、
**怎么停**（两级 context + drain 预算）。业务循环本身、以及"多副本如何不重复处理"，
分别属于业务代码与 `support/claim`、`support/lease`、消费者适配器。

> 部署方式、k8s 清单、排障表见 [`docs/worker-guide.md`](../../docs/worker-guide.md)；
> 设计取舍见 [`docs/worker-daemon-design.md`](../../docs/worker-daemon-design.md)。

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
	worker.Func("order-scan", func(ctx context.Context) error {
		return scanLoop(ctx) // 与 HTTP handler 调用同一个 service 方法
	}),
	worker.Func("order-consumer", func(ctx context.Context) error {
		return consumeLoop(ctx)
	}),
)
if err != nil {
	return err
}

app, err := gofy.New(
	gofy.WithName("demo-service"),
	gofy.WithWorkerGroups(group),
	gofy.WithWorkerConcurrency(map[string]int{"order/order-scan": 2}),
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
| 日志目录 | 示例落在 `storage/log/daemon/<date>/{info,error}/<worker>.log` | 应用自行分支，用 `systemenv.IsWorkerService()` / `WorkerNames()` 判断与命名 |
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

只要一个函数时用 `worker.Func(name, fn)`，不必为单个字段定义结构体。

### Group

`NewGroup(name, workers...)` 校验并构造：分组名合法、至少一个 worker、worker 非 nil、
名字不重复、名字合法——非法集合在构造阶段就失败，不会等到进程跑起来才发现。

- `Name()` / `Workers()` 返回副本，调用方改名切片不会影响分组；
- `Subset(names)` 按名字裁剪，`worker --name` 就是这样实现的；名字不存在返回
  `ErrUnknownWorker`，空切片返回原分组；
- `ValidName(name)` 导出给 CLI 校验（`stop --worker` 会把它拼进文件名）。

### Identity

```go
type Identity struct {
	Group string // 分组名
	Name  string // worker 名
	Index int    // 0 起的实例序号
	Total int    // 该 worker 的实例总数
	Host  string // hostname
}

func (i Identity) String() string // "order/order-scan#1"
```

Runner 把身份注入 context，业务用 `worker.IdentityFrom(ctx)` 取。它同时是 claim 任务行
`owner` 的天然取值：`owner = id.String()` 能把一行追溯回具体 Pod 的具体实例。

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
├─ 写 pid 文件（同组已有进程 → ErrAlreadyRunning）
├─ 可选：启动 /healthz
└─ Runner.Run
   ├─ 每个 worker × concurrency 个 goroutine
   └─ 阻塞，直到停机信号、致命错误或全部实例退出
```

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

`drainCtx` 用 `context.WithoutCancel(parent)` 加超时构造，所以它不随停机取消；只有预算
用完才失效。顺序固定为"先停拉取、再等 drain、最后关连接"，反过来会中断仍有预算的在途任务。

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

并发该开多少按协议不同：

| 类型 | 建议 | 原因 |
| --- | --- | --- |
| Kafka | 保持 1，靠分区与副本扩容 | 单个 `Consumer` 只对应一个拉取 goroutine |
| AMQP | 1 个实例 + `prefetch 32~128` | 单 channel 上的预取比多实例更有效 |
| RocketMQ | 1 个实例 | SDK 内部已有多 pull worker |
| Redis 队列 / claim 扫表 | 按数据库与下游能力配 2~8 | 每个实例独立 `Pop`/`Claim`，天然可并行 |

## 消费者适配器

`support/worker/consumer/` 下四个子包把现成的消费者接到 worker 生命周期上：

| 子包 | 包装 | 构造 |
| --- | --- | --- |
| `kafkaconsumer` | `*kafka.Consumer` | `New(name, consumer, Config{Handler, Options})` |
| `amqpconsumer` | `*amqp.Queue` | 同上 |
| `rocketmqconsumer` | `*rocketmq.Consumer` | 同上 |
| `queuesconsumer` | `queues.Queue`（或实现 `Consume` 的等价类型） | 同上 |

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

## 选型：先看工作形态

| 工作的形态 | 用什么 | 为什么安全 |
| --- | --- | --- |
| 消息 | `consumer/*` | 消费组把分区/队列分给成员 |
| Redis 队列 | `support/queues` + `queuesconsumer` | 出队即删除，Lua 原子完成 |
| 表里 `status=0` 的待办行 | `support/claim` | `FOR UPDATE SKIP LOCKED` 行级抢占 + owner CAS |
| 绝不能执行两次（对账、日切） | `support/lease` | 选主 + fencing token，旧持有者写入被拒 |
| 跟随本 HTTP 实例的循环 | `support.Starter` | 本来就该每个副本各跑一份 |

用 worker 包住 `lease.Run` 是最常见的全局型写法：

```go
w := worker.Func("settle", func(ctx context.Context) error {
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
| `WithWorkerConcurrency(map[string]int)` | 键为 `group/worker`，值为实例数，小于 1 报错 |
| `WithWorkerDrainTimeout(d)` | 覆盖 20s 的 drain 预算，必须为正 |
| `WithWorkerHealth(addr)` | 开启 worker 进程的 `/healthz`（就绪 200、drain 503） |

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
| `Worker` / `Func(name, fn)` | 常驻任务的接口与函数适配 |
| `NewGroup(name, workers...)` | 校验并构造分组 |
| `(*Group).Subset(names)` | 按名字裁剪，返回新分组 |
| `ValidName(name)` | 名字是否符合分组/worker 规则 |
| `Identity` / `IdentityFrom(ctx)` | 实例身份 |
| `DrainContext(ctx)` | 取 drain context |
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
