# worker

本目录演示 gofy 的常驻任务（worker）：一个进程跑一组 worker，分组就是部署单位。
与另外两种形态的区别只有边界：

| 形态 | 入口 | 生命周期 | 典型用途 |
| --- | --- | --- | --- |
| HTTP | `demo-service start` | 常驻，监听端口，写 pid | 对外接口 |
| script | `demo-service script <name>` | 跑完即退，不写 pid | 数据修复、一次性迁移 |
| worker | `demo-service worker --group=<g>` | 常驻，不监听业务端口 | 扫表、MQ 消费、定时汇总 |

worker 进程没有 HTTP 栈，也不注册路由。轮询类任务通过框架注入的 `Runtime` 拿
logger 与数据库，MQ 消费者则在 `Run` 里按配置自建连接——worker 进程不运行 `app/appio`
模块，因此没有现成的连接句柄可以引用，这一点与 HTTP 进程不同。

## 配置与 Nacos

- **启动时拉一次**：worker 启动会从 Nacos `Sync` 一次配置到 `config.yaml`，保证新起的
  进程不会用镜像里那份可能已过期的配置。`Sync` 失败按启动错误处理（直接退出），
  不静默回落到旧文件。
- **运行期不监听**：worker 不挂 `nacosWatchModule`，因此 `config_change_strategy` 的
  `reload` / `restart` / `exit` 三种策略对它都不生效。进程一旦启动配置就固定下来，
  避免消费位点、分区分配与在途消息被中途换配置搅乱。
- **配置变更靠滚动重启**：由部署层重建进程（k8s 滚动更新 / 容器重新拉起 /
  systemd 重启）；停机走 SIGTERM + `Runner` drain，`workerHealth` 会先返回 503 摘流。
  这是「进程不可变、配置变更等于发布一个新版本」的惯例，也避免同一批实例配置漂移。
- `worker --list` / `--help` 不访问 Nacos，配置中心不可用时仍能查分组清单。

## 目录

```
worker/
├── worker.go              # Register：构造全部分组，交给 main
├── connections.go         # ConnectionsFrom：把 config.yaml 的连接段转成实例名表
├── group1/
│   ├── abc_register.go    # group1 的 NewGroup：装配扫描、三个生产者、十个消费者与心跳
│   ├── scan.go            # order-scan：扫表 + 它的 Defaults
│   ├── heartbeat.go       # order-heartbeat：存活心跳
│   ├── handler_order.go   # 业务分支：order.paid / order.cancelled
│   ├── producer.go        # order-event-producer：每 5s 群发同一订单事件到五个 sink
│   ├── delay_producer.go  # order-delay-producer：每 10s 调度一条 5s 后到期的事件
│   ├── rocketmq_batch_producer.go # order-batch-producer：每 10s 发十条给批量订阅
│   ├── kafka_order.go     # order-kafka-consumer：一个订阅 + 它的配置
│   ├── kafka_manual_order.go # order-kafka-manual-consumer：手动提交位点样例
│   ├── amqp_order.go      # order-amqp-consumer
│   ├── rocketmq_order.go  # order-rocketmq-consumer
│   ├── rocketmq_batch_order.go # order-rocketmq-batch-consumer：一次拉十条，逐条确认
│   ├── queues_order.go    # order-queues-consumer（Redis 队列）
│   ├── delay_queues_order.go    # order-delay-queues-consumer（Redis ZSET）
│   ├── rocketmq_delay_order.go  # order-rocketmq-delay-consumer（DELAY topic）
│   ├── delay_amqp_direct_order.go # order-amqp-delay-consumer（发布端定延迟 + 死信，direct）
│   └── delay_amqp_topic_order.go  # order-amqp-delay-topic-consumer（同上，topic）
└── group2/
    ├── abc_register.go    # group2 的 NewGroup
    └── summary.go         # report-summary：小时级汇总 + 它的 Defaults
```

没有 `worker.yaml`：每个订阅的设置就在它自己的文件里，和 `scanDefaults` 一样是代码。
这是刻意的——配置文件那种"一个 broker 一个块"的形状，一个进程只能描述一个 Kafka 订阅，
而实际服务往往要读好几条事件流。多一条 stream 就是多一个文件。

消费者背后的信封、幂等台账、错误分类与四种 broker 的构造器都在框架的
[`support/consumer`](../../../support/consumer/README.md)。它不认识订单业务，只认
`Message`、`Envelope` 与一个 handler，所以该留在框架里——放进示例，第二个项目就得复制
一遍。示例只留"一个订阅一个文件"和业务分支，这也是照着抄的时候该抄的部分。

