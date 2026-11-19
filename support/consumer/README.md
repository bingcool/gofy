# support/consumer

常驻消费者的完整实现：**一个订阅一个文件**，文件里是配置、业务 handler，没有别的。
每种 broker 一个构造器（`NewKafka` / `NewAMQP` / `NewRocketMQ` / `NewQueues`），
延迟订阅另有 `NewDelayQueues`（Redis ZSET）与 `NewDelayAMQP`（TTL + 死信）。
返回的值直接满足 `worker.Worker`，和扫描循环一样注册进分组。

背后统一处理四件事：消息信封、数据库幂等台账、trace 续接、永久/可重试错误分类。

| | 负责 | 不负责 |
| --- | --- | --- |
| 本包 | 配置默认值与校验、建连、订阅循环、信封解码、幂等台账、错误分类、trace | 业务分支 |
| `support/worker` | 分组、并发实例、重启策略、drain、pid 与日志 | 消费语义 |
| 业务 | 一个 `HandlerFunc`：一条事件该改哪张表 | —— |

## 快速开始

### 1. 写一个订阅

`worker/group1/kafka_order.go`：

```go
var orderKafka = gofyconsumer.KafkaConfig{
	Name:    "order-kafka-consumer",       // worker 名，也是幂等作用域
	Topic:   "demo.order.events",
	Group:   "demo-service-order-consumer",
	Handler: handleOrderKafka,
}

func handleOrderKafka(ctx context.Context, cfg gofyconsumer.KafkaConfig, msg gofyconsumer.KafkaMessage) error {
	return applyOrderEvent(ctx, msg.Envelope) // cfg 是本订阅；msg.Record 是原生 kgo.Record
}
```

配置就是这个结构体：零值表示默认值，只写让它与众不同的字段。第二个 topic 就再写一个
文件——这正是它替代 `worker.yaml` 的原因：那种一个 broker 一个块的配置，天然只能描述
一个订阅。

### 2. 注册进分组

```go
func NewGroup(conns gofyconsumer.Connections) (*worker.Group, error) {
	kafka, err := gofyconsumer.NewKafka(conns, orderKafka)
	if err != nil {
		return nil, err
	}
	return worker.NewGroup("group1", newScanWorker(scanDefaults), kafka)
}
```

`NewKafka` 只校验配置、解析连接段，**不建连**：`worker --list` 和被 `--name` 过滤掉的
消费者都不产生成本。连接在 `Run` 里建立，并在退出时释放。

### 3. 提供连接表

`Connections` 是应用已经解码好的 broker 配置，按实例名索引：

```go
conns := gofyconsumer.Connections{
	Kafka:    map[string]kafka.Config{"default": {...}},
	AMQP:     map[string]amqp.Config{"default": {...}},
	RocketMQ: map[string]rocketmq.Config{"default": {...}},
	Redis:    map[string]redis.Config{"ratelimit": {...}},
}
```

消费者的 `Instance` / `RedisInstance` 写的是这里的键。缺失会在注册阶段报错——早于
进程打开连接池、日志文件或 broker 连接。`enabled: false` 的连接段也可以传进来：那个
标志回答的是"HTTP 进程启动时是否建连"，worker 的决定权在"有没有注册这个消费者"。

## 配置与默认值

| 字段 | Kafka | AMQP | RocketMQ | Queues | DelayQueues | DelayAMQP |
| --- | --- | --- | --- | --- | --- | --- |
| `Name`（必填） | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `Concurrency` | 1 | 1 | 1 | 1 | 1 | 1 |
| `Idempotency` | false | false | false | false | false | false |
| `MaxAttempts` | 5 | 5 | 5 | 5 | 5 | 5 |
| 连接实例 | `Instance`=default | `Instance`=default | `Instance`=default | `RedisInstance`=ratelimit | `RedisInstance`=ratelimit | `Instance`=default |
| 订阅目标 | `Topic`+`Group`（必填） | `Exchange`+`Queue`（必填） | `Topic`+`Group`（必填） | `Key`（必填） | `Key`（必填，延迟 ZSET） | 暂存 `DelayExchange`+`DelayQueue`、目标 `Exchange`+`Queue`（必填） |
| 协议特有 | `CommitMode`=manual、`AutoOffsetReset`=earliest、`AutoCommitInterval`=200ms、`SessionTimeout`=45s、`BlockRebalanceOnPoll`=false | `ExchangeType`=direct、`PrefetchCount`=32、`NonDurable`=false | `Tags`、`PullWorkers`=1、`BatchSize`=16、`AwaitDuration`=5s | `MaxRetries`=3、`IdleDelay`=200ms、`RetryDelay`=3s | `MaxRetries`=3、`PopLimit`=10、`IdleDelay`=200ms、`RetryDelay`=3s | `TTL`（可选，0=纯发布端）、`ExchangeType`=direct、`PreserveRoutingKey`=false、`Passive`=false、`PrefetchCount`=32、`NonDurable`=false |

