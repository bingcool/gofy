package group1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyqueues "github.com/bingcool/gofy/support/queues"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// OrderEventProducerConfig controls the demo's producer half: what to send, to
// which order, and how often.
//
// It lives here for the same reason ScanWorkerConfig does — the numbers belong
// to the loop they drive, not to config.yaml. Transport addresses are not here:
// they come from the same connection table the consumers use, so an address
// still has exactly one home.
// OrderEventProducerConfig 控制示例的生产端：发什么、对哪个订单、多久一次。
//
// 放在这里的原因与 ScanWorkerConfig 相同——数字属于它驱动的循环，不属于
// config.yaml。这里没有传输层地址：地址来自消费者用的同一张连接表，因此一个地址
// 仍然只有一个家。
type OrderEventProducerConfig struct {
	// Concurrency is how many producer instances run inside the worker process.
	// Keep it at 1 unless duplicate events are what you want to demo.
	// Concurrency 是 worker 进程内运行的生产者实例数。除非想演示重复事件，否则保持 1。
	Concurrency int
	// IntervalMS is the gap between two fan-outs, in milliseconds.
	// IntervalMS 是两次群发之间的间隔，单位毫秒。
	IntervalMS uint32
	// OrderID is the order every event points at.
	// OrderID 是每个事件指向的订单。
	OrderID uint64
	// UserID travels in the payload for audit; the update itself ignores it.
	// UserID 随 payload 一起走，供审计使用；更新本身并不需要它。
	UserID uint64
	// EventType selects the consumer branch, so it must be one applyOrderEvent
	// knows: order.paid or order.cancelled.
	// EventType 决定消费端的分支，因此必须是 applyOrderEvent 认识的值：
	// order.paid 或 order.cancelled。
	EventType string
}

// orderEventProducerDefaults is what group1's producer runs with: one event per
// transport every five seconds, all pointing at order 1.
// orderEventProducerDefaults 是 group1 生产者的运行参数：每五秒给每个传输发一条
// 事件，全部指向订单 1。
var orderEventProducerDefaults = OrderEventProducerConfig{
	Concurrency: 1,
	IntervalMS:  5000,
	OrderID:     1,
	UserID:      1,
	EventType:   "order.paid",
}

// publishTimeout bounds one fan-out. It is per tick, not per send: brokers
// that are all slow should fail the tick, not stretch it to twenty seconds.
// publishTimeout 限制一次群发的耗时。它是每 tick 而不是每次发送：所有 broker 都慢
// 应该让这个 tick 失败，而不是把它拖到二十秒。
const publishTimeout = 5 * time.Second

// eventSink is one transport the producer knows how to write to.
//
// reason is what the payload carries as its provenance: one line per
// transport, saying which queue the event came in on. The consumer's success
// log carries the transport too, so one `docker logs` pass shows the same
// event arriving once per sink — which is the whole point of running one event
// through every transport. The manual-commit Kafka topic is a second sink of
// the same kind, with its own reason.
// eventSink 是生产者会写入的一个传输层。
//
// reason 是 payload 里携带的来源记录：每个 sink 一行，写明事件来自哪个队列。消费端
// 成功日志也会带上传输名，因此翻一遍 `docker logs` 就能看到同一事件在每个 sink 各到达
// 一次——这正是让一个事件走遍每种传输的意义。手动提交的 Kafka topic 是同种传输的第二个
// sink，有自己的 reason。
type eventSink struct {
	transport string
	reason    string
	send      func(context.Context, gofyconsumer.Envelope) error
}

