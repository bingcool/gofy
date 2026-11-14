# support/consumer

MQ 消费语义层：在 [`support/worker/consumer/*`](../worker/README.md) 的协议适配器之上，
补上每个项目都会重写一遍的四件事——**配置块**、**消息信封**、**幂等台账**、
**永久/可重试错误分类**。

适配器只把 Kafka 记录、AMQP 投递、RocketMQ 消息或 Redis 队列项变成一条 `Message`；
`Processor.Handle` 负责之后的一切：接续 trace → 认领幂等行 → 解信封 → 调 handler → 记结果。

本包不建连接，也不拥有进程生命周期：连接由业务持有（`Connections` 只是按实例名解析配置），
goroutine 由 `support/worker` 管理。

| | 负责 | 不负责 |
| --- | --- | --- |
| 本包 | 配置结构与校验、信封解码、幂等台账、错误分类、trace 续接、结构化日志 | 建连、拉取循环、停机、重启策略 |
| `support/worker/consumer/*` | 把协议客户端接到 worker 生命周期 | 业务语义 |
| 业务 | `HandlerFunc`：一条事件该改哪张表 | —— |

## 配置块

```yaml
consumers:
  kafka:
    enabled: true
    instance: default          # 复用应用已有的 kafka.default 连接参数
    concurrency: 1
    topic: demo.order.events
    group: demo-service-order-consumer
    commit_mode: manual
    idempotency: true
    max_attempts: 5
```

`Config` 带 `default` 标签，用 `config.Load` 解码时自动生效；`Validate` 在解码过程中执行，
所以缺 `topic` 会在连接池和日志文件出现之前失败。报错前缀是字段路径（`consumers.kafka`），
且检查顺序固定——同一个文件每次启动给同一条报错。

四个块彼此独立：打开 Kafka 不会顺带启动 AMQP；未启用的块既不建连，也不会要求填 topic。

## 接入一条消费链路

```go
proc, err := consumer.NewProcessor(rt, consumer.ProcessorConfig{
	Consumer:    "order-consumer", // 幂等台账的作用域，通常等于 worker 名
	Handler:     handleOrderEvent,
	Idempotency: cfg.Consumers.Kafka.Idempotency,
	MaxAttempts: cfg.Consumers.Kafka.MaxAttempts,
})
if err != nil {
	return err
}

w, err := kafkaconsumer.New("order-consumer", kc, kafkaconsumer.Config{
	Handler: func(ctx context.Context, record *kgo.Record) error {
		return proc.Handle(ctx, consumer.Message{
			ID:      messageID(record), // 重投之间稳定的值，缺了就放弃去重
			Source:  "kafka",
			Topic:   record.Topic,
			Body:    record.Value,
			Attempt: 1,
			Headers: headers(record),   // traceparent 从这里续接
		})
	},
})
```

`Handle` 的返回值就是确认决定：`nil` 确认，非 nil 请 broker 重投。

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

## 关闭幂等

`idempotency: false` 时不建表、不查库，链路退化成"解码 + 调 handler + 记日志"。适合
在别处已经去重的项目；此时同一事件重投会重复执行 handler，handler 必须自己幂等。
