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
	gofyqueues "github.com/bingcool/gofy/support/queues"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// DelayEventProducerConfig controls the delayed half of the demo. The numbers
// live here for the same reason they live in producer.go: they belong to the
// loop they drive, not to config.yaml.
// DelayEventProducerConfig 控制示例的延迟生产端。参数放在这里的原因与 producer.go
// 相同：数字属于它驱动的循环，不属于 config.yaml。
type DelayEventProducerConfig struct {
	// Concurrency is the number of producer instances. Keep it at 1.
	// Concurrency 是生产者实例数，保持 1 即可。
	Concurrency int
	// IntervalMS is the gap between two scheduled events, in milliseconds.
	// IntervalMS 是两次调度之间的间隔，单位毫秒。
	IntervalMS uint32
	// DelayMS is how long each event waits before it becomes visible.
	// DelayMS 是每条事件多久之后才可见。
	DelayMS uint32
	// OrderID is the order every event points at.
	// OrderID 是每个事件指向的订单。
	OrderID uint64
	// UserID travels in the payload for audit.
	// UserID 随 payload 一起走，供审计使用。
	UserID uint64
	// EventType selects the consumer branch; order.paid is enough to show the
	// whole chain without changing the order state forever.
	// EventType 决定消费端分支；order.paid 足够展示完整链路。
	EventType string
}

// delayEventProducerDefaults schedules one event every ten seconds, due five
// seconds later. The shorter delay leaves room to watch it arrive inside one
// interval instead of waiting a minute to see anything.
// delayEventProducerDefaults 每十秒调度一条、五秒后到期。延迟短一些，一个间隔内
// 就能看到它到达，不必等上一分钟。
var delayEventProducerDefaults = DelayEventProducerConfig{
	Concurrency: 1,
	IntervalMS:  10000,
	DelayMS:     5000,
	OrderID:     1,
	UserID:      1,
	EventType:   "order.paid",
}

// newDelayEventProducer schedules the same order event through all three
// delayed transports: a Redis ZSET (support/queues.DelayQueue), a RocketMQ
// DELAY topic, and a RabbitMQ TTL + dead-letter workflow. Its three siblings in
// delay_queues_order.go, rocketmq_delay_order.go and delay_amqp_order.go
// consume them, so group1 demonstrates delayed produce → consume without a
// second terminal.
//
// The subscriptions own the addresses again: keys, topic, tag, staging/target
// topology and instance are read from the consumer configs, so this producer
// cannot drift away from the consumers it feeds.
// newDelayEventProducer 把同一个订单事件调度到三种延迟传输：Redis ZSET、RocketMQ
// DELAY topic 与 RabbitMQ 的 TTL + 死信流程。delay_queues_order.go、
// rocketmq_delay_order.go 与 delay_amqp_order.go 里的三个消费者负责消费，因此
// group1 单进程即可演示延迟的 生产 → 消费 闭环。
//
// 地址同样归订阅所有：键、topic、tag、暂存/目标拓扑与实例名都从消费者配置读取，
// 生产端不会与它所喂的消费者走偏。
func newDelayEventProducer(conns gofyconsumer.Connections, cfg DelayEventProducerConfig) gofyworker.Worker {
	intervalMS := cfg.IntervalMS
	if intervalMS == 0 {
		intervalMS = 10000
	}
	delayMS := cfg.DelayMS
	if delayMS == 0 {
		delayMS = 5000
	}
	delay := time.Duration(delayMS) * time.Millisecond
	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	return gofyworker.Func("order-delay-producer", uint8(concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok {
			return errors.New("order-delay-producer: runtime is required")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := zap.NewNop()
		if rt.Logger != nil {
			lg = rt.Logger.With(zap.String("worker", id.String()))
		}

		sinks, closeSinks, err := openDelayEventSinks(ctx, conns, cfg, delay)
		if err != nil {
			return err
		}
		defer closeSinks()

		lg.Info("delayed order event producer started",
			zap.Uint32("interval_ms", intervalMS),
			zap.Duration("delay", delay),
			zap.Uint64("order_id", cfg.OrderID),
			zap.Strings("transports", sinkNames(sinks)),
		)
		defer lg.Info("delayed order event producer stopped")

		return gofyworker.Ticker(ctx, intervalMS, func(ctx context.Context) error {
			publishCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), publishTimeout)
			defer cancel()

			eventID := uuid.NewString()
			if err := publishDelayEvent(publishCtx, sinks, cfg, eventID); err != nil {
				return err
			}
			lg.Info("delayed order event scheduled",
				zap.String("event_id", eventID),
				zap.Duration("delay", delay),
				zap.Time("due_at", time.Now().Add(delay)),
				zap.Uint64("order_id", cfg.OrderID),
			)
			return nil
		})
	})
}

