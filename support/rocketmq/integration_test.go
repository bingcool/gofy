package rocketmq

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests need a reachable RocketMQ 5.x proxy gRPC endpoint. The producer
// auto-creates the topic (broker default), so no manual provisioning is needed.
// Run them with:
//
//	GOFY_TEST_ROCKETMQ_ENDPOINT=127.0.0.1:8081 go test ./support/rocketmq -run Integration
// 这些测试需要可访问的 RocketMQ 5.x proxy gRPC 端点。Producer 会按 broker 默认
// 策略自动创建 topic，无需手工准备。运行方式见上方命令。

func testEndpoint(t *testing.T) string {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("GOFY_TEST_ROCKETMQ_ENDPOINT"))
	if endpoint == "" {
		t.Skip("GOFY_TEST_ROCKETMQ_ENDPOINT is not set")
	}
	return endpoint
}

func integrationName(t *testing.T, prefix string) string {
	t.Helper()
	name := strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())
	return fmt.Sprintf("gofy-%s-%s-%d", prefix, strings.ToLower(name), time.Now().UnixNano())
}

func TestIntegrationPublishConsume(t *testing.T) {
	cfg := Config{
		Endpoint:    testEndpoint(t),
		SendTimeout: 10 * time.Second,
	}
	client, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	topic := integrationName(t, "topic")
	result, err := client.PublishJSON(context.Background(), map[string]string{"id": "1"}, PublishOptions{
		Topic: topic,
		Tag:   "created",
		Keys:  []string{"order-1"},
	})
	if err != nil {
		t.Fatalf("PublishJSON: %v", err)
	}
	if result.MessageID == "" || result.Topic != topic {
		t.Fatalf("SendResult = %+v", result)
	}

	consumer, err := NewConsumer(cfg, ConsumerConfig{
		Topic:         topic,
		Group:         integrationName(t, "group"),
		Tags:          []string{"created"},
		BatchSize:     1,
		AwaitDuration: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(func() { _ = consumer.Close() })

	consumeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	received := make(chan *Message, 1)
	consumeDone := make(chan error, 1)
	go func() {
		consumeDone <- consumer.Consume(consumeCtx, func(_ context.Context, message *Message) error {
			select {
			case received <- message:
			default:
			}
			return nil
		}, ConsumeOptions{})
	}()

	select {
	case message := <-received:
		if string(message.Body) != `{"id":"1"}` {
			t.Fatalf("body = %q", message.Body)
		}
		if message.Tag != "created" || len(message.Keys) != 1 || message.Keys[0] != "order-1" {
			t.Fatalf("tag/keys = %q/%v", message.Tag, message.Keys)
		}
		if message.Properties["content-type"] != "application/json" {
			t.Fatalf("properties = %v", message.Properties)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no message was consumed")
	}

	// Give the worker a moment to acknowledge before shutting down.
	// 关闭前留给 worker 一点时间完成 ack。
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-consumeDone; err != nil {
		t.Fatalf("Consume: %v", err)
	}
}
