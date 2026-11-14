# worker

本目录演示 gofy 的常驻任务（worker）：一个进程跑一组 worker，分组就是部署单位。
与另外两种形态的区别只有边界：

| 形态 | 入口 | 生命周期 | 典型用途 |
| --- | --- | --- | --- |
| HTTP | `demo-service start` | 常驻，监听端口，写 pid | 对外接口 |
| script | `demo-service script <name>` | 跑完即退，不写 pid | 数据修复、一次性迁移 |
| worker | `demo-service worker --group=<g>` | 常驻，不监听业务端口 | 扫表、MQ 消费、定时汇总 |

worker 进程没有 HTTP 栈，也不注册路由。轮询类任务通过框架注入的 `Runtime` 拿
logger 与数据库，MQ 消费者则在 `Run` 里按配置自建连接——worker 进程不运行 `app/io`
模块，因此没有现成的连接句柄可以引用，这一点与 HTTP 进程不同。

## 目录

```
worker/
├── worker.yaml            # 四个消费者的全部配置（独立于 config.yaml）
├── worker.go              # Register：构造全部分组，交给 main
├── group1/
│   ├── abc_register.go    # group1 的 NewGroup：按配置装配成员
│   ├── config.go          # 四个消费者的全部可配置项与校验
│   ├── scan.go            # order-scan：扫表 + 它的 Defaults
│   ├── heartbeat.go       # order-heartbeat：存活心跳
│   ├── consume.go         # 共用链路：信封、幂等台账、trace、错误分类
│   ├── handler_order.go   # 业务分支：order.paid / order.cancelled
│   ├── kafka.go           # order-kafka-consumer
│   ├── amqp.go            # order-amqp-consumer
│   ├── rocketmq.go        # order-rocketmq-consumer
│   └── queues.go          # order-queues-consumer（Redis 队列）
└── group2/
    ├── abc_register.go    # group2 的 NewGroup
    └── summary.go         # report-summary：小时级汇总 + 它的 Defaults
```

`abc_` 前缀只是为了让入口文件排在成员文件之前，没有语义；下划线开头不行，Go 会忽略
`_register.go`，`NewGroup` 会跟着从编译里消失。

每个分组自成一个包，参数、构造、成员都在包内。给 group1 加一个 worker，不需要
碰 group2 的任何文件。

## 两个分组各做什么

| 分组 | worker | 实例数 | 节奏 | 说明 |
| --- | --- | --- | --- | --- |
| group1 | `order-scan` | 2 | 5s | 统计待支付订单；真实队列在这里调 `claim.Queue.Claim` |
| group1 | `order-kafka-consumer` | 1 | 常驻 | Kafka 订阅，手动提交位点 |
| group1 | `order-amqp-consumer` | 1 | 常驻 | RabbitMQ 队列，prefetch 32 |
| group1 | `order-rocketmq-consumer` | 1 | 常驻 | RocketMQ 订阅，SDK 侧拉取 |
| group1 | `order-queues-consumer` | 2 | 常驻 | Redis 队列，弹出即删除 |
| group1 | `order-heartbeat` | 1 | 10s | 不依赖数据库，留一行可 grep 的存活日志 |
| group2 | `report-summary` | 1 | 1h | 按状态聚合订单，多实例会重复写同一份报表 |

拆两个分组不是为了好看：group1 是秒级、随订单量扩缩；group2 是小时级、只要一份。
两者的副本数、发布节奏、资源画像都不同，混在一个进程里只能取交集。

四个消费者默认全部 `enabled: false`，因此默认的 `--list` 只显示扫描与心跳。启用哪个，
`--list` 就多出哪个；实例数由 `worker.consumers.<kind>.concurrency` 决定，`--concurrency`
仍可覆盖。

## 消费者怎么接入

四个消费者共用同一条链路，协议差异只留在各自的协议文件里：

```
kafka.go / amqp.go / rocketmq.go / queues.go   把协议记录转成 Message
        ↓
consume.go Processor.Handle                    提取 trace → 查幂等台账 → 解信封
        ↓
handler_order.go                               按 event.type 落到 orders 表
```

### 消息契约

