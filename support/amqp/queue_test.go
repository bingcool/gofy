package amqp

import (
	"context"
	"errors"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

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