// publishDelayEvent fans one event out to every delayed sink and joins the
// failures, so a broken transport never hides the others.
// publishDelayEvent 把一条事件写到所有延迟 sink 并合并失败，一个传输坏掉不会让其余
// 传输一起沉默。
func publishDelayEvent(ctx context.Context, sinks []eventSink, cfg DelayEventProducerConfig, eventID string) error {
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

// openDelayEventSinks connects the three delayed transports, closing them in
// reverse order if any opener fails.
// openDelayEventSinks 连接三个延迟传输层；任一打开失败时按逆序关闭已打开的部分。
func openDelayEventSinks(ctx context.Context, conns gofyconsumer.Connections, cfg DelayEventProducerConfig, delay time.Duration) ([]eventSink, func(), error) {
	var closers []func()
	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	open := []func(context.Context, gofyconsumer.Connections, DelayEventProducerConfig, time.Duration) (eventSink, func(), error){
		openDelayQueuesSink,
		openRocketMQDelaySink,
		openAMQPDelaySink,
	}
	sinks := make([]eventSink, 0, len(open))
	for _, openSink := range open {
		sink, closeSink, err := openSink(ctx, conns, cfg, delay)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		sinks = append(sinks, sink)
		closers = append(closers, closeSink)
	}
	return sinks, closeAll, nil
}

func openDelayQueuesSink(ctx context.Context, conns gofyconsumer.Connections, cfg DelayEventProducerConfig, delay time.Duration) (eventSink, func(), error) {
	name := orderDelayQueues.RedisInstance
	conn, ok := conns.Redis[name]
	if !ok {
		return eventSink{}, nil, fmt.Errorf("redis instance %q is not configured", name)
	}
	client, err := gofyredis.New(ctx, conn)
	if err != nil {
		return eventSink{}, nil, fmt.Errorf("redis instance %q: %w", name, err)
	}
	queue, err := gofyqueues.NewDelayQueue(client, gofyqueues.Config{
		Key:        orderDelayQueues.Key,
		MaxRetries: orderDelayQueues.MaxRetries,
	})
	if err != nil {
		_ = client.Close()
		return eventSink{}, nil, fmt.Errorf("delay queues key %q: %w", orderDelayQueues.Key, err)
	}
	return eventSink{
			transport: "delay_queues",
			reason:    "延迟投递，来自 redis delay_queue",
			send: func(ctx context.Context, event gofyconsumer.Envelope) error {
				// The due time is the ZSET score; the consumer's Pop only sees
				// the message once that score is behind the current time.
				// 到期时间就是 ZSET 的 score；消费者的 Pop 只有在 score 落到
				// 当前时间之后才看得到这条消息。
				return queue.Add(ctx, event, delay)
			},
		},
		func() { _ = client.Close() },
		nil
}

func openRocketMQDelaySink(ctx context.Context, conns gofyconsumer.Connections, cfg DelayEventProducerConfig, delay time.Duration) (eventSink, func(), error) {
	name := orderRocketMQDelay.Instance
	conn, ok := conns.RocketMQ[name]
	if !ok {
		return eventSink{}, nil, fmt.Errorf("rocketmq instance %q is not configured", name)
	}
	client, err := gofyrocketmq.New(ctx, conn)
	if err != nil {
		return eventSink{}, nil, fmt.Errorf("rocketmq instance %q: %w", name, err)
	}
	tag := ""
	if len(orderRocketMQDelay.Tags) > 0 && orderRocketMQDelay.Tags[0] != "*" {
		tag = orderRocketMQDelay.Tags[0]
	}
	return eventSink{
			transport: "rocketmq_delay",
			reason:    "延迟投递，来自 rocketmq delay topic",
			send: func(ctx context.Context, event gofyconsumer.Envelope) error {
				// Delay is the relative form of DeliverAt; the SDK turns it
				// into an absolute delivery timestamp on the wire.
				// Delay 是 DeliverAt 的相对写法，SDK 会把它转换成线路上的绝对
				// 投递时间戳。
				_, err := client.PublishJSON(ctx, event, gofyrocketmq.PublishOptions{
					Topic: orderRocketMQDelay.Topic,
					Tag:   tag,
					Delay: delay,
					Keys:  []string{"order-" + strconv.FormatUint(cfg.OrderID, 10)},
				})
				return err
			},
		},
		func() { _ = client.Close() },
		nil
}

func openAMQPDelaySink(ctx context.Context, conns gofyconsumer.Connections, cfg DelayEventProducerConfig, delay time.Duration) (eventSink, func(), error) {
	name := orderAMQPDelay.Instance
	conn, ok := conns.AMQP[name]
	if !ok {
		return eventSink{}, nil, fmt.Errorf("amqp instance %q is not configured", name)
	}
	client, err := gofyamqp.New(ctx, conn)
	if err != nil {
		return eventSink{}, nil, fmt.Errorf("amqp instance %q: %w", name, err)
	}
	// The subscription is the single source of truth for both topologies. The
	// producer only turns on confirms; a queue declared with different
	// x-message-ttl / dead-letter arguments would fail with PRECONDITION_FAILED.
	// 两套拓扑都来自订阅配置。生产端只额外打开发布确认：若声明队列时参数不同，
	// broker 会返回 PRECONDITION_FAILED。
	queueCfg := orderAMQPDelay.QueueConfig()
	queueCfg.Confirm = true
	queue, err := gofyamqp.NewQueue(client, queueCfg)
	if err != nil {
		_ = client.Close()
		return eventSink{}, nil, fmt.Errorf("amqp delay queue %q: %w", orderAMQPDelay.DelayQueue, err)
	}
	return eventSink{
			transport: "amqp_delay",
			reason:    "延迟投递，来自 rabbitmq 延迟队列",
			send: func(ctx context.Context, event gofyconsumer.Envelope) error {
				// Delay becomes the per-message TTL; the staging queue
				// dead-letters the message to the target once it expires.
				// It must stay within the subscription's TTL, which the
				// transport verifies before publishing.
				// Delay 会转成单条消息的 TTL；暂存队列在到期时把消息死信到目标队列。
				// 它必须在订阅的 TTL 之内，传输层会在发布前校验。
				return queue.PublishJSON(ctx, event, gofyamqp.PublishOptions{
					Delay:      delay,
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
