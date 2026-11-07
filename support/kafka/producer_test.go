package kafka

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
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
