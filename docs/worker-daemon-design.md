# gofy worker 常驻服务设计（v1）

> 状态：P0/P1 已实施，P2 见 §11.6。使用方式见 [`worker-guide.md`](./worker-guide.md)。
> 关联：`docs/interface-api-split-design.md`（分级与验收写法）、`AGENTS.md`（注释约定）。
> 范围：框架层新增能力 + 一个示例改造；不涉及业务代码迁移。

## 目录

- [1. 背景与问题](#1-背景与问题)
- [2. 核心决策](#2-核心决策)
- [3. 进程模型](#3-进程模型)
- [4. support/worker：生命周期抽象](#4-supportworker生命周期抽象)
- [5. 消费者托管层 Run(ctx)](#5-消费者托管层-runctx)
- [6. support/lease：全局单例](#6-supportlease全局单例)
- [7. support/claim：扫表任务抢占](#7-supportclaim扫表任务抢占)
- [8. 配置](#8-配置)
- [9. 可观测性](#9-可观测性)
- [10. 部署](#10-部署)
- [11. 优先级任务清单](#11-优先级任务清单)
- [12. 附录：与 swoolefy daemon 的对照](#12-附录与-swoolefy-daemon-的对照)

---

## 1. 背景与问题

### 1.1 现状（代码事实）

| 能力 | 现状 | 位置 |
| --- | --- | --- |
| HTTP 生命周期 | `App.Run` → 建 logger/DB/telemetry → 模块 `Register` → 监听 → `Start` → 信号 → `Shutdown` | `start.go` |
| 后台循环钩子 | 只有 `support.Starter` / `support.Stopper`，**跟随 HTTP 进程生死** | `support/module.go`、`start.go:545/795` |
| 现有后台进程 | `nacos_watch` 就是挂在 `Starter` 下的 goroutine | `examples/demo-service/nacos_watch.go:39` |
| 一次性任务 | `script` 子命令，独立进程、跑完退出 | `script/script.go` |
| 自定义命令 | `command` 子命令，独立进程、跑完退出 | `command/command.go` |
| 脱离终端 | `start --daemon=1` 只是 re-exec 到后台，不是常驻消费者概念 | `daemon.go` |
| MQ 消费 | `Consume(ctx, handler, opts)` 阻塞到 ctx 取消，取消返回 nil | `support/kafka/consumer.go:282`、`support/amqp/queue.go:224`、`support/rocketmq/consumer.go:343` |
| Redis 队列 | 只有 `Pop(ctx, timeout)`，没有循环 | `support/queues/queue.go:175` |
| 数据库 | gorm，单一 MySQL 方言 | `support/db/db.go:116` |
| 分布式协调 | 无锁、无租约、无选主 | — |

结论：**能跑循环，但没有"进程级托管"**。谁要用消费者，只能在 `Starter` 里 `go func()`，于是消费者的生死被绑在 HTTP 进程上。

### 1.2 三类常驻任务，差异在"能否并行"

| 类型 | 例子 | 多副本是否安全 | 原因 |
| --- | --- | --- | --- |
| 分区型 | Kafka / AMQP / RocketMQ 消费 | 安全 | 消费组内分摊，位移由组协调 |
| 抢占型 | 扫表处理 `status=0` | 不安全**除非**按行抢占 | 朴素 `SELECT` 会让每个 POD 拿到同一批 |
| 全局型 | 对账、结算、日切清理 | 不安全 | 语义上全系统只能跑一次 |

关键判断：**抢占型不是"必须单例"，而是"必须原子认领"**。把它当单例问题解（选主），会引入单点、滚动更新空窗、脑裂三个新问题；按行认领后，多副本反而是优点。

### 1.3 问题定义

1. 常驻任务缺少独立进程形态，被迫寄生 HTTP，POD 数 = 消费者数。
2. 抢占型任务在多 POD 下重复处理，缺少统一的 claim/lease 原语。
3. 每种 MQ 的启停、drain、错误重连，每个项目各写一遍。
4. 常驻任务没有独立的日志目录、pid、退出预算，运维无法区分"HTTP 挂了"和"消费者挂了"。

### 1.4 目标与非目标

**目标**

- 新增 `worker` 子命令：独立进程，按分组启动，与 HTTP 完全解耦。
- 分组内可配置每个 worker 的并发实例数。
- 统一优雅退出：停止拉取 → 在途完成 → 释放资源，且受时间预算约束。
- 提供 claim（MySQL 8）与 lease（Redis）两个原语，覆盖抢占型与全局型。
- 三种 MQ + Redis 队列有一层统一的 `Run(ctx)` 托管。

**非目标**

- 不模拟 swoolefy 的多进程模型：Go 用 goroutine，不 fork。
- 不做进程内热重载：worker 的"重载"就是滚动更新 + SIGTERM drain。
- 不做任务调度器（cron 表达式）：周期节奏由 worker 内部 ticker 控制，或交给 k8s CronJob（D11）。
- 不把 k8s Lease 实现放进主模块（依赖太重，见 §6.4）。

---

## 2. 核心决策

### 2.1 决策表

| ID | 决策 | 理由 |
| --- | --- | --- |
| D1 | worker 是独立进程：`app worker --group=<g>` | 生命周期、资源、发布节奏与 HTTP 解耦 |
| D2 | `support.Starter` 语义不变，仍是 HTTP 进程内的后台循环钩子 | `nacos_watch`、健康上报等确实该跟随 HTTP；改动它会破坏现有语义 |
| D3 | 分组（group）是编排单位，不是进程池 | 一个 group = 一个 Deployment；组内同生共死，组间故障隔离 |
| D4 | 并发用 goroutine 实例，不用进程 | Go 生态的调度单位是 goroutine；进程级隔离交给 Pod |
| D5 | 默认不选主：分区型靠消费组，抢占型靠 claim | 选主解决"谁是唯一"，不解决"旧 leader 迟到写入"，最终仍要幂等 |
| D6 | `lease` 只用于全局型动作，且必须带 fencing token | 把选主风险关进最小的笼子 |
| D7 | claim 只实现 MySQL 8（`SKIP LOCKED`） | 生产确定是 MySQL 8；方言抽象留接口，不提前实现 |
| D8 | 优雅退出预算：worker 的 drain 默认 20s，进程预算由它推导（drain + 5s） | 小于 k8s 默认 `terminationGracePeriodSeconds: 30`，留余量给强杀；HTTP 的 `shutdown_timeout`（默认 10s）保持原义，两者不共用 |
| D9 | worker 不复用 `support.Module` | Module 是 HTTP 装配单位（`Register(*Router)`），复用会把路由拖进无 HTTP 的进程 |
| D10 | Redis 队列由框架提供 `Consume`，不留给项目写 `for { Pop }` | 空轮询退避、失败重投、panic 兜底三件事各写一遍必然走样；一处实现、一处修 bug |
| D11 | 不做框架内 cron 调度器 | 周期节奏由 worker 内部 ticker 控制，或交给 k8s CronJob；框架调度器与 CronJob 双跑会产生"同一动作两份" |

### 2.2 四种"只跑一份"的方案对比

| 方案 | 重复处理 | 单点 | 滚动更新 | 扩容 | 结论 |
| --- | --- | --- | --- | --- | --- |
| Deployment `replicas: 1` | 不会 | **有** | 有空窗 | 不能 | 只适合可接受空窗的低价值任务 |
| 选主（Redis / k8s Lease） | 极端情况会 | 有切换期 | 有切换期 | 不能分担 | 仅用于全局型 |
| 分片（`id % N`） | 不会 | 无 | 需重平衡 | 需改 N | 超大表才用 |
| **按行 claim** | **不会**（原子认领） | 无 | 无感 | 直接加副本 | **抢占型默认** |

### 2.3 与既有文档的关系

本文是 `docs/gofy-architecture-optimization-plan.md` 的补充，不改动 HTTP 侧设计；`support.Starter` 的语义与 `interface-api-split-design.md` 的模块边界保持不变。

---

## 3. 进程模型

### 3.1 命令面

```bash
# 启动一个分组下的全部 worker
demo-service worker --group=order

# 只启动组内指定 worker（排障用）
demo-service worker --group=order --name=order-scan

# 覆盖并发（临时压测用，不写进配置）
demo-service worker --group=order --concurrency=4

# 列出配置里声明的分组与 worker
demo-service worker --list

# 全部已注册分组塞进一个进程（本地调试/冒烟，不是部署形态）
demo-service worker --group='*'
```

| flag | 默认 | 说明 |
| --- | --- | --- |
| `--group` | 空 | 必填（除 `--list`）；对应配置里的同名分组块（如 `worker.order`）；`*` 表示全部已注册分组 |
| `--name` | 空 | 可重复；只启动列出的 worker，用于排障 |
| `--concurrency` | 0 | 覆盖全部 worker 的实例数，0 表示按配置 |
| `--list` | false | 打印分组/worker/并发后退出，不连 MQ、不连 DB |

保留字与校验：`worker` 加入 `commandline` 的保留命令名；worker 名沿用模块名规则 `^[a-z][a-z0-9-]{0,62}$`，重名在启动时报错。

### 3.2 分组语义

- **一个 group 就是一个进程**，组内 worker 同生共死。
- 分组依据：故障域一致 + 资源画像接近 + 发布节奏一致。典型切法：`order`、`settle`、`notify`、`maintenance`。
- 组内某个 worker 触发 fatal，整个进程退出（由 k8s 重启容器），避免"进程活着但只剩一半 worker"的静默降级。
- 不同组之间无共享状态，可独立扩缩容。
- 例外是 `--group='*'`：它把全部分组放进一个进程，用于本地调试与冒烟测试。pid 后缀变成 `all`，`stop --worker='*'` 对应同一个文件；组间隔离随之消失，任一组失败会取消其余组。部署仍然是一组一个进程——通配符不是把"多组同进程"合法化，只是省掉本地连起三个进程的动作。

### 3.3 与现有命令的对比

| 维度 | `start` | `script <name>` | `worker --group=<g>` |
| --- | --- | --- | --- |
| 进程 | 常驻 | 一次性 | 常驻 |
| HTTP 监听 | 有 | 无 | 无 |
| 模块 `Register` | 有 | 无 | 无 |
| DB / logger / telemetry | 有 | 有（可关） | 有 |
| pid 文件 | 配了 `pid_file` 才写（demo 为 `app/demo-service.pid`） | 不写 | 复用 `pid_file` 加 `-worker-<group>` 后缀，未配置则不写 |
| 日志目录 | `storage/log/http` | `storage/log/script/{date}` | `storage/log/worker/{date}/info/<worker>.log` |
| 退出方式 | SIGTERM + `stop` | 自然结束 | SIGTERM + `stop --worker=<group>` |

### 3.4 进程内结构

一个二进制注册全部分组，一个进程只运行 `--group` 选中的那一个；下面画的是 group1（group2 结构相同，只是成员与节奏不同）：

```
demo-service worker --group=order
└── Process
    ├── Runtime（config / logger / db / telemetry）   ← 与 HTTP 同源，不同实例
    ├── Group "order"（同生共死）
    │   ├── order-scan      ×2   claim 扫表，每实例独立 owner
    │   ├── order-consumer  ×1   kafka 消费组成员
    │   └── order-notify    ×1   redis 延迟队列
    └── Shutdown：停止拉取 → drain 在途 → 释放 lease → 关客户端 → flush
```

### 3.5 启动时序

```
main → app.Execute
  └─ commandline: worker cmd
      ├─ 1. beginStart(false)（复用单次启动约束）
      ├─ 2. resolveWorkDir / 建 logger（worker 目录）
      ├─ 3. 建 DB（独立连接池）、telemetry、core.Runtime
      ├─ 4. 解析 group/name/concurrency（含默认值与校验）
      ├─ 5. 写 pid 文件（`pid_file` 加 `-worker-<group>` 后缀；未配置则跳过）；检测同组已有进程 → ErrAlreadyRunning
      ├─ 6. 逐个 worker 启动：N 个实例 = N 个 goroutine
      └─ 7. 阻塞：ctx.Done / fatal / 全部实例意外退出
```

启动失败即退出，不做半启动：任一 worker 的 `Run` 在启动阶段返回错误（例如 MQ 认证失败），整组退出并返回非零。

### 3.6 关闭时序（drain）

```
t=0    收到 SIGTERM → 取消 consumeCtx（不再拉取/不再 claim 新批次）
t=0~20 在途 handler 继续执行，使用 drainCtx（见 §5.5），最多 drain_timeout
t=20   释放 lease（显式 Release，保留 fencing token）
t=20+  关闭客户端：kafka commit + Close、amqp cancel + Close、rocketmq Stop + Close
t=24   telemetry flush
t=25   删除 pid 文件；若超预算由 k8s SIGKILL
```

预算关系：`drain_timeout < shutdown_timeout < terminationGracePeriodSeconds`，默认 `20s < 25s < 30s`。
第二次信号（连按两次 Ctrl+C）立即退出，不等 drain。

### 3.7 进程标识与模式判定

`core/systemenv` 增加三个无状态函数，与现有 `IsScriptService` 同一实现风格（解析第一个非 flag 参数）：

```go
// IsWorkerService reports whether the process runs the worker subcommand.
// IsWorkerService 报告当前进程是否通过 worker 子命令运行常驻任务。
func IsWorkerService() bool

// WorkerGroup returns the --group value, sanitised for use in paths.
// WorkerGroup 返回 --group 的值，并做路径安全过滤，可直接用于日志目录。
func WorkerGroup() string
```

同时确认一个边界：`IsHTTPService()` 对 `worker` 必须返回 false（现有实现按 `start/stop/restart` 白名单判断，天然成立），避免日志、pid、Nacos 注册等逻辑误判。

### 3.8 日志分目录

`applyLogPaths`（`examples/demo-service/app/config/log.go`）原有 script / http 两支，已补第三支（**已实现**）：

```go
// worker 分支：一个 worker 一组文件，与 script 的 info/ + error/ 布局一致
// storage/log/worker/{date}/info/<worker>.log            // hourly 时变成 <worker>-h<H>.log
// storage/log/worker/{date}/error/<worker>.log
// storage/log/worker/{date}/error/<worker>-system_error.log
```

`{date}` 由 logger 在滚动时替换。文件名取 `systemenv.WorkerNames()`（`--name`）：
进程只跑一个 worker 时就是它；整组启动时一个进程里有多个 worker，没有唯一的名字，文件名为 `worker`（`worker.log`），
实例序号（`order/order-scan#1`）仍然只进日志字段，避免按实例切文件把滚动打碎。
最初实现把整组日志写成 `<group>/info.log`；按 worker 命名后文件本身就回答了"这是谁的日志"，
不必打开去看字段。

---

## 4. support/worker：生命周期抽象

### 4.1 接口

```go
// Package worker hosts long-running tasks outside the HTTP process.
// 包 worker 托管不随 HTTP 进程存活的常驻任务。
package worker

// Worker is one long-running task. Run must block until ctx is cancelled and
// then return nil; a non-nil error is a failure the host decides how to handle.
// Worker 是一个常驻任务。Run 阻塞到 ctx 被取消后返回 nil；返回非 nil 表示
// 失败，由宿主决定重启还是退出整组。
type Worker interface {
	// Name is unique inside its group and matches ^[a-z][a-z0-9-]{0,62}$.
	// Name 在组内唯一，并匹配 ^[a-z][a-z0-9-]{0,62}$。
	Name() string
	Run(ctx context.Context) error
}

// Group is a set of workers that live and die together in one process.
// Group 是一组同生共死的 worker，对应一个进程、一个 Deployment。
type Group struct {
	name    string
	workers []Worker
}

func NewGroup(name string, workers ...Worker) (*Group, error)
func (g *Group) Name() string
func (g *Group) Workers() []Worker
```

无配置的 worker 用适配器构造，避免每个项目写 struct：

```go
// Func adapts a function to a Worker.
// Func 把函数适配成 Worker，名字与并发由配置决定。
func Func(name string, run func(ctx context.Context) error) Worker
```

### 4.2 注册

沿用 `WithScripts` / `WithCommands` 的形状：

```go
app, err := gofy.New(
	gofy.WithName("demo-service"),
	gofy.WithDatabase(cfg.DB[config.DefaultDatabase]),
	gofy.WithWorkerGroups(
		orderGroup,   // worker --group=order
		settleGroup,  // worker --group=settle
	),
)
```

`gofy.New` 阶段只做校验：组名格式、组内重名、worker 名为空、与保留字冲突。构造 worker 所需依赖（service / repository / MQ client）由 `main` 在装配阶段注入——**worker 与 `support.Module` 正交**，共享的是 service 层，不是 Module 层。

### 4.3 并发模型

`concurrency` 表示**实例数**，框架为每个实例起一个 goroutine，并向其注入身份：

```go
// Identity describes one worker instance and is available from its context.
// Identity 描述一个 worker 实例，可从 context 取得，用于日志与 owner 命名。
type Identity struct {
	Group string
	Name  string
	Index int // 0-based；并发为 1 时恒为 0
	Total int
	Host  string // os.Hostname，便于定位 POD
}

func IdentityFrom(ctx context.Context) (Identity, bool)
```

实例身份是 claim 的 `owner` 来源（`<host>-<name>-<index>`），也是日志字段，避免"N 个实例日志分不清是谁"。

一个实例会被 `concurrency` 个 goroutine 并发调用 `Run`，因此实现必须无共享可变状态：状态放在 `Run` 内部，或用 `worker.Func` 的闭包。`worker.Func` 天然满足这一约束。

各类型的正确并发姿势（与现有实现对齐）：

| 类型 | 并发单位 | 建议 | 依据 |
| --- | --- | --- | --- |
| Kafka | 消费组成员 / 分区 | 实例数 ≤ 分区数；跨进程扩容优先于单进程多实例 | `Consumer` 注释明确"每个 Consumer 只对应一个 Consume goroutine" |
| AMQP | channel + prefetch | 1 实例 + `prefetch 32~128` 通常比多实例更有效 | 单 channel 多投递已并行 |
| RocketMQ | SDK pull worker | 1 实例 | SDK 内部已是多 pull worker |
| Redis 队列 | goroutine | N 实例（`concurrency` 2~8） | `Pop` 是单条阻塞，多协程才有吞吐 |
| claim 扫表 | goroutine | N 实例（2~4） | 每轮 `LIMIT` 抢占，多实例分摊批次 |

框架不做"共享客户端自动复用"的魔法：每个实例拿到的是同一个已注入的 client，是否可以并发使用由该 client 的语义决定（上表即约束）。

### 4.4 错误策略

```go
// Policy controls what the host does when Run returns an error.
// Policy 控制 Run 返回错误时宿主的行为。
type Policy struct {
	Restart     bool          // false = fatal，整组退出；默认 true
	Backoff     time.Duration // 首次退避，默认 1s
	MaxBackoff  time.Duration // 退避上限，默认 30s
	MaxRestarts int           // 0 = 不限；超过则整组退出
}
```

- 默认 `restart`：MQ 断连、临时网络故障不应该让整组退出。
- `fatal` 用于配置错误、订阅不存在等"重启也不会好"的情况。
- 重启计数与最后一次错误进日志字段，并由指标暴露（§9.2），否则会变成"疯狂重启但没人知道"。
- 退避用指数 + 抖动，避免同一批 POD 同时重连打爆 broker。

### 4.5 健康与就绪

| 维度 | 做法 |
| --- | --- |
| 存活 | 进程存活即存活，不引入探针（崩溃由 k8s 重启） |
| 就绪 | 分组启动后返回 200，进入 drain 返回 503；由 `gofy.WithWorkerHealth("host:port")` 开启，关闭时不监听任何端口 |
| 业务健康 | "10 分钟没消费到消息"属于业务告警，不进探针，避免 broker 抖动引发批量重启 |

### 4.6 与 Starter / Script 的边界

| | `support.Starter` | `script.Script` | `worker.Worker` |
| --- | --- | --- | --- |
| 宿主 | HTTP 进程 | 独立进程（一次性） | 独立进程（常驻） |
| 生命周期 | 跟随 HTTP 监听就绪/关闭 | 跑完即退 | 跟随 SIGTERM，带 drain 预算 |
| 典型用途 | nacos_watch、注册心跳、连接预热 | 数据修复、一次性迁移 | MQ 消费、扫表、对账 |
| 多副本 | 每个 POD 都跑 | 手动指定一次 | 分区型/抢占型安全，全局型需 lease |

**判定规则**：任务与"这个 HTTP 实例"强相关（本地缓存、注册、监听配置）→ `Starter`；可以脱离 HTTP 且需要常驻 → `worker`。

### 4.7 使用示例：与 script 相同的装配方式

worker 的注册位置和 `script` 一致——都在 `main` 里构造分组、交给 `gofy.New`；区别只是宿主的生命周期。数据库不归 `main` 管（`WithDatabase` 由框架建池），因此 worker 通过 `worker.RuntimeFrom(ctx)` 取 Runtime，取法与脚本接收 `Runtime` 参数一致：

```go
// main.go：分组在 New 之前构造，实例数与节奏跟着 worker 走
group1, err := worker.NewGroup("group1",
	worker.Func("order-scan", uint8(cfg.Worker.Group1.Scan.Concurrency), func(ctx context.Context) error {
		rt, ok := worker.RuntimeFrom(ctx) // 与脚本的 rt 参数同源
		if !ok || rt.DB == nil {
			return errors.New("order-scan: database is required")
		}
		return scanLoop(ctx, order.NewRepository(rt.DB))
	}),
)
if err != nil {
	return err
}

app, err := gofy.New(
	gofy.WithName("demo-service"),
	gofy.WithDatabase(cfg.DB[config.DefaultDatabase]),
	gofy.WithWorkerGroups(group1),
)
```

约束（与 `script` 保持一致，避免把 HTTP 侧的东西带进来）：

- worker 只依赖 service/repository，**不依赖 `support.Router`、`*gin.Context`、HTTP middleware**。
- 需要"当前时间/当前用户"这类请求级信息时，从消息体或任务行读取，不从 context 里找 HTTP 值。
- 构造失败（分组名非法、并发小于 1、并发键对不上已注册的 worker）在 `main` 里直接返回错误；连接类失败（DB、MQ）在 `Run` 里暴露，由重启策略处理——早失败指编程错误，不是网络抖动。

多个分组按同样方式各构造一个，`WithWorkerGroups(groups...)` 一次传入；一个进程仍然只运行 `--group` 选中的那个，配置按分组名分层（见 §3.3 与 `docs/worker-guide.md` §3.4）。


---

## 5. 消费者托管层 Run(ctx)

### 5.1 现状与缺口

三种 MQ 的 `Consume(ctx, handler, opts)` 已经满足两个前提：**阻塞**、**ctx 取消返回 nil**。缺的不是循环，而是编排：

1. 启停顺序：先停拉取再关连接，顺序错了会丢消息或重复投递。
2. drain：在途 handler 需要一个不被立刻取消的 context（见 §5.5）。
3. 错误策略：区分"重连"与"致命"。
4. 身份与指标：谁在消费、积压多少、重启几次。
5. Redis 队列连循环都没有。

### 5.2 适配矩阵

| 类型 | 现有入口 | 适配器（**已实现**） | 关键点 |
| --- | --- | --- | --- |
| Kafka | `Consumer.Consume` | `kafkaconsumer.New(name, consumer, cfg)` | 停机时 `Close`；出错时不关，留给重启策略复用同一消费者 |
| AMQP | `Queue.Consume` | `amqpconsumer.New(name, queue, cfg)` | `PrefetchCount` 决定在途上限；未 ack 的消息在断开后重投 |
| RocketMQ | `Consumer.Consume` | `rocketmqconsumer.New(name, consumer, cfg)` | `Consumer` 一次性：`Consume` 一次后 `Close` |
| Redis 队列 | `queues.Consume` | `queuesconsumer.New(name, queue, cfg)` | 客户端归应用所有，适配器不负责关闭 |

包路径：`support/worker/consumer/{kafkaconsumer,amqpconsumer,rocketmqconsumer,queuesconsumer}`（子包，避免 `support/worker` 反向依赖三种 MQ 客户端，保持核心包零 MQ 依赖）。

每个适配器只做三件事：把投递 context 换成 drain context、退出时按需 `Close`、把错误原样返回给重启策略。`Concurrency` 由宿主解析后决定实例数，适配器不自己起协程——否则并发语义会散落在四个包里。

### 5.3 Options 不强行统一

适配器**不定义**统一 Options，而是原样转发各客户端自己的 `ConsumeOptions`：

```go
// 各自转发，不抽象：
kafkaconsumer.Config{Handler: h, Options: kafka.ConsumeOptions{MaxRecords: 100}}
amqpconsumer.Config{Handler: h, Options: amqp.ConsumeOptions{PrefetchCount: 32}}
```

理由：amqp 的 `AutoAck` 与 kafka 的 `commit_mode` 概念不同，硬统一会误导使用者。真正共用的只有 drain 与 close 的顺序，而那部分在适配器里，不在配置里。

### 5.4 Redis 队列补 `Consume`

```go
// Consume pops and handles messages until ctx is cancelled. An empty poll backs
// off with IdleDelay instead of spinning.
// Consume 持续取出并处理消息，直到 ctx 被取消；空轮询按 IdleDelay 退避，
// 避免打满 Redis。
func (q *Queue) Consume(ctx context.Context, handler HandlerFunc, opts ConsumeOptions) error
```

语义与现有 `Pop` / `Retry` 对齐（**已实现**，见 `support/queues/consume.go`）：

| 情况 | 行为 |
| --- | --- |
| 队列为空 | 等待 `IdleDelay`（默认 200ms）再轮询，不空转 |
| handler 返回错误 | 上报 `OnError`，再 `Retry(msg, RetryDelay)` 写入重试 ZSET；到期后由 `Pop` 自动回流 |
| 达到 `MaxRetries` | 丢弃并上报 `ErrRetryExhausted`（带消息 ID 与上限），不无限重投 |
| `DiscardOnError` | 只上报不重试，用于幂等且可丢弃的消息 |
| handler panic | `recover` 后按错误处理，一条坏消息不会拖垮消费者 |
| ctx 取消 | 返回 nil，与三种 MQ 消费者一致，可直接作为 `worker.Worker` |

**为什么用非阻塞 `Pop` 轮询而不是 `BRPOP`**：`Pop` 的阻塞形式精度是秒（内部还会向上取整），既拖慢停机响应，也让"到期的重试消息"最多延迟 1s 才回流。轮询 + 显式 `IdleDelay` 的代价是空闲时 5 次/秒的 Redis 往返，可以接受。

**已知边界（必须写进使用文档）**：`Pop` 取出即删除，handler 执行期间进程崩溃会丢这一条，即**对在途消息是"至多一次"**。不能丢的场景用 MQ 或 `support/claim`（后者有 lease 回收）。要在这层做到"至少一次"，需要 `BRPOPLPUSH` + 处理中列表 + 超时回收，属于独立改造，不在本期范围。

### 5.5 drain：两级 context（本设计最容易踩的坑）

现有 `Consume` 把同一个 ctx 传给 handler。如果停机时直接取消整棵树，正在处理的 handler 会立刻拿到 `context.Canceled`：数据库写入失败、消息未 ack → 重投 → 重复处理。正确做法是**两级**：

```go
// consumeCtx 控制"是否继续拉取"；取消后 Consume 返回 nil。
consumeCtx, stopConsume := context.WithCancel(root)

// drainCtx 控制"在途任务还能干多久"：不随 consumeCtx 取消，
// 但受 drain_timeout 约束，保证不会无限拖住停机。
drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(root), drainTimeout)

handler := func(_ context.Context, rec *kgo.Record) error {
	// 忽略每次投递自带的 ctx（它是 consumeCtx 的派生），改用 drainCtx。
	return userHandler(drainCtx, rec)
}
```

要点：

- `context.WithoutCancel`（Go 1.21+）保留 value（trace/identity），去掉取消传播，正是 drain 需要的语义。
- `drainCtx` 必须有 deadline，否则一个卡死的 handler 会拖到 SIGKILL，反而丢掉"未 ack → 重投"的机会。
- 停机后仍未完成的 handler：**放弃并让它重投**，而不是硬等。at-least-once 的代价由业务幂等承担。
- 适配器必须在 `Run` 里保证顺序：先 `stopConsume()`，再等 `drainDone`，最后 `Close()`。顺序反了会把在途消息的 ack 丢在已关闭的连接上。

### 5.6 代码草案（Kafka 适配器）

```go
// Run consumes until ctx is cancelled, then drains in-flight handlers.
// Run 消费到 ctx 取消，然后等待在途 handler 结束。
func (w *kafkaWorker) Run(ctx context.Context) error {
	consumeCtx, stopConsume := context.WithCancel(ctx)
	defer stopConsume()

	drainCtx, cancelDrain := context.WithTimeout(
		context.WithoutCancel(ctx), w.opts.DrainTimeout)
	defer cancelDrain()

	handler := func(_ context.Context, rec *kgo.Record) error {
		return w.handler(drainCtx, rec)
	}
	err := w.consumer.Consume(consumeCtx, handler, w.consumeOpts)
	if closeErr := w.consumer.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}
```

---

## 6. support/lease：全局单例

### 6.1 何时真的需要

只有同时满足下列三条才用 lease：

1. 动作**全局只能一个实例执行**（对账、日切、清理），不能靠 claim 拆分；
2. 重复执行的代价 **>** 选主带来的复杂度；
3. 频率低（分钟级及以上），能容忍切换期。

否则用 claim（§7）或消费组。**不要默认加锁**：锁会把"可水平扩展"退化成"单点 + 切换期"。

### 6.2 接口与 fencing token

```go
// Locker acquires leases by key. Implementations must be safe across processes.
// Locker 按 key 获取租约，实现必须跨进程安全。
type Locker interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lease, error)
}

// Lease is a held lock. Token is monotonic and must be passed to the resource
// being protected, so a paused holder's late write can be rejected.
// Lease 是一个已持有的锁。Token 单调递增，必须传给被保护的资源，使暂停后
// 苏醒的旧持有者的迟到写入能被拒绝。
type Lease interface {
	Key() string
	Token() int64
	Renew(ctx context.Context) error
	Release(ctx context.Context) error
}
```

fencing 的用法（业务表加一列 `fence BIGINT`）：

```sql
UPDATE job SET status = 2, fence = :token
 WHERE id = :id AND fence < :token;   -- 旧持有者的 token 更小，写入被拒
```

只做"获取锁"不做 fencing，等于把正确性押在"锁一定不会失效"上，而这在 GC 暂停、网络分区、Redis 主从切换下都不成立。

### 6.3 Redis 实现

| 动作 | 命令 | 说明 |
| --- | --- | --- |
| 取 token | `INCR fence:{key}` | 单调递增，进程重启不回退 |
| 获取 | `SET lease:{key} {token} NX PX {ttl}` | 失败即未抢到 |
| 续约 | Lua：`GET == token` 则 `PEXPIRE` | 必须比对 token，防止续别人的锁 |
| 释放 | Lua：`GET == token` 则 `DEL` | 同上 |

参数建议：`ttl = 3 × 续约间隔`，续约间隔取 `ttl/3`；`ttl` 至少 10s，否则一次 GC 停顿就丢锁。

**实现**（`support/lease/redislease`）与上表的差别，都是为了少一次往返、少烧一个号：

- 取号与抢锁合并成一条 Lua：先 `EXISTS`，无人持有才 `INCR` + `SET ... PX`。拆成两步时，每次抢锁失败都会白烧一个 fence 号。
- 键名是 `lease:{key}` 与 `fence:{key}`，花括号是 Redis Cluster 的 hash tag，两个键落在同一 slot，单条脚本才能原子操作。
- 续约失败（非 `ErrLeaseLost`）不立刻判定丢锁：继续按 `ttl/3` 重试，直到手上这段租期真正到期才报 `ErrLeaseLost`。否则一次网络抖动就会让健康的持有者主动下台。
- `lease.Run` 封装了"抢锁 → 续约 → 丢锁即取消业务 ctx → 停机释放"这一整套，worker 里只写业务函数。

### 6.4 为什么 k8s Lease 实现不进主模块

`k8s.io/client-go` 会把一整套 API machinery 拉进 `go.mod`（数百个包），与 gofy 目前"核心模块依赖克制"的取向冲突。因此：

- 接口 `Locker` 留在 `support/lease`；
- Redis 实现进主模块（Redis 已是既有依赖）；
- k8s Lease 实现放 `gofy-adapter`（`adapter/lease/k8slease`），用 `client-go/tools/leaderelection` 或直接操作 `coordination.k8s.io/v1 Lease`，RBAC 只需 leases 的 get/update/create。

好处：不用 k8s 的项目不为它付编译与依赖成本；用 k8s 的项目多引一个 adapter 包。

**实现**（`gofy-adapter/adapter/lease/k8slease`）的取舍：

- 只 import `client-go/kubernetes/typed/coordination/v1`，不 import 完整 clientset：只编译 coordination 这一个 API 组，adapter 的 `go.sum` 从 80 行涨到 255 行，核心模块的 `go list -deps` 里 `k8s.io/` 仍为 0。
- token 用 `spec.leaseTransitions`：它随对象存在 apiserver 上，每次接管加一。代价是**删除 Lease 对象会让它归零**，只要被保护的资源还认旧 token，运维就不能删这个对象。
- key 必须是 DNS-1123 subdomain（`settle-reconcile` 而不是 `settle:reconcile`）。不做字符替换，否则 `a:b` 与 `a-b` 会悄悄指向同一个对象。
- 同一 `Identity` 读到自己的未过期租约时只续期、不加 token，避免同名 Pod 重启让自己在途写入用的 token 失效；`Identity` 因此必须每进程唯一（用 Pod 名）。
- Release 按 k8s 选举的惯例清空 `holderIdentity` 并把租期缩到 1 秒，下一个实例立刻能接管。

### 6.5 失败模式

| 现象 | 后果 | 对策 |
| --- | --- | --- |
| GC / 调度暂停超过 TTL | 两个持有者并存 | fencing token + 业务侧校验 |
| Redis 主从切换丢锁 | 同上 | 同上；关键路径用 DB 行锁替代 |
| 续约协程被阻塞 | 锁提前失效 | 续约独立 goroutine + 超时 |
| 忘记 Release | 只能等 TTL | 正常路径 defer Release；异常路径靠 TTL |

---

## 7. support/claim：扫表任务抢占

### 7.1 表结构（业务侧，框架给 DDL 模板）

```sql
CREATE TABLE job_task (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  job         VARCHAR(64)  NOT NULL,              -- 任务类型，一个表可放多类任务
  status      TINYINT      NOT NULL DEFAULT 0,    -- 0 待处理 1 处理中 2 完成 3 死亡
  owner       VARCHAR(96)  NOT NULL DEFAULT '',   -- host-name-index
  lease_until DATETIME(3)  NULL,                  -- 处理中租约到期时间
  attempts    INT          NOT NULL DEFAULT 0,
  next_run_at DATETIME(3)  NULL,                  -- 失败退避后的可再执行时间
  payload     JSON         NULL,
  created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                           ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_claim (job, status, next_run_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

`idx_claim` 是硬要求：`SKIP LOCKED` 在没有合适索引时会退化成全表扫描 + 大量行锁，比不加锁更慢。

### 7.2 状态机

```
        claim                 succeed
  0 ──────────► 1 ──────────────────────► 2
  ▲             │
  │  reap       │ attempts >= max
  └─────────────┴───────────────────────► 3
   （lease 超时且 attempts 未超限）
```

- 只有 `1` 能被 `succeed` 或 `reap` 推进，其它转换一律拒绝（CAS 的 `WHERE` 条件保证）。
- `2/3` 是终态；重跑需要人工重置，避免"自动复活"掩盖 bug。

### 7.3 抢占 SQL（MySQL 8）

```sql
START TRANSACTION;

SELECT id, job, payload, attempts
  FROM job_task
 WHERE job = ? AND status = 0
   AND (next_run_at IS NULL OR next_run_at <= NOW(3))
 ORDER BY next_run_at, id
 LIMIT ?
 FOR UPDATE SKIP LOCKED;

UPDATE job_task
   SET status = 1, owner = ?, attempts = attempts + 1,
       lease_until = NOW(3) + INTERVAL ? SECOND
 WHERE id IN (...);

COMMIT;
```

**为什么不用 `UPDATE ... ORDER BY ... LIMIT`**：MySQL 的 `UPDATE` 不支持 `SKIP LOCKED`，遇到已被别的事务锁定的行会阻塞等待，并发下容易演变成锁等待甚至死锁。`SELECT ... FOR UPDATE SKIP LOCKED` 才是"跳过别人正在处理的，只拿空闲的"。

**为什么一次只抢一批**：批次过大导致单实例长时间持有大量行锁；批次过小则空轮询浪费。建议 `batch = 100`，`lease = 3 × p99 处理耗时`。

### 7.4 API

```go
// Package claim implements row-level task claiming on MySQL 8.
// 包 claim 在 MySQL 8 上实现按行认领任务。
package claim

// Config is the queue definition: table, job, batch, lease, retry policy and
// optional hooks.
// Config 是队列定义：表、job、批大小、租约、重试策略与可选钩子。
type Config struct {
	Table       string        // 可带库名前缀："app.job_task"
	Job         string
	Batch       int           // 默认 100
	Lease       time.Duration // 默认 60s，至少为处理耗时 p99 的 3 倍
	MaxAttempts int           // 默认 5
	Backoff     time.Duration // 默认 10s
	MaxBackoff  time.Duration // 默认 10m
	Hooks       Hooks
}

// Queue binds a job name to a table and its claim parameters.
// Queue 把一个 job 名绑定到表与认领参数。
type Queue struct{ /* db, job, table, lease, batch, maxAttempts */ }

// ErrLeaseLost means the row was taken over by another owner; the handler must
// not retry its side effects.
// ErrLeaseLost 表示该行已被其它 owner 接管，业务不得重试其副作用。
var ErrLeaseLost = errors.New("claim: lease lost")

func New(db *gorm.DB, cfg Config) (*Queue, error)

// Claim locks and marks up to n rows for this owner. It returns an empty slice
// when nothing is due; callers should back off instead of spinning.
// Claim 为当前 owner 锁定并标记最多 n 行；没有到期任务时返回空切片，调用方
// 应当退避而不是空转。
func (q *Queue) Claim(ctx context.Context, owner string, n int) ([]Task, error)

// Succeed closes a task. It fails with ErrLeaseLost when another owner has
// taken the row over, which means the handler must not retry its side effects.
// Succeed 结束任务；当该行已被其它 owner 接管时返回 ErrLeaseLost，此时
// 业务不得重试其副作用。
func (q *Queue) Succeed(ctx context.Context, t Task) error

// Fail records an attempt and schedules a retry with exponential backoff, or
// moves the task to the dead state once MaxAttempts is reached.
// Fail 记录一次失败并按指数退避安排重试；达到 MaxAttempts 后转为死亡态。
func (q *Queue) Fail(ctx context.Context, t Task, cause error) error

// Reap returns expired leases to the pending state and moves exhausted rows to
// the dead state.
// Reap 把租约过期的任务放回待处理态，并把重试耗尽的转为死亡态。
func (q *Queue) Reap(ctx context.Context) (reaped int64, dead int64, err error)

// DDL returns the CREATE TABLE statement the queries expect, index included.
// DDL 返回查询所依赖的建表语句，含索引。
func DDL(table string) (string, error)
```

实现位置：`support/claim/claim.go`；真实 MySQL 8 的集成测试在 `claim_integration_test.go`，未设置环境变量时跳过：

```bash
GOFY_MYSQL_DSN='user:pass@tcp(host:3306)/db?charset=utf8mb4&parseTime=True&loc=Local' \
  go test -count=1 -run TestIntegration ./support/claim/
```

### 7.5 幂等与丢失

| 场景 | 结果 | 保障 |
| --- | --- | --- |
| 同一行被两个实例同时扫到 | 只有一个 `Claim` 成功 | `FOR UPDATE SKIP LOCKED` |
| 处理成功但 `Succeed` 前崩溃 | 行被 `reap` 回收，重新执行 | 业务幂等 |
| 处理超时但仍在跑 | 另一个实例接管，两个都在写 | fencing：`Succeed` 带 owner CAS，旧实例写入被拒 |
| `attempts` 超限 | 转 `3`，不再自动重试 | 人工介入 + 告警 |

**幂等的落点**：`Succeed` 的 CAS 只保护"任务状态"，不保护业务副作用。业务写入必须自带幂等键（唯一索引 / 状态条件更新），这是使用 claim 的前提，写进使用文档。

### 7.6 与 lease 的关系

claim 自带行级租约（`owner + lease_until`），**不需要**再套全局 lease。两层都上只会增加切换期与锁竞争。判定：

- 能按行拆 → claim；
- 拆不动（全局动作）→ lease。

### 7.7 指标

`claim` 包只暴露 `Hooks`（**已实现**），不绑定具体监控库：

```go
type Hooks struct {
	OnClaim     func(job string, claimed int)          // 含 claimed=0 的空轮询
	OnReap      func(job string, reaped, dead int64)
	OnLeaseLost func(job string)                        // CAS 失败，即重复执行风险
}
```

应用把这三个回调接到自己的指标实现上。核心包因此不依赖 prometheus/otel，与 `support/otel`「可选埋点」的既有取向一致。

---

## 8. 配置

### 8.1 config.yaml 片段

```yaml
worker:
  shutdown_timeout: 25s      # 关停总预算，必须 < k8s terminationGracePeriodSeconds
  drain_timeout: 20s         # 在途任务完成预算，必须 < shutdown_timeout
  restart:
    backoff: 1s
    max_backoff: 30s
    max_restarts: 0          # 0 = 不限
  order:                     # 分组块，名字与 `worker --group` 一致
    scan:
      concurrency: 2
      claim:
        table: job_task
        batch: 100
        lease: 60s
        max_attempts: 5
        idle_delay: 2s       # 无任务时的退避
    consumer:
      concurrency: 1
      kafka:
        topic: order.created
        group: demo-order
        commit_mode: manual
  settle:
    reconcile:
      concurrency: 1
      lease:
        key: settle-reconcile
        ttl: 15s
```

上层结构由应用定义，框架不解析这一段：`main` 读出来，把 `<g>.<w>.concurrency` 作为第二个参数传给 `worker.Func(name, n, fn)`，计数跟着 worker 进分组。块内的键不必等于 worker 名（demo 的 `worker.group1.scan` 对应 `group1/order-scan`）。需要按部署覆盖时再用 `gofy.WithWorkerConcurrency` 或 `worker --concurrency`，覆盖优先于 `Func` 声明的值。`drain_timeout` 属于框架，用 `WithWorkerDrainTimeout` 设置。

每个分组的配置类型定义在它自己的子包里（`group1/abc_register.go`、`group2/abc_register.go`），加一个 worker 只改那个文件；公共项留在 `worker` 顶层。分组块用结构体字段而不是 map，严格解码才能拒绝块内拼错的键，`default` 标签也才会生效。

### 8.2 默认值

| 键 | 默认 | 约束 |
| --- | --- | --- |
| `shutdown_timeout` | `25s` | `> drain_timeout`，且 `< terminationGracePeriodSeconds` |
| `drain_timeout` | `20s` | `> 0` |
| `restart.backoff` | `1s` | — |
| `restart.max_backoff` | `30s` | `>= backoff` |
| `restart.max_restarts` | `0`（不限） | — |
| `<g>.<w>.concurrency` | `1` | `1..255`（`Func` 用 `uint8` 承载，越界在构造分组时报错） |
| `claim.batch` | `100` | `1..1000` |
| `claim.lease` | `60s` | `>= 3 × p99 处理耗时` |
| `claim.max_attempts` | `5` | `>= 1` |
| `claim.idle_delay` | `2s` | `>= 200ms` |

启动时校验并**在错误信息里给出键路径**（`worker.group1.scan.claim.lease`），不接受"配置错了但悄悄用默认值"。

### 8.3 与 HTTP 配置的复用

`Log`、`DB`、`OTel`、`Env` 共用同一份 `config.yaml`；worker 进程只额外读 `worker` 段。DB 连接池建议独立：worker 的 `max_open_conns` 与 HTTP 分开配，否则 `concurrency × batch` 的并发会把 HTTP 的连接挤掉。

---

## 9. 可观测性

### 9.1 日志字段

每条 worker 日志固定带：`group`、`worker`、`instance`（`name#index`）、`host`、`pid`。claim 相关再加 `job`、`owner`、`batch`、`reaped`。

退出与重启必须成对出现：`worker started` / `worker stopped (reason=ctx|error|fatal, restarts=n, drained=ms)`。

### 9.2 指标清单

| 指标 | 类型 | 标签 | 用途 |
| --- | --- | --- | --- |
| `gofy_worker_up` | gauge | group, worker, instance | 实例存活 |
| `gofy_worker_restarts_total` | counter | group, worker | 重启风暴告警 |
| `gofy_worker_run_seconds` | histogram | group, worker | 单次运行时长（异常短=启动失败循环） |
| `gofy_worker_drain_seconds` | histogram | group | 停机是否逼近预算 |
| `gofy_claim_backlog` | gauge | job | 积压量（告警主指标） |
| `gofy_claim_batch_size` | histogram | job | 批次利用率（长期偏小=任务不足或批次过大） |
| `gofy_claim_reaped_total` | counter | job | 崩溃频率 |
| `gofy_claim_lease_lost_total` | counter | job | 处理超时/重复执行 |
| `gofy_claim_dead_total` | counter | job | 进入死亡态，需人工介入 |

### 9.3 trace

MQ 消费已有 header 传播（`support/headerpropagation`）。worker 侧要求：从消息头提取 trace context 作为 handler 的父 span，batch 处理时每条消息一个 span（不要整批一个），否则定位不到具体消息。

---

## 10. 部署

### 10.1 k8s 清单

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-service-order-worker
spec:
  replicas: 2
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0     # 先起新的再停旧的，避免分组内断流
      maxSurge: 1
  template:
    spec:
      terminationGracePeriodSeconds: 30   # > worker.shutdown_timeout
      containers:
        - name: worker
          image: registry.example.com/demo-service:1.2.3
          command: ["./demo-service", "worker", "--group=order"]
          env:
            - name: GOFY_ENV
              value: prod
          resources:
            requests: { cpu: 200m, memory: 256Mi }
            limits:   { cpu: "1",  memory: 512Mi }
```

同一个镜像、不同 `command`：HTTP 是 `start`，worker 是 `worker --group=<g>`。构建产物只有一个，避免"worker 镜像和 API 镜像版本不一致"。

### 10.2 副本数与扩缩容

| 类型 | 扩展方式 | 上限 |
| --- | --- | --- |
| Kafka | 加副本 | 分区数（超出无收益） |
| AMQP | 加副本或加 prefetch | 受 broker 与下游限制 |
| RocketMQ | 加副本 | 队列数 |
| claim 扫表 | 加副本或加 `concurrency` | 数据库写能力 |
| 全局型（lease） | **不能扩**，多副本只有一个在干活 | 1 |

HPA 建议只对 claim/AMQP 类开，指标用 `gofy_claim_backlog` 而不是 CPU：消费者瓶颈通常在数据库或下游，CPU 常年不高。

### 10.3 滚动更新

- `maxUnavailable: 0` + `maxSurge: 1`：保证任意时刻组内至少有一份在跑（全局型除外，切换期必然有间隙）。
- 更新期间新旧实例并存：Kafka 会触发 rebalance（正常），claim 会按 owner 天然隔离（正常，旧的 lease 到期后被回收）。
- 不要对 worker 用 `Recreate`：会直接断流。

---

## 11. 优先级任务清单

> 规则同 `interface-api-split-design.md` §11：每条判据必须是可执行命令或可核对产物。
> P0 关掉之前不进入 P1（没有进程形态，claim 无处安放）。

### 11.1 P0：进程形态与优雅退出

| ID | 任务 | 来源 | 前置 | 完成判据 |
| --- | --- | --- | --- | --- |
| T3-1 | `worker` 子命令：`commandline` 新增 `newWorkerCmd`，`App.RunWorker` 复用 logger/DB/telemetry/Runtime，跳过 `Register` | §3.1/§3.5 | — | `go run . worker --list` 打印分组并退出 0；`worker --group=不存在` 返回非零且错误含组名 |
| T3-2 | `support/worker`：`Worker`/`Group`/`Func`/`IdentityFrom` + 组名与重名校验 | §4.1–§4.3 | T3-1 | `go test -race ./support/worker/...` 通过；非法组名、组内重名各有用例 |
| T3-3 | 并发实例与错误策略：`concurrency` 起 N 个实例，`Policy` 实现指数退避与 `max_restarts` | §4.3/§4.4 | T3-2 | 单测：注入必失败的 worker，重启次数与退避序列符合预期 |
| T3-4 | 优雅退出：`consumeCtx` / `drainCtx` 两级 context + drain 预算 + 关闭顺序 | §3.6/§5.5 | T3-3 | 集成测试：SIGTERM 后日志顺序为 `stop consume → drained → closed`，总耗时 ≤ `shutdown_timeout` |
| T3-5 | 进程标识与日志：`systemenv.IsWorkerService/WorkerGroup`、pid 文件、`applyLogPaths` 增 worker 分支 | §3.7/§3.8 | T3-1 | `worker --group=order` 生成 `storage/log/worker/{date}/info/worker.log`（带 `--name` 时用 worker 名）与 `app/demo-service-worker-order.pid`（demo 的 `pid_file` 为 `app/demo-service.pid`）；同名启动第二个进程返回 `ErrAlreadyRunning` |
| T3-6 | 示例改造：demo-service 增加 `order` 分组（`order-scan` 用 DB 统计占位到 claim + `order-heartbeat` 演示无依赖任务），MQ 示例随 T3-9 的适配器一起补 | §3.4 | T3-5 | `cd examples/demo-service && make check` 通过；`go run . worker --group=order` 起 2+1 个实例，SIGTERM 后日志出现 `stopped` 且退出码 0 |

**M3 里程碑**：T3-1…T3-6 全关 → 常驻服务有独立进程形态，能停得干净。

### 11.2 P1：抢占与消费者托管

| ID | 任务 | 来源 | 前置 | 完成判据 |
| --- | --- | --- | --- | --- |
| T3-7 | `support/claim`：`Claim/Succeed/Fail/Reap` + MySQL 8 `SKIP LOCKED` + DDL 模板 | §7 | M3 | 双进程并发消费 1000 行，`grep owner` 每行唯一；`Succeed` 的 CAS 在 owner 变更后返回 `ErrLeaseLost` |
| T3-8 | claim 的可观测钩子：批次、积压、回收、死亡计数 | §7.7/§9.2 | T3-7 | 单测断言钩子被调用；指标名与 §9.2 一致 |
| T3-9 | 消费者适配器：`support/worker/consumer/{kafka,amqp,rocketmq}`，统一 drain 与错误策略 | §5.2/§5.3/§5.6 | T3-4 | 三个适配器各有集成测试（可用 testcontainers 或本地 broker）；取消后不丢 ack |
| T3-10 | `queues.Consume`：Redis 队列补循环 + 空轮询退避 + `Retry` 集成 | §5.4 | T3-4 | 单测：空队列时 `idle_delay` 生效；失败消息按 `max_retries` 落入失败集合 |
| T3-11 | 配置与校验：`worker` 段解析、默认值、键路径错误信息 | §8 | T3-1 | 缺 `--group`、`lease < 3×p99`（配置校验项）、`drain > shutdown` 各有一条错误用例 |

**M4 里程碑**：T3-7…T3-11 全关 → 扫表场景可多副本部署且不重复。

### 11.3 P2：全局单例与运维增强

| ID | 任务 | 来源 | 前置 | 完成判据 |
| --- | --- | --- | --- | --- |
| T3-12 | `support/lease`：`Locker`/`Lease` + Redis 实现（INCR fence + SET NX PX + Lua 续约/释放） | §6.2/§6.3 | M4 | 双进程抢锁只有一个成功；模拟暂停超过 TTL 后，旧持有者写入被 fence 拒绝 |
| T3-13 | k8s Lease 实现放 `gofy-adapter` | §6.4 | T3-12 | 主模块 `go list -deps` 不含 `k8s.io/client-go`；adapter 内可编译并单测 |
| T3-14 | `stop --worker=<group>`：按 pid 文件停止指定分组 | §3.3 | T3-5 | 停掉 worker 不影响同机 HTTP 实例 |
| T3-15 | 可选 `:port/healthz`（只报 worker 就绪，不做业务健康） | §4.5 | T3-4 | 端口可关闭；开启时探针 200/503 语义正确 |
| T3-16 | 文档与模板：`docs/` 使用指南、`gofy new service` 模板增加 `worker` 入口示例 | §12 | M4 | 新生成项目 `worker --list` 可用，`make check` 通过 |

**M5 里程碑**：T3-12…T3-16 全关 → 全局单例、运维命令、脚手架齐备。

### 11.4 验收命令

```bash
# 框架
cd gofy && make check

# 示例（含 worker 改造）
cd examples/demo-service && make check
go run . worker --list
go run . worker --group=order &   # 另开终端 kill -TERM，检查退出日志

# 抢占正确性（T3-7）
go test -race -count=1 -run TestClaimConcurrent ./support/claim/...
```

### 11.5 决策记录

| ID | 备选方案 | 否决理由 |
| --- | --- | --- |
| D-1 | 消费者继续挂 `Starter`，用环境变量控制"只在某个 POD 跑" | 环境变量是人工同步状态，POD 重建即失效；HPA 抖动导致反复 rebalance |
| D-2 | 用 k8s `replicas: 1` 解决扫表重复 | 滚动更新有空窗、节点故障要等重调度、无法扩容；只适合低价值任务 |
| D-3 | 用 Redis 全局锁包住整个扫表循环 | 把可分区任务退化成单点；锁失效时重复处理更隐蔽 |
| D-4 | 模拟 swoolefy 的 task 进程池 | Go 无 fork 语义，goroutine + Pod 已覆盖；task 进程模型会引入 IPC 复杂度 |
| D-5 | `UPDATE ... ORDER BY ... LIMIT` 抢占（MySQL 5.7 写法） | 不支持 `SKIP LOCKED`，并发下阻塞等待甚至死锁；生产是 MySQL 8，无需兼容 |
| D-6 | 把 k8s Lease 实现放进主模块 | `client-go` 依赖过重，违背核心模块克制依赖的取向 |
| D-7 | 框架内实现 cron 调度器（robfig/cron 等） | 与 k8s CronJob 职责重叠，双跑即重复执行；调度语义一旦进框架就要背时区、DST、错过补偿、并发策略一整套包袱（D11） |
| D-8 | 只提供 `Queue.Pop`，消费循环留给项目 | 空轮询退避、失败重投、panic 兜底三件事各写一遍必然走样，且没人为它修 bug（D10） |

### 11.6 实施状态

| ID | 状态 | 产物 / 复核方式 |
| --- | --- | --- |
| T3-1 | 已完成（2026-11-14） | `internal/commandline/worker.go`、`worker_host.go`；`worker --list` 输出 GROUP/WORKER/CONCURRENCY，`--group` 缺失返回 `ErrUsage`、未知分组返回 `ErrWorkerGroupNotFound` |
| T3-2 | 已完成（2026-11-14） | `support/worker/{worker,identity,policy,runtime,runner}.go`；`go test -race ./support/worker/` 覆盖组名校验、重名、Subset、空并发 |
| T3-3 | 已完成（2026-11-14） | `runner.supervise` + `Policy`：指数退避、±20% 抖动、`MaxRestarts`；用例覆盖「重启到成功」「触顶退出」「ExitOnError 立即退出」「提前返回 nil 视为违约」 |
| T3-4 | 已完成（2026-11-14） | `Runner.Run` 两级 context：`runCtx` 停止拉取、`drainCtx = WithoutCancel + deadline`；用例断言 drain 期间 `DrainContext` 仍有效且 Runner 等在途任务结束 |
| T3-5 | 已完成（2026-11-14） | `systemenv.IsWorkerService/WorkerGroup` + `workerPIDPath` + `applyLogPaths` worker 分支；实测生成 `storage/log/worker/20261114/info/order-scan-h11.log`（`--name=order-scan`）与 `worker-h11.log`（整组），pid 文件用 `-worker-order` 后缀并在退出时清理 |
| T3-6 | 已完成（2026-11-14） | `examples/demo-service/worker/`：`config.go`（公共设置 + 按分组分层的 `Config`）、`worker.go`（`Register` 返回全部组）、`group1/`（`abc_register.go` 装组成员与配置 + `scan.go`、`heartbeat.go`）、`group2/`（`abc_register.go` + `summary.go`）；`config.yaml` 已无 `worker:` 段：并发与节奏写在各分组包内，探针未启用（`options.go` 未传 `WithWorkerHealth`）。实测 `worker --list` 列出 group1（order-scan×2、order-heartbeat×1）与 group2（report-summary×1）两组；`--group=group1` 与 `--group=group2` 各自生成 `-worker-<group>.pid` 与独立日志，SIGTERM 后均 `stopped`、退出码 0 |
| T3-7 | 已完成（2026-11-14） | `support/claim/{claim.go,claim_test.go,claim_integration_test.go}`；真实 MySQL 8.4 上 1000 行 × 2 个并发 owner 零重复、`job` 隔离、`Succeed/Fail` 的 CAS 拒绝过期 owner、回收过期租约后 `attempts` 递增 |
| T3-8 | 已完成（2026-11-14） | `claim.Hooks{OnClaim,OnReap,OnLeaseLost}`；用例断言租约丢失回调触发；不引入监控库依赖 |
| T3-9 | 已完成（2026-11-14） | `support/worker/consumer/{kafkaconsumer,amqpconsumer,rocketmqconsumer,queuesconsumer}`；用例断言 handler 收到的是 drain context（投递 ctx 已取消也仍有效）、停机才 `Close`、可重试错误不 `Close` |
| T3-10 | 已完成（2026-11-14） | `support/queues/consume.go`、`consume_test.go`；`go test -race -count=1 ./support/queues/` 通过；覆盖空轮询、失败重投、重试耗尽、丢弃、panic、空闲停机六条路径 |
| T3-11 | 已完成（2026-11-14） | `WithWorkerConcurrency` 校验 `group/worker` 形状与 `>=1`、`WithWorkerDrainTimeout` 校验正数、`worker` 缺 `--group` 报 `ErrUsage`；对应用例各一条 |

| T3-12 | 已完成（2026-11-14） | `support/lease/{lease,lease_test}.go` + `support/lease/redislease`；miniredis 用例覆盖双实例互斥、释放后 token 递增、续约延长、过期后旧持有者 `Renew/Release` 均失败、fence 拒绝旧持有者写入、`Run` 的待命/周期/丢锁/释放四条路径 |
| T3-13 | 已完成（2026-11-14） | `gofy-adapter/adapter/lease/k8slease`（不在本仓库）；核心模块 `go list -deps ./...` 中 `k8s.io/` 计数为 0；adapter `make check` EXIT=0，假 API 覆盖创建、互斥、接管递增、同名重入、冲突转 ErrHeld、Release 交还 |
| T3-14 | 已完成（2026-11-14） | `stop --worker=<group>` + `stop.StopOptions.Worker`；组名先过 `worker.ValidName` 再拼 pid 路径；用例断言只存在 HTTP pid 文件时返回 `ErrNotRunning` 且原文件未被触碰 |
| T3-15 | 已完成（2026-11-14） | `gofy.WithWorkerHealth(addr)` + `worker_health.go`；实测 `worker --group=order` 时 `/healthz` 200、未知路径 404、停机后端口关闭；未配置时不监听任何端口 |
| T3-16 | 已完成（2026-11-14） | `gofy new --with-worker`（默认开）生成 `worker/{register,example}.go` 并接入 `WithWorkerGroups`；`docs/worker-guide.md`；脚手架实测 `go mod tidy` clean、`go build ./...` 通过、`worker --list` 输出 default/example |
| T3-17 | 已完成（2026-11-14） | `worker --group='*'`：`selectWorkerGroups` 返回全部注册分组，pid 后缀取 `all`、`stop --worker='*'` 由旗标层归一为 `all`；多个 runner 并发运行，首个错误取消其余；`--name` 与 `*` 同用返回 `ErrUsage`。实测 demo 两组同进程 3 个 worker、`/healthz` 200、`stop --worker='*'` 后退出码 0 |

P1/P2 全关（M4、M5 达成）。`make check` 与 adapter `make check` 均 EXIT=0。

集成测试需要真实 MySQL 8（`GOFY_MYSQL_DSN`），未设置时自动跳过，`make check` 因此不依赖外部服务。

---

## 12. 附录：与 swoolefy daemon 的对照

| swoolefy | 本设计 | 说明 |
| --- | --- | --- |
| `php script.php start` 起多进程 | `app worker --group=<g>` 起多 goroutine | 不 fork；隔离单位从进程换成 Pod |
| `worker_num` | `concurrency` | 每个 worker 的实例数 |
| 进程组配置 | `worker.<g>` | 一个组 = 一个 Deployment |
| `reload` 平滑重启 | 滚动更新 + SIGTERM drain | 不做进程内 reload：状态在 DB/MQ，不在内存 |
| task 进程 + 管道投递 | claim 表 + MQ | Go 生态用存储/队列解耦，不用 IPC |
| 常驻进程内定时器 | worker 内部 ticker | 框架不管调度语义，避免与 CronJob 双跑 |

**可借用的一点**：分组 + 组内并发数 + 按组启停，这套编排抽象在运维上是有效的；**不借用的一点**：多进程与 IPC——Go 的调度与 k8s 的 Pod 已经把这两层吃掉了。
