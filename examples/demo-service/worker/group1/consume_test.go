package group1

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	amqp091 "github.com/rabbitmq/amqp091-go"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/support/queues"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
)

// TestDecodeEnvelope covers the happy path plus the fallbacks the processor
// relies on: a missing id is filled from the transport, and an empty payload
// becomes an empty object instead of a nil slice the handler must guard.
// TestDecodeEnvelope 覆盖正常路径与处理器依赖的兜底：缺失的 ID 用传输层补上，
// 空 payload 变成空对象，而不是一个 handler 必须自己判空的 nil 切片。
func TestDecodeEnvelope(t *testing.T) {
	msg := Message{
		ID:   "kafka-1",
		Body: []byte(`{"type":"order.cancelled","occurred_at":"2026-01-02T03:04:05Z","payload":{"order_id":7}}`),
	}
	env, err := DecodeEnvelope(msg)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.Type != "order.cancelled" {
		t.Fatalf("Type = %q, want order.cancelled", env.Type)
	}
	if env.ID != "kafka-1" {
		t.Fatalf("ID = %q, want the transport id as fallback", env.ID)
	}
	if env.OccurredAt.IsZero() {
		t.Fatal("OccurredAt was not parsed")
	}
	var payload struct {
		OrderID uint64 `json:"order_id"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.OrderID != 7 {
		t.Fatalf("order_id = %d, want 7", payload.OrderID)
	}

	empty, err := DecodeEnvelope(Message{ID: "m-2", Body: []byte(`{"type":"order.paid"}`)})
	if err != nil {
		t.Fatalf("DecodeEnvelope without payload: %v", err)
	}
	if string(empty.Payload) != "{}" {
		t.Fatalf("Payload = %s, want {}", empty.Payload)
	}
}

// TestDecodeEnvelopeRejectsBadInput keeps the two permanent cases permanent: an
// unparsable body and a body without a type can never succeed on redelivery.
// TestDecodeEnvelopeRejectsBadInput 把两种永久情况固定为永久：解不出的消息体与
// 没有 type 的消息体，重投多少次都不会成功。
func TestDecodeEnvelopeRejectsBadInput(t *testing.T) {
	tests := map[string]string{
		"not json":     `{"type":`,
		"missing type": `{"payload":{}}`,
		"blank type":   `{"type":"   "}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEnvelope(Message{ID: "m-1", Body: []byte(body)}); err == nil {
				t.Fatal("DecodeEnvelope succeeded, want an error")
			}
		})
	}
}

// TestPermanent checks the wrapper keeps both the cause and the classification.
// TestPermanent 校验包装器同时保留原因与分类。
func TestPermanent(t *testing.T) {
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must stay nil so it can wrap a check directly")
	}
	cause := errors.New("bad payload")
	err := Permanent(cause)
	if !IsPermanent(err) {
		t.Fatal("IsPermanent = false, want true")
	}
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is lost the cause")
	}
	if IsPermanent(cause) {
		t.Fatal("IsPermanent(unwrapped) = true, want false")
	}
}

// TestTruncateBytes pins the rune-boundary cut: a VARCHAR(512) column must not
// receive half a Chinese character, which MySQL would reject or replace.
// TestTruncateBytes 固定字符边界截断：VARCHAR(512) 列不能收到半个汉字，MySQL 会
// 拒绝或替换它。
func TestTruncateBytes(t *testing.T) {
	if got := string(truncateBytes([]byte("订单已取消"), 4)); got != "订" {
		t.Fatalf("cut = %q, want 订", got)
	}
	if got := string(truncateBytes([]byte("ok"), 10)); got != "ok" {
		t.Fatalf("short input changed: %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc" {
		t.Fatalf("truncate = %q, want abc", got)
	}
}

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
	if _, err := DecodeEnvelope(msg); err != nil {
		t.Fatalf("the queue payload must decode as an envelope: %v", err)
	}
}

// TestProcessorRequiresDatabase keeps a misconfiguration loud: idempotency
// without a database cannot be silently downgraded, because that would repeat
// every side effect on redelivery.
// TestProcessorRequiresDatabase 让配置错误足够响：没有数据库的幂等不能被静默降级，
// 否则每次重投都会重复副作用。
func TestProcessorRequiresDatabase(t *testing.T) {
	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop()})
	_, err := newProcessor(rt, processorConfig{
		Consumer:    kafkaConsumerName,
		Handler:     func(context.Context, Envelope) error { return nil },
		Idempotency: true,
		MaxAttempts: 5,
	})
	if err == nil {
		t.Fatal("newProcessor succeeded without a database, want an error")
	}
	if !strings.Contains(err.Error(), "idempotency requires a database") {
		t.Fatalf("error = %v, want it to name the missing database", err)
	}
}