生产者与消费者共用一个信封，`type` 决定业务分支，`payload` 由分支自己解码：

```json
{
  "id": "evt-1",
  "type": "order.cancelled",
  "occurred_at": "2026-01-01T00:00:00Z",
  "payload": {"order_id": 1, "user_id": 7, "reason": "user"}
}
```

示例认识 `order.paid` 与 `order.cancelled`。新增一个事件只需要在
`handler_order.go` 的 `orderEventStatus` 里加一行，消费端不用新增 worker。

### 幂等

`worker_consumed_messages` 以 `(consumer, message_id)` 建唯一索引，"认领"就是一次
`INSERT ... ON CONFLICT DO NOTHING`：拿到行就继续，拿不到就说明处理过。没有锁，两个
副本之间也没有读改写窗口。

| 台账状态 | 含义 | 下次投递 |
| --- | --- | --- |
| `processing` | 正在处理 | 重试（可能上次中途崩溃） |
| `done` | 已成功 | 跳过并确认 |
| `failed` | 可重试失败 | 重试，直到 `max_attempts` |
| `dead` | 不再重试 | 跳过并确认 |

消息 ID 的来源按协议降级：

| 协议 | 首选 | 兜底 |
| --- | --- | --- |
| Kafka | `message_id` 头 | `topic/partition@offset` |
| AMQP | `MessageId` | `X-Correlation-Id`，都没有则不去重 |
| RocketMQ | broker `MessageID` | `topic@offset` |
| Redis 队列 | 队列生成的 `__id` | 无（总是有） |

AMQP 的空兜底是刻意的：拿 `DeliveryTag` 当 ID 会把每次重投当成新消息，而随机 ID
只是放弃去重，不会把两条不同消息误判成同一条。

### 错误分类

| Handler 返回 | 处理 | 效果 |
| --- | --- | --- |
| `nil` | 台账置 `done`，确认 | 分区继续推进 |
| `group1.Permanent(err)` | 台账置 `dead`，确认 | 毒消息不再重投 |
| 其它 `error` | 台账置 `failed`，不确认 | broker 重投，直到 `max_attempts` |

解不出信封、未知 `event.type`、`payload` 缺 `order_id` 都属于永久错误——重投多少次
结果都一样，确认掉比占着分区更有信息量（`dead` 行本身就是待排查清单）。

订单不存在属于可重试错误：多分区乱序时，创建事件可能还在路上。它最终会被
`max_attempts` 截断，不会无限占用。

### 停机

适配器把投递上下文换成 drain context，所以 SIGTERM 到达时：

1. 各消费者停止拉取（`runCtx` 取消）；
2. 在途消息继续跑，最多 `drain_timeout`（默认 20s）；
3. handler 仍用 drain context 写库，不会因为停机被切断在半路；
4. 预算耗尽只写一条 warn，进程仍以 0 退出。

`drain` 预算从**停机时刻**起算，不是从进程启动起算——这一点由
`support/worker` 的 `newDrainContext` 保证，否则跑得比预算久的 worker 会拿到过期
context，每个 handler 都失败。

### 新增一个消费者

1. 在 `group1/config.go` 加一个配置结构（照 `KafkaConsumer`），带 `Enabled` 与
   `Instance`，在 `Consumers` 与 `Validate` 里各加一行；
2. 照 `kafka.go` 写一个文件：`Run` 里建连 → 构造适配器 → `return w.Run(ctx)`，
   协议记录转成 `Message`；
3. 在 `abc_register.go` 的 `NewGroup` 里 `if consumer.Enabled { ... }` 追加；
4. 在 `worker/worker.yaml` 的 `consumers` 下加同名配置块，`instance` 指向 config.yaml 里已有的
   `kafka`/`amqp`/`rocketmq`/`redis` 实例。

`worker --list` 出现新名字即接好，不用改 main、不用改部署清单。

## 运行

`--group` 必填，缺省直接报用法错误并退出：

```bash
$ go run . worker
[FAIL] gofy: usage error: worker: --group is required (see worker --list)
```

先看清有哪些分组、各自会起几个实例（不连数据库、不启动任何 worker）：

