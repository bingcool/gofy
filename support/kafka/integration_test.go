package kafka

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// These tests need a reachable Kafka cluster and use auto-created topics with
// unique names. Run them with:
//
//	GOFY_TEST_KAFKA_BROKERS=127.0.0.1:9092 go test ./support/kafka -run Integration
// 这些测试需要可访问的 Kafka 集群，并使用唯一名称的自动创建 topic。运行方式见
// 上方命令；未设置环境变量时相关用例会跳过。

func testBrokers(t *testing.T) []string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv("GOFY_TEST_KAFKA_BROKERS"))
	if value == "" {
		t.Skip("GOFY_TEST_KAFKA_BROKERS is not set")
	}
	brokers := make([]string, 0, 2)
	for _, broker := range strings.Split(value, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			brokers = append(brokers, broker)
		}
	}
	if len(brokers) == 0 {
		t.Skip("GOFY_TEST_KAFKA_BROKERS has no usable broker")
	}
	return brokers
}

func testName(t *testing.T, prefix string) string {
	t.Helper()
	name := strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())
	return fmt.Sprintf("gofy-%s-%s-%d", prefix, name, time.Now().UnixNano())
}

func testClient(t *testing.T, brokers []string) *Client {
	t.Helper()
	client, err := New(context.Background(), Config{
		Brokers:     brokers,
		ClientID:    "gofy-test",
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// createTopic creates the test topic explicitly: production deployments usually
// provision topics outside the application, and auto-create may race with the
// first produce.
// createTopic 显式创建测试 topic：生产环境通常由应用之外预先创建 topic，而自动
// 创建可能与首次生产发生竞争。
func createTopic(t *testing.T, brokers []string, topic string) {
	t.Helper()
	raw, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("gofy-test-admin"))
	if err != nil {
		t.Fatalf("new admin client: %v", err)
	}
	defer raw.Close()

	req := kmsg.NewPtrCreateTopicsRequest()
	req.TimeoutMillis = 5000
	create := kmsg.NewCreateTopicsRequestTopic()
	create.Topic = topic
	create.NumPartitions = 3
	create.ReplicationFactor = 1
	req.Topics = append(req.Topics, create)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := raw.Request(ctx, req)
	if err != nil {
		t.Fatalf("create topic %q: %v", topic, err)
	}
	for _, result := range response.(*kmsg.CreateTopicsResponse).Topics {
		if result.ErrorCode != 0 {
			t.Fatalf("create topic %q: %v", topic, kerr.ErrorForCode(result.ErrorCode))
		}
	}
}

func TestIntegrationProduceConsumeInOrder(t *testing.T) {
	brokers := testBrokers(t)
	client := testClient(t, brokers)
	topic := testName(t, "order")
	group := testName(t, "group")
	createTopic(t, brokers, topic)

	producer, err := NewProducer(client, ProducerConfig{Topic: topic, FlushTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	for i := 0; i < 3; i++ {
		err := producer.Publish(context.Background(), []byte(fmt.Sprintf("msg-%d", i)), PublishOptions{
			Key:     "order-1",
			Headers: map[string]string{"x-index": fmt.Sprintf("%d", i)},
		})
		if err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	consumer, err := NewConsumer(client, ConsumerConfig{
		Topic:           topic,
		Group:           group,
		AutoOffsetReset: "earliest",
		SessionTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(func() { _ = consumer.Close() })

	consumeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		payloads []string
		problems []error
	)
	err = consumer.Consume(consumeCtx, func(_ context.Context, record *kgo.Record) error {
		mu.Lock()
		defer mu.Unlock()
		payloads = append(payloads, string(record.Value))
		if len(payloads) == 3 {
			cancel()
		}
		return nil
	}, ConsumeOptions{
		OnError: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			problems = append(problems, err)
		},
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(problems) > 0 {
		t.Fatalf("consume errors: %v", problems)
	}
	want := []string{"msg-0", "msg-1", "msg-2"}
	if fmt.Sprint(payloads) != fmt.Sprint(want) {
		t.Fatalf("payloads = %v, want %v", payloads, want)
	}
}

func TestIntegrationManualCommitStopsAtFailure(t *testing.T) {
	brokers := testBrokers(t)
	client := testClient(t, brokers)
	topic := testName(t, "manual")
	group := testName(t, "group")
	createTopic(t, brokers, topic)

	producer, err := NewProducer(client, ProducerConfig{Topic: topic, FlushTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := producer.Publish(context.Background(), []byte(fmt.Sprintf("msg-%d", i)), PublishOptions{Key: "k"}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	consumer, err := NewConsumer(client, ConsumerConfig{
		Topic:           topic,
		Group:           group,
		AutoOffsetReset: "earliest",
		SessionTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	consumeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	failed := false
	err = consumer.Consume(consumeCtx, func(_ context.Context, record *kgo.Record) error {
		if string(record.Value) == "msg-2" && !failed {
			failed = true
			return errors.New("boom")
		}
		return nil
	}, ConsumeOptions{})
	cancel()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Consume error = %v, want handler error", err)
	}
	if err := consumer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Manual commits stop at msg-1, so the next consumer must redeliver msg-2.
	// 手动提交只到 msg-1，因此下一个 Consumer 必须重投 msg-2。
	replay, err := NewConsumer(client, ConsumerConfig{
		Topic:           topic,
		Group:           group,
		AutoOffsetReset: "earliest",
		SessionTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewConsumer replay: %v", err)
	}
	t.Cleanup(func() { _ = replay.Close() })

	replayCtx, replayCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer replayCancel()
	var first string
	err = replay.Consume(replayCtx, func(_ context.Context, record *kgo.Record) error {
		first = string(record.Value)
		replayCancel()
		return nil
	}, ConsumeOptions{})
	if err != nil {
		t.Fatalf("replay Consume: %v", err)
	}
	if first != "msg-2" {
		t.Fatalf("first redelivered record = %q, want msg-2", first)
	}
}

func TestIntegrationWrappedConsumer(t *testing.T) {
	brokers := testBrokers(t)
	client := testClient(t, brokers)
	topic := testName(t, "wrapped")
	group := testName(t, "group")
	createTopic(t, brokers, topic)

	producer, err := NewProducer(client, ProducerConfig{Topic: topic, FlushTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	if err := producer.Publish(context.Background(), []byte("injected"), PublishOptions{Key: "k"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	raw, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID("gofy-test-wrapped"),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.SessionTimeout(10*time.Second),
	)
	if err != nil {
		t.Fatalf("new raw client: %v", err)
	}
	wrapped := Wrap(raw)
	t.Cleanup(func() { _ = wrapped.Close() })

	consumer, err := NewConsumer(wrapped, ConsumerConfig{
		Topic: topic,
		Group: group,
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(func() { _ = consumer.Close() })

	consumeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var received string
	err = consumer.Consume(consumeCtx, func(_ context.Context, record *kgo.Record) error {
		received = string(record.Value)
		cancel()
		return nil
	}, ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if received != "injected" {
		t.Fatalf("received = %q", received)
	}
}

func TestIntegrationBrokerFallback(t *testing.T) {
	brokers := testBrokers(t)
	cfg := Config{
		Brokers:     append([]string{"127.0.0.1:1"}, brokers...),
		DialTimeout: time.Second,
	}
	client, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New with fallback brokers: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestIntegrationUnreachableBrokerFailsFast(t *testing.T) {
	_, err := New(context.Background(), Config{
		Brokers:     []string{"127.0.0.1:1"},
		DialTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("New accepted an unreachable broker")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "connect") {
		t.Fatalf("New error = %v", err)
	}
}