`abc_` 前缀只是为了让入口文件排在成员文件之前，没有语义；下划线开头不行，Go 会忽略
`_register.go`，`NewGroup` 会跟着从编译里消失。

每个分组自成一个包，参数、构造、成员都在包内。给 group1 加一个 worker，不需要
碰 group2 的任何文件。

## 两个分组各做什么

| 分组 | worker | 实例数 | 节奏 | 说明 |
| --- | --- | --- | --- | --- |
| group1 | `order-scan` | 2 | 5s | 统计待支付订单；真实队列在这里调 `claim.Queue.Claim` |
| group1 | `order-event-producer` | 1 | 5s | 把同一个订单事件（order_id=1）群发到五个 sink（Kafka 两个 topic），闭环演示生产 → 消费 |
| group1 | `order-delay-producer` | 1 | 10s | 每 10s 调度一条 5s 后到期的事件到 Redis ZSET、RocketMQ DELAY topic 与 RabbitMQ direct / topic 延迟队列 |
| group1 | `order-batch-producer` | 1 | 10s | 每 10s 给批量订阅发十条，让一次拉取真的能拿回一批 |
| group1 | `order-kafka-consumer` | 1 | 常驻 | Kafka 订阅 `demo.order.events`，默认手动提交位点 |
| group1 | `order-kafka-manual-consumer` | 1 | 常驻 | 独立 topic / group 的手动提交样例，逐条打印位点提交决策 |
| group1 | `order-amqp-consumer` | 1 | 常驻 | RabbitMQ 队列，prefetch 32 |
| group1 | `order-rocketmq-consumer` | 1 | 常驻 | RocketMQ 订阅 `test-demo` / `tag=demo`，SDK 侧拉取 |
| group1 | `order-rocketmq-batch-consumer` | 1 | 常驻 | 同一 topic 的 `tag=batch`，`batch_size=10` 一次拉十条、逐条确认 |
| group1 | `order-queues-consumer` | 2 | 常驻 | Redis 队列，弹出即删除 |
| group1 | `order-delay-queues-consumer` | 1 | 常驻 | Redis ZSET 到期消息，Lua 原子取出 |
| group1 | `order-rocketmq-delay-consumer` | 1 | 常驻 | `test-demo-delay` / `tag=delay`，broker 定时投递 |
| group1 | `order-amqp-delay-consumer` | 1 | 常驻 | RabbitMQ direct 延迟：发布端给 `Delay` → 暂存队列 → 死信到目标队列 |
| group1 | `order-amqp-delay-topic-consumer` | 1 | 常驻 | RabbitMQ topic 延迟：两半都是 `topic` + `order.*`，死信保留原 routing key |
| group1 | `order-heartbeat` | 1 | 10s | 不依赖数据库，留一行可 grep 的存活日志 |
| group2 | `report-summary` | 1 | 1h | 按状态聚合订单，多实例会重复写同一份报表 |

拆两个分组不是为了好看：group1 是秒级、随订单量扩缩；group2 是小时级、只要一份。
两者的副本数、发布节奏、资源画像都不同，混在一个进程里只能取交集。

十个消费者总是注册进 group1，`--list` 一定列出它们——"一个订阅一个文件"就是这个意思。
实际跑哪些用 `--name` 选；注册不建连，所以 `--list` 和被过滤掉的消费者都不产生成本。
实例数写在各自的配置文件里，`--concurrency` 仍可覆盖。

## 生产-消费闭环

`producer.go` 是示例的生产端：一个 worker，每 5 秒生成一条指向订单 1 的
`order.paid` 事件，同一时刻发到四个传输层的**五个 sink**（Kafka 占两个 topic：默认订阅与
手动提交样例），正是 group1 立即订阅消费的那些流。payload 里的 `reason` 写明这条事件来自
哪个 sink（`记录来自 kafka 队列`……），`docker logs` 里对同一个 `event_id` 能看到五条
`message consumed`，各带一个 `consumer` / `source`。

契约在消费者那边：topic、exchange、绑定键、tag 都从订阅配置读，生产者不重复声明，
因此移动订阅等于移动生产端。传输地址来自 `ConnectionsFrom` 的同一张连接表，一个
地址只有一个家。发送各带各的关键字：Kafka 用订单号做 key 保分区有序、`message_id`
头带事件 UUID 供去重；AMQP 用事件类型做 routing key 过 `order.*` 绑定；RocketMQ
带订阅的首个 tag；Redis 队列直接压栈。五个 sink 一次循环打开、逆序关闭，任一打不开
就拒绝启动——少一条腿的群发示例比没有示例更糟。

