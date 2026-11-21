package rocketmq

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
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

// TestIntegrationNackRedelivers: a failing handler leaves the message
// unacknowledged, the broker hides it for InvisibleDuration and redelivers it
// with an increased DeliveryAttempt. Success on the second delivery acks it.
// TestIntegrationNackRedelivers：handler 失败让消息保持未确认，broker 在
// InvisibleDuration 之后重投并递增 DeliveryAttempt；第二次成功后确认。
func TestIntegrationNackRedelivers(t *testing.T) {
	cfg := Config{Endpoint: testEndpoint(t), SendTimeout: 10 * time.Second}
	client, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	topic := testTopic(t)
	if _, err := client.PublishJSON(context.Background(), map[string]string{"id": "redeliver"}, PublishOptions{
		Topic: topic,
		Tag:   "nack",
		Keys:  []string{"redeliver-1"},
	}); err != nil {
		t.Fatalf("PublishJSON: %v", err)
	}

	consumer, err := NewConsumer(cfg, ConsumerConfig{
		Topic:             topic,
		Group:             integrationName(t, "group"),
		Tags:              []string{"nack"},
		BatchSize:         1,
		AwaitDuration:     3 * time.Second,
		InvisibleDuration: 2 * time.Second, // short so the redelivery lands quickly
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(func() { _ = consumer.Close() })

	consumeCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var (
		mu        sync.Mutex
		attempts  []int32
		messageID string
	)
	consumeDone := make(chan error, 1)
	go func() {
		consumeDone <- consumer.Consume(consumeCtx, func(_ context.Context, message *Message) error {
			mu.Lock()
			attempts = append(attempts, message.DeliveryAttempt)
			messageID = message.MessageID
			count := len(attempts)
			mu.Unlock()
			if count == 1 {
				// First delivery fails: no ack, broker redelivers.
				// 第一次失败：不 ack，broker 重投。
				return errors.New("integration: fail the first delivery")
			}
			cancel()
			return nil
		}, ConsumeOptions{})
	}()

	select {
	case err := <-consumeDone:
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("redelivery never arrived")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) < 2 {
		t.Fatalf("delivered %d time(s), want at least 2", len(attempts))
	}
	if messageID == "" {
		t.Fatal("MessageID is empty")
	}
	if attempts[1] <= attempts[0] {
		t.Fatalf("DeliveryAttempt did not increase: first=%d second=%d", attempts[0], attempts[1])
	}
}

// TestIntegrationInvisibleDurationHidesDelivery: while one member of the group
// is inside the invisible window, the same message must not be handed to the
// other member; after a successful ack it must not come back at all.
// TestIntegrationInvisibleDurationHidesDelivery：在不可见窗口内，同一条消息不能
// 同时交给组内另一个成员；成功确认后它不应再回来。
func TestIntegrationInvisibleDurationHidesDelivery(t *testing.T) {
	cfg := Config{Endpoint: testEndpoint(t), SendTimeout: 10 * time.Second}
	client, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	topic := testTopic(t)
	if _, err := client.PublishJSON(context.Background(), map[string]string{"id": "invisible"}, PublishOptions{
		Topic: topic,
		Tag:   "visible",
		Keys:  []string{"invisible-1"},
	}); err != nil {
		t.Fatalf("PublishJSON: %v", err)
	}

	group := integrationName(t, "group")
	newConsumer := func(name string) *Consumer {
		c, err := NewConsumer(cfg, ConsumerConfig{
			Topic:             topic,
			Group:             group,
			Tags:              []string{"visible"},
			BatchSize:         1,
			AwaitDuration:     2 * time.Second,
			InvisibleDuration: 10 * time.Second, // far beyond the test window
		})
		if err != nil {
			t.Fatalf("NewConsumer %s: %v", name, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	var (
		mu       sync.Mutex
		handled  = map[string]int{} // consumer name -> count
		firstID  string
		firstAck chan struct{}
	)
	firstAck = make(chan struct{})
	firstDone := make(chan error, 1)
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer firstCancel()
	go func() {
		firstDone <- newConsumer("first").Consume(firstCtx, func(_ context.Context, message *Message) error {
			mu.Lock()
			handled["first"]++
			if firstID == "" {
				firstID = message.MessageID
			}
			mu.Unlock()
			// Stay inside the invisible window for a moment, then ack.
			// 在不可见窗口内停留片刻，然后确认。
			time.Sleep(time.Second)
			close(firstAck)
			return nil
		}, ConsumeOptions{})
	}()

	// Wait until the first consumer holds the message, then let the second
	// member poll during the invisible window.
	// 等第一个消费者拿到消息，再让第二个成员在不可见窗口内拉取。
	select {
	case <-firstAck:
	case <-time.After(30 * time.Second):
		t.Fatal("first consumer never received the message")
	}

	secondDone := make(chan error, 1)
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 8*time.Second)
	go func() {
		secondDone <- newConsumer("second").Consume(secondCtx, func(_ context.Context, message *Message) error {
			mu.Lock()
			handled["second"]++
			mu.Unlock()
			return nil
		}, ConsumeOptions{})
	}()

	// Poll for the remainder of the invisible window; nothing may be handed
	// to the second member.
	// 在不可见窗口的剩余时间内轮询；任何消息都不能交给第二个成员。
	time.Sleep(5 * time.Second)
	mu.Lock()
	secondSaw := handled["second"]
	firstSaw := handled["first"]
	mu.Unlock()
	if secondSaw != 0 {
		t.Fatalf("second member received %d message(s) inside the invisible window", secondSaw)
	}
	if firstSaw != 1 {
		t.Fatalf("first member handled %d message(s), want exactly 1", firstSaw)
	}

	// The first member acked (handler returned nil). Nothing may be
	// redelivered after the invisible duration expires.
	// 第一个成员已确认（handler 返回 nil）。不可见时长过期后不得重投。
	time.Sleep(6 * time.Second)
	mu.Lock()
	secondSaw, firstSaw = handled["second"], handled["first"]
	redelivered := handled["first"] > 1
	mu.Unlock()
	if redelivered {
		t.Fatalf("acknowledged message was redelivered to the first member")
	}
	if secondSaw != 0 {
		t.Fatalf("acknowledged message was handed to the second member")
	}
	_ = firstSaw

	secondCancel()
	_ = <-secondDone
	_ = <-firstDone
}
