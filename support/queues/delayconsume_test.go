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