```bash
$ go run . worker --list
┌────────┬─────────────────┬─────────────┐
│ GROUP  │ WORKER          │ CONCURRENCY │
├────────┼─────────────────┼─────────────┤
│ group1 │ order-scan      │ 2           │
├────────┼─────────────────┼─────────────┤
│ group1 │ order-heartbeat │ 1           │
├────────┼─────────────────┼─────────────┤
│ group2 │ report-summary  │ 1           │
└────────┴─────────────────┴─────────────┘
```

启动整组。前台运行，日志同时写 stdout 与文件：

```bash
$ go run . worker --group=group1
{"level":"info","caller":"gofy/worker_host.go:112","msg":"worker group started","group":"group1","workers":2,"drain_timeout":20}
{"level":"info","caller":"group1/scan.go:53","msg":"order scan started","worker":"group1/order-scan#0","interval":5}
{"level":"info","caller":"group1/scan.go:53","msg":"order scan started","worker":"group1/order-scan#1","interval":5}
```

`workers:2` 是这个分组的成员数（`order-scan` 与 `order-heartbeat`），不是实例数；
`order-scan` 起了 2 个 goroutine，靠日志里的 `worker` 字段区分（`#0`、`#1`）。

本地调试时可以用通配符把全部注册分组放进同一个进程：

```bash
$ go run . worker --group='*'
{"level":"info","caller":"gofy/worker_host.go:145","msg":"worker groups started","workers":3,"groups":["group1","group2"]}
```

它只解决“一条命令起全部循环”，不改变部署语义：pid 变成 `-worker-all.pid`，
`stop --worker='*'` 一次停掉全部，任一分组失败会取消其余分组。引号不能省，否则 shell 先
展开 `*`；也不能和 `--name` 同用——worker 名只属于一个分组，跨组过滤会静默跳过不含它的组。

只跑组内某个 worker，或临时改实例数（不改代码）：

```bash
go run . worker --group=group1 --name=order-scan
go run . worker --group=group1 --name=order-scan --name=order-heartbeat
go run . worker --group=group1 --concurrency=1     # 组内所有 worker 都变 1 个实例
```

`--name` 可重复，也接受逗号分隔；`--concurrency=0`（默认）表示沿用 `worker.Func(name, n, run)`
里的数字。覆盖只在本次启动生效：`worker --list` 读的是代码里的值，不会跟着 `--concurrency`
变。

> 本地调试建议先 `go build -o /tmp/demo .` 再跑二进制：`go run` 会把信号交给 go 工具链，
> Ctrl+C 下 worker 的 drain 过程和退出码看不准确。

## 进程留下的东西

| 产物 | 位置 | 说明 |
| --- | --- | --- |
| pid 文件 | `app/demo-service-worker-group1.pid` | 默认 pid 文件的 `-worker-<分组>` 后缀；未配置 `pid_file` 则不写 |
| pid 文件 | `app/demo-service-worker-all.pid` | `--group='*'` 时用这个后缀，`stop --worker='*'` 解析同一个文件 |
| 日志 | `storage/log/worker/<date>/info/worker-h15.log` | 整组启动写 `worker-<host>.log` |
| 日志 | `storage/log/worker/<date>/info/order-scan-h15.log` | `--name` 只跑一个 worker 时用它的名字 |
| 表 | `worker_consumed_messages` | 消费幂等台账；消费者首次启动自动建表，也可先跑 `migrations/0003_worker_consumed_messages.sql` |

日志文件不带分组名：分组已经在每行日志的 `worker` 字段里（`group1/order-scan#0`）。
要一个 worker 一组文件，就用 `--name` 单独起；整组启动共用一份，靠字段区分。

demo 没开就绪探针：`options.go` 里没有 `gofy.WithWorkerHealth`，worker 进程不监听任何
端口。要接 readinessProbe 就在那里加一行地址，行为是启动前 503、就绪后 200、drain 时
回到 503。

## 停止

```bash
$ go run . stop --worker=group1 --force
[INFO] 正在停止 worker 分组【group1】 (pid 80656)
[ OK ] worker 分组【group1】 已停止 (pid 80656)

# 停掉 --group='*' 起的那个进程
$ go run . stop --worker='*' --force
[INFO] 正在停止 worker 分组【all】 (pid 80656)
[ OK ] worker 分组【all】 已停止 (pid 80656)
```