`DelayQueues` 由 `NewDelayQueues` 构造，订阅的是 Redis ZSET 里**已到期**的消息：生产端用
`queues.DelayQueue.Add(ctx, payload, delay)`，消费端轮询 `Pop`，失败由 `Retry` 以新到期
时间写回。`source` 是 `delay_queues`，日志与 trace 因此能与立即队列区分。它与 `Queues`
共用同一个 worker 适配器（两者 `Consume` 签名相同），在途消息同样是"至多一次"。

`DelayAMQP` 由 `NewDelayAMQP` 构造，消费的是 **TTL + 死信**流程里的目标队列：`Delay*`
字段声明暂存 exchange/queue，普通字段声明消息过期后被转发到的目标。**`TTL` 留零（默认、
推荐）即纯发布端模式**：队列不声明 `x-message-ttl`，延迟完全由生产端的
`amqp.PublishOptions.Delay` 决定，且该字段变成必填——漏写时 `Publish` 返回
`ErrDelayRequired`，避免消息永不过期地卡在暂存队列里。给 `TTL` 正值则回到经典固定延迟
队列，它既是默认延迟也是上限（broker 取两者较小值）。`QueueConfig()` 把两半拓扑一次性
导出，生产端用它发布就不会与消费端各自声明出不同的暂存队列。`source` 是
`amqp_delay`。它与 `AMQP` 共用 `amqpconsumer` 适配器——传输层的 `consumeQueue()` 在
存在 `Delay` 时自动改读目标队列。注意 RabbitMQ 只在队头判定过期，一个暂存队列请只承载
一种延迟；切换 TTL 模式要换队列名，已存在队列的参数不可改。