// newOrderEventProducer publishes the same order event to Kafka, RabbitMQ,
// RocketMQ, a Redis queue and the manual-commit Kafka topic every interval, so
// the immediate consumers in this group have something to consume without a
// second terminal.
//
// The consumers are the contract: topic, exchange, queue key and tag are read
// from their config values rather than repeated here, so moving a subscription
// moves its producer with it. Only the connection name and the payload are the
// producer's own.
// newOrderEventProducer 每间隔把同一个订单事件发往 Kafka、RabbitMQ、RocketMQ、
// Redis 队列与手动提交的 Kafka topic，本组的立即消费者因此不需要另开一个终端就能一直
// 有消息可消费。
//
// 契约在消费者那边：topic、exchange、队列 key 与 tag 都从它们的配置值读，而不是在这里
// 再写一遍，因此改订阅就等于改生产端。生产者自己只有连接名与 payload。
func newOrderEventProducer(conns gofyconsumer.Connections, cfg OrderEventProducerConfig) gofyworker.Worker {
	intervalMS := cfg.IntervalMS
	if intervalMS == 0 {
		intervalMS = 5000
	}
	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	return gofyworker.Func("order-event-producer", uint8(concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok {
			return errors.New("order-event-producer: runtime is required")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := zap.NewNop()
		if rt.Logger != nil {
			lg = rt.Logger.With(zap.String("worker", id.String()))
		}

		// Connections are opened once, before the first tick: a producer that
		// reconnected every five seconds would spend the demo's whole log on
		// handshakes.
		// 连接在第一个 tick 之前打开一次：每五秒重连一次的生产者会把示例的日志全花在
		// 握手。
		sinks, closeSinks, err := openEventSinks(ctx, conns, cfg)
		if err != nil {
			return err
		}
		defer closeSinks()

		lg.Info("order event producer started",
			zap.Uint32("interval_ms", intervalMS),
			zap.Uint64("order_id", cfg.OrderID),
			zap.String("type", cfg.EventType),
			zap.Strings("transports", sinkNames(sinks)),
		)
		defer lg.Info("order event producer stopped")

		return gofyworker.Ticker(ctx, intervalMS, func(ctx context.Context) error {
			// The drain context lets a send that is already on the wire finish
			// during shutdown, the same way the scan loop's count does.
			// drain context 让已经在链路上的发送在停机时发完，与扫描循环的计数一致。
			publishCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), publishTimeout)
			defer cancel()

			// One id for every delivery: the fan-out is one event, and the
			// consumer-side ledger is keyed by (consumer, message id), so the
			// shared id cannot collide across transports — even between the two
			// Kafka subscriptions, which are separate consumers.
			// 所有投递共用一个 ID：群发的是一个事件；消费端台账按 (consumer,
			// message id) 记账，因此这个共享 ID 不会跨传输撞车——两个 Kafka 订阅是
			// 不同的 consumer，也不会互相撞。
			eventID := uuid.NewString()
			if err := publishOrderEvent(publishCtx, sinks, cfg, eventID); err != nil {
				return err
			}
			lg.Info("order event published",
				zap.String("event_id", eventID),
				zap.Uint64("order_id", cfg.OrderID),
				zap.String("type", cfg.EventType),
			)
			return nil
		})
	})
}

// publishOrderEvent writes one event to every sink and joins the failures.
//
// A broken transport must not silence the others: each send is attempted, and
// what fails is reported together with the transport that failed. The caller
// gets nil only when every sink accepted the event.
// publishOrderEvent 把同一个事件写入每个 sink 并合并失败。
//
// 一个传输坏掉不能让其它传输闭嘴：每次发送都会尝试，失败与失败的传输名一起上报。
// 只有所有 sink 都收下，调用方才拿到 nil。
func publishOrderEvent(ctx context.Context, sinks []eventSink, cfg OrderEventProducerConfig, eventID string) error {
	occurredAt := time.Now()
	var errs []error
	for _, sink := range sinks {
		payload, err := json.Marshal(orderEventPayload{
			OrderID: cfg.OrderID,
			UserID:  cfg.UserID,
			Reason:  sink.reason,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: encode payload: %w", sink.transport, err))
			continue
		}
		event := gofyconsumer.Envelope{
			ID:         eventID,
			Type:       cfg.EventType,
			OccurredAt: occurredAt,
			Payload:    payload,
		}
		if err := sink.send(ctx, event); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sink.transport, err))
		}
	}
	return errors.Join(errs...)
}

