package rocketmqconsumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/rocketmq"
	"github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/rocketmqconsumer"
)

type fakeConsumer struct {
	deliver func(rocketmq.HandlerFunc) error
	fail    error
	closes  int
}

func (f *fakeConsumer) Consume(ctx context.Context, handler rocketmq.HandlerFunc, _ rocketmq.ConsumeOptions) error {
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

func (f *fakeConsumer) Close() error {
	f.closes++
	return nil
}

func TestNewValidates(t *testing.T) {
	if _, err := rocketmqconsumer.New("w", nil, rocketmqconsumer.Config{}); !errors.Is(err, rocketmqconsumer.ErrConsumerRequired) {
		t.Fatalf("New(nil) error = %v", err)
	}
	if _, err := rocketmqconsumer.New("w", &fakeConsumer{}, rocketmqconsumer.Config{}); !errors.Is(err, rocketmqconsumer.ErrHandlerRequired) {
		t.Fatalf("New without handler error = %v", err)
	}
}

func TestHandlerGetsDrainContextAndCloseOnShutdown(t *testing.T) {
	drain, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	parent, cancelParent := context.WithCancel(context.Background())

	var seen error
	fake := &fakeConsumer{deliver: func(handler rocketmq.HandlerFunc) error {
		dead, cancel := context.WithCancel(context.Background())
		cancel()
		return handler(dead, &rocketmq.Message{})
	}}
	w, err := rocketmqconsumer.New("orders", fake, rocketmqconsumer.Config{
		Handler: func(ctx context.Context, _ *rocketmq.Message) error {
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
	if fake.closes != 1 {
		t.Fatalf("Close calls = %d, want 1 on shutdown", fake.closes)
	}
}

func TestErrorPathKeepsConsumerOpen(t *testing.T) {
	fake := &fakeConsumer{fail: errors.New("broker down")}
	w, err := rocketmqconsumer.New("orders", fake, rocketmqconsumer.Config{
		Handler: func(context.Context, *rocketmq.Message) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("Run error = nil, want broker down")
	}
	if fake.closes != 0 {
		t.Fatalf("Close calls = %d, want 0 after a retryable error", fake.closes)
	}
}
