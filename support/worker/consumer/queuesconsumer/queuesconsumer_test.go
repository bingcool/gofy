package queuesconsumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/queues"
	"github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/queuesconsumer"
)

type fakeQueue struct {
	deliver func(queues.HandlerFunc) error
	fail    error
}

func (f *fakeQueue) Consume(ctx context.Context, handler queues.HandlerFunc, _ queues.ConsumeOptions) error {
	if f.deliver != nil {
		if err := f.deliver(handler); err != nil {
			return err
		}
	}
	if f.fail != nil {
		return f.fail
	}
	<-ctx.Done()
	return nil
}

func TestNewValidates(t *testing.T) {
	if _, err := queuesconsumer.New("w", nil, queuesconsumer.Config{}); !errors.Is(err, queuesconsumer.ErrQueueRequired) {
		t.Fatalf("New(nil) error = %v", err)
	}
	if _, err := queuesconsumer.New("w", &fakeQueue{}, queuesconsumer.Config{}); !errors.Is(err, queuesconsumer.ErrHandlerRequired) {
		t.Fatalf("New without handler error = %v", err)
	}
}

func TestHandlerGetsDrainContext(t *testing.T) {
	drain, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	parent, cancelParent := context.WithCancel(context.Background())

	var seen error
	fake := &fakeQueue{deliver: func(handler queues.HandlerFunc) error {
		// queues.Consume passes its own context, which dies with the shutdown;
		// the adapter must swap in the drain context.
		// queues.Consume 传的是自己那个随停机取消的 context，适配器必须换成
		// drain context。
		dead, cancel := context.WithCancel(context.Background())
		cancel()
		return handler(dead, &queues.Message{})
	}}
	w, err := queuesconsumer.New("orders", fake, queuesconsumer.Config{
		Handler: func(ctx context.Context, _ *queues.Message) error {
			seen = ctx.Err()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cancelParent()
	if err := w.Run(worker.WithDrainContext(parent, drain)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seen != nil {
		t.Fatalf("handler context was cancelled: %v", seen)
	}
}

func TestErrorPropagates(t *testing.T) {
	fake := &fakeQueue{fail: errors.New("redis down")}
	w, err := queuesconsumer.New("orders", fake, queuesconsumer.Config{
		Handler: func(context.Context, *queues.Message) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("Run error = nil, want redis down")
	}
}
