package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/bingcool/gofy/core"
)

// testConsumerName is the idempotency scope these tests use. It only has to be
// stable, because it is the left half of the ledger's unique key.
// testConsumerName 是这些测试使用的幂等作用域。它只需要稳定，因为它就是台账唯一
// 键的左半边。
const testConsumerName = "test-consumer"

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

// TestProcessorRequiresDatabase keeps a misconfiguration loud: idempotency
// without a database cannot be silently downgraded, because that would repeat
// every side effect on redelivery.
// TestProcessorRequiresDatabase 让配置错误足够响：没有数据库的幂等不能被静默降级，
// 否则每次重投都会重复副作用。
func TestProcessorRequiresDatabase(t *testing.T) {
	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop()})
	_, err := NewProcessor(rt, ProcessorConfig{
		Consumer:    testConsumerName,
		Handler:     func(context.Context, Message, Envelope) error { return nil },
		Idempotency: true,
		MaxAttempts: 5,
	})
	if err == nil {
		t.Fatal("NewProcessor succeeded without a database, want an error")
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
	proc, err := NewProcessor(rt, ProcessorConfig{
		Consumer:    testConsumerName,
		Handler:     func(context.Context, Message, Envelope) error { handled++; return nil },
		Idempotency: false,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
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
	permanent, err := NewProcessor(rt, ProcessorConfig{
		Consumer:    testConsumerName,
		Handler:     func(context.Context, Message, Envelope) error { return Permanent(errors.New("bad event")) },
		Idempotency: false,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	msg := Message{ID: "m-1", Source: "kafka", Body: []byte(`{"type":"order.paid"}`)}
	if err := permanent.Handle(context.Background(), msg); err != nil {
		t.Fatalf("permanent failure must be acknowledged, got %v", err)
	}

	retryable, err := NewProcessor(rt, ProcessorConfig{
		Consumer:    testConsumerName,
		Handler:     func(context.Context, Message, Envelope) error { return errors.New("database is down") },
		Idempotency: false,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
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
	proc, err := NewProcessor(rt, ProcessorConfig{
		Consumer:    testConsumerName,
		Handler:     func(context.Context, Message, Envelope) error { return nil },
		Idempotency: false,
		MaxAttempts: 5,
	})
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	if err := proc.Handle(context.Background(), Message{ID: "m-1", Body: []byte("not json")}); err != nil {
		t.Fatalf("undecodable body must be acknowledged, got %v", err)
	}
}
