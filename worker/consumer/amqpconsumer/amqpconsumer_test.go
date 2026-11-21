package amqpconsumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/amqp"
	"github.com/bingcool/gofy/worker"
	"github.com/bingcool/gofy/worker/consumer/amqpconsumer"
	amqp091 "github.com/rabbitmq/amqp091-go"
)

type fakeConsumer struct {
	deliver func(amqp.HandlerFunc) error
	fail    error
	closes  int
}

func (f *fakeConsumer) Consume(ctx context.Context, handler amqp.HandlerFunc, _ amqp.ConsumeOptions) error {
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
	if _, err := amqpconsumer.New("w", nil, amqpconsumer.Config{}); !errors.Is(err, amqpconsumer.ErrConsumerRequired) {
		t.Fatalf("New(nil) error = %v", err)
	}
	if _, err := amqpconsumer.New("w", &fakeConsumer{}, amqpconsumer.Config{}); !errors.Is(err, amqpconsumer.ErrHandlerRequired) {
		t.Fatalf("New without handler error = %v", err)
	}
}

func TestHandlerGetsDrainContextAndCloseOnShutdown(t *testing.T) {
	drain, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	parent, cancelParent := context.WithCancel(context.Background())

	var seen error
	fake := &fakeConsumer{deliver: func(handler amqp.HandlerFunc) error {
		dead, cancel := context.WithCancel(context.Background())
		cancel()
		return handler(dead, amqp091.Delivery{})
	}}
	w, err := amqpconsumer.New("orders", fake, amqpconsumer.Config{
		Handler: func(ctx context.Context, _ amqp091.Delivery) error {
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
	fake := &fakeConsumer{fail: errors.New("connection lost")}
	w, err := amqpconsumer.New("orders", fake, amqpconsumer.Config{
		Handler: func(context.Context, amqp091.Delivery) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("Run error = nil, want connection lost")
	}
	if fake.closes != 0 {
		t.Fatalf("Close calls = %d, want 0 after a retryable error", fake.closes)
	}
}
