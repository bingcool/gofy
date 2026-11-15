package lock

import (
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Config describes one Redis-backed lock manager. It is the shape a YAML file
// or an environment-driven settings struct decodes into; New stays the
// programmatic entry point.
//
// Enabled has no default on purpose: a missing section must not silently start
// writing lock keys, and a typo in the section name must not look like a working
// feature.
// Config 描述一个基于 Redis 的锁管理器，是 YAML 或环境变量配置解码成的形状；程序化
// 入口仍然是 New。
//
// Enabled 刻意不给默认值：配置里没有这一段时不应悄悄开始写锁键，段名拼错也不该看起来
// 像功能正常。
type Config struct {
	Enabled bool `yaml:"enabled"`
	// RedisInstance names the support/redis instance holding the keys. It is
	// optional: empty means the "default" instance, which is the whole point of
	// "configure Redis once".
	// RedisInstance 指定承载锁键的 support/redis 实例，可选；留空表示 "default"
	// 实例，这正是"Redis 只配一次"的含义。
	RedisInstance string `yaml:"redis_instance_name"`
	// Prefix namespaces the keys so several applications can share one database.
	// Prefix 为键加命名空间，多个应用因此可以共用一个库。
	Prefix string `yaml:"prefix"`
	// Expiry is the TTL of every lock. It must outlive the critical section.
	// Expiry 是每把锁的 TTL，必须长于临界区。
	Expiry time.Duration `yaml:"expiry" default:"10s"`
	// Tries is how many times a blocking Lock retries before ErrAlreadyHeld.
	// Tries 是阻塞式 Lock 报 ErrAlreadyHeld 之前的重试次数。
	Tries int `yaml:"tries" default:"3"`
	// RetryDelay is the pause between those retries.
	// RetryDelay 是这些重试之间的间隔。
	RetryDelay time.Duration `yaml:"retry_delay" default:"100ms"`
}

// WithDefaults returns a copy with trim work and defaults applied. Expiry,
// Tries and RetryDelay fall back to the package defaults; the instance name falls
// back to DefaultInstance. Nothing is invented for Enabled or Prefix.
// WithDefaults 返回补齐默认值、去掉名称空白的副本。Expiry、Tries、RetryDelay 回落到
// 包级默认值，实例名回落到 DefaultInstance；Enabled 与 Prefix 不做任何猜测。
func (c Config) WithDefaults() Config {
	out := c
	out.RedisInstance = strings.TrimSpace(out.RedisInstance)
	out.Prefix = strings.TrimSpace(out.Prefix)
	if out.RedisInstance == "" {
		out.RedisInstance = DefaultInstance
	}
	if out.Expiry == 0 {
		out.Expiry = DefaultExpiry
	}
	if out.Tries == 0 {
		out.Tries = DefaultTries
	}
	if out.RetryDelay == 0 {
		out.RetryDelay = DefaultRetryDelay
	}
	return out
}

// RedisInstanceName returns the alias after defaults, so a caller can resolve
// this generation's client before building the locker.
// RedisInstanceName 返回补齐默认值后的别名，调用方因此可以先取本代客户端再构造
// Locker。
func (c Config) RedisInstanceName() string {
	return c.WithDefaults().RedisInstance
}

// Validate checks the configuration without touching Redis.
// Validate 只校验配置，不访问 Redis。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	return c.WithDefaults().validate()
}

func (c Config) validate() error {
	if c.Expiry <= 0 {
		return fmt.Errorf("expiry must be positive, got %s", c.Expiry)
	}
	if c.Tries < 1 {
		return fmt.Errorf("tries must be at least 1, got %d", c.Tries)
	}
	if c.RetryDelay < 0 {
		return fmt.Errorf("retry_delay must not be negative, got %s", c.RetryDelay)
	}
	return nil
}

// Open builds the locker described by cfg. A non-nil client pins it to this
// caller's generation; nil makes it resolve the instance alias through
// support/redis on every call, which is what a script without a generation
// wants. A disabled config returns (nil, nil): there is no handle to use and
// nothing to close, and callers already have to check for nil.
//
// opts are applied after the config, so a caller can attach an error handler or
// override a default without copying the struct.
// Open 按 cfg 构造 Locker。client 非 nil 时把它固定到调用方这一代；为 nil 时每次调用
// 都通过 support/redis 解析实例别名，这正是没有装配代的脚本想要的。禁用时返回
// (nil, nil)：既没有句柄可用，也没有东西要关闭，调用方本来就要检查 nil。
//
// opts 在配置之后应用，调用方因此可以挂错误处理或覆盖默认值，而不必复制整个结构体。
func Open(cfg Config, client *goredis.Client, opts ...Option) (*Locker, error) {
	c := cfg.WithDefaults()
	if !c.Enabled {
		return nil, nil
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	base := make([]Option, 0, 4+len(opts))
	base = append(base,
		WithPrefix(c.Prefix),
		WithExpiry(c.Expiry),
		WithRetry(c.Tries, c.RetryDelay),
	)
	if client != nil {
		base = append(base, WithClient(client))
	} else {
		base = append(base, WithInstance(c.RedisInstance))
	}
	return New(append(base, opts...)...)
}
