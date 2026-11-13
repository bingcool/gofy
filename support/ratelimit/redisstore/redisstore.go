// Package redisstore implements ratelimit.Store on top of Redis so several
// instances share the same counters.
// 包 redisstore 在 Redis 之上实现 ratelimit.Store，使多个实例共享同一份计数。
package redisstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bingcool/gofy/support/ratelimit"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// DefaultPrefix namespaces limiter keys.
// DefaultPrefix 是限流 key 的命名空间前缀。
const DefaultPrefix = "gofy:ratelimit"

// allowScript is a sorted-set sliding window. It uses the Redis server clock so
// instances with skewed clocks still agree, and a UUID member so two requests
// in the same millisecond do not overwrite each other.
//
// KEYS[1]: bucket key
// ARGV[1]: window in milliseconds
// ARGV[2]: limit
// ARGV[3]: unique member
// Returns {allowed, limit, remaining, retry_after_ms, reset_at_ms}.
// allowScript 是用有序集合实现的滑动窗口：时间取自 Redis 服务器时钟，因此时钟
// 有偏差的实例也能得到一致结果；成员使用 UUID，同一毫秒内的两次请求不会互相
// 覆盖。
const allowScript = `
local key = KEYS[1]
local window = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])

local parts = redis.call('TIME')
local now = tonumber(parts[1]) * 1000 + math.floor(tonumber(parts[2]) / 1000)
local cutoff = now - window

redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)
local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
local base = now
if #oldest > 0 then
  base = tonumber(oldest[2])
end
local reset = base + window

if count >= limit then
  local retry = reset - now
  if retry < 0 then retry = 0 end
  return {0, limit, 0, retry, reset}
end

redis.call('ZADD', key, now, ARGV[3])
redis.call('PEXPIRE', key, window)
return {1, limit, limit - count - 1, 0, reset}
`

var script = goredis.NewScript(allowScript)

// Store implements ratelimit.Store with Redis sorted sets.
// Store 用 Redis 有序集合实现 ratelimit.Store。
type Store struct {
	client goredis.Scripter
	prefix string
}

// New builds a store. prefix is prepended to every key and defaults to
// DefaultPrefix; a trailing ":" is trimmed so callers can pass either form.
// New 构建 Store。prefix 会拼在每个 key 之前，默认 DefaultPrefix；末尾的 ":" 会
// 被去掉，调用方传哪种形式都可以。
func New(client goredis.Scripter, prefix string) (*Store, error) {
	if client == nil {
		return nil, errors.New("redisstore: client is required")
	}
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), ":")
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return &Store{client: client, prefix: prefix}, nil
}

// Allow implements ratelimit.Store. Redis errors are returned unchanged so the
// middleware can fail open or closed according to its configuration.
// Allow 实现 ratelimit.Store。Redis 错误原样返回，中间件按自身配置决定 fail open
// 还是 fail closed。
func (s *Store) Allow(ctx context.Context, key string, limit int, window time.Duration) (ratelimit.Result, error) {
	if limit <= 0 {
		return ratelimit.Result{}, ratelimit.ErrInvalidLimit
	}
	if window <= 0 {
		return ratelimit.Result{}, ratelimit.ErrInvalidWindow
	}
	windowMs := window.Milliseconds()
	if windowMs < 1 {
		windowMs = 1
	}

	reply, err := script.Run(ctx, s.client, []string{s.prefix + ":" + key},
		windowMs, limit, uuid.NewString()).Result()
	if err != nil {
		return ratelimit.Result{}, fmt.Errorf("redisstore: allow: %w", err)
	}
	values, ok := reply.([]any)
	if !ok || len(values) != 5 {
		return ratelimit.Result{}, fmt.Errorf("redisstore: unexpected reply %T", reply)
	}
	allowed, err := toInt64(values[0])
	if err != nil {
		return ratelimit.Result{}, err
	}
	lim, err := toInt64(values[1])
	if err != nil {
		return ratelimit.Result{}, err
	}
	remaining, err := toInt64(values[2])
	if err != nil {
		return ratelimit.Result{}, err
	}
	retryMs, err := toInt64(values[3])
	if err != nil {
		return ratelimit.Result{}, err
	}
	resetMs, err := toInt64(values[4])
	if err != nil {
		return ratelimit.Result{}, err
	}

	return ratelimit.Result{
		Allowed:    allowed == 1,
		Limit:      int(lim),
		Remaining:  int(remaining),
		RetryAfter: time.Duration(retryMs) * time.Millisecond,
		ResetAt:    time.UnixMilli(resetMs),
	}, nil
}

func toInt64(v any) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("redisstore: parse reply %q: %w", t, err)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("redisstore: unexpected reply element %T", v)
	}
}