## Kafka 手动提交

`kafka_manual_order.go` 是 `order-kafka-consumer` 的手动提交对照订阅：独立 topic
`demo.order.manual`、独立消费组 `demo-service-order-manual-consumer`，位点可以单独查看，
不会混进默认订阅的组。`producer.go` 的第五个 sink 每 5 秒喂它一条正常的 `order.paid`。

本地 Kafka 关了自动建 topic，第一次跑之前先建一次（1 个分区便于观察位点）：

```bash
docker exec dev-kafka /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 --create \
  --topic demo.order.manual --partitions 1 --replication-factor 1
```

缺这个 topic 时生产者会报 `UNKNOWN_TOPIC_OR_PARTITION`，而注册与 `worker --list` 都不会
提示——订阅只是代码里的声明，topic 是 broker 侧的资源。默认订阅用的 `demo.order.events`
同理需要存在。

**先澄清"手动"**：gofy 里不是让 handler 自己调 `CommitRecords`。`CommitMode: "manual"`
让传输层给 franz-go 加 `DisableAutoCommit()`，然后在**每个分区的一批处理完后**，提交
handler 接受的最后一条记录。handler 的返回值就是提交决策：

| handler 返回 | 共用链路 | 位点 | 日志 |
| --- | --- | --- | --- |
| `nil` | `message consumed` | 提交 | `decision=commit this offset` |
| 可重试错误 | `message handling failed, waiting for redelivery` | **不提交**；`Consume` 停止、worker 重启，失败记录及其后的记录重投 | `decision=hold offset, redeliver` |
| `Permanent(err)` | 记为 dead 但仍确认 | 提交，毒消息不会永久卡住分区 | `decision=park as dead, commit anyway` |

因此 handler **绝不能自己提交**：那会提交共用链路尚未处理完的位点，之后遇到
`Permanent` 或 panic 就可能丢消息。`handleOrderKafkaManual` 只做两件事——调
`applyOrderEvent`、把决策连同 `msg.Record` 的 `partition` / `offset` 打进一行日志——
然后**原样返回 err**。吞掉错误等于把"保留位点"改成"提交位点"。

配置上的几个刻意选择：

- `Idempotency: true` + `MaxAttempts: 3`：Kafka 不报告投递次数，靠台账计次。第 3 次
  认领会把消息记为 dead 并确认（`message skipped`），**分区得以继续**——否则一条坏消息
  会无限重投。生产环境用默认 5 或更大。
- `DiscardOnError: false`：打开它会把失败记录的位点也提交，正是本样例要对照的行为。
- `BlockRebalanceOnPoll: true`：把"先 handler 后提交"这一批锁在一起，避免重平衡落在
  两者之间导致提交失败、整批重放。

看位点是否真的前进（本地容器）：

```bash
docker exec dev-kafka /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server localhost:9092 \
  --describe --group demo-service-order-manual-consumer
```

想看"不提交 → 重投"，往这个 topic 发一条订单不存在的事件（`applyOrderEvent` 会返回
可重试错误）。手工消息没有 `message_id` 头，去重键回退到 `topic/partition@offset`，
重投时位点不变，仍能正确计次：

```bash
docker exec -i dev-kafka /opt/kafka/bin/kafka-console-producer.sh \
  --bootstrap-server localhost:9092 --topic demo.order.manual <<'EOF'
{"id":"manual-retry-1","type":"order.paid","occurred_at":"2026-11-15T12:00:00+08:00","payload":{"order_id":999999,"user_id":1,"reason":"manual commit demo"}}
EOF
```

日志里同一个 `offset` 会出现三次 `decision=hold offset, redeliver`（`MaxAttempts: 3`），
第四次变成 `message skipped`（attempts exhausted），随后位点提交、分区继续——这也说明
为什么可重试失败必须配 `Idempotency`，否则 Kafka 没有投递次数概念，会无限重投。把 `type`
改成 `order.unknown` 则走永久错误分支：`message discarded: permanent failure` +
`decision=park as dead, commit anyway`，位点同样提交。

## RocketMQ 批量消费

`rocketmq_batch_order.go` 与 `rocketmq_order.go` 是同一 topic 上的两个订阅，差别只在
拉取参数：前者 `batch_size=10`，后者默认 1。RocketMQ v5 没有 push 消费者，gofy 的
handler 又是单条的，所以「批量」完全在拉取侧：

