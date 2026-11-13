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
// Client 持有一个用于生产的 Kafka 客户端。
// New 创建的客户端会校验 Config、用 Ping 连接一次，之后 broker 掉线时由内部
// 自动重连；Wrap 借用已有的 franz-go 客户端，配置不可再改，Close 会关闭它
// 并接管其所有权（与 support/amqp 一致）。
// 消费者组配置是客户端级别的，消费者必须另建客户端，见 NewConsumer。
type Client struct {
	mu sync.Mutex

	cfg     Config
	raw     *kgo.Client
	managed bool // New built raw and may derive consumer clients from cfg；true 表示 raw 由 New 创建，可据此按 cfg 派生消费者客户端
	closed  bool
}

var (
	// ErrClientRequired is returned when an operation receives a nil client.
	// ErrClientRequired 在操作拿到 nil 客户端时返回。
	ErrClientRequired = errors.New("kafka: client is required")
	// ErrClientClosed is returned once Close has been called.
	// ErrClientClosed 在 Close 被调用之后返回。
	ErrClientClosed = errors.New("kafka: client is closed")
	// ErrNotConfigured is returned when no connection configuration is
	// available: a client built by Wrap(nil), or NewIO before Configure.
	// ErrNotConfigured 表示没有可用的连接配置：Wrap(nil) 创建的客户端，或
	// NewIO 尚未 Configure。
	ErrNotConfigured = errors.New("kafka: no connection configuration")
)

// New validates cfg, builds the franz-go client and verifies connectivity
// with a metadata request bounded by Config.DialTimeout.
// New 校验 cfg，构建 franz-go 客户端，并用一次 metadata 请求验证连通性，
// 该请求受 Config.DialTimeout 约束。
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
// Wrap 接管一个已有的 franz-go 客户端，其配置保持不变，NewProducer 与
// NewConsumer 直接使用它。Close 会关闭它；franz-go 自身负责重连，本层不再
// 维护重连逻辑。
func Wrap(raw *kgo.Client) *Client {
	return &Client{raw: raw}
}

// Raw returns the underlying franz-go client for advanced use. It is nil for
// Wrap(nil).
// Raw 返回底层的 franz-go 客户端，供高级用法使用。Wrap(nil) 时返回 nil。
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
// Config 返回实际生效的配置。Wrap 创建的客户端没有配置，返回零值。
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
// Close 关闭共享客户端，可重复调用。由它创建的 Consumer 拥有独立客户端，
// 必须单独关闭；关闭顺序上应先停消费者再关共享客户端。
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
// IsClosed 报告客户端是否已关闭或没有底层连接。
func (c *Client) IsClosed() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed || c.raw == nil
}

// Ping verifies that at least one broker answers a metadata request.
// Ping 确认至少有一个 broker 能响应 metadata 请求。
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
// rawClient 返回底层客户端以及 NewProducer、NewConsumer 需要的状态。Wrap 创建的
// 客户端 managed 为 false，因此无法据此派生出消费者组配置。
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
