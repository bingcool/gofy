package rocketmq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
)

func TestConsumerConfigValidate(t *testing.T) {
	valid := ConsumerConfig{Topic: "orders", Group: "order-service", Tags: []string{"paid", "cancel"}}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	tests := []struct {
		name string
		cfg  ConsumerConfig
		want error
	}{
		{name: "no topic", cfg: ConsumerConfig{Group: "g"}, want: ErrTopicRequired},
		{name: "no group", cfg: ConsumerConfig{Topic: "t"}, want: ErrGroupRequired},
		{
			name: "sql and tags conflict",
			cfg:  ConsumerConfig{Topic: "t", Group: "g", SQL: "region = 'cn'", Tags: []string{"paid"}},
			want: ErrTagsConflictSQL,
		},
		{name: "empty tag", cfg: ConsumerConfig{Topic: "t", Group: "g", Tags: []string{" "}}, want: ErrInvalidTag},
		{name: "tag with operator", cfg: ConsumerConfig{Topic: "t", Group: "g", Tags: []string{"a||b"}}, want: ErrInvalidTag},
		{name: "negative batch", cfg: ConsumerConfig{Topic: "t", Group: "g", BatchSize: -1}, want: nil},
		{name: "negative concurrency", cfg: ConsumerConfig{Topic: "t", Group: "g", Concurrency: -1}, want: nil},
		{name: "negative await", cfg: ConsumerConfig{Topic: "t", Group: "g", AwaitDuration: -time.Second}, want: nil},
		{name: "negative invisible", cfg: ConsumerConfig{Topic: "t", Group: "g", InvisibleDuration: -time.Second}, want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.cfg.validate()
			if err == nil {
				t.Fatal("validate succeeded, want an error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("validate = %v, want %v", err, test.want)
			}
		})
	}
}

func TestConsumerFilter(t *testing.T) {
	tests := []struct {
		name       string
		cfg        ConsumerConfig
		wantType   rmq.FilterExpressionType
		expression string
	}{
		{name: "no tags subscribes all", cfg: ConsumerConfig{}, wantType: rmq.TAG, expression: "*"},
		{name: "star subscribes all", cfg: ConsumerConfig{Tags: []string{"*"}}, wantType: rmq.TAG, expression: "*"},
		{name: "tags joined", cfg: ConsumerConfig{Tags: []string{"paid", "cancel"}}, wantType: rmq.TAG, expression: "paid || cancel"},
		{name: "sql wins", cfg: ConsumerConfig{SQL: "region = 'cn'"}, wantType: rmq.SQL92, expression: "region = 'cn'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expression, filterType := test.cfg.filter()
			if filterType != test.wantType {
				t.Fatalf("type = %v, want %v", filterType, test.wantType)
			}
			if expression != test.expression {
				t.Fatalf("expression = %q, want %q", expression, test.expression)
			}
		})
	}
}

// fakeView implements viewReader; the SDK MessageView has no public
// constructor, so the mapping is tested through this stand-in.
// fakeView 实现 viewReader；SDK 的 MessageView 没有公开构造函数，因此通过该
// 替身测试字段映射。
type fakeView struct {
	id         string
	topic      string
	tag        *string
	keys       []string
	body       []byte
	properties map[string]string
	group      *string
	offset     int64
	attempt    int32
	delivery   *time.Time
	born       *time.Time
}

func (v fakeView) GetMessageId() string             { return v.id }
func (v fakeView) GetTopic() string                 { return v.topic }
func (v fakeView) GetTag() *string                  { return v.tag }
func (v fakeView) GetKeys() []string                { return v.keys }
func (v fakeView) GetBody() []byte                  { return v.body }
func (v fakeView) GetProperties() map[string]string { return v.properties }
func (v fakeView) GetMessageGroup() *string         { return v.group }
func (v fakeView) GetOffset() int64                 { return v.offset }
func (v fakeView) GetDeliveryAttempt() int32        { return v.attempt }
func (v fakeView) GetDeliveryTimestamp() *time.Time { return v.delivery }
func (v fakeView) GetBornTimestamp() *time.Time     { return v.born }

