// Package rocketmqconsumer adapts a RocketMQ consumer to worker.Worker.
// 包 rocketmqconsumer 把 RocketMQ 消费者适配成 worker.Worker。
package rocketmqconsumer

import (
	"context"
	"errors"
	"fmt"

	"github.com/bingcool/gofy/support/rocketmq"
	"github.com/bingcool/gofy/support/worker"
)

var (
	// ErrConsumerRequired means New was called without a consumer.
	// ErrConsumerRequired 表示调用 New 时没有提供消费者。
	ErrConsumerRequired = errors.New("rocketmqconsumer: consumer is required")

	// ErrHandlerRequired means New was called without a handler.
	// ErrHandlerRequired 表示调用 New 时没有提供处理函数。
	ErrHandlerRequired = errors.New("rocketmqconsumer: handler is required")
)

// Consumer is the part of rocketmq.Consumer this adapter needs.
// Consumer 是 rocketmq.Consumer 中本适配器用到的部分。
type Consumer interface {
	Consume(ctx context.Context, handler rocketmq.HandlerFunc, opts rocketmq.ConsumeOptions) error
	Close() error
}

// Config carries the handler and the consume options unchanged.
// Config 原样携带处理函数与消费选项。
type Config struct {
	Handler rocketmq.HandlerFunc
	Options rocketmq.ConsumeOptions
}

type consumerWorker struct {
	name     string
	consumer Consumer
	handler  rocketmq.HandlerFunc
	options  rocketmq.ConsumeOptions
}

// New builds a worker around one consumer. A RocketMQ consumer is single-use:
// call Consume once, then Close, so concurrency means one consumer per instance.

// Name returns the worker name used by logs and metrics.
// Name 返回日志与指标使用的 worker 名。
func (w *consumerWorker) Name() string { return w.name }

// New 用一个消费者构造 worker。RocketMQ 消费者是一次性对象：Consume 一次后
// Close，因此并发意味着每个实例一个消费者。
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

// Run consumes until ctx is cancelled and closes the consumer on the way out.
// Deliveries use the drain context, and a failed message is acked or not by the
// handler's return value exactly as configured.
// Run 消费到 ctx 取消，并在退出时关闭消费者。投递使用 drain context；失败消息是否
// ack 仍由 handler 返回值与配置决定。
func (w *consumerWorker) Run(ctx context.Context) error {
	drain := worker.DrainContext(ctx)
	handler := func(_ context.Context, message *rocketmq.Message) error {
		return w.handler(drain, message)
	}
	err := w.consumer.Consume(ctx, handler, w.options)
	if ctx.Err() != nil {
		if closeErr := w.consumer.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("rocketmqconsumer %s: close: %w", w.name, closeErr)
		}
	}
	return err
}
