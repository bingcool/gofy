package group1

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	"github.com/bingcool/gofy/support/queues"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/queuesconsumer"
)

// queuesConsumerName is the worker name for the Redis queue consumer.
// queuesConsumerName 是 Redis 队列消费者的 worker 名。
const queuesConsumerName = "order-queues-consumer"

// newQueuesConsumer adapts one Redis queue to a worker.
//
// This is the cheapest of the four to run and the weakest of the four
// guarantees: a popped message is already removed from Redis, so a crash in
// the handler loses it. The demo keeps it because "fire and forget" work —
// thumbnails, notifications — is exactly what a Redis queue is for.
// newQueuesConsumer 把一个 Redis 队列适配成 worker。
//
// 这是四种里运行成本最低、保证也最弱的一种：消息弹出时已从 Redis 删除，handler
// 崩溃就会丢这一条。示例保留它，是因为"发出去就不用管"的工作——缩略图、通知——
// 正是 Redis 队列的用武之地。
func newQueuesConsumer(cfg gofyconsumer.QueuesConsumer, conn gofyredis.Config) gofyworker.Worker {
	return gofyworker.Func(queuesConsumerName, gofyconsumer.Concurrency(cfg.Concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok {
			return errors.New("order-queues-consumer: worker runtime is missing from the context")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := rt.Logger.With(
			zap.String("worker", id.String()),
			zap.String("consumer", queuesConsumerName),
		)

		handler, err := newOrderHandler(rt)
		if err != nil {
			return err
		}
		proc, err := gofyconsumer.NewProcessor(rt, gofyconsumer.ProcessorConfig{
			Consumer:    queuesConsumerName,
			Handler:     handler,
			Idempotency: cfg.Idempotency,
			MaxAttempts: cfg.MaxAttempts,
		})
		if err != nil {
			return err
		}

		client, err := gofyredis.New(ctx, conn)
		if err != nil {
			return fmt.Errorf("%s: connect: %w", queuesConsumerName, err)
		}
		defer func() { _ = client.Close() }()

		queue, err := queues.NewQueue(client, queues.Config{
			Key:        cfg.Key,
			MaxRetries: cfg.MaxRetries,
		})
		if err != nil {
			return fmt.Errorf("%s: open queue %s: %w", queuesConsumerName, cfg.Key, err)
		}

		w, err := queuesconsumer.New(queuesConsumerName, queue, queuesconsumer.Config{
			Handler: func(ctx context.Context, msg *queues.Message) error {
				return proc.Handle(ctx, queuesMessage(cfg.Key, msg))
			},
			Options: queues.ConsumeOptions{
				IdleDelay:      cfg.IdleDelay,
				RetryDelay:     cfg.RetryDelay,
				DiscardOnError: cfg.DiscardOnError,
				OnError: func(err error) {
					lg.Error("queues consume error", zap.Error(err))
				},
			},
		})
		if err != nil {
			return err
		}

		lg.Info("queues consumer started",
			zap.String("queue", cfg.Key),
			zap.Int("max_retries", cfg.MaxRetries),
			zap.Bool("idempotency", cfg.Idempotency),
		)
		defer lg.Info("queues consumer stopped", zap.String("queue", cfg.Key))

		return w.Run(ctx)
	})
}

// queuesMessage converts one Redis queue message into the shared view.
//
// The queue stamps every message with its own id, so deduplication works
// without asking the producer for anything.
// queuesMessage 把一条 Redis 队列消息转成共用视图。
//
// 队列会给每条消息盖自己的 ID，因此去重不需要生产者配合。
func queuesMessage(key string, msg *queues.Message) gofyconsumer.Message {
	attempt := int32(msg.RetryNum) + 1
	var enqueued time.Time
	if msg.Timestamp > 0 {
		enqueued = time.Unix(msg.Timestamp, 0)
	}
	return gofyconsumer.Message{
		ID:       msg.ID,
		Source:   "queues",
		Topic:    key,
		Body:     msg.Payload,
		Attempt:  attempt,
		Enqueued: enqueued,
	}
}
