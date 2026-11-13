package amqp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// Client owns one AMQP connection and reconnects it on demand.
//
// A Client built by New keeps its Config and can transparently reconnect after
// the broker closes the connection. A Client built by Wrap only borrows an
// existing connection and cannot reconnect.
//
// amqp091 channels are not safe for concurrent use, so the Client never shares
// a channel. Callers ask for a new channel for each workflow; Queue does this
// for publishing and consuming.
// Client 持有一个 AMQP 连接，并在需要时重建。
// New 创建的是受管客户端：保留 Config，broker 断开后可透明重连；Wrap 创建的
// 客户端只借用已有连接，断开后无法重连。
// amqp091 的 channel 不支持并发使用，Client 因此从不共享 channel。调用方应为
// 每个工作流申请新的 channel，Queue 的发布与消费也是如此。
type Client struct {
	mu sync.Mutex

	cfg     Config
	managed bool // New built the connection and may rebuild it；true 表示连接由 New 创建、断开后可重建，Wrap 创建的不会重建
	conn    *amqp091.Connection

	// next is the host that connected last; reconnects start there.
	// next 是最后连接成功的主机下标，重连时从这里开始尝试。
	next   int
	closed bool
}

// ErrClientClosed is returned once Close has been called.
// ErrClientClosed 在 Close 被调用之后返回。
var ErrClientClosed = errors.New("amqp: client is closed")

// ErrNotConfigured is returned when no connection configuration is available:
// a client built by Wrap after its connection is gone, or NewIO before
// Configure.
// ErrNotConfigured 表示没有可用的连接配置：Wrap 创建的客户端连接已失效，或
// NewIO 尚未 Configure。
var ErrNotConfigured = errors.New("amqp: no connection configuration")

// New validates cfg, connects to the first reachable host and returns a
// managed client. The connection handshake already verifies credentials.
// New 校验 cfg，连接第一个可达的 host 并返回受管客户端。凭据在握手阶段就已
// 校验，因此这里失败即代表配置不可用。
func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}

	client := &Client{cfg: cfg, managed: true}
	if err := client.connectLocked(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// Wrap takes ownership of an existing connection. Close still closes it, but
// the client cannot reconnect: once conn is closed, Channel reports
// ErrNotConfigured. The connection's vhost must match the topology you use.
// Wrap 接管一个已有连接。Close 仍会关闭它，但客户端无法重连：conn 关闭后
// Channel 返回 ErrNotConfigured。该连接的 vhost 必须与使用的 topology 一致。
func Wrap(conn *amqp091.Connection) *Client {
	return &Client{conn: conn}
}

// Channel opens a channel, rebuilding the connection first when it was closed
// unexpectedly. It reports ErrClientClosed after Close and ErrNotConfigured
// when a wrapped connection is no longer usable.
// Channel 打开一个 channel，连接意外关闭时先重建连接。Close 之后返回
// ErrClientClosed；Wrap 的连接不可用时返回 ErrNotConfigured。
func (c *Client) Channel() (*amqp091.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, ErrClientClosed
	}
	if c.conn == nil || c.conn.IsClosed() {
		if err := c.connectLocked(context.Background()); err != nil {
			return nil, err
		}
	}
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("amqp: open channel: %w", err)
	}
	return ch, nil
}

// Close closes the connection and marks the client closed. It is safe to call
// more than once.
// Close 关闭连接并把客户端标记为已关闭，可重复调用。
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true
	if c.conn == nil || c.conn.IsClosed() {
		return nil
	}
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("amqp: close: %w", err)
	}
	return nil
}

// IsClosed reports whether the client is closed or currently disconnected. A
// managed client may still reconnect on the next Channel call.
// IsClosed 报告客户端是否已关闭或当前处于断开状态。受管客户端仍可能在下次
// Channel 调用时重连。
func (c *Client) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed || c.conn == nil || c.conn.IsClosed()
}

// Config returns the effective configuration. Wrapped clients return the zero
// value because they have no configuration.
// Config 返回实际生效的配置。Wrap 创建的客户端没有配置，返回零值。
func (c *Client) Config() Config {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := c.cfg
	out.Hosts = append([]Host(nil), c.cfg.Hosts...)
	return out
}

func (c *Client) connectLocked(ctx context.Context) error {
	if c.closed {
		return ErrClientClosed
	}
	if !c.managed {
		return ErrNotConfigured
	}

	var errs []error
	for i := 0; i < len(c.cfg.Hosts); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		index := (c.next + i) % len(c.cfg.Hosts)
		host := c.cfg.Hosts[index]
		conn, err := amqp091.DialConfig(host.uri(), amqp091.Config{
			Heartbeat: c.cfg.Heartbeat,
			Locale:    c.cfg.Locale,
			Dial:      amqp091.DefaultDial(c.cfg.ConnectionTimeout),
		})
		if err == nil {
			c.conn = conn
			c.next = index
			return nil
		}
		// Only the address is kept, so a URI password never enters the error
		// chain.
		// 只带 address，避免把 URI 里的密码写进错误链。
		errs = append(errs, fmt.Errorf("%s: %w", host.address(), err))
	}
	return fmt.Errorf("amqp: connect failed: %w", errors.Join(errs...))
}
