package health

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RedisPingFunc is the subset of a Redis client a health check needs. Keeping it
// a function means support/health does not import a specific client, so an
// application can use go-redis, another driver or its own pool by passing
// func(ctx context.Context) error { return rdb.Ping(ctx).Err() }.
// RedisPingFunc 是健康检查所需的 Redis 客户端子集。用函数表示意味着 support/health
// 不依赖具体客户端：应用可用 go-redis、其他驱动或自己的连接池，传入
// func(ctx context.Context) error { return rdb.Ping(ctx).Err() } 即可。
type RedisPingFunc func(ctx context.Context) error

// NewRedis builds a checker around ping. A nil function fails at construction,
// because a checker that can never answer would keep readiness red forever.
// NewRedis 基于 ping 构建检查器。nil 函数在构造期失败：一个永远回答不了的检查会让
// readiness 永久飘红。
func NewRedis(name string, ping RedisPingFunc) (Checker, error) {
	if ping == nil {
		return nil, errors.New("health: redis checker requires a ping function")
	}
	return &redisChecker{name: name, ping: ping}, nil
}

type redisChecker struct {
	name string
	ping RedisPingFunc
}

func (c *redisChecker) Name() string { return c.name }

// CheckTimeout implements TimeoutProvider.
// CheckTimeout 实现 TimeoutProvider。
func (c *redisChecker) CheckTimeout() time.Duration { return DefaultPoolTimeout }

func (c *redisChecker) Check(ctx context.Context) error {
	if err := c.ping(ctx); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}
