package kafka

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Client owns one Kafka client used for producing.
//
// A Client built by New validates its Config, connects once with Ping, and
// then reconnects internally whenever a broker goes away. A Client built by
// Wrap borrows an existing franz-go client: its configuration cannot be
// changed, and Close closes it (taking ownership, like support/amqp).
//
// Consumers need their own franz-go client because the consumer group is
// configured per client; see NewConsumer.
type Client struct {
	mu sync.Mutex

	cfg     Config
	raw     *kgo.Client
	managed bool // New built raw and may derive consumer clients from cfg
	closed  bool
}

var (
	// ErrClientRequired is returned when an operation receives a nil client.
	ErrClientRequired = errors.New("kafka: client is required")
	// ErrClientClosed is returned once Close has been called.
	ErrClientClosed = errors.New("kafka: client is closed")
	// ErrNotConfigured is returned when no connection configuration is
	// available: a client built by Wrap(nil), or NewIO before Configure.
	ErrNotConfigured = errors.New("kafka: no connection configuration")
)

// New validates cfg, builds the franz-go client and verifies connectivity
// with a metadata request bounded by Config.DialTimeout.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}

	raw, err := kgo.NewClient(cfg.clientOptions()...)
	if err != nil {
		return nil, fmt.Errorf("kafka: new client: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	if err := raw.Ping(pingCtx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("kafka: connect %s: %w", strings.Join(cfg.Brokers, ","), err)
	}
	return &Client{cfg: cfg, raw: raw, managed: true}, nil
}

// Wrap takes ownership of an existing franz-go client. The client keeps its
// own configuration; NewProducer and NewConsumer use it as-is. Close closes it
// but there is no reconnect at this layer because franz-go already reconnects
// internally.
func Wrap(raw *kgo.Client) *Client {
	return &Client{raw: raw}
}

// Raw returns the underlying franz-go client for advanced use. It is nil for
// Wrap(nil).
func (c *Client) Raw() *kgo.Client {
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
	out.Brokers = append([]string(nil), c.cfg.Brokers...)
	return out
}

// Close closes the shared client. It is safe to call more than once. Consumers
// created from this client own separate clients and must be closed separately.
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
		c.raw.Close()
	}
	return nil
}

// IsClosed reports whether the client is closed or has no connection.
func (c *Client) IsClosed() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed || c.raw == nil
}

// Ping verifies that at least one broker answers a metadata request.
func (c *Client) Ping(ctx context.Context) error {
	raw, _, _, err := c.rawClient()
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := raw.Ping(ctx); err != nil {
		return fmt.Errorf("kafka: ping: %w", err)
	}
	return nil
}

// rawClient returns the underlying client together with the state NewProducer
// and NewConsumer need. Wrapped clients report managed == false, so consumers
// cannot derive group configuration from them.
func (c *Client) rawClient() (*kgo.Client, Config, bool, error) {
	if c == nil {
		return nil, Config{}, false, ErrClientRequired
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, Config{}, false, ErrClientClosed
	}
	if c.raw == nil {
		return nil, Config{}, false, ErrNotConfigured
	}
	return c.raw, c.cfg, c.managed, nil
}
