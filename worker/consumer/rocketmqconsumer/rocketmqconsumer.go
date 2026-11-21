// Package rocketmqconsumer adapts a RocketMQ consumer to worker.Worker.
//
// Two constructors exist because a RocketMQ consumer is single-use: Consume
// once, then Close. New takes such a consumer and is therefore a one-shot
// worker; NewFactory takes a factory and builds a new consumer per Run, which
// is what a Runner restart policy needs. The production path
// (support/consumer.NewRocketMQ) already builds its consumer inside Run.
// 包 rocketmqconsumer 把 RocketMQ 消费者适配成 worker.Worker。
//
// 之所以有两个构造器：RocketMQ 消费者是一次性对象，Consume 一次后 Close。New 接收
// 这样的消费者，因此是一次性 worker；NewFactory 接收工厂函数并每次 Run 新建消费者，
// 这才是 Runner 重启策略需要的。生产路径（support/consumer.NewRocketMQ）本来就在
// Run 内构建消费者。
package rocketmqconsumer

import (
	"context"
	"errors"
	"fmt"

	"github.com/bingcool/gofy/support/rocketmq"
	"github.com/bingcool/gofy/worker"
)

var (
	// ErrConsumerRequired means New was called without a consumer.
	// ErrConsumerRequired 表示调用 New 时没有提供消费者。
	ErrConsumerRequired = errors.New("rocketmqconsumer: consumer is required")

	// ErrFactoryRequired means NewFactory was called without a factory.
	// ErrFactoryRequired 表示调用 NewFactory 时没有提供工厂函数。
	ErrFactoryRequired = errors.New("rocketmqconsumer: consumer factory is required")

	// ErrHandlerRequired means the config carries no handler.
	// ErrHandlerRequired 表示配置里没有处理函数。
	ErrHandlerRequired = errors.New("rocketmqconsumer: handler is required")
)

// Consumer is the part of rocketmq.Consumer this adapter needs.
// Consumer 是 rocketmq.Consumer 中本适配器用到的部分。
type Consumer interface {
	Consume(ctx context.Context, handler rocketmq.HandlerFunc, opts rocketmq.ConsumeOptions) error
	Close() error
}

// ConsumerFactory builds one consumer. It is called at the start of every Run,
// so a restart gets a consumer that has never been consumed from.
// ConsumerFactory 构建一个消费者。它在每次 Run 开始时调用，因此重启拿到的是一个
// 从未 Consume 过的消费者。
type ConsumerFactory func(ctx context.Context) (Consumer, error)

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

// factoryWorker builds a fresh consumer for every Run.
// factoryWorker 为每次 Run 构建全新的消费者。
type factoryWorker struct {
	name    string
	factory ConsumerFactory
	handler rocketmq.HandlerFunc
	options rocketmq.ConsumeOptions
}

// New builds a worker around one already-built consumer.
//
// A RocketMQ consumer is single-use, so this worker is one-shot: a Runner that
// restarts it after a failure would consume from a client that has already been
// consumed. Use NewFactory in that position. Close happens only when ctx was
// cancelled, because on an error the consumer is still the only one this worker
// has and the caller may reuse it.
// New 用已经创建好的消费者构造 worker。
//
// RocketMQ 消费者是一次性对象，因此这个 worker 也是一次性的：Runner 在失败后重启它
// 会从一个已经 Consume 过的客户端继续消费。那种位置请用 NewFactory。Close 只在 ctx
// 已取消时执行：出错时这个消费者仍是本 worker 唯一的那个，调用方可能继续复用。
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

// NewFactory builds a worker that creates its consumer at the start of every
// Run and always closes it on the way out. This is the constructor to use
// behind a Runner: the restart policy calls Run again after a failure, and a
// single-use consumer cannot survive that.
//
// The trade-off is one consumer per attempt, which is what reconnecting costs
// anyway.
// NewFactory 构造一个在每次 Run 开始时创建消费者、退出时总是关闭的 worker。放在
// Runner 后面时应该用它：重启策略在失败后会再次调用 Run，而一次性消费者撑不过一次
// 重启。
//
// 代价是每次尝试一个消费者，这本来就是重连的成本。
func NewFactory(name string, factory ConsumerFactory, cfg Config) (worker.Worker, error) {
	if factory == nil {
		return nil, ErrFactoryRequired
	}
	if cfg.Handler == nil {
		return nil, ErrHandlerRequired
	}
	return &factoryWorker{
		name:    name,
		factory: factory,
		handler: cfg.Handler,
		options: cfg.Options,
	}, nil
}

// Name returns the worker name used by logs and metrics.
// Name 返回日志与指标使用的 worker 名。
func (w *consumerWorker) Name() string { return w.name }

// Name returns the worker name used by logs and metrics.
// Name 返回日志与指标使用的 worker 名。
func (w *factoryWorker) Name() string { return w.name }

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

// Run builds a consumer, consumes until ctx is cancelled or the consumer
// returns, and always closes it. Closing is unconditional here — unlike New,
// where the consumer belongs to the caller — because each Run owns the consumer
// it built.
// Run 构建消费者、消费到 ctx 取消或消费者返回，并且总是关闭它。与 New 中消费者归
// 调用方所有不同，这里的关闭是无条件的：每次 Run 拥有自己构建的消费者。
func (w *factoryWorker) Run(ctx context.Context) error {
	consumer, err := w.factory(ctx)
	if err != nil {
		return fmt.Errorf("rocketmqconsumer %s: build consumer: %w", w.name, err)
	}
	drain := worker.DrainContext(ctx)
	handler := func(_ context.Context, message *rocketmq.Message) error {
		return w.handler(drain, message)
	}
	consumeErr := consumer.Consume(ctx, handler, w.options)
	if closeErr := consumer.Close(); closeErr != nil && consumeErr == nil {
		consumeErr = fmt.Errorf("rocketmqconsumer %s: close: %w", w.name, closeErr)
	}
	return consumeErr
}