不带 `--force` 会先问一次 `你确定 [停止] 应用【demo-service】? (yes or no):`。
停止走 SIGTERM：worker 的拉取先停（`runCtx` 取消），在途任务继续跑到
`drain_timeout`（默认 20s，`gofy.WithWorkerDrainTimeout` 可改），然后进程退出，pid
文件清理。超时未退才在 `--force` 下补 SIGKILL。

`stop` 的粒度是分组，`stop --worker=group1` 不会碰到 group2，也不会碰 HTTP 进程。
`restart` 只用于 HTTP 实例，worker 用 `stop` + 重新启动。

## 参数

参数分两类，来源不同不是偶然：

| 参数 | 放在哪 | 理由 |
| --- | --- | --- |
| 扫描节奏、默认实例数 | 代码里的 `xxxDefaults` | 属于实现细节，改它意味着改逻辑 |
| topic、消费组、开关、并发、重试预算 | `worker/worker.yaml` | 属于部署事实，每个环境不同 |

扫描任务的参数写在它自己的文件里，结构体和取值放在一起，一个文件读完就知道会起几个
实例、多久跑一次：

```go
// group1/scan.go
type ScanWorkerConfig struct {
	Concurrency int    // 进程内扫描实例数
	IntervalMS  uint32 // 两次扫描的间隔，单位毫秒
}

var scanDefaults = ScanWorkerConfig{
	Concurrency: 2,
	IntervalMS:  5000, // 5 秒
}
```

`abc_register.go` 只负责把 worker 装进分组，不碰数字：`newScanWorker(scanDefaults)`。
改参数就改这个 `scanDefaults`；临时改实例数用 `--group=group1 --concurrency=4`，不必动代码。
结构体上没有 `yaml` 标签，配置里写 `scan:` 会被严格解码拒绝——扫描参数不再是配置来源。

消费者相反：`group1.Config` 带 `yaml` 标签，由 `app/config` 从 `worker/worker.yaml`
解码后经 `worker.Register(Deps)` 传进分组。为什么单独一个文件：

- 只有 `worker --group=<g>` 读它，HTTP 进程与脚本完全不加载；
- 它随 worker 的 Deployment 走，改 topic 或并发只重启 worker，不碰 API 配置；
- 容器里挂 ConfigMap 时用 `WORKER_CONFIG_PATH` 指向挂载路径，镜像布局不用改。

文件顶层就是 `consumers:`，一个配置块长这样：

```yaml
# worker/worker.yaml
consumers:
  kafka:
    enabled: true
    instance: default          # 复用 config.yaml 里 kafka.default 的连接参数
    concurrency: 1
    topic: demo.order.events
    group: demo-service-order-consumer
    commit_mode: manual
    idempotency: true
    max_attempts: 5
```

文件不存在等同于四个消费者全部关闭，进程照常启动（只跑 API 的部署不必携带它）；
文件存在但解析或校验失败会在启动时报错。`config.yaml` 里再写 `worker:` 段会得到一条
指向新路径的错误，而不是被静默忽略。

`instance` / `redis_instance` 只写实例名，broker 地址在 `kafka` / `amqp` / `rocketmq` /
`redis` 段配一次。引用了未启用的实例会在 `Register` 阶段报错，不会静默跑一个什么都不消费的
worker。

四条约束：

- **实例数在 `worker.Func` 里是 `uint8`**：配置里的值先由 `Validate` 挡在 `1..255`，
  转换函数再兜一层，越界回落到 1，不会静默截断成另一个数字。
- **`--concurrency` 优先级最高**：命令行覆盖配置与 worker 自己声明的数字，临时压测不必改文件。
- **worker 名全应用唯一**：日志文件名取自 worker 名，两个分组用同一个名字会写到同一个
  文件（demo 里是 `order-scan`、`order-kafka-consumer`、`report-summary` 等）。
- **配置错误在启动前失败**：`config.Load` 调 `Worker.Validate`，`Register` 解析实例名，
  两者都发生在写 pid 文件与建连之前。

