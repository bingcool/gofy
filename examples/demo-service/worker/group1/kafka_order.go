package group1

import (
	"context"

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
	Idempotency: true,
	Handler:     handleOrderKafka,
}

// handleOrderKafka is the business branch for one Kafka record.
//
// msg.Record is the native kgo.Record: it carries the partition, offset and
// headers the envelope does not, and it is the value a manual commit takes.
// handleOrderKafka 是一条 Kafka 记录的业务分支。
//
// msg.Record 是原生 kgo.Record：它带信封里没有的分区、位点与消息头，也是手动提交时
// 使用的值。
func handleOrderKafka(ctx context.Context, msg gofyconsumer.KafkaMessage) error {
	return applyOrderEvent(ctx, msg.Envelope)
}