// openEventSinks connects every immediate sink and returns them with one
// closer. There are five sinks over four transports: Kafka appears twice, once
// for the default subscription and once for the manual-commit sample.
//
// It fails on the first broken transport instead of starting a producer that
// silently writes to the rest: a fan-out demo with a missing leg is worse than
// no demo, because the absent messages look like a consumer bug.
// openEventSinks 连接所有立即链路的 sink 并把它们与一个关闭函数一起返回。四种传输共有
// 五个 sink：Kafka 出现两次，一次给默认订阅，一次给手动提交样例。
//
// 任一传输层坏掉就直接失败，而不是启动一个只写其余部分的生产者：少一条腿的群发示例比
// 没有示例更糟——缺失的消息看起来会像消费端的 bug。
func openEventSinks(ctx context.Context, conns gofyconsumer.Connections, cfg OrderEventProducerConfig) ([]eventSink, func(), error) {
	var closers []func()
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}

	// Opened in one loop, closed in reverse order: the list is what keeps the
	// near-identical openers from turning into near-identical error branches.
	// 用一次循环打开、逆序关闭：有了这张列表，几乎相同的 opener 才不会退化成一段段
	// 几乎相同的错误分支。
	open := []func(context.Context, gofyconsumer.Connections, OrderEventProducerConfig) (eventSink, func(), error){
		openKafkaSink,
		openKafkaManualSink,
		openAMQPSink,
		openRocketMQSink,
		openQueuesSink,
	}
	sinks := make([]eventSink, 0, len(open))
	for _, openSink := range open {
		sink, closeSink, err := openSink(ctx, conns, cfg)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		sinks = append(sinks, sink)
		closers = append(closers, closeSink)
	}
	return sinks, closeAll, nil
}

func openKafkaSink(ctx context.Context, conns gofyconsumer.Connections, cfg OrderEventProducerConfig) (eventSink, func(), error) {
	return openKafkaTopicSink(ctx, conns, cfg, orderKafka, "kafka", "记录来自 kafka 队列")
}

// openKafkaManualSink feeds the manual-commit subscription. It is a second
// topic, not a second group on the first: the sample's whole point is a set of
// committed offsets that can be read on its own.
// openKafkaManualSink 给手动提交订阅供消息。它是第二个 topic，而不是第一个 topic 上的
// 第二个组：本样例的重点就是一组可以单独查看的已提交位点。
func openKafkaManualSink(ctx context.Context, conns gofyconsumer.Connections, cfg OrderEventProducerConfig) (eventSink, func(), error) {
	return openKafkaTopicSink(ctx, conns, cfg, orderKafkaManual, "kafka_manual", "记录来自 kafka 手动提交队列")
}

func openKafkaTopicSink(
	ctx context.Context,
	conns gofyconsumer.Connections,
	cfg OrderEventProducerConfig,
	sub gofyconsumer.KafkaConfig,
	transport, reason string,
) (eventSink, func(), error) {
	name := sub.Instance
	conn, ok := conns.Kafka[name]
	if !ok {
		return eventSink{}, nil, fmt.Errorf("kafka instance %q is not configured", name)
	}
	client, err := gofykafka.New(ctx, conn)
	if err != nil {
		return eventSink{}, nil, fmt.Errorf("kafka instance %q: %w", name, err)
	}
	producer, err := gofykafka.NewProducer(client, gofykafka.ProducerConfig{Topic: sub.Topic})
	if err != nil {
		_ = client.Close()
		return eventSink{}, nil, fmt.Errorf("kafka topic %q: %w", sub.Topic, err)
	}
	return eventSink{
			transport: transport,
			reason:    reason,
			send: func(ctx context.Context, event gofyconsumer.Envelope) error {
				// The order id as key keeps one order's events on one
				// partition, which is what makes per-order order possible.
				// The message_id header is the consumer's first-choice dedup
				// key; without it the ledger falls back to topic/partition@offset.
				// 用订单号作 key，让同一订单的事件落在同一分区，这是按订单保序的前提。
				// message_id 头是消费端去重的首选键；不设置时台账退到
				// topic/partition@offset。
				return producer.PublishJSON(ctx, event, gofykafka.PublishOptions{
					Key: strconv.FormatUint(cfg.OrderID, 10),
					Headers: map[string]string{
						"message_id": event.ID,
					},
				})
			},
		},
		func() { _ = client.Close() },
		nil
}

