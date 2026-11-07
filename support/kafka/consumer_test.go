package kafka

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestConsumerConfigValidate(t *testing.T) {
	client := Wrap(newLazyRawClient(t))
	cases := []struct {
		name string
		cfg  ConsumerConfig
		want string
	}{
		{"missing topic", ConsumerConfig{Group: "g"}, "topic is required"},
		{"missing group", ConsumerConfig{Topic: "orders"}, "consumer group is required"},
		{"bad offset reset", ConsumerConfig{Topic: "orders", Group: "g", AutoOffsetReset: "middle"}, "auto_offset_reset"},
		{"negative interval", ConsumerConfig{Topic: "orders", Group: "g", AutoCommitInterval: -time.Second}, "negative"},
		{"negative session timeout", ConsumerConfig{Topic: "orders", Group: "g", SessionTimeout: -time.Second}, "negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewConsumer(client, tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewConsumer error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestNewConsumerRequiresLiveClient(t *testing.T) {
	if _, err := NewConsumer(nil, ConsumerConfig{Topic: "orders", Group: "g"}); !errors.Is(err, ErrClientRequired) {
		t.Fatalf("nil client error = %v", err)
	}
	if _, err := NewConsumer(Wrap(nil), ConsumerConfig{Topic: "orders", Group: "g"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("wrapped nil error = %v", err)
	}
}

func TestConsumerConfigDefaults(t *testing.T) {
	cfg := ConsumerConfig{Topic: " orders ", Group: " group "}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cfg = cfg.withDefaults()

	if cfg.Topic != "orders" || cfg.Group != "group" {
		t.Fatalf("topic/group = %q/%q", cfg.Topic, cfg.Group)
	}
	if cfg.AutoOffsetReset != "earliest" {
		t.Fatalf("offset reset = %q", cfg.AutoOffsetReset)
	}
	if cfg.AutoCommitInterval != 200*time.Millisecond {
		t.Fatalf("auto commit interval = %s", cfg.AutoCommitInterval)
	}
	if cfg.SessionTimeout != 45*time.Second {
		t.Fatalf("session timeout = %s", cfg.SessionTimeout)
	}
}

func TestWrappedConsumerDoesNotOwnSharedClient(t *testing.T) {
	raw := newLazyRawClient(t)
	client := Wrap(raw)
	consumer, err := NewConsumer(client, ConsumerConfig{Topic: "orders", Group: "g"})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	if err := consumer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if client.IsClosed() {
		t.Fatal("closing a wrapped consumer closed the shared client")
	}
	if client.Raw() != raw {
		t.Fatal("shared client was replaced")
	}
}

func TestConsumeRequiresHandler(t *testing.T) {
	consumer, err := NewConsumer(Wrap(newLazyRawClient(t)), ConsumerConfig{Topic: "orders", Group: "g"})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	if err := consumer.Consume(context.Background(), nil, ConsumeOptions{}); !errors.Is(err, ErrHandlerRequired) {
		t.Fatalf("Consume error = %v", err)
	}
}

func TestConsumeStopsOnCancelledContext(t *testing.T) {
	consumer, err := NewConsumer(Wrap(newLazyRawClient(t)), ConsumerConfig{Topic: "orders", Group: "g"})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := consumer.Consume(ctx, func(context.Context, *kgo.Record) error { return nil }, ConsumeOptions{}); err != nil {
		t.Fatalf("Consume: %v", err)
	}
}

func TestConsumeAfterClose(t *testing.T) {
	consumer, err := NewConsumer(Wrap(newLazyRawClient(t)), ConsumerConfig{Topic: "orders", Group: "g"})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	if err := consumer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := consumer.Consume(context.Background(), func(context.Context, *kgo.Record) error { return nil }, ConsumeOptions{}); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Consume error = %v", err)
	}
}

func TestCallHandlerRecoversPanic(t *testing.T) {
	err := callHandler(context.Background(), func(context.Context, *kgo.Record) error {
		panic("boom")
	}, &kgo.Record{})
	if err == nil || !strings.Contains(err.Error(), "handler panic") {
		t.Fatalf("panic error = %v", err)
	}
}

func TestConsumeOptionsDefaults(t *testing.T) {
	opts := ConsumeOptions{}.withDefaults()
	if opts.RetryDelay != time.Second {
		t.Fatalf("retry delay = %s", opts.RetryDelay)
	}
}
