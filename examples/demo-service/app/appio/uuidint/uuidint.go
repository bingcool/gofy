// Package uuidint exposes the demo's distributed ID generator.
//
// 业务代码通过快照取生成器（appio.WithContext(ctx).UUID()），脚本、守护进程与
// 请求走同一条路径；句柄由 app/appio 在启动时构建并发布。生成器优先绑定本代 Redis Set 的
// 客户端（support/uuidint 的 WithClient），每次取号直接使用这一代连接，不受
// 之后 Reload 安装的新 Registry 影响。
package uuidint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	gofyuuidint "github.com/bingcool/gofy/support/uuidint"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Config configures the demo ID generator.
// Config 描述示例 ID 生成器；PoolSize 为 0 表示不用本地预取池。
type Config struct {
	Enabled bool `yaml:"enabled"`
	// RedisInstance names the Redis instance holding the counter. It is
	// required: a default would silently write IDs into the wrong key space.
	// RedisInstance 指定承载计数器的 Redis 实例，必填；给默认值会把 ID 悄悄写进
	// 错误的键空间。
	RedisInstance string `yaml:"redis_instance_name"`
	// FallbackRedisInstance is optional: an empty value disables the fallback.
	// It must name an enabled redis instance and must differ from RedisInstance.
	// A separate Redis server is fine: the generator pauses 10ms before its
	// first fallback attempt, so the handover lands in a new millisecond — the
	// only way two counters starting at 1 could otherwise produce a duplicate.
	// FallbackRedisInstance 可选，留空即不启用。它必须指向已启用的 redis 实例，
	// 且不能与 RedisInstance 同名。备用可以是另一台独立 Redis：框架在切换前停顿
	// 10ms 让毫秒前缀错开，两套各自从 1 开始的计数器就不会发出重复 ID。
	FallbackRedisInstance string `yaml:"fallback_redis_instance_name"`
	// Key is the Redis counter key. It is required for the same reason as the
	// instance: two deployments on different keys are not one ID sequence.
	// Key 是 Redis 计数器的键名，同样必填：两个部署用不同键就不是同一条 ID 序列。
	Key           string        `yaml:"key"`
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
// Set 持有一个装配代的生成器。本代 Redis Set 含配置的实例时，生成器直接绑定
// 该客户端；否则每次取号都退回 support/redis 按别名解析。
type Set struct {
	gen *gofyuuidint.Generator
}

// Validate checks the configuration without touching Redis. Both identity
// fields are required; a fallback alias equal to the primary is rejected here,
// before any connection is opened.
// Validate 只校验配置、不访问 Redis：两个标识字段必填；备用别名与主实例同名会在
// 建连之前被拒绝。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	c = c.withDefaults()
	if err := c.validate(); err != nil {
		return err
	}
	_, err := c.newGenerator(zap.NewNop(), nil, nil)
	return err
}

// validate holds the checks shared by Validate and Open. Open calls it again
// because a script can modify Config after config.Load has validated it.
// validate 收集 Validate 与 Open 共用的校验。Open 会再校验一次：脚本可能在
// config.Load 之后改写 Config。
func (c Config) validate() error {
	if c.RedisInstance == "" {
		return errors.New("redis_instance_name is required")
	}
	if c.Key == "" {
		return errors.New("key is required")
	}
	return c.validateFallback()
}

// validateFallback rejects a fallback that would only retry the same broken
// connection. An empty alias means the feature is off.
// validateFallback 拒绝与主实例同名的备用——那只是把失败的连接再试一遍；空串表示未启用。
func (c Config) validateFallback() error {
	if c.FallbackRedisInstance == "" || c.FallbackRedisInstance != c.RedisInstance {
		return nil
	}
	return fmt.Errorf("fallback_redis_instance_name %q must differ from redis_instance_name", c.FallbackRedisInstance)
}

// RedisInstanceName returns the Redis alias after applying defaults; app/appio uses
// it to pick this generation's client before building the generator.
// RedisInstanceName 返回补齐默认值后的 Redis 别名；app/appio 先按它取本代客户端，
// 再构造生成器，取号就固定在这一代连接上。
func (c Config) RedisInstanceName() string {
	return c.withDefaults().RedisInstance
}

// FallbackRedisInstanceName returns the optional backup alias; an empty string
// means no fallback, so callers can skip looking up a client entirely.
// FallbackRedisInstanceName 返回可选的备用别名；空串表示未配置，调用方无需查找客户端。
func (c Config) FallbackRedisInstanceName() string {
	return c.withDefaults().FallbackRedisInstance
}

