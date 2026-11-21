package amqp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

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

	// dialing is non-nil while one goroutine is dialing. Other callers wait on
	// it instead of dialing in parallel, and Close is never blocked by a dial.
	// dialing 非空表示已有 goroutine 正在拨号。其它调用方等待它而不是并行拨号，
	// Close 也不会被拨号阻塞。
	dialing chan struct{}
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
	conn, index, err := client.dial(ctx, 0)
	if err != nil {
		return nil, err
	}
	client.conn = conn
	client.next = index
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

// Channel opens a channel with a background context. See ChannelContext.
// Channel 用后台 context 打开一个 channel，语义见 ChannelContext。
func (c *Client) Channel() (*amqp091.Channel, error) {
	return c.ChannelContext(context.Background())
}

// ChannelContext opens a channel, rebuilding the connection first when it was
// closed unexpectedly. It reports ErrClientClosed after Close and
// ErrNotConfigured when a wrapped connection is no longer usable.
//
// ctx bounds the reconnect, not the channel itself: cancelling it aborts a
// dial in flight, so shutdown does not have to wait out every host. Dialing
// happens outside the client lock, so a concurrent Close is never blocked.
// ChannelContext 打开一个 channel，连接意外关闭时先重建。Close 之后返回
// ErrClientClosed；Wrap 的连接不可用时返回 ErrNotConfigured。
//
// ctx 约束的是重连而不是 channel 本身：取消它会中止在途拨号，停机不必等完
// 每个 host。拨号在客户端锁之外进行，因此并发的 Close 不会被阻塞。
func (c *Client) ChannelContext(ctx context.Context) (*amqp091.Channel, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := c.connection(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
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

// connection returns a live connection, dialing one when needed. Only one
// goroutine dials at a time; the others wait for its result and re-check,
// because a dial that fails for the leader may still succeed on a retry with
// another context.
// connection 返回可用连接，必要时拨号建立。同一时刻只有一个 goroutine 拨号，其余
// 等待结果后重新检查：领头的拨号失败不代表带另一个 context 的重试也会失败。
func (c *Client) connection(ctx context.Context) (*amqp091.Connection, error) {
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, ErrClientClosed
		}
		if !c.managed {
			c.mu.Unlock()
			return nil, ErrNotConfigured
		}
		if c.conn != nil && !c.conn.IsClosed() {
			conn := c.conn
			c.mu.Unlock()
			return conn, nil
		}
		if wait := c.dialing; wait != nil {
			c.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		wait := make(chan struct{})
		c.dialing = wait
		start := c.next
		c.mu.Unlock()

		conn, index, err := c.dial(ctx, start)

		c.mu.Lock()
		c.dialing = nil
		closed := c.closed
		if err == nil && !closed {
			c.conn = conn
			c.next = index
		}
		close(wait)
		c.mu.Unlock()

		if err != nil {
			return nil, err
		}
		if closed {
			// Close ran while the dial was in flight: the new connection must
			// not outlive the client.
			// 拨号期间 Close 已经执行：新连接不能比客户端活得更久。
			_ = conn.Close()
			return nil, ErrClientClosed
		}
		return conn, nil
	}
}

// dial tries every host once, starting at start. Each attempt is bounded by
// ConnectionTimeout and by ctx, so a cancelled caller stops before the next
// host instead of waiting out the whole list.
// dial 从 start 开始把每个 host 试一次。每次尝试都受 ConnectionTimeout 与 ctx
// 约束，调用方取消后不会继续尝试后续 host。
func (c *Client) dial(ctx context.Context, start int) (*amqp091.Connection, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	hosts := c.cfg.Hosts
	var errs []error
	for i := 0; i < len(hosts); i++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		index := (start + i) % len(hosts)
		host := hosts[index]
		conn, err := c.dialHost(ctx, host)
		if err == nil {
			return conn, index, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, ctxErr
		}
		// Only the address is kept, so a URI password never enters the error
		// chain.
		// 只带 address，避免把 URI 里的密码写进错误链。
		errs = append(errs, fmt.Errorf("%s: %w", host.address(), err))
	}
	return nil, 0, fmt.Errorf("amqp: connect failed: %w", errors.Join(errs...))
}

func (c *Client) dialHost(ctx context.Context, host Host) (*amqp091.Connection, error) {
	dialCtx, cancel := context.WithTimeout(ctx, c.cfg.ConnectionTimeout)
	var stopInterrupt func() bool
	defer func() {
		// Unregister before cancel: cancelling dialCtx would otherwise close
		// the connection the handshake just established.
		// 先注销再 cancel：否则 cancel 会关掉刚握手成功的连接。
		if stopInterrupt != nil {
			stopInterrupt()
		}
		cancel()
	}()

	conn, err := amqp091.DialConfig(host.uri(), amqp091.Config{
		Heartbeat: c.cfg.Heartbeat,
		Locale:    c.cfg.Locale,
		Dial: func(network, addr string) (net.Conn, error) {
			var d net.Dialer
			nc, err := d.DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			// The AMQP handshake runs on this connection; bound it and let a
			// cancelled dial interrupt it instead of waiting for the deadline.
			// AMQP 握手跑在这条连接上：给它一个上限，并让取消能打断握手，
			// 而不是等到 deadline。
			if err := nc.SetDeadline(time.Now().Add(c.cfg.ConnectionTimeout)); err != nil {
				_ = nc.Close()
				return nil, err
			}
			stopInterrupt = context.AfterFunc(dialCtx, func() { _ = nc.Close() })
			return nc, nil
		},
	})
	return conn, err
}