func TestReceivedMessage(t *testing.T) {
	tag := "paid"
	group := "order-1"
	delivery := time.UnixMilli(1700000002000)
	born := time.UnixMilli(1700000000000)

	message := receivedMessage(fakeView{
		id:         "msg-id",
		topic:      "orders",
		tag:        &tag,
		keys:       []string{"order-1", "user-1"},
		body:       []byte(`{"id":1}`),
		properties: map[string]string{"region": "cn"},
		group:      &group,
		offset:     42,
		attempt:    2,
		delivery:   &delivery,
		born:       &born,
	})

	if message.MessageID != "msg-id" || message.Topic != "orders" || message.Tag != "paid" {
		t.Fatalf("identity = %+v", message)
	}
	if len(message.Keys) != 2 || message.Keys[0] != "order-1" || message.Keys[1] != "user-1" {
		t.Fatalf("keys = %v", message.Keys)
	}
	if string(message.Body) != `{"id":1}` {
		t.Fatalf("body = %q", message.Body)
	}
	if message.Properties["region"] != "cn" {
		t.Fatalf("properties = %v", message.Properties)
	}
	if message.MessageGroup != "order-1" || message.Offset != 42 || message.DeliveryAttempt != 2 {
		t.Fatalf("group/offset/attempt = %q/%d/%d", message.MessageGroup, message.Offset, message.DeliveryAttempt)
	}
	if !message.DeliveryTimestamp.Equal(delivery) || !message.BornTimestamp.Equal(born) {
		t.Fatalf("timestamps = %v/%v", message.DeliveryTimestamp, message.BornTimestamp)
	}
}

func TestNewConsumerValidates(t *testing.T) {
	cfg := testConfig(t)
	if _, err := NewConsumer(cfg, ConsumerConfig{Group: "g"}); !errors.Is(err, ErrTopicRequired) {
		t.Fatalf("NewConsumer = %v, want ErrTopicRequired", err)
	}
	if _, err := NewConsumer(cfg, ConsumerConfig{Topic: "t"}); !errors.Is(err, ErrGroupRequired) {
		t.Fatalf("NewConsumer = %v, want ErrGroupRequired", err)
	}
	if _, err := NewConsumer(Config{}, ConsumerConfig{Topic: "t", Group: "g"}); !errors.Is(err, ErrEndpointRequired) {
		t.Fatalf("NewConsumer = %v, want ErrEndpointRequired", err)
	}
}

func TestConsumerRequiresHandler(t *testing.T) {
	consumer, err := NewConsumer(testConfig(t), ConsumerConfig{Topic: "orders", Group: "gofy-test-consumer"})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(func() {
		_ = consumer.Close()
	})

	if err := consumer.Consume(context.Background(), nil, ConsumeOptions{}); !errors.Is(err, ErrHandlerRequired) {
		t.Fatalf("Consume = %v, want ErrHandlerRequired", err)
	}
	got := consumer.Config()
	if got.Topic != "orders" || got.Group != "gofy-test-consumer" {
		t.Fatalf("Config = %+v", got)
	}
	if got.BatchSize != defaultBatchSize || got.Concurrency != defaultConcurrency ||
		got.AwaitDuration != defaultAwaitDuration || got.InvisibleDuration != defaultInvisibleDuration {
		t.Fatalf("defaults not applied: %+v", got)
	}
}

// fakeConsumer scripts the pull loop without a broker.
// fakeConsumer 在没有 broker 的情况下编排拉取循环的返回结果。
type fakeConsumer struct {
	mu         sync.Mutex
	batches    [][]deliveredMessage
	startErr   error
	stopErr    error
	receiveErr error
	timeouts   int
	calls      int
	acked      []string
	stops      int
	ackCh      chan deliveredMessage
}

func (f *fakeConsumer) Start() error { return f.startErr }

func (f *fakeConsumer) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return f.stopErr
}

func (f *fakeConsumer) Receive(ctx context.Context, _ int32, _ time.Duration) ([]deliveredMessage, error) {
	f.mu.Lock()
	f.calls++
	if len(f.batches) > 0 {
		batch := f.batches[0]
		f.batches = f.batches[1:]
		f.mu.Unlock()
		return batch, nil
	}
	if f.receiveErr != nil {
		err := f.receiveErr
		f.receiveErr = nil
		f.mu.Unlock()
		return nil, err
	}
	if f.timeouts > 0 {
		f.timeouts--
		f.mu.Unlock()
		return nil, fmt.Errorf("[error] CODE=DEADLINE_EXCEEDED")
	}
	f.mu.Unlock()
	<-ctx.Done()
	return nil, fmt.Errorf("[error] CODE=DEADLINE_EXCEEDED")
}

