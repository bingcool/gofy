// Package kafkaconsumer adapts a Kafka consumer to worker.Worker, so a project
// does not have to reimplement start, drain and close ordering.
// 包 kafkaconsumer 把 Kafka 消费者适配成 worker.Worker，项目因此不必再自己实现
// 启动、drain 与关闭的顺序。
package kafkaconsumer

import (
	"context"
	"errors"
	"fmt"

	"github.com/bingcool/gofy/support/kafka"
	"github.com/bingcool/gofy/worker"
	"github.com/twmb/franz-go/pkg/kgo"
)

var (
	// ErrConsumerRequired means New was called without a consumer.
	// ErrConsumerRequired 表示调用 New 时没有提供消费者。
	ErrConsumerRequired = errors.New("kafkaconsumer: consumer is required")

	// ErrHandlerRequired means New was called without a handler.
	// ErrHandlerRequired 表示调用 New 时没有提供处理函数。
	ErrHandlerRequired = errors.New("kafkaconsumer: handler is required")
)

// Consumer is the part of kafka.Consumer this adapter needs, which keeps the
// adapter testable without a broker.
// Consumer 是 kafka.Consumer 中本适配器用到的部分，因此没有 broker 也能测试。
type Consumer interface {
	Consume(ctx context.Context, handler kafka.HandlerFunc, opts kafka.ConsumeOptions) error
	Close() error
}

// Config carries the handler and the consumer options unchanged.
// Config 原样携带处理函数与消费者选项。
type Config struct {
	Handler kafka.HandlerFunc
	Options kafka.ConsumeOptions
}

type consumerWorker struct {
	name     string
	consumer Consumer
	handler  kafka.HandlerFunc
	options  kafka.ConsumeOptions
}

// New builds a worker around one consumer. Each worker must own its consumer:
// kafka.Consumer is meant for a single Consume goroutine, so raising the
// worker's concurrency means building that many consumers with the same group.

// Name returns the worker name used by logs and metrics.
// Name 返回日志与指标使用的 worker 名。
func (w *consumerWorker) Name() string { return w.name }

// New 用一个消费者构造 worker。每个 worker 必须独占消费者：kafka.Consumer 只
// 对应一个 Consume goroutine，因此提高 worker 并发数意味着用同一个消费组构造
// 同样数量的消费者。
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

// Run consumes until ctx is cancelled. Deliveries are handled with the drain
// context: without it a shutdown would abort a record that still had budget to
// finish, and the record would be redelivered and processed twice.
// Run 消费到 ctx 取消。投递使用 drain context 处理：否则停机时本可完成的记录会被
// 中断，随后重新投递并处理两次。
func (w *consumerWorker) Run(ctx context.Context) error {
	drain := worker.DrainContext(ctx)
	handler := func(_ context.Context, record *kgo.Record) error {
		// Ignore the per-delivery context: it is derived from ctx and dies with
		// the shutdown, which is exactly what in-flight work must survive.
		// 忽略每次投递自带的 context：它是 ctx 的派生，会随停机一起取消，而在途
		// 任务恰恰需要活过这个取消。
		return w.handler(drain, record)
	}
	err := w.consumer.Consume(ctx, handler, w.options)
	// Close only on shutdown: an error return is handed to the restart policy,
	// which reuses this consumer, and closing it would make that impossible.
	// 只在停机时 Close：错误返回会交给重启策略复用这个消费者，提前关闭会让复用
	// 失效。
	if ctx.Err() != nil {
		if closeErr := w.consumer.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("kafkaconsumer %s: close: %w", w.name, closeErr)
		}
	}
	return err
}
