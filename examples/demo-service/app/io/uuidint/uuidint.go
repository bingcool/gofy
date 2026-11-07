// Package uuidint exposes the demo's distributed ID generator.
//
// 业务代码直接使用包级句柄 uuidint.Default.Get(ctx)；句柄由 app/io 在启动时
// 通过 support/uuidint 构建并发布，生成器本身不持有连接：每次取号经
// support/redis 的命名实例别名懒解析，热更新后自动用新连接。
package uuidint

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	gofyuuidint "github.com/bingcool/gofy/support/uuidint"
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

// Default is the generator published by app/io; nil when disabled.
var Default *gofyuuidint.Generator

var mu sync.RWMutex

// Set holds one generation of the generator. It owns no Redis connection: the
// alias is resolved by support/redis on every call.
type Set struct {
	gen *gofyuuidint.Generator
}

// Validate checks the configuration without touching Redis.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	_, err := c.newGenerator(zap.NewNop())
	return err
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

func (c Config) newGenerator(logger *zap.Logger) (*gofyuuidint.Generator, error) {
	c = c.withDefaults()
	if c.RedisInstance == "" {
		return nil, errors.New("uuidint: redis_instance_name is required")
	}
	opts := []gofyuuidint.Option{
		gofyuuidint.WithInstance(c.RedisInstance),
		gofyuuidint.WithTTL(c.TTL),
		gofyuuidint.WithRetry(c.RetryTimes, c.RetryInterval),
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

// Open builds the generator for cfg. A disabled config returns an empty Set.
func Open(cfg Config, logger *zap.Logger) (*Set, error) {
	if !cfg.Enabled {
		return &Set{}, nil
	}
	gen, err := cfg.newGenerator(logger)
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

// Install publishes the generator to the package handle.
func (s *Set) Install() {
	if s == nil {
		return
	}
	mu.Lock()
	Default = s.gen
	mu.Unlock()
}

// Close stops the pool and clears the handle when it still points at this set,
// so a reload that already installed a new generator keeps it intact. No Redis
// connection is owned here.
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	if s.gen != nil {
		s.gen.StopPool()
	}
	mu.Lock()
	if Default == s.gen {
		Default = nil
	}
	mu.Unlock()
	return nil
}
