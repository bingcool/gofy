// Package redislock exposes the demo's Redis-backed distributed lock.
//
// 业务代码通过快照取锁（appio.WithContext(ctx).RedisLock()），请求、脚本与后台任务
// 走同一条路径；句柄由 app/appio 在启动时构建并发布。锁固定绑定本代 Redis Set 的
// 客户端（support/lock 的 WithClient），因此每次加锁都落在这一代连接上，不受之后
// Reload 安装的新 Registry 影响。
package redislock

import (
	"fmt"
	"sync/atomic"

	gofylock "github.com/bingcool/gofy/support/lock"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// current 指向当前一代的 Set；发布与读取都不持有互斥锁。
var current atomic.Pointer[Set]

// Default 返回当前一代的 Locker；未启用时为 nil。
func Default() *gofylock.Locker { return current.Load().Locker() }

// Set holds one generation of the locker. It owns no connection: the client
// belongs to the generation's Redis Set, so Close only withdraws the handle.
// Set 持有一个装配代的 Locker。它不持有连接：客户端属于本代的 Redis Set，因此 Close
// 只撤回句柄。
type Set struct {
	locker *gofylock.Locker
}

// Open builds the locker for cfg. client is this generation's Redis client; nil
// is an error here — unlike support/lock, this wrapper has a Registry to consult,
// so a missing client means the configured instance is not enabled, and that must
// fail at startup instead of at the first lock.
// Open 为 cfg 构造 Locker。client 是本代 Redis 客户端；这里 nil 直接报错——与
// support/lock 不同，本包装层有 Registry 可查，缺客户端说明配置的实例没启用，必须在
// 启动期失败，而不是等到第一次加锁。
func Open(cfg gofylock.Config, logger *zap.Logger, client *goredis.Client) (*Set, error) {
	if !cfg.Enabled {
		return &Set{}, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("redislock: %w", err)
	}
	if client == nil {
		return nil, fmt.Errorf("redislock: redis instance %q has no client; enable it in the redis config",
			cfg.RedisInstanceName())
	}

	var opts []gofylock.Option
	if logger != nil {
		// One place logs lock failures, and it carries the instance and prefix:
		// support/lock deliberately has no logger dependency.
		// 锁的错误集中在一处记录，并带上实例与前缀：support/lock 刻意不依赖日志库。
		c := cfg.WithDefaults()
		opts = append(opts, gofylock.WithErrorHandler(func(err error) {
			logger.Warn("redis lock failed",
				zap.String("redis_instance_name", c.RedisInstance),
				zap.String("prefix", c.Prefix),
				zap.Error(err))
		}))
	}
	locker, err := gofylock.Open(cfg, client, opts...)
	if err != nil {
		return nil, err
	}
	return &Set{locker: locker}, nil
}

// Locker returns this generation's locker; nil when disabled.
// Locker 返回本代 Locker；未启用时返回 nil。
func (s *Set) Locker() *gofylock.Locker {
	if s == nil {
		return nil
	}
	return s.locker
}

// Install publishes this generation to the package handle.
// Install 把本代发布为包级句柄，之后的 Default() 读到的就是这一代。
func (s *Set) Install() {
	if s == nil {
		return
	}
	current.Store(s)
}

// Close clears the handle only while it still points at this set, so a reload
// that already installed a new locker keeps it intact.
// Close 只在发布指针仍指向自己时清空句柄，Reload 已安装的新 Locker 不受影响。
func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	current.CompareAndSwap(s, nil)
	return nil
}
