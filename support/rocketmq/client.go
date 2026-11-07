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
	ErrClientRequired = errors.New("rocketmq: client is required")
	// ErrClientClosed is returned once Close has been called.
	ErrClientClosed = errors.New("rocketmq: client is closed")
	// ErrNotConfigured is returned when no connection configuration is
	// available: a client built by Wrap(nil), or NewIO before Configure.
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
type Client struct {
	mu sync.Mutex

	cfg    Config
	raw    rmq.Producer
	closed bool
}

// New validates cfg, builds the producer and starts it.
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
		_ = raw.GracefulStop()
		return nil, fmt.Errorf("rocketmq: start producer at %s: %w", cfg.Endpoint, err)
	}
	return &Client{cfg: cfg, raw: raw}, nil
}

// Wrap takes ownership of an existing producer. The client keeps no
// configuration; Close stops the producer.
func Wrap(raw rmq.Producer) *Client {
	return &Client{raw: raw}
}

// Raw returns the underlying producer for advanced use.
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