并发实例共享同一个 `Worker` 值，`Run` 里不能有可变的接收者状态；需要每实例独立的数据
就放在 `Run` 内部，从 `worker.IdentityFrom(ctx)` 取 `group/name#index`。

## 新增任务

组内加一个 worker，只动那个分组包：

1. 新建成员文件，例如 `group1/settle.go`，用 `worker.Func("order-settle", 1, run)` 定义；
2. 参数结构体与它的 `xxxDefaults` 也写在成员文件里（照 `ScanWorkerConfig` + `scanDefaults`）；
3. 在 `abc_register.go` 的 `NewGroup` 里把新 worker 加进参数列表；
4. `go run . worker --list` 核对，列表里出现新名字即可。

轮询型任务在 `run` 里用 `worker.Ticker(ctx, intervalMS, fn)`：按固定节拍触发，单次失败只记日志、
panic 被隔离，ctx 取消返回 nil（回调比间隔慢时，积压的 tick 会合并成一次）。三个成员文件都是
这个写法，不必自己写 ticker + select。

## 新增分组

1. 新建子包 `worker/group3/`，加 `abc_register.go`（只含 `NewGroup`）与成员文件（含各自的参数结构体与 defaults）；
2. `worker/worker.go` 的 `Register` 里构造该分组并加进返回列表；
3. `go run . worker --list` 核对，`worker --group=group3` 即可启动。

`main.go` 与部署清单不用改结构：复制一份 Deployment，改 `--group=group3` 和副本数即可。
同一个镜像服务所有分组，因此不必为每个分组单独出一个二进制。

## 排障

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `[FAIL] worker: --group is required` | 漏了 `--group` | `worker --list` 看分组名 |
| 退出码 1，终端打印 `[FAIL] gofy: worker group not found: "x"` | `--group` 写错 | `worker --list` 看有哪些分组 |
| 退出码 1，终端打印 `[FAIL] worker: unknown worker: "x" in group "group1"` | `--name` 不在该分组里 | `worker --list` 看组内成员 |
| 进程起来就退出、退出码 0 | `Run` 在 ctx 取消前返回了 nil | 循环必须阻塞到 `ctx.Done()` |
| 日志找不到 | 目录看错 | `storage/log/worker/<date>/info/`，整组是 `worker-<host>.log` |
| `stop --worker=x` 报 not running | 未配置 `pid_file`，或分组名与启动时不同 | 查 `app/*-worker-<group>.pid` |
| `--group='*'` 配 `--name` 报 usage error | 通配符按组运行，`--name` 只能指定一个组内的 worker | 改成 `--group=<拥有该 worker 的组>` |
| `--list` 的数字和实际不同 | 用了 `--concurrency` | 覆盖只在本次启动生效，`--list` 读的是配置或代码里的数字 |
| 启动即报 `kafka instance "x" is not configured or not enabled` | `instance` 写了未启用的实例名 | 改 `instance`，或把对应的 `kafka.x.enabled` 打开 |
| 启动即报 `worker.consumers.kafka: topic is required` | 打开了 `enabled` 但没填必填项 | 按报错里的路径补配置 |
| 日志出现 `consume claim failed` | 幂等台账不可用（库连不上、表被锁） | 先看数据库；台账不可用时不确认消息，broker 会重投 |
| 同一条消息反复处理 | handler 返回可重试错误 | 看台账 `last_error`；`max_attempts` 用完后自动转 `dead` |
| 日志出现 `message skipped` | 幂等命中或尝试次数用完 | 正常行为；查台账 `status` 区分是哪一种 |
| 日志出现 `message discarded: permanent failure` | 未知 `event.type` 或消息体解不出 | 台账 `status=dead`，按 `last_error` 修生产端或补分支 |

## 相关文档

- `docs/worker-guide.md`：四种循环（claim / MQ / Redis 队列 / lease）的写法与部署建议
- `docs/worker-daemon-design.md`：设计与验收记录
- `support/worker/README.md`：包 API、`Policy` 重启策略、drain 语义
- `examples/demo-service/storage/log/README.md`：日志目录与文件命名规则
