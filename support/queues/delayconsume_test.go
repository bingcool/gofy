package queues_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/queues"
)

func TestDelayConsumeRequiresHandler(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay"})
	if err := q.Consume(context.Background(), nil, queues.ConsumeOptions{}); !errors.Is(err, queues.ErrHandlerRequired) {
		t.Fatalf("Consume(nil handler) = %v, want ErrHandlerRequired", err)
	}
}

func TestDelayConsumeTakesOnlyDueMessages(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay"})
	ctx := context.Background()
	if err := q.Add(ctx, map[string]any{"n": 1}, 0); err != nil {
		t.Fatalf("Add due: %v", err)
	}
	if err := q.Add(ctx, map[string]any{"n": 2}, time.Hour); err != nil {
		t.Fatalf("Add future: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan int, 8)
	var errs []error
	var mu sync.Mutex
	done := make(chan error, 1)
	go func() {
		done <- q.Consume(runCtx, func(_ context.Context, msg *queues.Message) error {
			var payload struct {
				N int `json:"n"`
			}
			if err := msg.Decode(&payload); err != nil {
				return err
			}
			got <- payload.N
			return nil
		}, queues.ConsumeOptions{
			IdleDelay: 5 * time.Millisecond,
			PopLimit:  4,
			OnError: func(err error) {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			},
		})
	}()

	select {
	case n := <-got:
		if n != 1 {
			t.Fatalf("consumed payload n = %d, want 1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("due message was never consumed")
	}
	// Give the loop a few idle polls so a bug that ignores the score would show.
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume returned %v after cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Consume did not stop on cancellation")
	}

	length, err := q.Len(context.Background())
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if length != 1 {
		t.Fatalf("Len = %d, want 1 (the future message must stay)", length)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(errs) != 0 {
		t.Fatalf("unexpected consume errors: %v", errs)
	}
}

func TestDelayConsumeRetriesFailedMessage(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay", MaxRetries: 3})
	if err := q.Add(context.Background(), map[string]any{"n": 7}, 0); err != nil {
		t.Fatalf("Add: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := make(chan int, 8)
	done := make(chan error, 1)
	seen := 0
	go func() {
		done <- q.Consume(runCtx, func(_ context.Context, msg *queues.Message) error {
			seen++
			attempts <- seen
			if seen == 1 {
				return errors.New("transient")
			}
			return nil
		}, queues.ConsumeOptions{
			IdleDelay:  5 * time.Millisecond,
			RetryDelay: 10 * time.Millisecond,
		})
	}()

	for want := 1; want <= 2; want++ {
		select {
		case n := <-attempts:
			if n != want {
				t.Fatalf("attempt = %d, want %d", n, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for attempt %d", want)
		}
	}
	cancel()
	<-done

	length, err := q.Len(context.Background())
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if length != 0 {
		t.Fatalf("Len = %d, want 0 after the retry succeeded", length)
	}
}

// TestDelayConsumeDefersInFlightWithoutRetryBudget is the delayed-queue twin of
// the B1 fix: an in-flight error writes the message back with the same RetryNum
// and ignores MaxRetries, so the copy survives until the other handler settles.
// TestDelayConsumeDefersInFlightWithoutRetryBudget 是 B1 修复在延迟队列上的同款
// 用例：在途错误以相同 RetryNum 写回且不受 MaxRetries 限制，副本因此能活到另一个
// handler 收尾。
func TestDelayConsumeDefersInFlightWithoutRetryBudget(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay", MaxRetries: 1})
	if err := q.Add(context.Background(), map[string]any{"n": 7}, 0); err != nil {
		t.Fatalf("Add: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deliveries := make(chan int, 3)
	retryNums := make(chan int, 3)
	done := make(chan error, 1)
	calls := 0
	go func() {
		done <- q.Consume(runCtx, func(_ context.Context, msg *queues.Message) error {
			calls++
			deliveries <- calls
			retryNums <- msg.RetryNum
			if calls <= 2 {
				return inFlightTestError{}
			}
			return nil
		}, queues.ConsumeOptions{
			IdleDelay:     5 * time.Millisecond,
			InFlightDelay: 10 * time.Millisecond,
		})
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-deliveries:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out after %d deliveries", i)
		}
		select {
		case retryNum := <-retryNums:
			if retryNum != 0 {
				t.Fatalf("delivery %d RetryNum = %d, want 0 (in-flight must not consume the budget)", i+1, retryNum)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("delivery %d carried no RetryNum", i+1)
		}
	}
	cancel()
	<-done
	if calls != 3 {
		t.Fatalf("handler calls = %d, want 3", calls)
	}
}

// TestDelayConsumeReportsReadyOnceAfterFirstPop is the delayed-queue twin: the
// first range query that returns defines readiness, and a handled batch must not
// repeat it.
// TestDelayConsumeReportsReadyOnceAfterFirstPop 是延迟队列的同款用例：第一次成功的
// 范围查询就确定就绪，处理完一批也不能重复上报。
func TestDelayConsumeReportsReadyOnceAfterFirstPop(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay-ready"})
	ready := make(chan struct{}, 4)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- q.Consume(ctx, func(context.Context, *queues.Message) error { return nil },
			queues.ConsumeOptions{OnReady: func() { ready <- struct{}{} }})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Consume returned %v, want nil", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Consume did not stop after cancellation")
		}
	})

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("OnReady did not fire after the first pop")
	}
	if err := q.Add(context.Background(), map[string]string{"id": "a"}, 0); err != nil {
		t.Fatalf("Add: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case <-ready:
		t.Fatal("OnReady fired again after a due message")
	default:
	}
}

// TestDelayConsumeRequeuesBatchOnShutdown is the P0 regression: the pop script
// deletes the whole due batch from the ZSET, so between Pop and the handler the
// messages exist only in memory. Returning on a cancelled context therefore
// discarded up to PopLimit messages without a trace. Every message of the batch
// must come back — the one the handler failed on included, because the failure
// was the cancellation.
// TestDelayConsumeRequeuesBatchOnShutdown 是 P0 的回归测试：弹出脚本把整批到期
// 消息从 ZSET 删除，因此在 Pop 与 handler 之间消息只存在于内存里。因 ctx 取消而返回
// 会静默丢掉最多 PopLimit 条。整批都必须回来——包括 handler 失败的那条，因为导致失败
// 的是取消。
func TestDelayConsumeRequeuesBatchOnShutdown(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay"})
	for i := 0; i < 3; i++ {
		if err := q.Add(context.Background(), map[string]int{"n": i}, 0); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	handled := make(chan struct{})
	var attempts int
	go func() {
		done <- q.Consume(ctx, func(handlerCtx context.Context, _ *queues.Message) error {
			attempts++
			close(handled)
			cancel()
			return handlerCtx.Err()
		}, queues.ConsumeOptions{IdleDelay: 10 * time.Millisecond})
	}()

	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never ran")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Consume did not return after cancellation")
	}

	if attempts != 1 {
		t.Fatalf("handler attempts = %d, want 1: the batch must stop at the cancel", attempts)
	}
	length, err := q.Len(context.Background())
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if length != 3 {
		t.Fatalf("delayed queue length = %d, want 3: the popped batch was dropped", length)
	}
}

// TestDelayConsumeRequeuesOnlyUnprocessedOnShutdown pins the other branch: a
// message that was handled successfully is not written back, and the ones the
// loop never reached are. Requeueing the handled one would duplicate work; the
// unprocessed ones would be lost.
// TestDelayConsumeRequeuesOnlyUnprocessedOnShutdown 固定另一条分支：处理成功的消息
// 不写回，循环没走到的消息要写回。写回已处理的那条会重复副作用，不写回未处理的则会丢。
func TestDelayConsumeRequeuesOnlyUnprocessedOnShutdown(t *testing.T) {
	q, _ := newDelayQueue(t, queues.Config{Key: "orders:delay"})
	for i := 0; i < 3; i++ {
		if err := q.Add(context.Background(), map[string]int{"n": i}, 0); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	handled := make(chan struct{})
	go func() {
		done <- q.Consume(ctx, func(_ context.Context, _ *queues.Message) error {
			// Succeed, then ask for shutdown: the cancellation now has to be
			// noticed between two messages, not inside a handler.
			// 先成功，再请求停机：此时取消是在两条消息之间被看到，而不是在 handler 内。
			cancel()
			close(handled)
			return nil
		}, queues.ConsumeOptions{IdleDelay: 10 * time.Millisecond})
	}()

	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("handler never ran")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Consume = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Consume did not return after cancellation")
	}

	length, err := q.Len(context.Background())
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if length != 2 {
		t.Fatalf("delayed queue length = %d, want 2: one handled, two must come back", length)
	}
}
