package group1

import (
	"context"
	"time"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
)

// orderKafka is one subscription, described in one file the way scanDefaults
// describes the scan loop. A second topic is a second file, not another field
// in a shared block.
// orderKafka 是一个订阅，像 scanDefaults 描述扫描循环那样用一个文件描述它。第二个
// topic 就是第二个文件，而不是共享配置块里再加一个字段。
var orderKafka = gofyconsumer.KafkaConfig{
	Name: "order-kafka-consumer",
	// Instance picks the kafka.default block from config.yaml: the brokers live
	// there once, shared by producers and consumers.
	// Instance 指向 config.yaml 的 kafka.default：broker 地址在那里配一次，
	// 生产者与消费者共用。
	Instance:    "default",
	Topic:       "demo.order.events",
	Group:       "demo-service-order-consumer",
	Concurrency: 1,
	// Auto commit interval. This subscription commits manually, so the value
	// only takes effect if CommitMode becomes "auto"; the 200ms default is
	// written out to show where that knob is.
	// 自动提交间隔。本订阅是手动提交，因此只有 CommitMode 改成 "auto" 才生效；
	// 这里写出 200ms 默认值，标出调参的位置。
	AutoCommitInterval: 200 * time.Millisecond,
	// Finish this batch — handler, then commit — before letting a rebalance
	// proceed. franz-go heartbeats independently, so a slow handler never drops
	// the member out of the group; this closes the smaller window where a
	// rebalance lands between the handler and its commit.
	// 这一批处理并提交完再放行重平衡。franz-go 独立心跳，慢 handler 不会把成员踢出
	// 组；这个开关关掉的是更小的窗口——重平衡恰好落在 handler 与提交之间。
	BlockRebalanceOnPoll: true,
	Idempotency:          true,
	Handler:              handleOrderKafka,
}

// handleOrderKafka is the business branch for one Kafka record.
//
// cfg is the subscription that received it — topic, group, idempotency switch —
// so a handler can log or branch on its own settings without reaching into the
// consumer. msg.Record is the native kgo.Record: it carries the partition,
// offset and headers the envelope does not, and it is the value a manual commit
// takes.
// handleOrderKafka 是一条 Kafka 记录的业务分支。
//
// cfg 是收到它的那个订阅——topic、group、幂等开关——因此 handler 可以直接就自己的
// 设置记日志或分支，不必伸手去拿消费者。msg.Record 是原生 kgo.Record：它带信封里
// 没有的分区、位点与消息头，也是手动提交时使用的值。
func handleOrderKafka(ctx context.Context, cfg gofyconsumer.KafkaConfig, msg gofyconsumer.KafkaMessage) error {
	return applyOrderEvent(ctx, msg.Envelope)
}
