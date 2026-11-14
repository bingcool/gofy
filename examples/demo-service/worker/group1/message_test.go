package group1

import (
	"testing"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/twmb/franz-go/pkg/kgo"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	"github.com/bingcool/gofy/support/queues"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
)

// The handling chain lives in support/consumer and is tested there. What stays
// here is the part each protocol owns: turning its own record type into a
// shared Message, and picking the idempotency key out of it. Those two
// decisions are exactly what a new adapter has to get right, so they are worth
// pinning per protocol.
// 处理链路在 support/consumer，测试也在那里。留在这里的是每种协议自己负责的部分：
// 把自己的记录类型转成共用的 Message，并从中挑出幂等键。这两件事正是新增适配器必须
// 做对的地方，因此按协议逐个固定下来。

// TestKafkaMessageID pins the deduplication key: the producer header wins, and
// the partition offset is the fallback because it is stable across
// redeliveries of the same record.
// TestKafkaMessageID 固定去重键：生产者头优先，分区位点兜底，因为它在同一条记录的
// 重投之间保持稳定。
func TestKafkaMessageID(t *testing.T) {
	record := &kgo.Record{Topic: "orders", Partition: 2, Offset: 42}
	if got := kafkaMessageID(record, nil); got != "orders/2@42" {
		t.Fatalf("id = %q, want orders/2@42", got)
	}
	if got := kafkaMessageID(record, map[string]string{"message_id": "evt-9"}); got != "evt-9" {
		t.Fatalf("id = %q, want evt-9", got)
	}
}

// TestKafkaMessageKeepsHeaders checks that traceparent survives the conversion:
// without it every consumed message starts a new trace instead of continuing
// the producer's.
// TestKafkaMessageKeepsHeaders 校验 traceparent 在转换中保留：丢了它，每条消费
// 消息都会新起一条链路，而不是接续生产者。
func TestKafkaMessageKeepsHeaders(t *testing.T) {
	record := &kgo.Record{
		Topic:     "orders",
		Partition: 1,
		Offset:    9,
		Key:       []byte("user-7"),
		Value:     []byte(`{"type":"order.paid"}`),
		Headers: []kgo.RecordHeader{
			{Key: "traceparent", Value: []byte("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")},
		},
	}
	msg := kafkaMessage(record)
	if msg.ID != "orders/1@9" {
		t.Fatalf("ID = %q", msg.ID)
	}
	if msg.Headers["traceparent"] == "" {
		t.Fatal("traceparent header was dropped")
	}
	if msg.Key != "user-7" {
		t.Fatalf("Key = %q, want user-7", msg.Key)
	}
}

// TestAMQPMessageID documents the deliberate empty fallback: without a
// MessageId the message is not deduplicated, which is safer than collapsing two
// different messages that share a routing key.
// TestAMQPMessageID 记录刻意的空兜底：没有 MessageId 的消息不做去重，这比把两条
// 共用 routing key 的不同消息当成同一条更安全。
func TestAMQPMessageID(t *testing.T) {
	if got := amqpMessageID(amqp091.Delivery{}, nil); got != "" {
		t.Fatalf("id = %q, want empty", got)
	}
	if got := amqpMessageID(amqp091.Delivery{MessageId: "msg-1"}, nil); got != "msg-1" {
		t.Fatalf("id = %q, want msg-1", got)
	}
	headers := map[string]string{"X-Correlation-Id": "corr-1"}
	if got := amqpMessageID(amqp091.Delivery{}, headers); got != "corr-1" {
		t.Fatalf("id = %q, want corr-1", got)
	}
}

// TestRocketMQMessageID prefers the broker id over the offset.
// TestRocketMQMessageID 优先使用 broker 的消息 ID，而不是位点。
func TestRocketMQMessageID(t *testing.T) {
	if got := rocketmqMessageID(&gofyrocketmq.Message{MessageID: "A1", Topic: "t", Offset: 3}); got != "A1" {
		t.Fatalf("id = %q, want A1", got)
	}
	if got := rocketmqMessageID(&gofyrocketmq.Message{Topic: "t", Offset: 3}); got != "t@3" {
		t.Fatalf("id = %q, want t@3", got)
	}
}

// TestQueuesMessageMapsRetryCount checks the attempt conversion: RetryNum is
// zero-based because the queue counts retries, while Attempt is one-based
// because the ledger counts deliveries.
// TestQueuesMessageMapsRetryCount 校验尝试次数的换算：RetryNum 从 0 开始（队列
// 计的是重试次数），Attempt 从 1 开始（台账计的是投递次数）。
func TestQueuesMessageMapsRetryCount(t *testing.T) {
	msg := queuesMessage("demo:order", &queues.Message{
		ID:        "q-1",
		RetryNum:  2,
		Timestamp: 1_700_000_000,
		Payload:   []byte(`{"type":"order.paid","payload":{"order_id":1}}`),
	})
	if msg.Attempt != 3 {
		t.Fatalf("Attempt = %d, want 3", msg.Attempt)
	}
	if msg.ID != "q-1" {
		t.Fatalf("ID = %q, want q-1", msg.ID)
	}
	if msg.Enqueued.Unix() != 1_700_000_000 {
		t.Fatalf("Enqueued = %v", msg.Enqueued)
	}
	if _, err := gofyconsumer.DecodeEnvelope(msg); err != nil {
		t.Fatalf("the queue payload must decode as an envelope: %v", err)
	}
}
