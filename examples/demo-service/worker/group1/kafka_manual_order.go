package group1

import (
	"context"

	"go.uber.org/zap"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// orderKafkaManual is the manual-commit counterpart of orderKafka. It has its
// own topic and consumer group so its committed offsets — the thing this sample
// is about — can be inspected without mixing them into the default
// subscription's group.
//
// "Manual" in gofy does NOT mean the handler calls CommitRecords. With
// CommitModeManual the transport disables franz-go's auto commit and, once a
// partition's batch is done, commits the last record the handler accepted.
// The handler's return value is therefore the commit decision:
//
//   - nil           -> acknowledged; this offset is included in the commit
//   - retryable err -> not committed; Consume stops, the worker restarts and
//     the failed record and everything after it are redelivered
//   - Permanent err -> parked as dead but still acknowledged, so a poison
//     message cannot hold the partition forever
//
// This is why the handler below never touches a Kafka client: doing its own
// CommitRecords would commit an offset the shared chain has not finished
// processing, and a later Permanent/panic could then lose the message.
//
// orderKafkaManual 是 orderKafka 的手动提交对照订阅。它有自己的 topic 与消费组，
// 这样本样例真正要展示的「已提交位点」可以单独查看，不会混进默认订阅的组里。
//
// gofy 里的「手动」**不是**让 handler 自己调 CommitRecords。CommitModeManual 下传输层
// 关闭 franz-go 的自动提交，并在某分区的一批处理完后，提交 handler 接受的最后一条记录。
// 因此 handler 的返回值就是提交决策：
//
//   - nil           -> 确认；该位点会被提交
//   - 可重试错误     -> 不提交；Consume 停止，worker 重启，失败记录及其后的记录重投
//   - Permanent 错误 -> 记为 dead 但仍然确认，避免毒消息永久卡住分区
//
// 所以下面的 handler 完全不碰 Kafka 客户端：自己 CommitRecords 会提交共用链路尚未处理完
// 的位点，之后遇到 Permanent 或 panic 就可能丢消息。
var orderKafkaManual = gofyconsumer.KafkaConfig{
	Name: "order-kafka-manual-consumer",
	// The same kafka.default block as every other subscription; only the topic
	// and the group differ. 与其它订阅共用 kafka.default 连接段，只有 topic 与组不同。
	Instance:    "default",
	Topic:       "demo.order.manual",
	Group:       "demo-service-order-manual-consumer",
	Concurrency: 1,
	// Written out even though it is the default: this file is the reference for
	// what manual commit means. 即使它是默认值也显式写出：本文件就是手动提交的参照。
	CommitMode:      "manual",
	AutoOffsetReset: "earliest",
	// Keep the batch — handler, then commit — together across a rebalance.
	// Without it a revoke can land between the two, the commit fails and the
	// whole batch is replayed. 把「先 handler 后提交」这一批锁在一起；否则重平衡落在
	// 两者之间会让提交失败、整批重放。
	BlockRebalanceOnPoll: true,
	// A retryable failure needs a ledger to count attempts, otherwise the same
	// record would be redelivered forever. 可重试失败需要台账计次数，否则同一条记录会
	// 无限重投。
	Idempotency: true,
	// Three instead of the default five keeps the redelivery demo short: the
	// third claim parks the message as dead and acknowledges it, so the
	// partition moves on. 用 3 而非默认 5 缩短重投演示：第三次认领会把消息记为 dead
	// 并确认，分区得以继续。
	MaxAttempts: 3,
	// Left at false on purpose. DiscardOnError would commit a failed offset —
	// exactly the behaviour this sample exists to contrast with.
	// 刻意保持 false。DiscardOnError 会提交失败记录的位点，正是本样例要对照的行为。
	DiscardOnError: false,
	Handler:        handleOrderKafkaManual,
}

// handleOrderKafkaManual shows what a manual-commit handler actually decides.
// It runs the same business branch as every other consumer, then logs which of
// the three acknowledgement paths the shared chain will take. Returning err
// unchanged is essential: swallowing it would turn "keep the offset" into
// "commit the offset".
//
// msg.Record is the native *kgo.Record. It is the value CommitRecords takes,
// and here it supplies the partition and offset the envelope does not carry —
// the two numbers to look for in the log when checking whether a redelivery
// really came back to the same position.
//
// handleOrderKafkaManual 展示手动提交的 handler 到底决定什么。它先走与其它消费者相同的
// 业务分支，再记下共用链路会走三条确认路径里的哪一条。必须原样返回 err：吞掉错误等于把
// 「保留位点」改成「提交位点」。
//
// msg.Record 是原生 *kgo.Record，也就是 CommitRecords 接收的值；这里用它取信封没有的
// 分区与位点——核对重投是否真的回到同一位置时，看的就是这两个数。
func handleOrderKafkaManual(ctx context.Context, cfg gofyconsumer.KafkaConfig, msg gofyconsumer.KafkaMessage) error {
	err := applyOrderEvent(ctx, msg.Envelope)
	logCommitDecision(ctx, cfg, msg, err)
	return err
}

// logCommitDecision makes the acknowledgement rule visible in one line. The
// shared chain already logs "message consumed" / "message discarded" /
// "message handling failed"; this line adds the offset consequence of each.
// logCommitDecision 用一行日志把确认规则显出来。共用链路已有「message consumed」等日志，
// 这一行补的是每种结果对位点的影响。
func logCommitDecision(ctx context.Context, cfg gofyconsumer.KafkaConfig, msg gofyconsumer.KafkaMessage, err error) {
	rt, ok := gofyworker.RuntimeFrom(ctx)
	if !ok || rt.Logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("consumer", cfg.Name),
		zap.String("commit_mode", cfg.CommitMode),
		zap.String("topic", cfg.Topic),
		zap.String("event_id", msg.Envelope.ID),
	}
	if rec := msg.Record; rec != nil {
		fields = append(fields,
			zap.Int32("partition", rec.Partition),
			zap.Int64("offset", rec.Offset),
			zap.ByteString("record_key", rec.Key),
		)
	}
	switch {
	case err == nil:
		fields = append(fields, zap.String("decision", "commit this offset"))
	case gofyconsumer.IsPermanent(err):
		// A bad payload will never become good: park it and commit, or one
		// poison record stops the whole partition.
		// 坏 payload 不会自己变好：记为 dead 并提交，否则一条毒消息会卡住整个分区。
		fields = append(fields, zap.String("decision", "park as dead, commit anyway"))
	default:
		fields = append(fields, zap.String("decision", "hold offset, redeliver"))
	}
	rt.Logger.Info("kafka manual commit decision", fields...)
}