| 环节 | 行为 | 代码位置 |
| --- | --- | --- |
| 拉取 | 一次 `Receive(batch_size, invisible_duration)` 要回至多 10 条 | `support/rocketmq/consumer.go` 的 `poll` |
| 处理 | 一个 worker 按顺序逐条调 handler，**没有批量 handler** | 同文件 `process` |
| 确认 | 每条成功各自 `Ack`，失败的那条不 ack | 同文件 `ack` |
| 重投 | 未确认的消息在 `InvisibleDuration` 后重新可见，`DeliveryAttempt` +1 | 同文件 `process` |
| 并发 | `PullWorkers > 1` 时多个 goroutine 各自拉一批，批次之间交错 | 同文件 `Consume` |

一句话：**批量拉取、逐条确认**。这条设计带来两个结果：

- **失败隔离**。批次里一条失败只让它自己重投，其余九条照常 ack——不像 Kafka 手动提交
  那样把失败位点之后的记录一起留下。代价是 handler 看不到批次边界，没法把十条合并成
  一次 `UPDATE` 或一个事务。
- **顺序只在单 worker 内成立**。样例把 `PullWorkers` 设成 1，一批内的消息按到达顺序逐条
  处理；调大之后批次会被不同 goroutine 拿走，跨批次没有顺序保证。另外位点是**队列内**的
  编号，一批通常跨多个队列（`test-demo` 有多个队列），所以日志里的 `offset` 不会连续——
  判断"是不是一批"看的是处理时间与 `queued`（消息在 broker 里等了多久），不是位点。

为什么不能"整批 ack"：v5.1.2 的 `SimpleConsumer` 只暴露单条 `Ack(ctx, *MessageView)`，
SDK 没有批量确认接口，框架也做不出比 SDK 更强的语义。真要批量写库，得在 handler 里自己
攒窗口——那就绕开了幂等台账与逐条重投，本示例不这么做。

配套生产者 `rocketmq_batch_producer.go` 每 10 秒发十条，正好凑满一批（条数不超过
`BatchSize`，否则日志里的批次会混进上一批的尾巴）。每条消息用自己的信封 ID：台账按
`(consumer, message_id)` 记账，共用一个 ID 会让九条变成 `message skipped`。

看日志确认一次拉取拿回整批（十条在同一两秒内处理完，`queued` 都在亚秒级）：

```bash
$ go run . worker --group=group1 --name=order-batch-producer --name=order-rocketmq-batch-consumer
{"msg":"order batch published","burst":10,...}
{"msg":"rocketmq batch delivery","offset":446,"delivery_attempt":1,"queued":0.588,"decision":"ack this message",...}
{"msg":"rocketmq batch delivery","offset":462,"delivery_attempt":1,"queued":0.283,"decision":"ack this message",...}
...
```

首次用一个新建的消费组启动时，可能会先看到一次 proxy 的
`INTERNAL_SERVER_ERROR ... NullPointerException`（`ReceiveMessageActivity`）：这是
broker 侧还没建好组分配。框架按 1s 退避重试，随后自愈；这批消息会带着
`delivery_attempt=2` 重投，`Idempotency: true` 保证重复处理不重复写库。

想看不确认的路径，让其中一条失败即可（例如把 `OrderID` 改成不存在的订单）：那条会打出
`decision=leave unacknowledged, redeliver after invisible_duration`，15 秒后带着
`delivery_attempt=2` 回来，同批其余九条此时早已 ack。第三次认领会把消息记为 dead
（`MaxAttempts: 3`），后续重投变成 `message skipped`，消费继续向前。

## 延迟队列

`delay_producer.go` 是立即链路的镜像：每 10 秒生成一条 `order.paid`，但四条流都要
等 5 秒才可见。payload 的 `reason` 写明来源，四个 handler 各打一行
`delayed order event arrived`，带上实测等待时间，因此一行日志就能验证延迟是否生效。

| 传输 | 生产端 | 到期机制 | 消费端 |
| --- | --- | --- | --- |
| Redis ZSET | `queues.DelayQueue.Add(ctx, event, delay)` | score 是到期时间，`Pop` 用 Lua 一次「取到期 + 删除」，多实例不重复 | `gofyconsumer.NewDelayQueues`，键 `demo:order:delay` |
| RocketMQ | `PublishJSON(..., PublishOptions{Delay: delay})` | SDK 转成绝对投递时间，broker 保管到点才投放 | `gofyconsumer.NewRocketMQ`，`test-demo-delay` / `tag=delay` |
| RabbitMQ direct | `PublishJSON(..., PublishOptions{Delay: delay})` | `Delay` 转成单条消息 TTL，暂存队列到期后由 broker 死信转发到目标队列 | `gofyconsumer.NewDelayAMQP`，读 `demo.order.delay.ready` |
| RabbitMQ topic | `PublishJSON(..., PublishOptions{Delay: delay, RoutingKey: "order.paid"})` | 同上，但死信**保留原 routing key**，由 `order.*` 绑定选中目标队列 | `gofyconsumer.NewDelayAMQP` + `PreserveRoutingKey: true`，读 `demo.order.delay.topic.ready` |

