// Package amqpconsumer adapts an AMQP queue consumer to worker.Worker.
// 包 amqpconsumer 把 AMQP 队列消费者适配成 worker.Worker。
package amqpconsumer

import (
	"context"
	"errors"
	"fmt"

	"github.com/bingcool/gofy/support/amqp"
	"github.com/bingcool/gofy/support/worker"
	amqp091 "github.com/rabbitmq/amqp091-go"
)

var (
	// ErrConsumerRequired means New was called without a queue.
	// ErrConsumerRequired 表示调用 New 时没有提供队列。
	ErrConsumerRequired = errors.New("amqpconsumer: consumer is required")

	// ErrHandlerRequired means New was called without a handler.
	// ErrHandlerRequired 表示调用 New 时没有提供处理函数。
	ErrHandlerRequired = errors.New("amqpconsumer: handler is required")
)

// Consumer is the part of amqp.Queue this adapter needs.
// Consumer 是 amqp.Queue 中本适配器用到的部分。
type Consumer interface {
	Consume(ctx context.Context, handler amqp.HandlerFunc, opts amqp.ConsumeOptions) error
	Close() error
}

// Config carries the handler and the consume options unchanged.
// Config 原样携带处理函数与消费选项。
type Config struct {
	Handler amqp.HandlerFunc
	Options amqp.ConsumeOptions
}

type consumerWorker struct {
	name     string
	consumer Consumer
	handler  amqp.HandlerFunc
	options  amqp.ConsumeOptions
}

// New builds a worker around one queue. Concurrency above one means several
// channels on the same queue, which the broker treats as competing consumers;
// raising PrefetchCount is usually the cheaper way to add throughput.

// Name returns the worker name used by logs and metrics.
// Name 返回日志与指标使用的 worker 名。
func (w *consumerWorker) Name() string { return w.name }

// New 用一个队列构造 worker。并发大于 1 意味着同一队列上多个 channel，broker 会把
// 它们当作竞争消费者；通常调大 PrefetchCount 是更划算的扩容方式。
func New(name string, consumer Consumer, cfg Config) (worker.Worker, error) {
	if consumer == nil {
		return nil, ErrConsumerRequired
	}
	if cfg.Handler == nil {
		return nil, ErrHandlerRequired
	}
	return &consumerWorker{
		name:     name,
		consumer: consumer,
		handler:  cfg.Handler,
		options:  cfg.Options,
	}, nil
}

// Run consumes until ctx is cancelled and closes the queue on the way out.
// Deliveries are handled with the drain context so in-flight work can finish
// instead of being requeued on shutdown.
// Run 消费到 ctx 取消，并在退出时关闭队列。投递使用 drain context 处理，在途任务
// 因此可以完成，而不是因停机被重新入队。
func (w *consumerWorker) Run(ctx context.Context) error {
	drain := worker.DrainContext(ctx)
	handler := func(_ context.Context, delivery amqp091.Delivery) error {
		return w.handler(drain, delivery)
	}
	err := w.consumer.Consume(ctx, handler, w.options)
	if ctx.Err() != nil {
		if closeErr := w.consumer.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("amqpconsumer %s: close: %w", w.name, closeErr)
		}
	}
	return err
}
