package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Redis is a Redis-backed Cache implementation.
// Redis 是基于 Redis 的 Cache 实现。
type Redis struct {
	client goredis.Cmdable
}

var _ Cache = (*Redis)(nil)

// NewRedis wraps an already connected Redis client. The package only uses it
// and never closes it; a nil client returns ErrClientRequired.
// NewRedis 包装一个已连接的 Redis 客户端。本包只使用它，不关闭它；
// 客户端为 nil 时返回 ErrClientRequired。
func NewRedis(client goredis.Cmdable) (*Redis, error) {
	if client == nil {
		return nil, ErrClientRequired
	}
	return &Redis{client: client}, nil
}

// Set stores a string value; ttl <= 0 means no expiry.
// Set 写入字符串值；ttl <= 0 表示不过期。
func (r *Redis) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := r.client.Set(contextOrBackground(ctx), key, value, expiration(ttl)).Err(); err != nil {
		return fmt.Errorf("cache: set %q: %w", key, err)
	}
	return nil
}

// SetJSON marshals the value before storing it; use GetJSON to restore it.
// SetJSON 先序列化再写入，读取时用 GetJSON 还原。
func (r *Redis) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache: marshal %q: %w", key, err)
	}
	return r.Set(ctx, key, string(payload), ttl)
}

// Get reads a string value; a missing key returns ok=false and err=nil.
// Get 读取字符串值；key 不存在时返回 ok=false 且 err=nil。
func (r *Redis) Get(ctx context.Context, key string) (string, bool, error) {
	value, err := r.client.Get(contextOrBackground(ctx), key).Result()
	if errors.Is(err, goredis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("cache: get %q: %w", key, err)
	}
	return value, true, nil
}

// GetJSON reads and unmarshals into dest; a missing key leaves dest untouched.
// GetJSON 读取并反序列化到 dest；key 不存在时不修改 dest。
func (r *Redis) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	value, ok, err := r.Get(ctx, key)
	if err != nil || !ok {
		return ok, err
	}
	if err := json.Unmarshal([]byte(value), dest); err != nil {
		return false, fmt.Errorf("cache: decode %q: %w", key, err)
	}
	return true, nil
}

// Delete removes one or more keys and returns how many were actually deleted.
// Delete 删除一个或多个 key，返回实际删除的数量。
func (r *Redis) Delete(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	removed, err := r.client.Del(contextOrBackground(ctx), keys...).Result()
	if err != nil {
		return 0, fmt.Errorf("cache: delete: %w", err)
	}
	return removed, nil
}

// Has reports whether the key exists.
// Has 判断 key 是否存在。
func (r *Redis) Has(ctx context.Context, key string) (bool, error) {
	count, err := r.client.Exists(contextOrBackground(ctx), key).Result()
	if err != nil {
		return false, fmt.Errorf("cache: exists %q: %w", key, err)
	}
	return count > 0, nil
}

// TTL returns the remaining lifetime: exists=false when the key is missing and
// ttl=0 when no expiry is set.
// TTL 返回剩余有效期：key 不存在时 exists=false；没有过期时间时 ttl=0。
func (r *Redis) TTL(ctx context.Context, key string) (time.Duration, bool, error) {
	ttl, err := r.client.TTL(contextOrBackground(ctx), key).Result()
	if err != nil {
		return 0, false, fmt.Errorf("cache: ttl %q: %w", key, err)
	}
	if ttl < 0 {
		// go-redis puts Redis's -2 (missing key) and -1 (no expiry) into
		// Duration unchanged, without converting seconds.
		// go-redis 把 Redis 的 -2（key 不存在）和 -1（未设置过期时间）
		// 原样放进 Duration，不做秒换算。
		if ttl == -2 {
			return 0, false, nil
		}
		return 0, true, nil
	}
	return ttl, true, nil
}

// GetMany reads several keys at once; missing keys are absent from the result.
// GetMany 批量读取；不存在的 key 不会出现在结果中。
func (r *Redis) GetMany(ctx context.Context, keys ...string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	values, err := r.client.MGet(contextOrBackground(ctx), keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("cache: mget: %w", err)
	}
	got := make(map[string]string, len(values))
	for i, item := range values {
		if i >= len(keys) {
			break
		}
		switch value := item.(type) {
		case string:
			got[keys[i]] = value
		case []byte:
			got[keys[i]] = string(value)
		}
	}
	return got, nil
}

// SetMany writes several string values. With ttl <= 0 it writes them in one
// MSET; with ttl > 0 it uses a transaction pipeline so every key gets the same
// expiry.
// SetMany 批量写入字符串值。ttl <= 0 时用 MSET 一次写入；
// ttl > 0 时用事务管道给每个 key 设置相同的过期时间。
func (r *Redis) SetMany(ctx context.Context, values map[string]string, ttl time.Duration) error {
	if len(values) == 0 {
		return nil
	}
	ctx = contextOrBackground(ctx)
	if ttl <= 0 {
		args := make([]interface{}, 0, len(values)*2)
		for key, value := range values {
			args = append(args, key, value)
		}
		if err := r.client.MSet(ctx, args...).Err(); err != nil {
			return fmt.Errorf("cache: mset: %w", err)
		}
		return nil
	}
	_, err := r.client.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		for key, value := range values {
			pipe.Set(ctx, key, value, ttl)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cache: mset with ttl: %w", err)
	}
	return nil
}

// expiration converts the "non-positive means no expiry" convention into the
// argument go-redis expects.
// expiration 把“非正数表示不过期”的约定转换成 go-redis 的参数。
func expiration(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 0
	}
	return ttl
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
