package queues_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy/support/queues"
	goredis "github.com/redis/go-redis/v9"
)

type client struct {
	mr     *miniredis.Miniredis
	client *goredis.Client
}

func newClient(t *testing.T) *client {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &client{mr: mr, client: rdb}
}

func newQueue(t *testing.T, cfg queues.Config) (*queues.Queue, *client) {
	t.Helper()
	c := newClient(t)
	q, err := queues.NewQueue(c.client, cfg)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	return q, c
}

func newDelayQueue(t *testing.T, cfg queues.Config) (*queues.DelayQueue, *client) {
	t.Helper()
	c := newClient(t)
	q, err := queues.NewDelayQueue(c.client, cfg)
	if err != nil {
		t.Fatalf("NewDelayQueue: %v", err)
	}
	return q, c
}

func TestConstructorsValidateConfig(t *testing.T) {
	c := newClient(t)

	if _, err := queues.NewQueue(nil, queues.Config{Key: "orders"}); !errors.Is(err, queues.ErrClientRequired) {
		t.Fatalf("NewQueue(nil) = %v, want ErrClientRequired", err)
	}
	if _, err := queues.NewDelayQueue(nil, queues.Config{Key: "orders"}); !errors.Is(err, queues.ErrClientRequired) {
		t.Fatalf("NewDelayQueue(nil) = %v, want ErrClientRequired", err)
	}
	if _, err := queues.NewQueue(c.client, queues.Config{}); !errors.Is(err, queues.ErrKeyRequired) {
		t.Fatalf("NewQueue(empty key) = %v, want ErrKeyRequired", err)
	}
	if _, err := queues.NewQueue(c.client, queues.Config{Key: "orders", MaxRetries: -1}); err == nil {
		t.Fatal("NewQueue(negative retries) = nil error, want error")
	}

	q, err := queues.NewQueue(c.client, queues.Config{Key: "orders"})
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	if q.MaxRetries() != queues.DefaultMaxRetries {
		t.Fatalf("MaxRetries() = %d, want %d", q.MaxRetries(), queues.DefaultMaxRetries)
	}
	if q.Key() != "orders" || q.PriorityKey() != "orders:priority_queue" || q.RetryKey() != "orders:retry_queue_sort" {
		t.Fatalf("unexpected keys: %q %q %q", q.Key(), q.PriorityKey(), q.RetryKey())
	}
}

func TestQueuePushPopFIFO(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})
	ctx := context.Background()

	pushed, err := q.Push(ctx, map[string]any{"n": 1}, map[string]any{"n": 2}, map[string]any{"n": 3})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if pushed != 3 {
		t.Fatalf("Push = %d, want 3", pushed)
	}

	for want := 1; want <= 3; want++ {
		msg, ok, err := q.Pop(ctx, 0)
		if err != nil || !ok {
			t.Fatalf("Pop #%d = (%v, %v), want ok", want, ok, err)
		}
		var payload struct {
			N int `json:"n"`
		}
		if err := msg.Decode(&payload); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if payload.N != want {
			t.Fatalf("payload.N = %d, want %d", payload.N, want)
		}
		if msg.ID == "" || msg.RetryNum != 0 || msg.Timestamp == 0 {
			t.Fatalf("unexpected envelope: %+v", msg)
		}
	}
}

func TestQueuePriorityIsConsumedFirst(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})
	ctx := context.Background()

	if _, err := q.Push(ctx, "main"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := q.PushPriority(ctx, "priority"); err != nil {
		t.Fatalf("PushPriority: %v", err)
	}

	first, ok, err := q.Pop(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("Pop priority = (%v, %v), want ok", ok, err)
	}
	if got := payloadString(t, first); got != "priority" {
		t.Fatalf("first payload = %q, want priority", got)
	}

	second, ok, err := q.Pop(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("Pop main = (%v, %v), want ok", ok, err)
	}
	if got := payloadString(t, second); got != "main" {
		t.Fatalf("second payload = %q, want main", got)
	}
}

func TestQueuePopEmpty(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})

	msg, ok, err := q.Pop(context.Background(), 0)
	if err != nil || ok || msg != nil {
		t.Fatalf("non-blocking Pop = (%v, %v, %v), want empty", msg, ok, err)
	}
}

func TestQueuePopBlocksUntilTimeout(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})

	start := time.Now()
	msg, ok, err := q.Pop(context.Background(), 50*time.Millisecond)
	if err != nil || ok || msg != nil {
		t.Fatalf("blocking Pop = (%v, %v, %v), want empty", msg, ok, err)
	}
	// Redis 阻塞超时以秒为单位，50ms 会向上取整到 1s。
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("blocking Pop returned after %s, want ~1s", elapsed)
	}
}

func TestQueuePopBlockingWithMessage(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})
	ctx := context.Background()

	if _, err := q.Push(ctx, "job"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	msg, ok, err := q.Pop(ctx, time.Second)
	if err != nil || !ok {
		t.Fatalf("blocking Pop = (%v, %v), want ok", ok, err)
	}
	if got := payloadString(t, msg); got != "job" {
		t.Fatalf("payload = %q, want job", got)
	}
}

