package kafka

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/bingcool/gofy/support/headerpropagation"
)

func newLazyRawClient(t *testing.T) *kgo.Client {
	t.Helper()
	raw, err := kgo.NewClient(kgo.SeedBrokers("127.0.0.1:1"))
	if err != nil {
		t.Fatalf("new kgo client: %v", err)
	}
	t.Cleanup(raw.Close)
	return raw
}

func TestNewProducerValidatesInput(t *testing.T) {
	client := Wrap(newLazyRawClient(t))

	if _, err := NewProducer(nil, ProducerConfig{Topic: "orders"}); !errors.Is(err, ErrClientRequired) {
		t.Fatalf("nil client error = %v", err)
	}
	if _, err := NewProducer(Wrap(nil), ProducerConfig{Topic: "orders"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("wrapped nil error = %v", err)
	}
	if _, err := NewProducer(client, ProducerConfig{}); !errors.Is(err, ErrTopicRequired) {
		t.Fatalf("empty topic error = %v", err)
	}
	if _, err := NewProducer(client, ProducerConfig{Topic: "orders", FlushTimeout: -time.Second}); err == nil {
		t.Fatal("negative flush timeout accepted")
	}
}

func TestNewProducerAppliesDefaults(t *testing.T) {
	producer, err := NewProducer(Wrap(newLazyRawClient(t)), ProducerConfig{Topic: " orders "})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	cfg := producer.Config()
	if cfg.Topic != "orders" {
		t.Fatalf("topic = %q", cfg.Topic)
	}
	if cfg.FlushTimeout != 5*time.Second {
		t.Fatalf("flush timeout = %s", cfg.FlushTimeout)
	}
}

func TestProducerPublishAfterClientClose(t *testing.T) {
	client := Wrap(newLazyRawClient(t))
	producer, err := NewProducer(client, ProducerConfig{Topic: "orders"})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = producer.Publish(context.Background(), []byte("payload"), PublishOptions{})
	if !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Publish error = %v, want ErrClientClosed", err)
	}
}

func TestPublishJSONReportsMarshalError(t *testing.T) {
	client := Wrap(newLazyRawClient(t))
	producer, err := NewProducer(client, ProducerConfig{Topic: "orders"})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}

	err = producer.PublishJSON(context.Background(), func() {}, PublishOptions{})
	if err == nil || !strings.Contains(err.Error(), "marshal message") {
		t.Fatalf("PublishJSON error = %v", err)
	}
}

func TestRecordHeaders(t *testing.T) {
	if headers := recordHeaders(nil); headers != nil {
		t.Fatalf("empty headers = %#v", headers)
	}
	headers := recordHeaders(map[string]string{"x-trace-id": "abc"})
	if len(headers) != 1 || headers[0].Key != "x-trace-id" || string(headers[0].Value) != "abc" {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestWithContentType(t *testing.T) {
	headers := withContentType(nil, "application/json")
	if headers["content-type"] != "application/json" {
		t.Fatalf("headers = %#v", headers)
	}
	original := map[string]string{"content-type": "text/plain"}
	headers = withContentType(original, "application/json")
	if headers["content-type"] != "text/plain" {
		t.Fatalf("existing content-type overwritten: %#v", headers)
	}
	if original["content-type"] != "text/plain" {
		t.Fatal("caller map was mutated")
	}
}

// TestPublishHeadersDoNotMutateCaller is the regression for the audit finding:
// the merge used to write the trace headers straight into opts.Headers, so a
// publish quietly rewrote the caller's map.
// TestPublishHeadersDoNotMutateCaller 是审计结论的回归测试：合并原先把链路头直接写进
// opts.Headers，一次发布就悄悄改掉了调用方的 map。
func TestPublishHeadersDoNotMutateCaller(t *testing.T) {
	ctx := headerpropagation.ContextWithHeader(context.Background(),
		headerpropagation.HeaderTraceID, "trace-from-ctx")
	caller := map[string]string{"content-type": "application/json"}

	got := publishHeaders(ctx, caller)

	if len(caller) != 1 || caller["content-type"] != "application/json" {
		t.Fatalf("caller map was mutated: %v", caller)
	}
	if _, ok := caller[headerpropagation.HeaderTraceID]; ok {
		t.Fatalf("trace header leaked into the caller map: %v", caller)
	}
	if got[headerpropagation.HeaderTraceID] != "trace-from-ctx" {
		t.Fatalf("trace header missing from the merged set: %v", got)
	}
	if got["content-type"] != "application/json" {
		t.Fatalf("caller header lost in the merge: %v", got)
	}
}

// TestPublishHeadersCallerWins keeps the documented precedence: a producer that
// names its own trace id means it.
// TestPublishHeadersCallerWins 保持已写入文档的优先级：生产者自己指定的 trace id 是有意的。
func TestPublishHeadersCallerWins(t *testing.T) {
	ctx := headerpropagation.ContextWithHeader(context.Background(),
		headerpropagation.HeaderTraceID, "from-ctx")

	got := publishHeaders(ctx, map[string]string{headerpropagation.HeaderTraceID: "caller-wins"})
	if got[headerpropagation.HeaderTraceID] != "caller-wins" {
		t.Fatalf("trace id = %q, want the caller's value", got[headerpropagation.HeaderTraceID])
	}
}

// TestPublishHeadersWithoutTraceContext pins the no-op case: with nothing to
// add there is no copy, and a nil caller stays nil.
// TestPublishHeadersWithoutTraceContext 固定无事可做的情形：没有可新增的头就不复制，
// nil 调用方仍然是 nil。
func TestPublishHeadersWithoutTraceContext(t *testing.T) {
	if got := publishHeaders(context.Background(), nil); got != nil {
		t.Fatalf("headers = %v, want nil", got)
	}
	caller := map[string]string{"k": "v"}
	got := publishHeaders(context.Background(), caller)
	if len(got) != 1 || got["k"] != "v" {
		t.Fatalf("headers = %v, want the caller's map untouched", got)
	}
}
