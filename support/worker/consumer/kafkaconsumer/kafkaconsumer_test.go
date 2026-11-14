package kafkaconsumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/kafka"
	"github.com/bingcool/gofy/support/worker"
	"github.com/bingcool/gofy/support/worker/consumer/kafkaconsumer"
	"github.com/twmb/franz-go/pkg/kgo"
)

type fakeConsumer struct {
	deliver func(kafka.HandlerFunc) error
	fail    error
	closes  int
}

func (f *fakeConsumer) Consume(ctx context.Context, handler kafka.HandlerFunc, _ kafka.ConsumeOptions) error {
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
	if _, err := kafkaconsumer.New("w", nil, kafkaconsumer.Config{}); !errors.Is(err, kafkaconsumer.ErrConsumerRequired) {
		t.Fatalf("New(nil) error = %v", err)
	}
	fake := &fakeConsumer{}
	if _, err := kafkaconsumer.New("w", fake, kafkaconsumer.Config{}); !errors.Is(err, kafkaconsumer.ErrHandlerRequired) {
		t.Fatalf("New without handler error = %v", err)
	}
}

func TestHandlerGetsDrainContextAndCloseOnShutdown(t *testing.T) {
	drain, cancelDrain := context.WithCancel(context.Background())
	defer cancelDrain()
	parent, cancelParent := context.WithCancel(context.Background())

	var seen error
	fake := &fakeConsumer{deliver: func(handler kafka.HandlerFunc) error {
		// A delivery context that already died with the shutdown: the adapter
		// must ignore it and hand the handler the drain context instead.
		// 一个已随停机取消的投递 context：适配器必须忽略它，改把 drain context
		// 交给 handler。
		dead, cancel := context.WithCancel(context.Background())
		cancel()
		return handler(dead, &kgo.Record{})
	}}
	w, err := kafkaconsumer.New("orders", fake, kafkaconsumer.Config{
		Handler: func(ctx context.Context, _ *kgo.Record) error {
			seen = ctx.Err()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cancelParent()
	ctx := worker.WithDrainContext(parent, drain)
	if err := w.Run(ctx); err != nil {
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
	w, err := kafkaconsumer.New("orders", fake, kafkaconsumer.Config{
		Handler: func(context.Context, *kgo.Record) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatalf("Run error = nil, want broker down")
	}
	// The restart policy reuses this consumer, so an error must not close it.
	// 重启策略会复用这个消费者，因此出错时不能关闭它。
	if fake.closes != 0 {
		t.Fatalf("Close calls = %d, want 0 after a retryable error", fake.closes)
	}
}
