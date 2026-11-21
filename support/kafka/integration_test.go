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

// TestIntegrationRebalanceHandsOffWithoutLoss: two consumers join the same
// group; closing the first forces a rebalance and the second must observe
// every produced message exactly through at-least-once semantics — no loss.
// TestIntegrationRebalanceHandsOffWithoutLoss：两个消费者加入同一个组；关闭第一
// 个会触发再平衡，第二个必须在 at-least-once 语义下见到全部消息——不允许丢失。
func TestIntegrationRebalanceHandsOffWithoutLoss(t *testing.T) {
	brokers := testBrokers(t)
	client := testClient(t, brokers)
	topic := testName(t, "rebalance")
	group := testName(t, "group")
	createTopic(t, brokers, topic)

	const total = 6
	producer, err := NewProducer(client, ProducerConfig{Topic: topic, FlushTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	for i := 0; i < total; i++ {
		if err := producer.Publish(context.Background(), []byte(fmt.Sprintf("msg-%d", i)), PublishOptions{
			Key: fmt.Sprintf("key-%d", i%2), // two keys so both partitions see work
		}); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	newConsumer := func() *Consumer {
		c, err := NewConsumer(client, ConsumerConfig{
			Topic:           topic,
			Group:           group,
			AutoOffsetReset: "earliest",
			SessionTimeout:  6 * time.Second,
		})
		if err != nil {
			t.Fatalf("NewConsumer: %v", err)
		}
		return c
	}

	first := newConsumer()
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer firstCancel()

	var (
		mu     sync.Mutex
		seen   = map[string]int{}
		firstN int
	)
	// The first consumer exits the group as soon as it has processed one
	// message: closing the consumer sends a LeaveGroup, which forces the
	// rebalance the test is about to observe.
	// 第一个消费者处理一条消息后就退出组：关闭消费者会发送 LeaveGroup，
	// 立即触发本用例要观察的再平衡。
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- first.Consume(firstCtx, func(_ context.Context, record *kgo.Record) error {
			mu.Lock()
			seen[string(record.Value)]++
			firstN++
			enough := firstN >= 1
			mu.Unlock()
			if enough {
				firstCancel()
			}
			return nil
		}, ConsumeOptions{})
	}()
	if err := <-firstErr; err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	mu.Lock()
	handledByFirst := append([]string(nil), keysOf(seen)...)
	mu.Unlock()
	if len(handledByFirst) == 0 {
		t.Fatal("first consumer processed nothing — rebalance scenario has no handover")
	}

	// The second consumer joins the same group and must drain the rest.
	// 第二个消费者加入同组并清空剩余消息。
	second := newConsumer()
	t.Cleanup(func() { _ = second.Close() })
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer secondCancel()

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- second.Consume(secondCtx, func(_ context.Context, record *kgo.Record) error {
			mu.Lock()
			seen[string(record.Value)]++
			complete := len(seen) == total
			mu.Unlock()
			if complete {
				secondCancel()
			}
			return nil
		}, ConsumeOptions{})
	}()
	if err := <-secondErr; err != nil {
		t.Fatalf("second Consume: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != total {
		t.Fatalf("received %d distinct messages, want %d: %v", len(seen), total, seen)
	}
	for i := 0; i < total; i++ {
		if seen[fmt.Sprintf("msg-%d", i)] == 0 {
			t.Fatalf("message msg-%d was lost during rebalance", i)
		}
	}
}

// TestIntegrationRedeliveryWithoutCommit: in manual mode a failing handler
// must not commit the offset, so the same record is delivered again. This is
// the duplicate-delivery contract at-least-once depends on.
// TestIntegrationRedeliveryWithoutCommit：手动模式下 handler 失败不得提交位点，
// 同一条记录会再次投递。这是 at-least-once 语义依赖的重复投递契约。
func TestIntegrationRedeliveryWithoutCommit(t *testing.T) {
	brokers := testBrokers(t)
	client := testClient(t, brokers)
	topic := testName(t, "redeliver")
	group := testName(t, "group")
	createTopic(t, brokers, topic)

	producer, err := NewProducer(client, ProducerConfig{Topic: topic, FlushTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	if err := producer.Publish(context.Background(), []byte("must-redeliver"), PublishOptions{Key: "dup"}); err != nil {
		t.Fatalf("Publish: %v", err)
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

	var mu sync.Mutex
	deliveries := 0
	err = consumer.Consume(consumeCtx, func(_ context.Context, record *kgo.Record) error {
		mu.Lock()
		deliveries++
		count := deliveries
		mu.Unlock()
		if count == 1 {
			// First delivery fails and must not be committed.
			// 第一次投递失败且不得提交。
			return errors.New("integration: fail the first delivery")
		}
		cancel()
		return nil
	}, ConsumeOptions{})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if deliveries < 2 {
		t.Fatalf("record delivered %d time(s), want at least 2 (failed delivery must not be committed)", deliveries)
	}
}

func keysOf(seen map[string]int) []string {
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}
