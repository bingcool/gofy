package group1

import (
	"context"
	"time"

	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/worker"
)

// orderRocketMQBatch is the batch-pull subscription: one Receive asks the
// broker for ten messages instead of one.
//
// RocketMQ v5 has no push consumer and the gofy handler is per message, so
// "batch" here lives entirely on the pull side. Receive returns up to
// BatchSize messages, one worker walks them in order, and each message is
// acknowledged on its own. A failure therefore costs one redelivery, not the
// other nine messages: they are acknowledged and the failed one becomes
// visible again after InvisibleDuration. The handler never sees a batch
// boundary — it cannot sum ten messages into one write, and it must stay
// idempotent because the same message can arrive again alone.
//
// The topic is the same test-demo the immediate subscription reads, with a tag
// of its own: RocketMQ filters on tags, so the demo tag never reaches this
// consumer and no extra topic has to be created on the broker.
//
// orderRocketMQBatch 是批量拉取订阅：一次 Receive 向 broker 要十条，而不是一条。
//
// RocketMQ v5 没有 push 消费者，gofy 的 handler 又是一条一条的，因此这里的「批量」
// 完全在拉取侧：Receive 一次拿回至多 BatchSize 条，一个 worker 按顺序走一遍，每条各自
// 确认。所以一次失败只换来一条重投，而不是另外九条跟着重来——它们已经确认，失败的那条
// 在 InvisibleDuration 后重新可见。handler 看不到批次边界，既不能把十条合并成一次写入，
// 也必须保持幂等，因为同一条消息可能单独再投一次。
//
// topic 与立即订阅共用 test-demo，但用自己的 tag：RocketMQ 按 tag 过滤，demo tag 的消息
// 到不了这个消费者，broker 上也不需要新建 topic。
var orderRocketMQBatch = gofyconsumer.RocketMQConfig{
	Name: "order-rocketmq-batch-consumer",
	// Instance points at the same rocketmq.default block; only the group and
	// the tag differ from orderRocketMQ.
	// Instance 指向同一个 rocketmq.default 连接段，与 orderRocketMQ 只差组与 tag。
	Instance:    "default",
	Topic:       orderRocketMQ.Topic,
	Group:       "demo-service-order-batch-consumer",
	Tags:        []string{"batch"},
	Concurrency: 1,
	// One pull worker keeps the batch visible: the ten messages of one burst
	// are handled by the same goroutine and the log shows consecutive queue
	// offsets. Raise it for throughput, not for order — several workers pull
	// concurrently and batches then interleave.
	// 一个拉取 worker 让批次可见：同一批十条由同一个 goroutine 处理，日志里的队列位点
	// 是连续的。想提吞吐再调大，但别指望顺序——多个 worker 并发拉取，批次会交错。
	PullWorkers: 1,
	// The pull size. Ten matches the burst the sibling producer sends, so one
	// Receive usually returns the whole burst.
	// 一次拉取的条数。取 10 与配套生产者每次群发的条数一致，一次 Receive 通常能拿回
	// 整批。
	BatchSize: 10,
	// A five-second long poll: the demo's burst arrives every ten seconds, so
	// half the polls come back empty by design.
	// 五秒长轮询：示例每十秒群发一次，因此有一半的拉取按设计是空的。
	AwaitDuration: 5 * time.Second,
	// Short enough to watch a failure come back inside one demo session.
	// 足够短，一次演示里就能看到失败消息重投回来。
	InvisibleDuration: 15 * time.Second,
	// The ledger is what makes a redelivery a no-op and counts attempts; three
	// parks a message as dead before the demo runs too long.
	// 台账让重投变成空操作并计次；三次就在演示变得冗长之前把消息记为 dead。
	Idempotency: true,
	MaxAttempts: 3,
	Handler:     handleOrderRocketMQBatch,
}

// handleOrderRocketMQBatch runs the shared business branch and logs what the
// acknowledgement decision was.
//
// msg.Message is the native message: it carries the queue offset and the
// broker's delivery attempt, the two numbers that show ten messages really
// arrived as one pull and that a redelivery is the same message coming back
// with a higher attempt.
//
// handleOrderRocketMQBatch 走共用业务分支，并记下确认决定。
//
// msg.Message 是原生消息：带队列位点与 broker 的投递次数——前者说明这一批确实来自一次
// 拉取，后者说明重投是同一条消息带着更大的次数回来。
func handleOrderRocketMQBatch(ctx context.Context, cfg gofyconsumer.RocketMQConfig, msg gofyconsumer.RocketMQMessage) error {
	err := applyOrderEvent(ctx, msg.Envelope)
	logBatchDelivery(ctx, cfg, msg, err)
	return err
}

// logBatchDelivery makes the per-message acknowledgement visible. The shared
// chain already logs consumed/failed; this line adds the queue offset, the
// delivery attempt and how long the message waited, which is what tells a
// batch apart from ten single pulls.
// logBatchDelivery 把逐条确认显出来。共用链路已有 consumed/failed 日志，这一行补上队列
// 位点、投递次数与消息等待时长——正是这几个数把「一次批量拉取」与「十次单条拉取」区分开。
func logBatchDelivery(ctx context.Context, cfg gofyconsumer.RocketMQConfig, msg gofyconsumer.RocketMQMessage, err error) {
	rt, ok := gofyworker.RuntimeFrom(ctx)
	if !ok || rt.Logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("consumer", cfg.Name),
		zap.String("topic", cfg.Topic),
		zap.String("event_id", msg.Envelope.ID),
		zap.Int("batch_size", cfg.BatchSize),
		zap.Int("pull_workers", cfg.PullWorkers),
	}
	if native := msg.Message; native != nil {
		fields = append(fields,
			zap.String("message_id", native.MessageID),
			zap.String("tag", native.Tag),
			// The offset is per queue, not per topic: a batch that spans
			// queues has no continuous range, which is why the queue id would
			// be the missing half. The SDK's MessageView does not expose it.
			// 位点是队列内的，不是 topic 内的：跨队列的批次没有连续区间，缺的另一半正是
			// 队列 ID，而 SDK 的 MessageView 没有暴露它。
			zap.Int64("offset", native.Offset),
			zap.Int32("delivery_attempt", native.DeliveryAttempt),
			zap.Duration("queued", time.Since(native.BornTimestamp)),
		)
	}
	switch {
	case err == nil:
		fields = append(fields, zap.String("decision", "ack this message"))
	case gofyconsumer.IsPermanent(err):
		// The shared chain parks it as dead and returns nil, so this branch is
		// only reachable before the ledger exists.
		// 共用链路会把它记为 dead 并返回 nil，因此这条分支只在台账尚不存在时走到。
		fields = append(fields, zap.String("decision", "park as dead, ack anyway"))
	default:
		fields = append(fields, zap.String("decision", "leave unacknowledged, redeliver after invisible_duration"))
	}
	rt.Logger.Info("rocketmq batch delivery", fields...)
}
