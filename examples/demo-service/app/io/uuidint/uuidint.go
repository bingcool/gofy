// Package uuidint exposes the demo's distributed ID generator.
//
// 业务代码通过快照取生成器（appio.FromContext(ctx).UUID()），无请求上下文时用
// Default()；句柄由 app/io 在启动时构建并发布。生成器优先绑定本代 Redis Set 的
// 客户端（support/uuidint 的 WithClient），每次取号直接使用这一代连接，不受
// 之后 Reload 安装的新 Registry 影响。
package uuidint

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	gofyuuidint "github.com/bingcool/gofy/support/uuidint"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Config configures the demo ID generator.
type Config struct {
	Enabled       bool          `yaml:"enabled"`
	RedisInstance string        `yaml:"redis_instance_name" default:"ratelimit"`
	Key           string        `yaml:"key" default:"demo:uuid"`
	TTL           time.Duration `yaml:"ttl" default:"15s"`
	RetryTimes    int           `yaml:"retry_times" default:"3"`
	RetryInterval time.Duration `yaml:"retry_interval" default:"150ms"`
	// PoolSize 为 0 表示不用本地预取池；大于 0 时启动即预热一批，
	// 并按 PoolInterval 定时补货。
	PoolSize     int           `yaml:"pool_size"`
	PoolInterval time.Duration `yaml:"pool_interval" default:"1s"`
}

// current 指向当前一代的 Set；发布和读取都不再持有互斥锁。
var current atomic.Pointer[Set]

// Default 返回当前一代的生成器；未启用时为 nil。
func Default() *gofyuuidint.Generator { return current.Load().Generator() }

// Set holds one generation of the generator. When the generation Redis Set has
// the configured instance, the generator is bound to that client; otherwise it
// falls back to resolving the alias through support/redis on every call.
type Set struct {
	gen *gofyuuidint.Generator
}

// Validate checks the configuration without touching Redis.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	_, err := c.newGenerator(zap.NewNop(), nil)
	return err
}

// RedisInstanceName returns the Redis alias after applying defaults; app/io uses
// it to pick this generation's client before building the generator.
func (c Config) RedisInstanceName() string {
	return c.withDefaults().RedisInstance
}

func (c Config) withDefaults() Config {
	out := c
	out.RedisInstance = strings.TrimSpace(out.RedisInstance)
	if out.RedisInstance == "" {
		out.RedisInstance = "ratelimit"
	}
	out.Key = strings.TrimSpace(out.Key)
	if out.Key == "" {
		out.Key = "demo:uuid"
	}
	if out.TTL == 0 {
		out.TTL = 15 * time.Second
	}
	if out.RetryTimes == 0 {
		out.RetryTimes = 3
	}
	if out.RetryInterval == 0 {
		out.RetryInterval = 150 * time.Millisecond
	}
	if out.PoolSize > 0 && out.PoolInterval == 0 {
		out.PoolInterval = time.Second
	}
	return out
}

// newGenerator builds the generator. A non-nil client pins it to this
// generation's Redis connection; nil keeps the alias-resolution path, which
// Validate uses because it runs before any connection exists.
func (c Config) newGenerator(logger *zap.Logger, client *goredis.Client) (*gofyuuidint.Generator, error) {
	c = c.withDefaults()
	opts := []gofyuuidint.Option{
		gofyuuidint.WithTTL(c.TTL),
		gofyuuidint.WithRetry(c.RetryTimes, c.RetryInterval),
	}
	if client != nil {
		opts = append(opts, gofyuuidint.WithClient(client))
	} else {
		opts = append(opts, gofyuuidint.WithInstance(c.RedisInstance))
	}
	if c.PoolSize > 0 {
		opts = append(opts, gofyuuidint.WithPool(c.PoolSize, c.PoolInterval))
	}
	if logger != nil {
		opts = append(opts, gofyuuidint.WithErrorHandler(func(err error) {
			logger.Warn("uuidint primary redis failed", zap.String("redis_instance_name", c.RedisInstance), zap.Error(err))
		}))
	}
	return gofyuuidint.New(c.Key, opts...)
}

// Open builds the generator for cfg. client is this generation's Redis client
// for RedisInstanceName; when nil the generator falls back to resolving the
// alias through support/redis. A disabled config returns an empty Set.
func Open(cfg Config, logger *zap.Logger, client *goredis.Client) (*Set, error) {
	if !cfg.Enabled {
		return &Set{}, nil
	}
	gen, err := cfg.newGenerator(logger, client)
	if err != nil {
		return nil, err
	}
	return &Set{gen: gen}, nil
}

// Generator returns this generation's generator; nil when disabled.
func (s *Set) Generator() *gofyuuidint.Generator {
	if s == nil {
		return nil
	}
	return s.gen
}

// Start warms the prefetch pool (when configured) and starts its ticker.
func (s *Set) Start(ctx context.Context) error {
	if s == nil || s.gen == nil {
		return nil
	}
	return s.gen.StartPool(ctx)
}

// Install publishes this generation to the package handle.
func (s *Set) Install() {
	if s == nil {
		return
	}
	current.Store(s)
}

// Close stops the pool and clears the handle only while it still points at this
// set, so a reload that already installed a new generator keeps it intact. No
// Redis connection is owned here.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	current.CompareAndSwap(s, nil)
	if s.gen != nil {
		s.gen.StopPool()
	}
	return nil
}
