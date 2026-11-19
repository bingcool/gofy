package amqp

import (
	"context"
	"errors"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/internal/inflight"
)

func TestNewQueueRequiresClientAndValidTopology(t *testing.T) {
	if _, err := NewQueue(nil, QueueConfig{}); !errors.Is(err, ErrClientRequired) {
		t.Fatalf("nil client error = %v", err)
	}
	client := Wrap(nil)
	if _, err := NewQueue(client, QueueConfig{}); !errors.Is(err, ErrExchangeRequired) {
		t.Fatalf("invalid topology error = %v", err)
	}
	q, err := NewQueue(client, QueueConfig{Topology: Topology{Exchange: "events", ExchangeType: ExchangeFanout}})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	if q.Config().Exchange != "events" {
		t.Fatalf("config = %+v", q.Config())
	}
}

func TestConsumeRequiresHandlerAndQueue(t *testing.T) {
	client := Wrap(nil)
	q, err := NewQueue(client, QueueConfig{Topology: Topology{Exchange: "events", ExchangeType: ExchangeFanout}})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	handler := func(context.Context, amqp091.Delivery) error { return nil }

	if err := q.Consume(context.Background(), nil, ConsumeOptions{}); !errors.Is(err, ErrHandlerRequired) {
		t.Fatalf("nil handler error = %v", err)
	}
	if err := q.Consume(context.Background(), handler, ConsumeOptions{}); !errors.Is(err, ErrQueueRequired) {
		t.Fatalf("missing queue error = %v", err)
	}
}

func TestPublishWithoutConnectionReportsNotConfigured(t *testing.T) {
	q, err := NewQueue(Wrap(nil), QueueConfig{Topology: Topology{Exchange: "events", ExchangeType: ExchangeFanout}})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	if err := q.Publish(context.Background(), []byte("payload"), PublishOptions{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Publish error = %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPublishJSONRejectsUnmarshalableValue(t *testing.T) {
	q, err := NewQueue(Wrap(nil), QueueConfig{Topology: Topology{Exchange: "events", ExchangeType: ExchangeFanout}})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	if err := q.PublishJSON(context.Background(), make(chan int), PublishOptions{}); err == nil {
		t.Fatal("PublishJSON accepted an unmarshalable value")
	}
}

func TestWrappedClientWithoutConnection(t *testing.T) {
	client := Wrap(nil)
	if !client.IsClosed() {
		t.Fatal("client without a connection must report closed")
	}
	if _, err := client.Channel(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Channel error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := client.Channel(); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Channel after Close error = %v", err)
	}
}

func TestConsumeOptionsDefaults(t *testing.T) {
	opts := ConsumeOptions{}.withDefaults()
	if opts.PrefetchCount != 1 {
		t.Fatalf("prefetch = %d", opts.PrefetchCount)
	}
	if opts.ReconnectDelay != 3*time.Second {
		t.Fatalf("reconnect delay = %s", opts.ReconnectDelay)
	}
	if opts.InFlightDelay != inflight.DefaultDelay {
		t.Fatalf("in-flight delay = %s, want %s", opts.InFlightDelay, inflight.DefaultDelay)
	}

	explicit := ConsumeOptions{PrefetchCount: 20, ReconnectDelay: time.Second}.withDefaults()
	if explicit.PrefetchCount != 20 || explicit.ReconnectDelay != time.Second {
		t.Fatalf("explicit options were overwritten: %+v", explicit)
	}
}

func TestCallHandlerRecoversPanic(t *testing.T) {
	err := callHandler(context.Background(), func(context.Context, amqp091.Delivery) error {
		panic("boom")
	}, amqp091.Delivery{})
	if err == nil {
		t.Fatal("panic was not converted to an error")
	}
}

// TestPublishHeadersDoNotMutateCaller is the regression for the audit finding:
// the merge used to write the trace headers straight into opts.Headers, so a
// publish quietly rewrote the caller's table.
// TestPublishHeadersDoNotMutateCaller 是审计结论的回归测试：合并原先把链路头直接写进
// opts.Headers，一次发布就悄悄改掉了调用方的表。
func TestPublishHeadersDoNotMutateCaller(t *testing.T) {
	ctx := headerpropagation.ContextWithHeader(context.Background(),
		headerpropagation.HeaderTraceID, "trace-from-ctx")
	caller := amqp091.Table{"content-type": "application/json"}

	got := publishHeaders(ctx, caller)

	if len(caller) != 1 || caller["content-type"] != "application/json" {
		t.Fatalf("caller table was mutated: %v", caller)
	}
	if _, ok := caller[headerpropagation.HeaderTraceID]; ok {
		t.Fatalf("trace header leaked into the caller table: %v", caller)
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

	got := publishHeaders(ctx, amqp091.Table{headerpropagation.HeaderTraceID: "caller-wins"})
	if got[headerpropagation.HeaderTraceID] != "caller-wins" {
		t.Fatalf("trace id = %v, want the caller's value", got[headerpropagation.HeaderTraceID])
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
	caller := amqp091.Table{"k": "v"}
	got := publishHeaders(context.Background(), caller)
	if len(got) != 1 || got["k"] != "v" {
		t.Fatalf("headers = %v, want the caller's table untouched", got)
	}
}