RabbitMQ 这一路没有"DELAY 类型 topic"可切，延迟在拓扑里：消息先发布到
`demo.orders.delay` / `demo.order.delay.staging`，等发布时设定的消息过期后由 broker
死信到 `demo.orders.delay.ready` / `demo.order.delay.ready`，消费者读的始终是后者。
两个 exchange 都与立即订阅的 `demo.orders` 分开，否则延迟事件也会投进
`demo.order.events`。生产端通过 `orderAMQPDelay.QueueConfig()` 拿两半拓扑，不自己
拼参数——死信参数一旦不一致，broker 会直接 `PRECONDITION_FAILED`。

**订阅里刻意不写 `TTL`**：留零即不声明 `x-message-ttl`，四条延迟流的等待时长统一
由生产端的 `Delay: 5s` 决定，与 RocketMQ、Redis 写法一致。队列 TTL 是第二套延迟
写法，broker 取两者较小值会静默截断更大的 `Delay`；漏写 `Delay` 时消息还会悄悄
等满队列 TTL 后迟到而不报错。零 TTL 模式下 `Publish` 强制要求 `Delay`
（`ErrDelayRequired`），错误在调用点就暴露。暂存队列因此叫 `staging` 而不叫 `5s`，
改名也避开了"已带 x-message-ttl 的队列不能改成不带"的参数冲突。可用
`rabbitmqctl list_queues name arguments` 核对：新暂存队列没有 `x-message-ttl`。
RabbitMQ 只在队头判定过期，所以一个暂存队列只放一种延迟。

