package rocketmq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Endpoint:         "127.0.0.1:1",
		SendTimeout:      50 * time.Millisecond,
		SkipStartupCheck: true,
	}
}

func TestBuildMessage(t *testing.T) {
	deliverAt := time.Now().Add(time.Minute).Truncate(time.Millisecond)
	message, err := buildMessage([]byte(`{"id":1}`), PublishOptions{
		Topic:        " orders ",
		Tag:          "paid",
		Keys:         []string{"order-1", "user-1", "order-1"},
		Properties:   map[string]string{"region": "cn", "trace-id": "abc"},
		DeliverAt:    deliverAt,
		MessageGroup: "order-1",
	})
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	if message.Topic != "orders" {
		t.Fatalf("topic = %q", message.Topic)
	}
	if tag := message.GetTag(); tag == nil || *tag != "paid" {
		t.Fatalf("tag = %v", tag)
	}
	if got := strings.Join(message.GetKeys(), " "); got != "order-1 user-1" {
		t.Fatalf("keys = %q, want deduplicated order", got)
	}
	if got := message.GetProperties()["region"]; got != "cn" {
		t.Fatalf("region = %q", got)
	}
	if got := message.GetDeliveryTimestamp(); got == nil || !got.Equal(deliverAt) {
		t.Fatalf("delivery timestamp = %v", got)
	}
	if group := message.GetMessageGroup(); group == nil || *group != "order-1" {
		t.Fatalf("message group = %v", group)
	}
	if string(message.Body) != `{"id":1}` {
		t.Fatalf("body = %q", message.Body)
	}
}

func TestBuildMessageRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		opts PublishOptions
		want error
	}{
		{name: "no topic", opts: PublishOptions{}, want: ErrTopicRequired},
		{
			name: "delay in the past",
			opts: PublishOptions{Topic: "t", DeliverAt: time.Now().Add(-time.Second)},
			want: ErrInvalidDelay,
		},
		{name: "empty key", opts: PublishOptions{Topic: "t", Keys: []string{" "}}, want: ErrInvalidKey},
		{name: "key with whitespace", opts: PublishOptions{Topic: "t", Keys: []string{"order 1"}}, want: ErrInvalidKey},
		{
			name: "oversized key",
			opts: PublishOptions{Topic: "t", Keys: []string{strings.Repeat("k", maxKeyBytes+1)}},
			want: ErrInvalidKey,
		},
		{name: "too many keys", opts: PublishOptions{Topic: "t", Keys: manyKeys(maxKeys + 1)}, want: ErrTooManyKeys},
		{
			name: "invalid property key",
			opts: PublishOptions{Topic: "t", Properties: map[string]string{"bad key": "v"}},
			want: ErrInvalidPropertyKey,
		},
		{
			name: "reserved property key",
			opts: PublishOptions{Topic: "t", Properties: map[string]string{"__internal": "v"}},
			want: ErrInvalidPropertyKey,
		},
		{
			name: "empty property value",
			opts: PublishOptions{Topic: "t", Properties: map[string]string{"region": ""}},
			want: ErrInvalidPropertyValue,
		},
		{
			name: "oversized property value",
			opts: PublishOptions{Topic: "t", Properties: map[string]string{"region": strings.Repeat("v", maxPropertyValueBytes+1)}},
			want: ErrInvalidPropertyValue,
		},
		{
			name: "too many properties",
			opts: PublishOptions{Topic: "t", Properties: manyProperties(maxProperties + 1)},
			want: ErrTooManyProperties,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildMessage(nil, test.opts)
			if err == nil {
				t.Fatal("buildMessage succeeded, want an error")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("buildMessage = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBuildMessageRejectsOversizedProperties(t *testing.T) {
	properties := make(map[string]string, 20)
	for i := 0; i < 20; i++ {
		properties[fmt.Sprintf("key.%02d", i)] = strings.Repeat("v", maxPropertyValueBytes)
	}
	_, err := buildMessage(nil, PublishOptions{Topic: "t", Properties: properties})
	if !errors.Is(err, ErrPropertiesTooLarge) {
		t.Fatalf("buildMessage = %v, want ErrPropertiesTooLarge", err)
	}
}

func TestPublishRequiresClient(t *testing.T) {
	var client *Client
	if _, err := client.Publish(context.Background(), nil, PublishOptions{Topic: "t"}); !errors.Is(err, ErrClientRequired) {
		t.Fatalf("Publish = %v, want ErrClientRequired", err)
	}
	if _, err := client.PublishJSON(context.Background(), nil, PublishOptions{Topic: "t"}); !errors.Is(err, ErrClientRequired) {
		t.Fatalf("PublishJSON = %v, want ErrClientRequired", err)
	}
}

func TestNewFailsWhenEndpointUnreachable(t *testing.T) {
	cfg := Config{
		Endpoint:      "127.0.0.1:1",
		SendTimeout:   50 * time.Millisecond,
		StartupTopics: []string{"orders"},
	}

	_, err := New(context.Background(), cfg)
	if err == nil {
		t.Fatal("New succeeded, want a connection error")
	}
	if !strings.Contains(err.Error(), cfg.Endpoint) {
		t.Fatalf("error does not mention the endpoint: %v", err)
	}
}

func TestNewSkipsStartupCheck(t *testing.T) {
	cfg := testConfig(t)
	client, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.IsClosed() {
		t.Fatal("new client reports closed")
	}
	got := client.Config()
	if got.Endpoint != cfg.Endpoint || got.MaxAttempts != defaultMaxAttempts {
		t.Fatalf("config = %+v", got)
	}
	if client.Raw() == nil {
		t.Fatal("Raw returned nil")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.IsClosed() {
		t.Fatal("client not closed after Close")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := client.Publish(context.Background(), nil, PublishOptions{Topic: "t"}); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Publish after Close = %v, want ErrClientClosed", err)
	}
}

func TestWrapHasNoConfig(t *testing.T) {
	client := Wrap(nil)
	if !client.IsClosed() {
		t.Fatal("a wrapped nil producer should report closed")
	}
	if got := client.Config(); got.Endpoint != "" || len(got.StartupTopics) != 0 {
		t.Fatalf("wrapped config = %+v, want zero", got)
	}
	if _, err := client.Publish(context.Background(), nil, PublishOptions{Topic: "t"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Publish = %v, want ErrNotConfigured", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func manyKeys(n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%d", i)
	}
	return keys
}

func manyProperties(n int) map[string]string {
	properties := make(map[string]string, n)
	for i := 0; i < n; i++ {
		properties[fmt.Sprintf("key-%d", i)] = "v"
	}
	return properties
}
