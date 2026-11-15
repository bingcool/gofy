package rocketmq

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests need a reachable RocketMQ 5.x proxy gRPC endpoint. Run them with:
//
//	GOFY_TEST_ROCKETMQ_ENDPOINT=127.0.0.1:8081 go test ./support/rocketmq -run Integration
//
// A 5.x proxy does not create topics for gRPC producers even when the broker
// has autoCreateTopicEnable=true. On such a deployment create the topic with
// mqadmin and pass it in GOFY_TEST_ROCKETMQ_TOPIC; without it each run uses a
// unique name and relies on the broker's auto-creation.
// 这些测试需要可访问的 RocketMQ 5.x proxy gRPC 端点。运行方式：
//
//	GOFY_TEST_ROCKETMQ_ENDPOINT=127.0.0.1:8081 go test ./support/rocketmq -run Integration
//
// 5.x proxy 不会为 gRPC producer 自动创建 topic，即使 broker 开了
// autoCreateTopicEnable；这类环境要用 mqadmin 预建 topic 并通过
// GOFY_TEST_ROCKETMQ_TOPIC 传入，否则每次运行使用唯一名称并依赖 broker 自动创建。

func testEndpoint(t *testing.T) string {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv("GOFY_TEST_ROCKETMQ_ENDPOINT"))
	if endpoint == "" {
		t.Skip("GOFY_TEST_ROCKETMQ_ENDPOINT is not set")
	}
	return endpoint
}

// testTopic lets a proxy deployment supply a pre-created topic. A unique name
// is still the default on brokers that do auto-create topics.
// testTopic 让 proxy 部署可以传入预建的 topic；能自动创建 topic 的 broker 仍默认用唯一名称。
func testTopic(t *testing.T) string {
	t.Helper()
	if topic := strings.TrimSpace(os.Getenv("GOFY_TEST_ROCKETMQ_TOPIC")); topic != "" {
		return topic
	}
	return integrationName(t, "topic")
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

	topic := testTopic(t)
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
