package group1

import (
	"context"
	"time"

	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/worker"
)

// orderDelayQueues is one delayed Redis subscription, one file like the four
// immediate ones. The ZSET key is separate from demo:order:events on purpose:
// mixing due messages into the FIFO list would let a consumer pick them up
// before their delay, which is exactly the bug this sample demonstrates.
// orderDelayQueues 是一个 Redis 延迟订阅，与四个立即订阅一样各自一个文件。
// ZSET 键刻意与 demo:order:events 分开：把到期消息混进 FIFO 列表会让消费者在
// 延迟未到时就取到，那正是本样例要展示的区别。
var orderDelayQueues = gofyconsumer.DelayQueuesConfig{
	Name:          "order-delay-queues-consumer",
	RedisInstance: "ratelimit",
	Key:           "demo:order:delay",
	Concurrency:   1,
	// Ten due messages at most per poll; the producer sends one per tick.
	// 单次最多取 10 条到期消息；生产者每 tick 只发一条。
	PopLimit:    10,
	Idempotency: true,
	Handler:     handleOrderDelayQueues,
}

// handleOrderDelayQueues logs how long the message actually waited before
// running the same business branch as every other consumer.
//
// Message.Timestamp is when it entered the ZSET, so the gap to now is the
// observed delay. It is the number that answers "did the delay really happen?"
// without opening Redis.
// handleOrderDelayQueues 先记下消息实际等了多久，再走与其它消费者共用的业务分支。
//
// Message.Timestamp 是入 ZSET 的时间，与当前时间的差就是实测延迟。不用打开 Redis
// 就能回答"延迟到底生效了没有"。
func handleOrderDelayQueues(ctx context.Context, cfg gofyconsumer.DelayQueuesConfig, msg gofyconsumer.DelayQueuesMessage) error {
	if rt, ok := gofyworker.RuntimeFrom(ctx); ok && rt.Logger != nil {
		var waited time.Duration
		if msg.Message != nil && msg.Message.Timestamp > 0 {
			waited = time.Since(time.Unix(msg.Message.Timestamp, 0)).Truncate(time.Millisecond)
		}
		rt.Logger.Info("delayed order event arrived",
			zap.String("consumer", cfg.Name),
			zap.String("source", "delay_queues"),
			zap.String("event_id", msg.Envelope.ID),
			zap.Duration("waited", waited),
			zap.Int32("attempt", msg.Attempt),
		)
	}
	return applyOrderEvent(ctx, msg.Envelope)
}
