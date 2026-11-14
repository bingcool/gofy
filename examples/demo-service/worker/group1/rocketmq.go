package group1

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/rocketmqconsumer"
)

// rocketmqConsumerName is the worker name for the RocketMQ consumer.
// rocketmqConsumerName 是 RocketMQ 消费者的 worker 名。
const rocketmqConsumerName = "order-rocketmq-consumer"

// newRocketMQConsumer adapts one RocketMQ subscription to a worker.
//
// A RocketMQ consumer is single-use: it is created, consumed once and closed,
// so the worker instance count stays at the configured value and the SDK's own
// pull workers provide the parallelism.
// newRocketMQConsumer 把一个 RocketMQ 订阅适配成 worker。
//
// RocketMQ 消费者是一次性对象：创建、消费一次、关闭，因此 worker 实例数按配置
// 走，并行度由 SDK 自己的拉取 worker 提供。
func newRocketMQConsumer(cfg RocketMQConsumer, conn gofyrocketmq.Config) gofyworker.Worker {
	return gofyworker.Func(rocketmqConsumerName, concurrency(cfg.Concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok {
			return errors.New("order-rocketmq-consumer: worker runtime is missing from the context")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := rt.Logger.With(
			zap.String("worker", id.String()),
			zap.String("consumer", rocketmqConsumerName),
		)

		handler, err := newOrderHandler(rt)
		if err != nil {
			return err
		}
		proc, err := newProcessor(rt, processorConfig{
			Consumer:    rocketmqConsumerName,
			Handler:     handler,
			Idempotency: cfg.Idempotency,
			MaxAttempts: cfg.MaxAttempts,
		})
		if err != nil {
			return err
		}

		consumer, err := gofyrocketmq.NewConsumer(conn, gofyrocketmq.ConsumerConfig{
			Topic:             cfg.Topic,
			Group:             cfg.Group,
			Tags:              cfg.Tags,
			BatchSize:         cfg.BatchSize,
			Concurrency:       cfg.PullWorkers,
			AwaitDuration:     cfg.AwaitDuration,
			InvisibleDuration: cfg.InvisibleDuration,
		})
		if err != nil {
			return fmt.Errorf("%s: subscribe %s: %w", rocketmqConsumerName, cfg.Topic, err)
		}
		// The adapter closes on shutdown; this close covers the restart path.
		// 适配器在停机时关闭；这里覆盖重启路径。
		defer func() { _ = consumer.Close() }()

		w, err := rocketmqconsumer.New(rocketmqConsumerName, consumer, rocketmqconsumer.Config{
			Handler: func(ctx context.Context, message *gofyrocketmq.Message) error {
				return proc.Handle(ctx, rocketmqMessage(message))
			},
			Options: gofyrocketmq.ConsumeOptions{
				DiscardOnError: cfg.DiscardOnError,
				OnError: func(err error) {
					lg.Error("rocketmq consume error", zap.Error(err))
				},
			},
		})
		if err != nil {
			return err
		}

		lg.Info("rocketmq consumer started",
			zap.String("topic", cfg.Topic),
			zap.String("group", cfg.Group),
			zap.Strings("tags", cfg.Tags),
			zap.Bool("idempotency", cfg.Idempotency),
		)
		defer lg.Info("rocketmq consumer stopped", zap.String("topic", cfg.Topic))

		return w.Run(ctx)
	})
}

// rocketmqMessage converts one RocketMQ message into the shared view.
//
// DeliveryAttempt is the broker's own counter and is the most trustworthy
// attempt number of the four protocols, which makes it useful for spotting a
// message that keeps failing.
// rocketmqMessage 把一条 RocketMQ 消息转成共用视图。
//
// DeliveryAttempt 是 broker 自己的计数器，是四种协议里最可信的尝试次数，因此
// 适合用来发现一条持续失败的消息。
func rocketmqMessage(message *gofyrocketmq.Message) Message {
	return Message{
		ID:       rocketmqMessageID(message),
		Source:   "rocketmq",
		Topic:    message.Topic,
		Key:      strings.Join(message.Keys, ","),
		Body:     message.Body,
		Attempt:  message.DeliveryAttempt,
		Headers:  message.Properties,
		Enqueued: message.BornTimestamp,
	}
}

// rocketmqMessageID prefers the broker message id and falls back to the queue
// offset.
// rocketmqMessageID 优先用 broker 的消息 ID，缺失时回退到队列位点。
func rocketmqMessageID(message *gofyrocketmq.Message) string {
	if id := message.MessageID; id != "" {
		return id
	}
	return fmt.Sprintf("%s@%s", message.Topic, strconv.FormatInt(message.Offset, 10))
}