func openAMQPSink(ctx context.Context, conns gofyconsumer.Connections, cfg OrderEventProducerConfig) (eventSink, func(), error) {
	name := orderAMQP.Instance
	conn, ok := conns.AMQP[name]
	if !ok {
		return eventSink{}, nil, fmt.Errorf("amqp instance %q is not configured", name)
	}
	client, err := gofyamqp.New(ctx, conn)
	if err != nil {
		return eventSink{}, nil, fmt.Errorf("amqp instance %q: %w", name, err)
	}
	queue, err := gofyamqp.NewQueue(client, gofyamqp.QueueConfig{
		Topology: gofyamqp.Topology{
			Exchange:     orderAMQP.Exchange,
			ExchangeType: orderAMQP.ExchangeType,
			Queue:        orderAMQP.Queue,
			BindingKey:   orderAMQP.BindingKey,
			RoutingKey:   orderAMQP.RoutingKey,
			Durable:      true,
		},
		Confirm: true,
	})
	if err != nil {
		_ = client.Close()
		return eventSink{}, nil, fmt.Errorf("amqp queue %q: %w", orderAMQP.Queue, err)
	}
	return eventSink{
			transport: "amqp",
			reason:    "记录来自 rabbitmq 队列",
			send: func(ctx context.Context, event gofyconsumer.Envelope) error {
				// Publish with the event type as routing key: the binding is
				// order.*, so order.paid routes and anything else does not.
				// 用事件类型作 routing key 发布：绑定是 order.*，因此 order.paid
				// 能路由进去，别的事件不会。
				return queue.PublishJSON(ctx, event, gofyamqp.PublishOptions{
					RoutingKey: cfg.EventType,
					Persistent: true,
					MessageID:  event.ID,
					Timestamp:  event.OccurredAt,
				})
			},
		},
		func() {
			_ = queue.Close()
			_ = client.Close()
		},
		nil
}

func openRocketMQSink(ctx context.Context, conns gofyconsumer.Connections, cfg OrderEventProducerConfig) (eventSink, func(), error) {
	name := orderRocketMQ.Instance
	conn, ok := conns.RocketMQ[name]
	if !ok {
		return eventSink{}, nil, fmt.Errorf("rocketmq instance %q is not configured", name)
	}
	client, err := gofyrocketmq.New(ctx, conn)
	if err != nil {
		return eventSink{}, nil, fmt.Errorf("rocketmq instance %q: %w", name, err)
	}
	// Publish under the subscription's first concrete tag. The consumer narrows
	// on that tag, so a message without it would never be delivered.
	// 用订阅里第一个具体 tag 发布。消费者正是按这个 tag 收窄订阅，不带它的消息投不到。
	tag := ""
	if len(orderRocketMQ.Tags) > 0 && orderRocketMQ.Tags[0] != "*" {
		tag = orderRocketMQ.Tags[0]
	}
	return eventSink{
			transport: "rocketmq",
			reason:    "记录来自 rocketmq 队列",
			send: func(ctx context.Context, event gofyconsumer.Envelope) error {
				_, err := client.PublishJSON(ctx, event, gofyrocketmq.PublishOptions{
					Topic: orderRocketMQ.Topic,
					Tag:   tag,
					Keys:  []string{"order-" + strconv.FormatUint(cfg.OrderID, 10)},
				})
				return err
			},
		},
		func() { _ = client.Close() },
		nil
}

func openQueuesSink(ctx context.Context, conns gofyconsumer.Connections, cfg OrderEventProducerConfig) (eventSink, func(), error) {
	name := orderQueues.RedisInstance
	conn, ok := conns.Redis[name]
	if !ok {
		return eventSink{}, nil, fmt.Errorf("redis instance %q is not configured", name)
	}
	client, err := gofyredis.New(ctx, conn)
	if err != nil {
		return eventSink{}, nil, fmt.Errorf("redis instance %q: %w", name, err)
	}
	queue, err := gofyqueues.NewQueue(client, gofyqueues.Config{Key: orderQueues.Key})
	if err != nil {
		_ = client.Close()
		return eventSink{}, nil, fmt.Errorf("queues key %q: %w", orderQueues.Key, err)
	}
	return eventSink{
			transport: "queues",
			reason:    "记录来自 redis 队列",
			send: func(ctx context.Context, event gofyconsumer.Envelope) error {
				_, err := queue.Push(ctx, event)
				return err
			},
		},
		func() { _ = client.Close() },
		nil
}

func sinkNames(sinks []eventSink) []string {
	names := make([]string, 0, len(sinks))
	for _, sink := range sinks {
		names = append(names, sink.transport)
	}
	return names
}
