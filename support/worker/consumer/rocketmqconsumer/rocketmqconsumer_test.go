package rocketmqconsumer_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// TestNewFactoryRebuildsConsumerPerRun is the P2-05 acceptance test: a Runner
// restart calls Run again, and the second Run must get a consumer that has
// never been consumed. The single-use New cannot do this, which is exactly the
// lifecycle confusion the audit pointed at.
// TestNewFactoryRebuildsConsumerPerRun 是 P2-05 的验收测试：Runner 重启会再次调用
// Run，第二次必须拿到一个从未 Consume 过的消费者。一次性 New 做不到，这正是审计
// 指出的生命周期混淆。
func TestNewFactoryRebuildsConsumerPerRun(t *testing.T) {
	// A channel rather than a slice: the factory runs on the Run goroutine while
	// the test goroutine waits for it.
	// 用 channel 而不是切片：工厂跑在 Run 的 goroutine 上，测试 goroutine 在等它。
	built := make(chan *fakeConsumer, 4)
	factory := func(context.Context) (rocketmqconsumer.Consumer, error) {
		c := &fakeConsumer{}
		built <- c
		return c, nil
	}
	w, err := rocketmqconsumer.NewFactory("orders", factory, rocketmqconsumer.Config{
		Handler: func(context.Context, *rocketmq.Message) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	var consumers []*fakeConsumer
	for run := 1; run <= 2; run++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()

		select {
		case c := <-built:
			consumers = append(consumers, c)
		case <-time.After(2 * time.Second):
			t.Fatalf("run %d did not build a consumer", run)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("run %d did not return after cancel", run)
		}
	}

	if len(consumers) != 2 {
		t.Fatalf("consumers built = %d, want 2 (one per Run)", len(consumers))
	}
	if consumers[0] == consumers[1] {
		t.Fatal("the second Run reused the first consumer")
	}
	for i, c := range consumers {
		if c.closes != 1 {
			t.Fatalf("consumer %d closed %d times, want 1", i, c.closes)
		}
	}
}

// TestNewFactoryClosesConsumerEvenWhenConsumeFails proves the close is
// unconditional: a failed attempt must not leak the client it built.
// TestNewFactoryClosesConsumerEvenWhenConsumeFails 证明关闭是无条件的：失败的
// 尝试不能漏掉它构建的客户端。
func TestNewFactoryClosesConsumerEvenWhenConsumeFails(t *testing.T) {
	sentinel := errors.New("broker unreachable")
	fake := &fakeConsumer{fail: sentinel}
	w, err := rocketmqconsumer.NewFactory("orders", func(context.Context) (rocketmqconsumer.Consumer, error) {
		return fake, nil
	}, rocketmqconsumer.Config{
		Handler: func(context.Context, *rocketmq.Message) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := w.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Run error = %v, want the consume error", err)
	}
	if fake.closes != 1 {
		t.Fatalf("closes = %d, want 1", fake.closes)
	}
}

// TestNewFactoryValidates covers the constructor guards.
// TestNewFactoryValidates 覆盖构造器校验。
func TestNewFactoryValidates(t *testing.T) {
	if _, err := rocketmqconsumer.NewFactory("w", nil, rocketmqconsumer.Config{}); !errors.Is(err, rocketmqconsumer.ErrFactoryRequired) {
		t.Fatalf("NewFactory(nil) error = %v", err)
	}
	factory := func(context.Context) (rocketmqconsumer.Consumer, error) { return &fakeConsumer{}, nil }
	if _, err := rocketmqconsumer.NewFactory("w", factory, rocketmqconsumer.Config{}); !errors.Is(err, rocketmqconsumer.ErrHandlerRequired) {
		t.Fatalf("NewFactory without handler error = %v", err)
	}
}

// TestFactoryWorkerReportsBuildError keeps a factory failure visible instead of
// letting the supervisor treat it as an idle exit.
// TestFactoryWorkerReportsBuildError 让工厂失败可见，而不是被监督者当成空闲退出。
func TestFactoryWorkerReportsBuildError(t *testing.T) {
	sentinel := errors.New("no client")
	w, err := rocketmqconsumer.NewFactory("orders", func(context.Context) (rocketmqconsumer.Consumer, error) {
		return nil, sentinel
	}, rocketmqconsumer.Config{
		Handler: func(context.Context, *rocketmq.Message) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := w.Run(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("Run error = %v, want the factory error", err)
	}
}
