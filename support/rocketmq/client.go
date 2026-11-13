package rocketmq

import (
	"context"
	"errors"
	"fmt"
	"sync"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
)

var (
	// ErrClientRequired is returned when an operation receives a nil client.
	// ErrClientRequired 在操作拿到 nil 客户端时返回。
	ErrClientRequired = errors.New("rocketmq: client is required")
	// ErrClientClosed is returned once Close has been called.
	// ErrClientClosed 在 Close 被调用之后返回。
	ErrClientClosed = errors.New("rocketmq: client is closed")
	// ErrNotConfigured is returned when no connection configuration is
	// available: a client built by Wrap(nil), or NewIO before Configure.
	// ErrNotConfigured 表示没有可用的连接配置：Wrap(nil) 创建的客户端，或
	// NewIO 尚未 Configure。
	ErrNotConfigured = errors.New("rocketmq: no connection configuration")
)

// Client owns one RocketMQ producer.
//
// New validates Config and starts the producer. When StartupTopics is set and
// SkipStartupCheck is not, it also asks the proxy for the route of every
// entry, which fails startup when the endpoint or a topic is unavailable. v5
// producers are not bound to a topic, so there is no Ping: set StartupTopics
// to fail fast, or leave it empty and let the first publish establish the
// connection. Wrap takes ownership of an existing producer: it has no Config
// and does not start or check anything.
//
// Consumers are independent clients in RocketMQ; build them with NewConsumer
// from the same Config.
// Client 持有一个 RocketMQ Producer。
// New 校验 Config 并启动 Producer。设置了 StartupTopics 且未开启
// SkipStartupCheck 时，还会向 proxy 查询每一条的路由，端点不可达或 topic 不存在
// 都会让启动失败。v5 的 Producer 不绑定 topic，因此没有 Ping：需要快速失败就
// 设置 StartupTopics，否则留空，由第一次发送建立连接。Wrap 接管已有 Producer，
// 它没有 Config，也不做启动或检查。
// RocketMQ 的 Consumer 是独立客户端，需用同一份 Config 交给 NewConsumer 构建。
type Client struct {
	mu sync.Mutex

	cfg    Config
	raw    rmq.Producer
	closed bool
}

// New validates cfg, builds the producer and starts it.
// New 校验 cfg，构建并启动 Producer。
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	raw, err := rmq.NewProducer(cfg.rmqConfig(""), cfg.producerOptions()...)
	if err != nil {
		return nil, fmt.Errorf("rocketmq: new producer at %s: %w", cfg.Endpoint, err)
	}
	if err := raw.Start(); err != nil {
		// Start may have registered the client before failing.
		// Start 失败前可能已经注册了客户端，这里停掉它，避免泄漏半启动的
		// Producer。
		_ = raw.GracefulStop()
		return nil, fmt.Errorf("rocketmq: start producer at %s: %w", cfg.Endpoint, err)
	}
	return &Client{cfg: cfg, raw: raw}, nil
}

// Wrap takes ownership of an existing producer. The client keeps no
// configuration; Close stops the producer.
// Wrap 接管一个已有的 Producer。该客户端不保存配置，Close 会停止这个 Producer。
func Wrap(raw rmq.Producer) *Client {
	return &Client{raw: raw}
}

// Raw returns the underlying producer for advanced use.
// Raw 返回底层 Producer，供高级用法使用。
func (c *Client) Raw() rmq.Producer {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.raw
}

// Config returns the effective configuration. A wrapped client returns the
// zero value because it has no configuration.
// Config 返回实际生效的配置。Wrap 创建的客户端没有配置，返回零值。
func (c *Client) Config() Config {
	if c == nil {
		return Config{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	out := c.cfg
	out.StartupTopics = append([]string(nil), c.cfg.StartupTopics...)
	return out
}

// Close stops the producer. It is safe to call more than once.
// Close 停止 Producer，可重复调用。关闭前应先停掉由同一份 Config 构建的
// Consumer。
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	if c.raw != nil {
		return c.raw.GracefulStop()
	}
	return nil
}

// IsClosed reports whether the client is closed or has no producer.
// IsClosed 报告客户端是否已关闭或没有底层 Producer。
func (c *Client) IsClosed() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed || c.raw == nil
}

// producer returns the producer under the lock so a concurrent Close cannot
// invalidate it between the check and the call.
// producer 在持锁状态下返回 Producer，避免并发的 Close 在检查与调用之间让它
// 失效。
func (c *Client) producer() (rmq.Producer, error) {
	if c == nil {
		return nil, ErrClientRequired
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, ErrClientClosed
	}
	if c.raw == nil {
		return nil, ErrNotConfigured
	}
	return c.raw, nil
}