`Passive` 用于拓扑由别的服务（例如 Java）声明的场景：开启后两半拓扑只做存在性检查，
不再重新声明，因此不会因对方队列上的 `x-message-ttl` 等参数与本侧不一致而
`PRECONDITION_FAILED`。`QueueConfig()` 会把开关同时透传给暂存与目标，生产端复用它发布
就不会有一半被悄悄重建。名称、key、`TTL` 仍用于发布路由与发布前校验，必须与对方一致；
broker 死信时用的是它自己队列上的参数，不是本侧构造的 `DelayConfig`。详见
[support/amqp 的跨语言接入小节](../amqp/README.md#接入其它语言声明的延迟队列)。

把 `ExchangeType` / `DelayExchangeType` 设为 `topic` 就是 topic 延迟队列，此时**必须**
同时设 `PreserveRoutingKey: true`：默认的固定死信键会把每条消息的 routing key 覆盖成
同一个值，topic 绑定既分不出 `order.paid` 与 `order.cancelled`，也可能因字面 pattern
匹配不上而丢消息。开启后 broker 沿用原始键；生产端用 `PublishOptions.RoutingKey` 逐条
给出具体键，订阅只写 `BindingKey`（如 `order.*`）。topic 拓扑的 key 不能为空，注册阶段
会拒绝空 binding——那种队列永远收不到消息。direct 订阅保持默认即可。

`Idempotency` 需要 **`db.default`（框架托管的连接池）**，因此默认关闭：打开它就从
"handler 必须幂等"变成"重投是空操作"。台账是进程级、自带 DDL 的一张表，所以不放在命名实例
（`db.order`）里；只配命名实例的 worker 进程会在启动阶段被 `worker.RequireDefaultDB` 拒绝，
而不是让消费者在 `Run` 里失败、无限重启、就绪一直 503。
`MaxAttempts` 只在开启幂等时生效——没有台账就没有地方记尝试次数。

AMQP 的持久化用反向字段 `NonDurable`：普通 bool 的零值会让"重启即丢队列"变成默认，
那不是业务队列该有的默认。

Kafka 没有 `max.poll.interval.ms` 可调：franz-go 的心跳在独立 goroutine 上，慢 handler 不会
把成员踢出组。想避免重平衡落在 handler 与提交之间，用 `BlockRebalanceOnPoll: true`——当前
批次跑完、位点提交后才放行。`AutoCommitInterval` 只在 `CommitMode: auto` 下生效。

`CommitMode: manual`（默认）下 handler **不自己提交位点**：传输层给 franz-go 加
`DisableAutoCommit()`，在每个分区的一批处理完后提交 handler 接受的最后一条记录。
因此 handler 的返回值就是提交决策——`nil` 确认、可重试错误不提交并整批重投、
`Permanent` 记为 dead 但仍确认（避免毒消息卡住分区）——**前提是 dead 已落库**，写不进去
时按可重试错误处理，绝不确认。逐条决策日志与三条路径的
实跑样例见 [demo worker README](../../examples/demo-service/worker/README.md#kafka-手动提交)。

RocketMQ 的 `Tags` 是"或"列表：SDK 用 `||` 把元素拼成一个订阅表达式，因此元素在构造时
去掉首尾空格（原样拼接的话，多一个空格就是永远匹配不上的 tag），空元素或自身含 `||`
的元素在注册阶段报错。留空或只有一个 `*` 表示订阅全部 tag。

## handler 拿到的消息

每种消息都内嵌 `Envelope`（`ID`、`Type`、`OccurredAt`、`Payload`），另有 `Attempt`
投递次数与协议原生实体：

| 类型 | 原生字段 | 手动操作用得上它 |
| --- | --- | --- |
| `KafkaMessage` | `Record *kgo.Record` | 分区、位点、消息头；`CommitRecords` 的入参 |
| `AMQPMessage` | `Delivery amqp091.Delivery` | `DeliveryTag`（`Ack`/`Nack` 的入参）、`Redelivered` |
| `RocketMQMessage` | `Message *rocketmq.Message` | broker 消息 ID、keys、`DeliveryAttempt` |
| `QueuesMessage` | `Message *queues.Message` | 队列 ID、`RetryNum`、时间戳 |

原生实体就是"有时候要自己动 MQ"的那个入口：手动确认、读 offset 记日志、按
`DeliveryTag` 做批量 ack，都不需要再绕一层。

四种 handler 的签名一致：`func(ctx context.Context, cfg XxxConfig, msg XxxMessage) error`。
`cfg` 是收到这条消息的订阅本身——topic、group、queue、幂等开关——按值传入，因此同一个
handler 可以被多个订阅复用，靠参数区分，而不是靠闭包去捕获消费者。

## 信封

默认契约是 JSON：

```json
{"id":"evt-1","type":"order.cancelled","occurred_at":"2026-01-01T00:00:00Z",
 "payload":{"order_id":1,"user_id":7}}
```

`id` 缺失时回退到传输层消息 ID（重投之间稳定）：Kafka 用位点、RocketMQ 用 broker 消息 ID、
Redis 队列用队列 ID。AMQP 只有投递带 `MessageId`（或 `message_id` 头 / `X-Correlation-Id`）
时才稳定；都没有时每次投递都会生成随机 ID，去重实际失效——消费者在 `Idempotency=true`
时每个队列只 Warn 一次。需要去重的生产端必须设置 `MessageId`（延迟队列经死信转发仍保留它）。

`payload` 缺失时给 `{}`，`type` 必填。消息体解不出或没有 `type` 都算永久失败——它不会在
第五次投递时变成合法 JSON。

## 幂等台账

`worker_consumed_messages` 每个 `(consumer, message_id)` 一行，唯一索引就是全部机制：
认领是一条 `INSERT ... ON CONFLICT DO NOTHING`，抢到行才处理，没有锁，也没有两个副本
之间的读改写窗口。

| 场景 | 结果 |
| --- | --- |
| 首次投递 | 插入 `processing`（attempts=1），处理 |
| 重投且已 `done` / `dead` | 跳过，确认（终态不可重开） |
| 重投且仍在处理中、租约有效 | **不确认**，返回 `ErrInFlight`，由 broker 稍后重投 |
| 重投且 `processing` 租约已过期 | `attempts + 1` 接管，处理 |
| 重投且曾 `failed` | `attempts + 1` 重试；达到 `max_attempts` 记 `dead` 并跳过 |
| 数据库不可用 | 按可重试错误上报，让 broker 重投，不丢消息 |

「处理中」不能当成「可以重跑」：第一个 handler 可能还活着，确认掉重投的副本就等于在它
崩溃时丢掉唯一的备份。`ErrInFlight` 就是为此存在的可重试错误。Redis 队列会识别这个标记：
按 `ProcessingLease`（默认 5 分钟）延后重新入队，`RetryNum` 保持不变，`MaxRetries` 与
`DiscardOnError` 都不生效；其它 broker 依赖自身的重投机制。

`ProcessingLease`（默认 5 分钟，`ProcessorConfig.ProcessingLease`）决定一条 `processing`
记录多久后允许接管。它复用 `updated_at` 作为隐式租约，不新增列；接管用
`id + status + attempts` 做 CAS，两个并发重投只有一个能进入 handler，`processing` 行还在
UPDATE 里复核一次租约（读到这里之间可能已有别的投递接管并推后了时间戳）。

handler 执行期间租约会自动续期：`NewProcessor` 每 `lease/3`（下限 10ms）按
`consumer + message_id + attempts` 围栏刷新一次 `updated_at`，`ErrStaleClaim` 时停止；
handler 返回时取消并 join 续期协程，panic 也不会泄漏。因此 `ProcessingLease` 不再需要覆盖
最慢 handler 的完整耗时，但**必须不小于 broker 的重投间隔**，且 handler 仍须幂等：
续期失败（例如数据库短暂不可用）后其它节点仍可能接管。

**租约的写入与比较都用数据库时钟。** `updated_at` 由数据库时钟写入（MySQL 用
`CURRENT_TIMESTAMP(3)` 保留毫秒精度，PostgreSQL / SQLite 用 `CURRENT_TIMESTAMP`），过期判断也
在 SQL 里用数据库时钟做（MySQL / PostgreSQL / SQLite 各一种写法，见
`elapsedPredicate`）。这样做的原因很直接：只把比较换成 `NOW()` 而写入仍用应用时钟，
等于把偏差从"比较"挪到"写入"，列里存的还是那台机器的时钟。两边同源之后，节点时钟偏差不再
影响接管时机——时钟快的节点不能提前接管（那会让两个 handler 同时跑），时钟慢的节点也不会
让消息被一直占着。

未知方言退回本节点时钟比较，此时必须保证所有节点 NTP 同步。

所有写回（`succeed` / `fail` / `park`）都带本次认领的 `attempts` 做围栏，且 `fail` /
`park` 只接受 `processing` / `failed`：迟到的旧 handler 既不能把新一轮认领标成完成，
也不能把已 `done` 的消息改回 `failed` / `dead`。冲突时返回 `ErrStaleClaim`，消息仍然确认
（副作用已经发生），只在日志里留下一条 warn。

写回失败的处理因状态而异：`fail` / `park`（dead）写失败按可重试错误处理，消息不确认；
`succeed` 的 `done` 写失败只 Warn，ACK 仍跟随 handler 结果——副作用已经发生，撤回确认也
撤销不了它。`ProcessorConfig.OnLedgerError(messageID, err)` 与 Warn 同时触发，用来接指标或
告警。因此 done 路径依赖业务幂等，只有 `dead` 台账保证落库。

### 台账清理

`Processor.Prune(ctx, olderThan)` 删除窗口之外、状态为 `done` / `dead` 的行，`processing`
与 `failed` 永不删除。窗口按数据库时钟比较（未知方言回退本节点时钟），非正值返回
`ErrInvalidPruneWindow`。清理不自动调度：由部署方按日或按周调用（维护脚本或 worker），
`olderThan` 应大于最长重投窗口与排查所需时间。

表在消费者首次启动时创建（先 `HasTable` 再 `AutoMigrate`，避免多副本同时建表撞 1050）。
schema 变更走发布流程的环境，用 `migrations/0003_worker_consumed_messages.sql` 里的同一份
DDL（`examples/demo-service` 提供）。

## 错误分类

| handler 返回 | 处理 | 效果 |
| --- | --- | --- |
| `nil` | 台账 `done`（带 attempts 围栏），确认 | 分区继续推进 |
| `consumer.Permanent(err)` | 台账 `dead`，确认 | 毒消息不再重投；dead 写失败则按可重试错误处理，不确认 |
| 其它 `error` | 台账 `failed`，不确认 | broker 重投，直到 `max_attempts` |
| 另一实例仍在处理 | 不改台账，返回 `ErrInFlight` | broker 按 `InFlightDelay` 延后重投，副本不丢，也不耗普通重试预算 |

`Permanent(nil)` 返回 `nil`，可以直接写 `return consumer.Permanent(check())`。

`ErrInFlight` 走的是每个传输各自的延后通道，`DiscardOnError` 对它一律不生效：

| 传输 | 延后方式 |
| --- | --- |
| Redis 队列 / 延迟队列 | `Retry` 写回 ZSET，`RetryNum` 不变 |
| AMQP | 保持未 ack，`InFlightDelay` 后 `Nack` 回队 |
| Kafka | 提交之前的位点，回退到该记录，等待后重新拉取；不退出消费组 |
| RocketMQ | 不 ack，用 `ChangeInvisibleDuration` 延长不可见时间 |

`InFlightDelay` 由消费者适配器统一设为 `DefaultProcessingLease`（5 分钟），因此副本
不会在原 handler 仍持有租约时提前重投。

## 只跑其中一个

```bash
demo-service worker --group=group1 --list
demo-service worker --group=group1 --name=order-queues-consumer
demo-service worker --group=group1            # 整组，四个 broker 都要在
```

分组里注册了哪些消费者，整组启动就需要哪些 broker。只想跑其中一个，用 `--name`。