func (f *fakeConsumer) Ack(_ context.Context, delivery deliveredMessage) error {
	f.mu.Lock()
	f.acked = append(f.acked, delivery.message.MessageID)
	f.mu.Unlock()
	if f.ackCh != nil {
		select {
		case f.ackCh <- delivery:
		default:
		}
	}
	return nil
}

func (f *fakeConsumer) callsSeen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeConsumer) ackedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acked...)
}

func (f *fakeConsumer) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

func delivery(id string) deliveredMessage {
	return deliveredMessage{message: &Message{MessageID: id, Topic: "orders"}}
}

func newPullConsumer(cfg ConsumerConfig, raw consumerClient) *Consumer {
	return &Consumer{cfg: cfg.withDefaults(), raw: raw, done: make(chan struct{})}
}

func waitCalls(t *testing.T, fake *fakeConsumer, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.callsSeen() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Receive was called %d times, want at least %d", fake.callsSeen(), want)
}

func TestConsumerConsumesAndAcks(t *testing.T) {
	fake := &fakeConsumer{
		batches: [][]deliveredMessage{{delivery("1"), delivery("2")}},
		ackCh:   make(chan deliveredMessage, 4),
	}
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "g"}, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var handled []string
	go func() {
		done <- c.Consume(ctx, func(_ context.Context, message *Message) error {
			handled = append(handled, message.MessageID)
			return nil
		}, ConsumeOptions{})
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-fake.ackCh:
		case <-time.After(2 * time.Second):
			t.Fatal("messages were not acknowledged")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(handled) != 2 || handled[0] != "1" || handled[1] != "2" {
		t.Fatalf("handled = %v", handled)
	}
	if got := fake.ackedIDs(); len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("acked = %v", got)
	}
	if fake.stopCount() != 1 {
		t.Fatalf("Stop called %d times", fake.stopCount())
	}
}

func TestConsumerLeavesFailedMessageUnacked(t *testing.T) {
	fake := &fakeConsumer{batches: [][]deliveredMessage{{delivery("1")}}}
	reported := make(chan error, 4)
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "g"}, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.Consume(ctx, func(context.Context, *Message) error {
			return errors.New("boom")
		}, ConsumeOptions{OnError: func(err error) { reported <- err }})
	}()

	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "handle") {
			t.Fatalf("report = %v, want a handle error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler failure was not reported")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := fake.ackedIDs(); len(got) != 0 {
		t.Fatalf("failed message was acknowledged: %v", got)
	}
}

func TestConsumerDiscardsFailedMessage(t *testing.T) {
	fake := &fakeConsumer{
		batches: [][]deliveredMessage{{delivery("1")}},
		ackCh:   make(chan deliveredMessage, 4),
	}
	reported := make(chan error, 4)
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "g"}, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.Consume(ctx, func(context.Context, *Message) error {
			return errors.New("boom")
		}, ConsumeOptions{
			DiscardOnError: true,
			OnError:        func(err error) { reported <- err },
		})
	}()

	select {
	case <-fake.ackCh:
	case <-time.After(2 * time.Second):
		t.Fatal("discarded message was not acknowledged")
	}
	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "discard") {
			t.Fatalf("report = %v, want a discard error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("discard was not reported")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := fake.ackedIDs(); len(got) != 1 || got[0] != "1" {
		t.Fatalf("acked = %v", got)
	}
}

func TestConsumerRecoversFromHandlerPanic(t *testing.T) {
	fake := &fakeConsumer{batches: [][]deliveredMessage{{delivery("1")}}}
	reported := make(chan error, 4)
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "g"}, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.Consume(ctx, func(context.Context, *Message) error {
			panic("boom")
		}, ConsumeOptions{OnError: func(err error) { reported <- err }})
	}()

	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "panic") {
			t.Fatalf("report = %v, want a panic error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("panic was not reported")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if got := fake.ackedIDs(); len(got) != 0 {
		t.Fatalf("panicking message was acknowledged: %v", got)
	}
}

func TestConsumerTreatsPollTimeoutAsEmpty(t *testing.T) {
	fake := &fakeConsumer{timeouts: 1}
	reported := make(chan error, 4)
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "g"}, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.Consume(ctx, func(context.Context, *Message) error { return nil },
			ConsumeOptions{OnError: func(err error) { reported <- err }})
	}()

	waitCalls(t, fake, 2)
	select {
	case err := <-reported:
		t.Fatalf("timeout was reported as an error: %v", err)
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume: %v", err)
	}
}