这里的拓扑由 gofy 自己声明。若暂存队列由别的服务（例如 Java）建好、且带了
`x-message-ttl`，默认声明会因参数不一致直接 `PRECONDITION_FAILED`；给订阅配置加
`Passive: true` 即可只检查存在、不重新声明（`QueueConfig()` 会同时透传到两半拓扑）。
跨语言接入的完整说明见
[support/amqp README](../../../support/amqp/README.md#接入其它语言声明的延迟队列)。

`delay_amqp_topic_order.go` 是同一套死信流程的 topic 版本：两半都是 topic exchange，
绑定 `order.*`，暂存拓扑只写 `BindingKey`（pattern），生产端逐条给 `RoutingKey:
order.paid`（字面键）。关键是 `PreserveRoutingKey: true` —— 不开时 broker 会把死信
routing key 固定成目标拓扑那一个值，`order.paid` 与 `order.cancelled` 再也分不开，
字面的 `order.*` 甚至匹配不上具体绑定而丢消息。开启后 broker 转发原始键，handler
把它打进 `routing_key` 字段，这就是延迟链路里唯一能直接观察路由语义的一行。可用
`rabbitmqctl list_queues name arguments` 核对：topic 暂存队列没有
`x-dead-letter-routing-key`，direct 暂存队列有；两者都没有 `x-message-ttl`。

Redis 侧的失败消息由 `DelayQueue.Retry` 以新的到期时间写回（受 `MaxRetries` 限制）；
`Pop` 已经把条目删掉，所以与立即队列一样是**在途至多一次**——不能丢消息的场景请用 MQ。

**RocketMQ 的 topic 必须是 DELAY 类型**：v5 在普通 topic 上会直接拒绝定时消息，报
`current message type not match with topic accept message types`。本地 broker 建一次即可：

```bash
docker exec dev-rocketmq-broker sh -c 'export PATH=$PATH:/home/rocketmq/rocketmq-5.3.2/bin && \
  mqadmin updateTopic -n rocketmq-namesrv:9876 -t test-demo-delay \
  -c DefaultCluster -a +message.type=DELAY'
```

生产端写 `Delay` 还是 `DeliverAt` 都行：前者是「5 秒后」，后者是绝对时刻，两者同时
设置会报 `ErrInvalidDelay`。消费端 `msg.Message.DeliveryTimestamp` 与 `BornTimestamp`
可以算出计划延迟，日志里的 `scheduled` / `waited` 就是这么来的。

实跑一轮：

```
11:12:30 delayed order event scheduled  delay=5s
11:12:35 delayed order event arrived   source=delay_queues      waited≈6.0s
11:12:35 delayed order event arrived   source=amqp_delay        waited≈6.0s
11:12:35 delayed order event arrived   source=amqp_delay_topic  waited≈6.0s  routing_key=order.paid
11:12:35 delayed order event arrived   source=rocketmq_delay    scheduled=5s waited≈5.1s
```

同一个 `event_id` 四条到达，幂等台账里四个消费者各一行——延迟链路的四种实现
（客户端轮询 / broker 定时投递 / direct 死信 / topic 死信）用同一套信封、handler 与
错误分类。

## 消费者怎么接入

每个订阅一个文件：配置、handler、没有别的：

```
kafka_order.go / kafka_manual_order.go / amqp_order.go / rocketmq_order.go
queues_order.go / delay_queues_order.go / rocketmq_delay_order.go
delay_amqp_direct_order.go / delay_amqp_topic_order.go
        一个订阅一个文件：Config + Handler
        ↓
support/consumer Processor.Handle
        提取 trace → 查幂等台账 → 解信封 → 调 handler
        ↓
handler_order.go
        按 event.type 落到 orders 表
```

### 消息契约

生产者与消费者共用一个信封，`type` 决定业务分支，`payload` 由分支自己解码：

```json
{
  "id": "evt-1",
  "type": "order.paid",
  "occurred_at": "2026-01-01T00:00:00Z",
  "payload": {"order_id": 1, "user_id": 7, "reason": "记录来自 kafka 队列"}
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
| `processing`，租约未过期 | 另一个实例正在处理 | **不确认**，`ErrInFlight`，等 broker 再投 |
| `processing`，租约已过期 | 上次处理中途崩溃 | `attempts + 1` 接管 |
| `done` | 已成功 | 跳过并确认 |
| `failed` | 可重试失败 | `attempts + 1` 重试，直到 `max_attempts` |
| `dead` | 不再重试 | 跳过并确认 |

"处理中"必须当"还有人持有"，不能当"可以重跑"：确认掉重投的副本，第一个 handler 崩溃时
就没有备份了。租约默认 5 分钟，由 `ProcessorConfig.ProcessingLease` 配置，复用
`updated_at` 做判断，不新增列。

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
| `gofyconsumer.Permanent(err)` | 台账置 `dead`，确认 | 毒消息不再重投 |
| 其它 `error` | 台账置 `failed`，不确认 | broker 重投，直到 `max_attempts` |
| 另一实例仍在处理 | 台账不动，返回 `ErrInFlight` | broker 稍后重投，副本不丢 |

所有写回都带本次 `attempts`：迟到的旧 handler 无法把新一轮认领标成完成，也无法把已
`done` 的消息改回 `failed` / `dead`（返回 `ErrStaleClaim`，只留一条 warn 日志）。

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

### 新增一种消费者

已支持的四种协议，加订阅就是在分组里加一个文件；要接一种新协议（Pulsar、NATS），
第一步落在框架里：

1. 照 `support/consumer/kafka.go` 写一个文件：`XxxConfig`（配置 + 默认值 + `validate`）、
   `XxxMessage`（信封 + 原生实体）、`NewXxx(conns, cfg)` 返回实现 `Name`/`Concurrency`/`Run`
   的值，`Run` 里建连 → 装配 `Processor` → 调用 `support/worker/consumer` 的适配器；
2. 在 `support/consumer/consume.go` 的编译期断言里补两行，保证它仍然满足 `worker.Worker`；
3. 在 `group1` 里加一个文件：`var xxxConfig = gofyconsumer.XxxConfig{...}` 加业务 handler；
4. 在 `abc_register.go` 的 `NewGroup` 里 `gofyconsumer.NewXxx(conns, xxxConfig)` 追加。

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
┌────────┬─────────────────────────────────┬─────────────┐
│ GROUP  │ WORKER                          │ CONCURRENCY │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-scan                      │ 2           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-event-producer            │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-delay-producer            │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-batch-producer            │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-kafka-consumer            │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-kafka-manual-consumer     │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-amqp-consumer             │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-rocketmq-consumer         │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-rocketmq-batch-consumer   │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-queues-consumer           │ 2           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-delay-queues-consumer     │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-rocketmq-delay-consumer   │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-amqp-delay-consumer       │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-amqp-delay-topic-consumer │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group1 │ order-heartbeat                 │ 1           │
├────────┼─────────────────────────────────┼─────────────┤
│ group2 │ report-summary                  │ 1           │
└────────┴─────────────────────────────────┴─────────────┘
```

启动整组。前台运行，日志同时写 stdout 与文件：

```bash
$ go run . worker --group=group1
{"level":"info","caller":"gofy/worker_host.go:112","msg":"worker group started","group":"group1","workers":15,"drain_timeout":20}
{"level":"info","caller":"group1/scan.go:53","msg":"order scan started","worker":"group1/order-scan#0","interval":5}
{"level":"info","caller":"group1/scan.go:53","msg":"order scan started","worker":"group1/order-scan#1","interval":5}
```

`workers:15` 是这个分组的成员数（扫描 1 + 三个生产者 + 十个消费者 + 心跳 1），不是实例数；
`order-scan` 起了 2 个 goroutine，靠日志里的 `worker` 字段区分（`#0`、`#1`）。

整组启动意味着四个 broker 都要在。本地只有数据库与 Redis 时，用 `--name` 挑一个跑：

```bash
go run . worker --group=group1 --name=order-scan
go run . worker --group=group1 --name=order-queues-consumer
```

本地调试时可以用通配符把全部注册分组放进同一个进程。**写 `all`，不要写 `*`**：

```bash
$ go run . worker --group=all
{"level":"info","caller":"gofy/worker_host.go:145","msg":"worker groups started","workers":7,"groups":["group1","group2"]}
```

`--group='*'` 等价，但 `*` 在进程启动之前就被 shell 展开：zsh 直接报
`no matches found: --group=*` 并拒绝执行，bash 只是因为当前目录没有匹配项才把它原样
传下来。想要“不用引号”，就用 `all`；确实要用 `*` 时写 `--group='*'`、`--group=\*`，
或者 `noglob go run . worker --group=*`。

它只解决“一条命令起全部循环”，不改变部署语义：pid 变成 `-worker-all.pid`，
`worker stop` 一次停掉全部，任一分组失败会取消其余分组。也不能和 `--name` 同用——worker
名只属于一个分组，跨组过滤会静默跳过不含它的组。

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

### 后台运行与启停

`--daemon=1` 让 worker 在独立会话里重新执行自身，父进程打印 pid 后退出：

```bash
$ ./demo-service worker --group=all --daemon=1
[ OK ] worker 分组【all】已在后台启动 (pid 80656)
使用 worker stop --group=all 停止，worker restart --group=all 重启
```

子进程的 stdout / stderr 指向 `/dev/null`，日志照常写 `storage/log/worker/...`。脱离之前
父进程先查 pid 文件：已在运行时直接报 `another instance is running`，不会出现“父进程报
成功、子进程立刻退出”的假成功。提示里不写 `demo-service` 前缀——进程不知道自己是怎么被
启动的（`go run .`、绝对路径、PATH 上的名字都会走到这里），写出来可能是一条不存在的命令。

| 命令 | 作用 |
| --- | --- |
| `worker --group=group1 --daemon=1` | 后台运行该分组，父进程退出 |
| `worker stop` | 停掉所有留下 pid 文件的 worker 进程 |
| `worker stop --group=group1` | 只停这一组 |
| `worker restart --group=group1` | 先停再起，默认仍后台（`--daemon=false` 回前台） |

`worker restart` 不带 `--group` 时按通配符重启：一个进程托管全部分组。它默认后台，因为
重启一个原本在后台跑的服务，不该悄悄把它挂到当前终端上。

## 进程留下的东西

| 产物 | 位置 | 说明 |
| --- | --- | --- |
| pid 文件 | `app/demo-service-worker-group1.pid` | 默认 pid 文件的 `-worker-<分组>` 后缀；未配置 `pid_file` 则不写 |
| pid 文件 | `app/demo-service-worker-all.pid` | `--group=all` / `--group='*'` 时用这个后缀；`worker stop` 会连同各分组的文件一起处理 |
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
# 不带 --group：停掉所有留下 pid 文件的 worker 进程
$ go run . worker stop --force
[INFO] 正在停止 worker 分组【group1】 (pid 80656)
[ OK ] worker 分组【group1】 已停止 (pid 80656)
[INFO] 正在停止 worker 分组【group2】 (pid 80657)
[ OK ] worker 分组【group2】 已停止 (pid 80657)

# 只停一组
$ go run . worker stop --group=group1 --force
[INFO] 正在停止 worker 分组【group1】 (pid 80656)
[ OK ] worker 分组【group1】 已停止 (pid 80656)
```

不带 `--group`（或写 `--group=*` / `--group=all`）时，`worker stop` 扫
`app/demo-service-worker-*.pid`，把每个活着的 worker 进程都停掉——分组可能是分别起的，
只停 `-worker-all.pid` 会漏掉其余的。`stop --worker=group1` 仍然可用，与
`worker stop --group=group1` 等价，区别只是 `worker stop` 与启动命令同形。

不带 `--force` 会先问一次 `你确定 [停止] worker 分组【group1】? (yes or no):`。
停止走 SIGTERM：worker 的拉取先停（`runCtx` 取消），在途任务继续跑到
`drain_timeout`（默认 20s，`gofy.WithWorkerDrainTimeout` 可改），然后进程退出，pid
文件清理。超时未退才在 `--force` 下补 SIGKILL。

`stop` 的粒度是分组，`worker stop --group=group1` 不会碰到 group2，也不会碰 HTTP 进程。
`worker restart` 先停再起（默认后台，`--daemon=false` 回前台）；HTTP 实例仍然用 `restart`。

## 参数

参数只有一个来源：写在使用它的那个文件里。

| 参数 | 放在哪 | 理由 |
| --- | --- | --- |
| 扫描节奏、扫描实例数 | `scan.go` 的 `scanDefaults` | 属于实现细节，改它意味着改逻辑 |
| topic、消费组、并发、重试预算 | 各消费者文件的 `gofyconsumer.XxxConfig` | 一个订阅一个文件，才写得下多条事件流 |
| broker 地址、账号、超时 | `config.yaml` | HTTP 进程也要用，只配一次 |

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

消费者是同一个形状，只是配置结构由框架提供：

```go
// group1/kafka_order.go
var orderKafka = gofyconsumer.KafkaConfig{
	Name:     "order-kafka-consumer",
	Instance: "default",   // config.yaml 里 kafka 段的键名，地址在那边
	Topic:    "demo.order.events",
	Group:    "demo-service-order-consumer",
	Concurrency: 1,
	Idempotency: true,
	Handler:     handleOrderKafka,
}
```

零值就是默认值，只写让它与众不同的字段（默认值表见
[`support/consumer/README.md`](../../../support/consumer/README.md)）。要读第二条事件流？
再开一个文件，不要在共享块里加字段。

`Instance` / `RedisInstance` 只写实例名，broker 地址在 `config.yaml` 的 `kafka` / `amqp` /
`rocketmq` / `redis` 段配一次。四个订阅都把它显式写出来：接一个新环境时，这是唯一必须改
的一行，而 `default` / `ratelimit` 这两个默认值只在漏写时兜底。写错了会在 `Register` 阶段
报错，不会静默跑一个什么都不消费的 worker。

四条约束：

- **实例数是 `uint8`**：`NewXxx` 校验时挡在 `1..255`，越界直接报错，不会静默改成 1。
- **`--concurrency` 优先级最高**：命令行覆盖配置里写的数字，临时压测不必改代码。
- **worker 名全应用唯一**：日志文件名取自 worker 名，两个分组用同一个名字会写到同一个
  文件（demo 里是 `order-scan`、`order-kafka-consumer`、`report-summary` 等）。
- **错误在启动前失败**：`NewXxx` 校验配置并解析实例名，发生在写 pid 文件与建连之前。

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
| 启动即报 `kafka instance "x" is not configured` | `Instance` 写了 config.yaml 里没有的实例名 | 改 `Instance`，或在 `config.yaml` 里补上该连接段 |
| 启动即报 `kafka consumer "x": topic is required` | 订阅文件里没填必填项 | 报错里带消费者名与字段名，补在对应文件里 |
| 日志出现 `consume claim failed` | 幂等台账不可用（库连不上、表被锁） | 先看数据库；台账不可用时不确认消息，broker 会重投 |
| 同一条消息反复处理 | handler 返回可重试错误 | 看台账 `last_error`；`max_attempts` 用完后自动转 `dead` |
| 日志出现 `message skipped` | 幂等命中或尝试次数用完 | 正常行为；查台账 `status` 区分是哪一种 |
| 日志出现 `message discarded: permanent failure` | 未知 `event.type` 或消息体解不出 | 台账 `status=dead`，按 `last_error` 修生产端或补分支 |

## 相关文档

- `docs/worker-guide.md`：四种循环（claim / MQ / Redis 队列 / lease）的写法与部署建议
- `docs/worker-daemon-design.md`：设计与验收记录
- `support/worker/README.md`：包 API、`Policy` 重启策略、drain 语义
- `support/consumer/README.md`：四种消费者的配置、原生实体、幂等与错误分类
- `examples/demo-service/storage/log/README.md`：日志目录与文件命名规则
