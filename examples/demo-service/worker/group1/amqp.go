package group1

import (
	"context"
	"errors"
	"fmt"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/amqpconsumer"
)

// amqpConsumerName is the worker name for the RabbitMQ consumer.
// amqpConsumerName 是 RabbitMQ 消费者的 worker 名。
const amqpConsumerName = "order-amqp-consumer"

// newAMQPConsumer adapts one AMQP queue to a worker.
//
// The queue declares its topology on first consume, so enabling this consumer
// is what creates the exchange, queue and binding. That is deliberate for a
// sample: one config block is enough to go from nothing to a working
// subscription.
// newAMQPConsumer 把一个 AMQP 队列适配成 worker。
//
// 队列在第一次消费时声明 topology，因此启用这个消费者就会创建 exchange、队列与
// 绑定。示例里这是刻意的：一个配置块就能从零到可用的订阅。
func newAMQPConsumer(cfg AMQPConsumer, conn gofyamqp.Config) gofyworker.Worker {
	return gofyworker.Func(amqpConsumerName, concurrency(cfg.Concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok {
			return errors.New("order-amqp-consumer: worker runtime is missing from the context")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := rt.Logger.With(
			zap.String("worker", id.String()),
			zap.String("consumer", amqpConsumerName),
		)

		handler, err := newOrderHandler(rt)
		if err != nil {
			return err
		}
		proc, err := newProcessor(rt, processorConfig{
			Consumer:    amqpConsumerName,
			Handler:     handler,
			Idempotency: cfg.Idempotency,
			MaxAttempts: cfg.MaxAttempts,
		})
		if err != nil {
			return err
		}

		client, err := gofyamqp.New(ctx, conn)
		if err != nil {
			return fmt.Errorf("%s: connect: %w", amqpConsumerName, err)
		}
		defer func() { _ = client.Close() }()

		queue, err := gofyamqp.NewQueue(client, gofyamqp.QueueConfig{
			Topology: gofyamqp.Topology{
				Exchange:     cfg.Exchange,
				ExchangeType: cfg.ExchangeType,
				Queue:        cfg.Queue,
				BindingKey:   cfg.BindingKey,
				RoutingKey:   cfg.RoutingKey,
				Durable:      cfg.Durable,
			},
		})
		if err != nil {
			return fmt.Errorf("%s: declare queue %s: %w", amqpConsumerName, cfg.Queue, err)
		}
		// Closed twice for the same reason as Kafka: the adapter keeps the
		// queue open across a restart, while this worker rebuilds it.
		// 与 Kafka 同理关闭两次：适配器为重启保留队列，而本实现会重建它。
		defer func() { _ = queue.Close() }()

		w, err := amqpconsumer.New(amqpConsumerName, queue, amqpconsumer.Config{
			Handler: func(ctx context.Context, delivery amqp091.Delivery) error {
				return proc.Handle(ctx, amqpMessage(delivery))
			},
			Options: gofyamqp.ConsumeOptions{
				PrefetchCount:  cfg.PrefetchCount,
				AutoAck:        cfg.AutoAck,
				DiscardOnError: cfg.DiscardOnError,
				ReconnectDelay: cfg.ReconnectDelay,
				OnError: func(err error) {
					lg.Error("amqp consume error", zap.Error(err))
				},
			},
		})
		if err != nil {
			return err
		}

		lg.Info("amqp consumer started",
			zap.String("exchange", cfg.Exchange),
			zap.String("queue", cfg.Queue),
			zap.Int("prefetch", cfg.PrefetchCount),
			zap.Bool("idempotency", cfg.Idempotency),
		)
		defer lg.Info("amqp consumer stopped", zap.String("queue", cfg.Queue))

		return w.Run(ctx)
	})
}

// amqpMessage converts one delivery into the shared message view.
// amqpMessage 把一次投递转成共用的消息视图。
func amqpMessage(delivery amqp091.Delivery) Message {
	headers := amqpHeaders(delivery.Headers)
	attempt := int32(1)
	if delivery.Redelivered {
		// AMQP only exposes a flag, not a count; a redelivery is at least the
		// second attempt, which is all the ledger needs to know.
		// AMQP 只提供重投标志而非次数；重投至少是第二次，这对台账已经足够。
		attempt = 2
	}
	return Message{
		ID:       amqpMessageID(delivery, headers),
		Source:   "amqp",
		Topic:    delivery.RoutingKey,
		Key:      delivery.MessageId,
		Body:     delivery.Body,
		Attempt:  attempt,
		Headers:  headers,
		Enqueued: delivery.Timestamp,
	}
}

// amqpMessageID prefers the broker-provided message id.
//
// An empty result is intentional: the processor then generates a random id,
// which disables deduplication for that message instead of collapsing two
// different messages published under the same tag. Producers that care about
// redelivery must set MessageId.
// amqpMessageID 优先使用 broker 提供的消息 ID。
//
// 返回空值是刻意保留的行为：处理器会补一个随机 ID，使这条消息不做去重，而不是把
// 两条不同的消息当成同一条。在意重投的生产者必须设置 MessageId。
func amqpMessageID(delivery amqp091.Delivery, headers map[string]string) string {
	if id := delivery.MessageId; id != "" {
		return id
	}
	if id := headers["message_id"]; id != "" {
		return id
	}
	return headers["X-Correlation-Id"]
}

// amqpHeaders flattens the broker table into strings, keeping the traceparent
// readable for the propagator.
// amqpHeaders 把 broker 的 table 压成字符串，同时让传播器能读到 traceparent。
func amqpHeaders(table amqp091.Table) map[string]string {
	if len(table) == 0 {
		return nil
	}
	out := make(map[string]string, len(table))
	for key, value := range table {
		switch v := value.(type) {
		case nil:
			continue
		case string:
			out[key] = v
		case []byte:
			out[key] = string(v)
		default:
			out[key] = fmt.Sprint(v)
		}
	}
	return out
}
