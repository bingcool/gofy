package queues_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/queues"
)

// startConsume runs Consume in the background and returns a stop function that
// cancels the context and asserts a clean nil return.
// startConsume 在后台运行 Consume，返回的停止函数会取消 context 并断言返回 nil。
func startConsume(t *testing.T, q *queues.Queue, handler queues.HandlerFunc, opts queues.ConsumeOptions) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Consume(ctx, handler, opts) }()

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Consume returned %v, want nil", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Consume did not stop after cancellation")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestQueueConsumeHandlesMessagesUntilCancelled(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "jobs"})
	pushCtx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := q.Push(pushCtx, map[string]int{"n": i}); err != nil {
			t.Fatalf("Push: %v", err)
		}
	}

	handled := make(chan int, 3)
	stop := startConsume(t, q, func(_ context.Context, msg *queues.Message) error {
		var payload struct {
			N int `json:"n"`
		}
		if err := msg.Decode(&payload); err != nil {
			return err
		}
		handled <- payload.N
		return nil
	}, queues.ConsumeOptions{IdleDelay: 5 * time.Millisecond})

	seen := make(map[int]bool, 3)
	for i := 0; i < 3; i++ {
		select {
		case n := <-handled:
			seen[n] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d messages", i)
		}
	}
	stop()
	for i := 0; i < 3; i++ {
		if !seen[i] {
			t.Fatalf("message %d not handled; got %v", i, seen)
		}
	}
}

func TestQueueConsumeRetriesFailedMessage(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "jobs", MaxRetries: 2})
	if _, err := q.Push(context.Background(), map[string]string{"id": "a"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	var calls int32
	retryNums := make(chan int, 2)
	stop := startConsume(t, q, func(_ context.Context, msg *queues.Message) error {
		retryNums <- msg.RetryNum
		if atomic.AddInt32(&calls, 1) == 1 {
			return errors.New("boom")
		}
		return nil
	}, queues.ConsumeOptions{
		IdleDelay:  5 * time.Millisecond,
		RetryDelay: 10 * time.Millisecond,
		OnError:    func(error) {},
	})

	for i := 0; i < 2; i++ {
		select {
		case <-retryNums:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d deliveries", i)
		}
	}
	stop()
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls = %d, want 2", got)
	}
}

func TestQueueConsumeDropsAfterRetryLimit(t *testing.T) {
	q, c := newQueue(t, queues.Config{Key: "jobs", MaxRetries: 1})
	if _, err := q.Push(context.Background(), map[string]string{"id": "a"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	var calls int32
	exhausted := make(chan error, 1)
	stop := startConsume(t, q, func(context.Context, *queues.Message) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("always")
	}, queues.ConsumeOptions{
		IdleDelay:  5 * time.Millisecond,
		RetryDelay: 10 * time.Millisecond,
		OnError: func(err error) {
			if errors.Is(err, queues.ErrRetryExhausted) {
				select {
				case exhausted <- err:
				default:
				}
			}
		},
	})

	select {
	case <-exhausted:
	case <-time.After(2 * time.Second):
		t.Fatalf("retry exhaustion not reported")
	}
	stop()
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("handler calls = %d, want 2 (initial + one retry)", got)
	}
	pending, err := c.client.ZCard(context.Background(), q.RetryKey()).Result()
	if err != nil {
		t.Fatalf("ZCard: %v", err)
	}
	if pending != 0 {
		t.Fatalf("retry queue has %d entries, want 0", pending)
	}
}

func TestQueueConsumeDiscardsFailedMessage(t *testing.T) {
	q, c := newQueue(t, queues.Config{Key: "jobs", MaxRetries: 3})
	if _, err := q.Push(context.Background(), map[string]string{"id": "a"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	var calls int32
	failed := make(chan error, 1)
	stop := startConsume(t, q, func(context.Context, *queues.Message) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("always")
	}, queues.ConsumeOptions{
		IdleDelay:      5 * time.Millisecond,
		DiscardOnError: true,
		OnError: func(err error) {
			select {
			case failed <- err:
			default:
			}
		},
	})

	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatalf("handler failure not reported")
	}
	stop()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
	pending, err := c.client.ZCard(context.Background(), q.RetryKey()).Result()
	if err != nil {
		t.Fatalf("ZCard: %v", err)
	}
	if pending != 0 {
		t.Fatalf("retry queue has %d entries, want 0", pending)
	}
}

func TestQueueConsumeRecoversHandlerPanic(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "jobs", MaxRetries: 1})
	if _, err := q.Push(context.Background(), map[string]string{"id": "a"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	var calls int32
	delivered := make(chan int, 2)
	stop := startConsume(t, q, func(context.Context, *queues.Message) error {
		n := int(atomic.AddInt32(&calls, 1))
		delivered <- n
		if n == 1 {
			panic("handler exploded")
		}
		return nil
	}, queues.ConsumeOptions{
		IdleDelay:  5 * time.Millisecond,
		RetryDelay: 10 * time.Millisecond,
		OnError:    func(error) {},
	})

	for i := 0; i < 2; i++ {
		select {
		case <-delivered:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d deliveries", i)
		}
	}
	stop()
}

func TestQueueConsumeRequiresHandler(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "jobs"})
	err := q.Consume(context.Background(), nil, queues.ConsumeOptions{})
	if !errors.Is(err, queues.ErrHandlerRequired) {
		t.Fatalf("Consume error = %v, want ErrHandlerRequired", err)
	}
}

func TestQueueConsumeStopsWhenIdle(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "jobs"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- q.Consume(ctx, func(context.Context, *queues.Message) error { return nil },
			queues.ConsumeOptions{IdleDelay: 10 * time.Millisecond})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("idle Consume did not stop on cancellation")
	}
}

// TestConsumeReportsReadyOnceAfterFirstPop pins the queue's readiness rule: a
// polled queue has no subscription to wait for, so the first successful pop is
// the whole signal — and it must not repeat for every message.
// TestConsumeReportsReadyOnceAfterFirstPop 固定队列的就绪规则：轮询队列没有订阅可
// 等，第一次成功弹出本身就是全部信号，而且不能每处理一条消息就重复一次。
func TestConsumeReportsReadyOnceAfterFirstPop(t *testing.T) {
	q, _ := newQueue(t, queues.Config{Key: "ready"})
	ready := make(chan struct{}, 4)
	handled := make(chan struct{}, 1)
	startConsume(t, q, func(context.Context, *queues.Message) error {
		handled <- struct{}{}
		return nil
	}, queues.ConsumeOptions{OnReady: func() { ready <- struct{}{} }})

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("OnReady did not fire after the first pop")
	}
	if _, err := q.Push(context.Background(), map[string]string{"id": "a"}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("message was not handled")
	}
	select {
	case <-ready:
		t.Fatal("OnReady fired again after a message; it must run once per Consume")
	case <-time.After(200 * time.Millisecond):
	}
}