func TestQueueRetryPromotesMessageBack(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})
	ctx := context.Background()

	if _, err := q.Push(ctx, map[string]any{"n": 1}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	failed, ok, err := q.Pop(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("Pop = (%v, %v), want ok", ok, err)
	}

	scheduled, err := q.Retry(ctx, failed, 0)
	if err != nil || !scheduled {
		t.Fatalf("Retry = (%v, %v), want scheduled", scheduled, err)
	}

	again, ok, err := q.Pop(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("Pop after retry = (%v, %v), want ok", ok, err)
	}
	if again.ID != failed.ID {
		t.Fatalf("retried ID = %q, want %q", again.ID, failed.ID)
	}
	if again.RetryNum != 1 {
		t.Fatalf("retried RetryNum = %d, want 1", again.RetryNum)
	}
}

func TestQueueRetryWaitsForDelay(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})
	ctx := context.Background()

	if _, err := q.Push(ctx, "job"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	msg, ok, err := q.Pop(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("Pop = (%v, %v), want ok", ok, err)
	}
	if _, err := q.Retry(ctx, msg, time.Hour); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if _, ok, err := q.Pop(ctx, 0); err != nil || ok {
		t.Fatalf("Pop before retry deadline = (%v, %v), want empty", ok, err)
	}
}

func TestQueueRetryStopsAtLimit(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders", MaxRetries: 2})
	ctx := context.Background()

	if _, err := q.Push(ctx, "job"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	msg, ok, err := q.Pop(ctx, 0)
	if err != nil || !ok {
		t.Fatalf("Pop = (%v, %v), want ok", ok, err)
	}

	for want := 1; want <= 2; want++ {
		scheduled, err := q.Retry(ctx, msg, 0)
		if err != nil || !scheduled {
			t.Fatalf("Retry #%d = (%v, %v), want scheduled", want, scheduled, err)
		}
		msg, ok, err = q.Pop(ctx, 0)
		if err != nil || !ok {
			t.Fatalf("Pop #%d = (%v, %v), want ok", want, ok, err)
		}
		if msg.RetryNum != want {
			t.Fatalf("RetryNum = %d, want %d", msg.RetryNum, want)
		}
	}

	scheduled, err := q.Retry(ctx, msg, 0)
	if err != nil || scheduled {
		t.Fatalf("Retry over limit = (%v, %v), want dropped", scheduled, err)
	}
}

func TestQueueLen(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})
	ctx := context.Background()

	if _, err := q.Push(ctx, "a", "b"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if _, err := q.PushPriority(ctx, "c"); err != nil {
		t.Fatalf("PushPriority: %v", err)
	}

	length, err := q.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if length != 2 {
		t.Fatalf("Len = %d, want 2", length)
	}
}

func TestDelayQueueAddAndPop(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay"})
	ctx := context.Background()

	if err := q.Add(ctx, map[string]any{"n": 1}, 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := q.Add(ctx, map[string]any{"n": 2}, time.Hour); err != nil {
		t.Fatalf("Add future: %v", err)
	}

	length, err := q.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if length != 2 {
		t.Fatalf("Len = %d, want 2", length)
	}

	messages, err := q.Pop(ctx, 0)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("Pop returned %d messages, want 1", len(messages))
	}
	var payload struct {
		N int `json:"n"`
	}
	if err := messages[0].Decode(&payload); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if payload.N != 1 {
		t.Fatalf("payload.N = %d, want 1", payload.N)
	}

	// Expired messages are deleted while the unexpired ones stay in the queue.
	// 已到期的消息会被删除，未到期的仍在队列里。
	length, err = q.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if length != 1 {
		t.Fatalf("Len after Pop = %d, want 1", length)
	}
}

func TestDelayQueuePopEmpty(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay"})

	messages, err := q.Pop(context.Background(), 5)
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("Pop = %d messages, want 0", len(messages))
	}
}

func TestDelayQueueRetry(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay", MaxRetries: 1})
	ctx := context.Background()

	if err := q.Add(ctx, "job", 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	messages, err := q.Pop(ctx, 1)
	if err != nil || len(messages) != 1 {
		t.Fatalf("Pop = (%d, %v), want 1", len(messages), err)
	}

	scheduled, err := q.Retry(ctx, messages[0], 0)
	if err != nil || !scheduled {
		t.Fatalf("Retry = (%v, %v), want scheduled", scheduled, err)
	}
	retried, err := q.Pop(ctx, 1)
	if err != nil || len(retried) != 1 {
		t.Fatalf("Pop after retry = (%d, %v), want 1", len(retried), err)
	}
	if retried[0].ID != messages[0].ID || retried[0].RetryNum != 1 {
		t.Fatalf("retried message = %+v, want ID %q with RetryNum 1", retried[0], messages[0].ID)
	}

	scheduled, err = q.Retry(ctx, retried[0], 0)
	if err != nil || scheduled {
		t.Fatalf("Retry over limit = (%v, %v), want dropped", scheduled, err)
	}
}

func TestRetryRejectsNilMessage(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "orders"})
	if _, err := q.Retry(context.Background(), nil, 0); err == nil {
		t.Fatal("Retry(nil) = nil error, want error")
	}
}

func payloadString(t *testing.T, msg *queues.Message) string {
	t.Helper()
	var value string
	if err := msg.Decode(&value); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return value
}