// TestProcessorWithoutIdempotencyRuns proves the chain can be used without the
// ledger, which is the escape hatch for a project that deduplicates elsewhere.
// TestProcessorWithoutIdempotencyRuns 验证链路可以不用台账，供在别处去重的项目使用。
func TestProcessorWithoutIdempotencyRuns(t *testing.T) {
	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop()})
	handled := 0
	proc, err := newProcessor(rt, processorConfig{
		Consumer:    kafkaConsumerName,
		Handler:     func(context.Context, Envelope) error { handled++; return nil },
		Idempotency: false,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("newProcessor: %v", err)
	}
	msg := Message{
		ID:     "m-1",
		Source: "kafka",
		Topic:  "orders",
		Body:   []byte(`{"type":"order.paid","payload":{"order_id":1}}`),
	}
	if err := proc.Handle(context.Background(), msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if handled != 1 {
		t.Fatalf("handler calls = %d, want 1", handled)
	}
}

// TestProcessorAcknowledgesPermanentFailure checks the acknowledgement
// decision: a permanent failure returns nil so the broker stops redelivering,
// while a retryable failure returns the error.
// TestProcessorAcknowledgesPermanentFailure 校验确认决定：永久失败返回 nil，让
// broker 停止重投；可重试失败则返回错误。
func TestProcessorAcknowledgesPermanentFailure(t *testing.T) {
	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop()})
	permanent, err := newProcessor(rt, processorConfig{
		Consumer:    kafkaConsumerName,
		Handler:     func(context.Context, Envelope) error { return Permanent(errors.New("bad event")) },
		Idempotency: false,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("newProcessor: %v", err)
	}
	msg := Message{ID: "m-1", Source: "kafka", Body: []byte(`{"type":"order.paid"}`)}
	if err := permanent.Handle(context.Background(), msg); err != nil {
		t.Fatalf("permanent failure must be acknowledged, got %v", err)
	}

	retryable, err := newProcessor(rt, processorConfig{
		Consumer:    kafkaConsumerName,
		Handler:     func(context.Context, Envelope) error { return errors.New("database is down") },
		Idempotency: false,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("newProcessor: %v", err)
	}
	if err := retryable.Handle(context.Background(), msg); err == nil {
		t.Fatal("retryable failure must return the error, got nil")
	}
}

// TestProcessorAcknowledgesUndecodableBody keeps a poison message from being
// redelivered forever: the body cannot become valid JSON later.
// TestProcessorAcknowledgesUndecodableBody 防止毒消息被无限重投：消息体不会在之后
// 变成合法 JSON。
func TestProcessorAcknowledgesUndecodableBody(t *testing.T) {
	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop()})
	proc, err := newProcessor(rt, processorConfig{
		Consumer:    kafkaConsumerName,
		Handler:     func(context.Context, Envelope) error { return nil },
		Idempotency: false,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("newProcessor: %v", err)
	}
	if err := proc.Handle(context.Background(), Message{ID: "m-1", Body: []byte("not json")}); err != nil {
		t.Fatalf("undecodable body must be acknowledged, got %v", err)
	}
}