// withDefaults trims the names and fills in numeric defaults. The two identity
// fields (RedisInstance, Key) are deliberately left empty when absent so
// validate can reject them instead of guessing a counter location.
// withDefaults 只做名称去空白与数值默认值。RedisInstance 与 Key 这两个标识字段
// 缺失时保持为空，交由 validate 拒绝，而不是猜一个计数器位置。
func (c Config) withDefaults() Config {
	out := c
	out.RedisInstance = strings.TrimSpace(out.RedisInstance)
	out.FallbackRedisInstance = strings.TrimSpace(out.FallbackRedisInstance)
	out.Key = strings.TrimSpace(out.Key)
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

// newGenerator builds the generator. A non-nil client pins the primary source
// to this generation's Redis connection; nil keeps the alias-resolution path,
// which Validate uses because it runs before any connection exists. The
// fallback follows the same split: a client when one was built, otherwise the
// alias resolved lazily through support/redis.
// newGenerator 构造生成器。client 非 nil 时主连接固定在传入的本代连接上；为 nil 时
// 走别名解析，Validate 用的就是这条路径，因为它在任何连接建立之前执行。备用同样
// 分两路：有本代客户端就用它，否则退回 support/redis 按别名懒解析。
func (c Config) newGenerator(logger *zap.Logger, client, fallbackClient *goredis.Client) (*gofyuuidint.Generator, error) {
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
	if c.FallbackRedisInstance != "" {
		if fallbackClient != nil {
			opts = append(opts, gofyuuidint.WithFallbackClients(fallbackClient))
		} else {
			opts = append(opts, gofyuuidint.WithFallbackInstances(c.FallbackRedisInstance))
		}
	}
	if c.PoolSize > 0 {
		opts = append(opts, gofyuuidint.WithPool(c.PoolSize, c.PoolInterval))
	}
	if logger != nil {
		opts = append(opts, gofyuuidint.WithErrorHandler(func(err error) {
			logger.Warn("uuidint primary redis failed",
				zap.String("redis_instance_name", c.RedisInstance),
				zap.String("fallback_redis_instance_name", c.FallbackRedisInstance),
				zap.Error(err))
		}))
	}
	return gofyuuidint.New(c.Key, opts...)
}

// Open builds the generator for cfg. client and fallbackClient are this
// generation's Redis clients; a nil primary falls back to alias resolution.
// A configured fallback without a client is an error: silently degrading to
// the package-level alias would hide a misconfigured redis instance until the
// first failure, when it is too late to fix.
// Open 为 cfg 构造生成器。client 与 fallbackClient 是本代 Redis 客户端；主客户端为
// nil 时走别名解析。配置了备用却没有客户端会直接报错：悄悄退化到包级别名会把
// "实例没启用"藏到第一次故障才暴露，那时已经来不及修。
func Open(cfg Config, logger *zap.Logger, client, fallbackClient *goredis.Client) (*Set, error) {
	if !cfg.Enabled {
		return &Set{}, nil
	}
	c := cfg.withDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	if c.FallbackRedisInstance != "" && fallbackClient == nil {
		return nil, fmt.Errorf("uuidint: fallback redis instance %q has no client; enable it in the redis config", c.FallbackRedisInstance)
	}
	gen, err := c.newGenerator(logger, client, fallbackClient)
	if err != nil {
		return nil, err
	}
	return &Set{gen: gen}, nil
}

// Generator returns this generation's generator; nil when disabled.
// Generator 返回本代生成器；未启用时返回 nil。
func (s *Set) Generator() *gofyuuidint.Generator {
	if s == nil {
		return nil
	}
	return s.gen
}

// Start warms the prefetch pool (when configured) and starts its ticker.
// Start 预热预取池（配置了才预热）并启动补货定时器；未启用时直接返回。
func (s *Set) Start(ctx context.Context) error {
	if s == nil || s.gen == nil {
		return nil
	}
	return s.gen.StartPool(ctx)
}

// Install publishes this generation to the package handle.
// Install 把本代发布为包级句柄，之后的 Default() 读到的就是这一代。
func (s *Set) Install() {
	if s == nil {
		return
	}
	current.Store(s)
}

// Close stops the pool and clears the handle only while it still points at this
// set, so a reload that already installed a new generator keeps it intact. No
// Redis connection is owned here.
// Close 停止预取池，并只在发布指针仍指向自己时清空句柄，Reload 已安装的新生成器
// 不受影响。本包不持有 Redis 连接，连接由 app/appio 的 Redis Set 释放。
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