// TestConsumerTreatsEmptyLongPollAsEmpty pins the other half of an idle poll:
// the proxy answering 40401 MESSAGE_NOT_FOUND, which is what a v5 broker sends
// when its long-poll window closes before the client's deadline. It is the
// common case on an idle topic, so reporting it would log an error every
// AwaitDuration for every worker.
// TestConsumerTreatsEmptyLongPollAsEmpty 固定空轮询的另一半：proxy 回答 40401
// MESSAGE_NOT_FOUND。v5 服务端在长轮询窗口先到期时就这么答复，空 topic 上它才是
// 常见形态；若把它当错误，每个 worker 每个 AwaitDuration 都会打一行错误。
func TestConsumerTreatsEmptyLongPollAsEmpty(t *testing.T) {
	fake := &fakeConsumer{receiveErr: &rmq.ErrRpcStatus{Code: 40401, Message: "no new message"}}
	reported := make(chan error, 4)
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "g"}, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.Consume(ctx, func(context.Context, *Message) error { return nil },
			ConsumeOptions{OnError: func(err error) { reported <- err }})
	}()

	waitCalls(t, fake, 2)
	select {
	case err := <-reported:
		t.Fatalf("empty long poll was reported as an error: %v", err)
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume: %v", err)
	}
}

// TestIsIdleReceive covers the classifier both poll paths lean on, including the
// shapes that must still be reported: a proxy that is down, and an RPC status
// that is a real failure rather than an empty poll.
// TestIsIdleReceive 覆盖两条轮询路径共用的判定，也包括必须照旧上报的形态：proxy
// 不可用，以及并非空轮询的真实 RPC 失败。
func TestIsIdleReceive(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "typed deadline", err: context.DeadlineExceeded, want: true},
		{name: "client deadline string", err: fmt.Errorf("[error] CODE=DEADLINE_EXCEEDED"), want: true},
		{name: "wrapped client deadline", err: fmt.Errorf("receive: %w", context.DeadlineExceeded), want: true},
		{name: "proxy says no message", err: &rmq.ErrRpcStatus{Code: 40401, Message: "no new message"}, want: true},
		{
			name: "wrapped proxy answer",
			err:  fmt.Errorf("rocketmq: %w", &rmq.ErrRpcStatus{Code: 40401, Message: "no new message"}),
			want: true,
		},
		{name: "proxy internal error", err: &rmq.ErrRpcStatus{Code: 50002, Message: "internal"}, want: false},
		{name: "proxy down", err: errors.New("connection refused"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isIdleReceive(test.err); got != test.want {
				t.Fatalf("isIdleReceive(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestConsumerReportsReceiveError(t *testing.T) {
	fake := &fakeConsumer{receiveErr: errors.New("proxy down")}
	reported := make(chan error, 4)
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "g"}, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.Consume(ctx, func(context.Context, *Message) error { return nil },
			ConsumeOptions{OnError: func(err error) { reported <- err }})
	}()

	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "receive") {
			t.Fatalf("report = %v, want a receive error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("receive failure was not reported")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Consume: %v", err)
	}
}

func TestConsumerStartFailure(t *testing.T) {
	fake := &fakeConsumer{startErr: errors.New("nope")}
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "group"}, fake)

	err := c.Consume(context.Background(), func(context.Context, *Message) error { return nil }, ConsumeOptions{})
	if err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatalf("Consume = %v, want a start error naming the group", err)
	}
	if fake.stopCount() != 0 {
		t.Fatal("Stop was called on a consumer that never started")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestConsumerCloseUnblocksConsume(t *testing.T) {
	fake := &fakeConsumer{}
	c := newPullConsumer(ConsumerConfig{Topic: "orders", Group: "g"}, fake)

	done := make(chan error, 1)
	go func() {
		done <- c.Consume(context.Background(), func(context.Context, *Message) error { return nil }, ConsumeOptions{})
	}()

	waitCalls(t, fake, 1)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Consume")
	}
	if fake.stopCount() != 1 {
		t.Fatalf("Stop called %d times", fake.stopCount())
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := c.Consume(context.Background(), func(context.Context, *Message) error { return nil }, ConsumeOptions{}); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("Consume after Close = %v, want ErrClientClosed", err)
	}
}
