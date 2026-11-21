// Package queuesconsumer adapts the Redis queue consume loop to worker.Worker.
// 包 queuesconsumer 把 Redis 队列的消费循环适配成 worker.Worker。
package queuesconsumer

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/support/queues"
	"github.com/bingcool/gofy/worker"
)

var (
	// ErrQueueRequired means New was called without a queue.
	// ErrQueueRequired 表示调用 New 时没有提供队列。
	ErrQueueRequired = errors.New("queuesconsumer: queue is required")

	// ErrHandlerRequired means New was called without a handler.
	// ErrHandlerRequired 表示调用 New 时没有提供处理函数。
	ErrHandlerRequired = errors.New("queuesconsumer: handler is required")
)

// Consumer is the part of queues.Queue this adapter needs. The Redis client is
// owned by the application, so there is nothing to close here.
// Consumer 是 queues.Queue 中本适配器用到的部分。Redis 客户端归应用所有，因此
// 这里没有需要关闭的资源。
type Consumer interface {
	Consume(ctx context.Context, handler queues.HandlerFunc, opts queues.ConsumeOptions) error
}

// Config carries the handler and the consume options unchanged.
// Config 原样携带处理函数与消费选项。
type Config struct {
	Handler queues.HandlerFunc
	Options queues.ConsumeOptions
}

type consumerWorker struct {
	name    string
	queue   Consumer
	handler queues.HandlerFunc
	options queues.ConsumeOptions
}

// New builds a worker around one queue.
// New 用一个队列构造 worker。
func New(name string, queue Consumer, cfg Config) (worker.Worker, error) {
	if queue == nil {
		return nil, ErrQueueRequired
	}
	if cfg.Handler == nil {
		return nil, ErrHandlerRequired
	}
	return &consumerWorker{
		name:    name,
		queue:   queue,
		handler: cfg.Handler,
		options: cfg.Options,
	}, nil
}

// Name returns the worker name used by logs and metrics.
// Name 返回日志与指标使用的 worker 名。
func (w *consumerWorker) Name() string { return w.name }

// Run pops and handles messages until ctx is cancelled. The handler gets the
// drain context: queues.Consume passes its own context to the handler, which
// dies with the shutdown.
// Run 持续取出并处理消息直到 ctx 取消。handler 收到的是 drain context：
// queues.Consume 默认把自己那个会随停机取消的 context 传给 handler。
func (w *consumerWorker) Run(ctx context.Context) error {
	drain := worker.DrainContext(ctx)
	return w.queue.Consume(ctx, func(_ context.Context, msg *queues.Message) error {
		return w.handler(drain, msg)
	}, w.options)
}
