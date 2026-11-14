# support/consumer

常驻消费者的完整实现：**一个订阅一个文件**，文件里是配置、业务 handler，没有别的。
每种 broker 一个构造器（`NewKafka` / `NewAMQP` / `NewRocketMQ` / `NewQueues`），
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

| 字段 | Kafka | AMQP | RocketMQ | Queues | DelayQueues |
| --- | --- | --- | --- | --- | --- |
| `Name`（必填） | ✓ | ✓ | ✓ | ✓ | ✓ |
| `Concurrency` | 1 | 1 | 1 | 1 | 1 |
| `Idempotency` | false | false | false | false | false |
| `MaxAttempts` | 5 | 5 | 5 | 5 | 5 |
| 连接实例 | `Instance`=default | `Instance`=default | `Instance`=default | `RedisInstance`=ratelimit | `RedisInstance`=ratelimit |
| 订阅目标 | `Topic`+`Group`（必填） | `Exchange`+`Queue`（必填） | `Topic`+`Group`（必填） | `Key`（必填） | `Key`（必填，延迟 ZSET） |
| 协议特有 | `CommitMode`=manual、`AutoOffsetReset`=earliest、`AutoCommitInterval`=200ms、`SessionTimeout`=45s、`BlockRebalanceOnPoll`=false | `ExchangeType`=direct、`PrefetchCount`=32、`NonDurable`=false | `Tags`、`PullWorkers`=1、`BatchSize`=16、`AwaitDuration`=5s | `MaxRetries`=3、`IdleDelay`=200ms、`RetryDelay`=3s | `MaxRetries`=3、`PopLimit`=10、`IdleDelay`=200ms、`RetryDelay`=3s |

`DelayQueues` 由 `NewDelayQueues` 构造，订阅的是 Redis ZSET 里**已到期**的消息：生产端用
`queues.DelayQueue.Add(ctx, payload, delay)`，消费端轮询 `Pop`，失败由 `Retry` 以新到期
时间写回。`source` 是 `delay_queues`，日志与 trace 因此能与立即队列区分。它与 `Queues`
共用同一个 worker 适配器（两者 `Consume` 签名相同），在途消息同样是"至多一次"。

`Idempotency` 需要数据库，因此默认关闭：打开它就从"handler 必须幂等"变成"重投是空操作"。
`MaxAttempts` 只在开启幂等时生效——没有台账就没有地方记尝试次数。

AMQP 的持久化用反向字段 `NonDurable`：普通 bool 的零值会让"重启即丢队列"变成默认，
那不是业务队列该有的默认。

Kafka 没有 `max.poll.interval.ms` 可调：franz-go 的心跳在独立 goroutine 上，慢 handler 不会
把成员踢出组。想避免重平衡落在 handler 与提交之间，用 `BlockRebalanceOnPoll: true`——当前
批次跑完、位点提交后才放行。`AutoCommitInterval` 只在 `CommitMode: auto` 下生效。

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

`id` 缺失时回退到传输层消息 ID（重投之间稳定），`payload` 缺失时给 `{}`，`type` 必填。
消息体解不出或没有 `type` 都算永久失败——它不会在第五次投递时变成合法 JSON。

## 幂等台账

`worker_consumed_messages` 每个 `(consumer, message_id)` 一行，唯一索引就是全部机制：
认领是一条 `INSERT ... ON CONFLICT DO NOTHING`，抢到行才处理，没有锁，也没有两个副本
之间的读改写窗口。

| 场景 | 结果 |
| --- | --- |
| 首次投递 | 插入成功，处理 |
| 重投且已 `done` | 跳过，确认 |
| 重投且曾失败 | `attempts + 1`；达到 `max_attempts` 记 `dead` 并跳过 |
| 数据库不可用 | 按可重试错误上报，让 broker 重投，不丢消息 |

表在消费者首次启动时创建（先 `HasTable` 再 `AutoMigrate`，避免多副本同时建表撞 1050）。
schema 变更走发布流程的环境，用 `migrations/0003_worker_consumed_messages.sql` 里的同一份
DDL（`examples/demo-service` 提供）。

## 错误分类

| handler 返回 | 处理 | 效果 |
| --- | --- | --- |
| `nil` | 台账 `done`，确认 | 分区继续推进 |
| `consumer.Permanent(err)` | 台账 `dead`，确认 | 毒消息不再重投 |
| 其它 `error` | 台账 `failed`，不确认 | broker 重投，直到 `max_attempts` |

`Permanent(nil)` 返回 `nil`，可以直接写 `return consumer.Permanent(check())`。

## 只跑其中一个

```bash
demo-service worker --group=group1 --list
demo-service worker --group=group1 --name=order-queues-consumer
demo-service worker --group=group1            # 整组，四个 broker 都要在
```

分组里注册了哪些消费者，整组启动就需要哪些 broker。只想跑其中一个，用 `--name`。
