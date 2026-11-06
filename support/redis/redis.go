// Package redis opens and closes a redis.Client. Like support/mysql it keeps
// no global state: callers pass a context and a Config, and own the returned
// client.
package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// ErrAddrRequired is returned when Config.Addr is empty.
var ErrAddrRequired = errors.New("redis: addr is required")

// Config describes a Redis connection pool.
type Config struct {
	Network      string        `yaml:"network" default:"tcp"` // tcp|unix
	Addr         string        `yaml:"addr"`                  // host:port or socket path
	Username     string        `yaml:"username"`
	Password     string        `yaml:"password"`
	DB           int           `yaml:"db"`
	PoolSize     int           `yaml:"pool_size" default:"10"`
	MinIdleConns int           `yaml:"min_idle_conns" default:"2"`
	DialTimeout  time.Duration `yaml:"dial_timeout" default:"5s"`
	ReadTimeout  time.Duration `yaml:"read_timeout" default:"3s"`
	WriteTimeout time.Duration `yaml:"write_timeout" default:"3s"`
	PingTimeout  time.Duration `yaml:"ping_timeout" default:"5s"`
	// MaxRetries is the number of command retries. Zero uses the go-redis
	// default (3); -1 disables retries.
	MaxRetries int `yaml:"max_retries"`
}

// Validate checks the config without connecting.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return ErrAddrRequired
	}
	switch strings.ToLower(strings.TrimSpace(c.Network)) {
	case "", "tcp", "unix":
	default:
		return fmt.Errorf("redis: invalid network %q", c.Network)
	}
	if c.DB < 0 {
		return errors.New("redis: db must not be negative")
	}
	if c.PoolSize < 0 || c.MinIdleConns < 0 {
		return errors.New("redis: pool sizes must not be negative")
	}
	if c.PoolSize > 0 && c.MinIdleConns > c.PoolSize {
		return errors.New("redis: min_idle_conns must not exceed pool_size")
	}
	if c.DialTimeout < 0 || c.ReadTimeout < 0 || c.WriteTimeout < 0 || c.PingTimeout < 0 {
		return errors.New("redis: durations must not be negative")
	}
	if c.MaxRetries < -1 {
		return errors.New("redis: max_retries must be -1 or greater")
	}
	return nil
}

func (c Config) withDefaults() Config {
	out := c
	out.Network = strings.ToLower(strings.TrimSpace(out.Network))
	if out.Network == "" {
		out.Network = "tcp"
	}
	out.Addr = strings.TrimSpace(out.Addr)
	if out.PoolSize == 0 {
		out.PoolSize = 10
	}
	if out.MinIdleConns == 0 {
		out.MinIdleConns = 2
	}
	if out.DialTimeout == 0 {
		out.DialTimeout = 5 * time.Second
	}
	if out.ReadTimeout == 0 {
		out.ReadTimeout = 3 * time.Second
	}
	if out.WriteTimeout == 0 {
		out.WriteTimeout = 3 * time.Second
	}
	if out.PingTimeout == 0 {
		out.PingTimeout = 5 * time.Second
	}
	return out
}

// New validates cfg, opens a client and verifies it with a ping. The client is
// closed before returning when the ping fails.
func New(ctx context.Context, cfg Config) (*goredis.Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}

	opts := &goredis.Options{
		Network:      cfg.Network,
		Addr:         cfg.Addr,
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
	if cfg.MaxRetries != 0 {
		opts.MaxRetries = cfg.MaxRetries
	}
	client := goredis.NewClient(opts)

	if err := Ping(ctx, client, cfg.PingTimeout); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// Ping verifies connectivity. A zero timeout uses 5s.
func Ping(ctx context.Context, client *goredis.Client, timeout time.Duration) error {
	if client == nil {
		return errors.New("redis: client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		return fmt.Errorf("redis: ping %s: %w", describeAddr(client, timeout), err)
	}
	return nil
}

// Close releases the client. A nil client is a no-op. The client must not be
// used afterwards.
func Close(client *goredis.Client) error {
	if client == nil {
		return nil
	}
	if err := client.Close(); err != nil {
		return fmt.Errorf("redis: close: %w", err)
	}
	return nil
}

// describeAddr keeps the error useful without echoing credentials; only the
// network, address and database index are included.
func describeAddr(client *goredis.Client, timeout time.Duration) string {
	opts := client.Options()
	return fmt.Sprintf("%s:%s db=%d timeout=%s", opts.Network, opts.Addr, opts.DB, timeout)
}
