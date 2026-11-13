// Package cache provides a uniform key-value cache facade on top of an injected
// Redis client.
//
// Like support/redis, the package keeps no global state and does not own the
// client: business code passes an already connected *redis.Client (or any
// goredis.Cmdable), and either the business code or app/io closes it. See Redis
// for the default implementation.
// 包 cache 在注入的 Redis 客户端之上提供统一的键值缓存门面。
//
// 与 support/redis 一样，本包不持有全局状态，也不接管客户端生命周期：
// 业务传入已连接的 *redis.Client（或任意 goredis.Cmdable），由业务或 app/io
// 负责关闭。默认实现见 Redis。
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrClientRequired reports that no Redis client was injected when the cache
// was built.
// ErrClientRequired 表示构造缓存时没有注入 Redis 客户端。
var ErrClientRequired = errors.New("cache: redis client is required")

// Cache is the cache contract used by business code. A ttl <= 0 means the value
// never expires.
// Cache 是业务层使用的缓存契约。ttl <= 0 表示写入后不过期。
type Cache interface {
	// Set stores a string value.
	// Set 写入字符串值。
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	// SetJSON marshals value to JSON and stores it as a string.
	// SetJSON 先把 value 序列化为 JSON，再按字符串写入。
	SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error
	// Get reads a string value; ok is false when the key does not exist.
	// Get 读取字符串值；ok 为 false 表示 key 不存在。
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	// GetJSON reads a string and unmarshals it into dest; ok is false when the
	// key does not exist.
	// GetJSON 读取字符串并反序列化到 dest；ok 为 false 表示 key 不存在。
	GetJSON(ctx context.Context, key string, dest any) (ok bool, err error)
	// Delete removes one or more keys and returns how many were actually
	// deleted.
	// Delete 删除一个或多个 key，返回实际删除的数量。
	Delete(ctx context.Context, keys ...string) (int64, error)
	// Has reports whether the key exists.
	// Has 判断 key 是否存在。
	Has(ctx context.Context, key string) (bool, error)
	// TTL returns the remaining lifetime. exists is false when the key does not
	// exist; ttl is 0 when no expiry is set.
	// TTL 返回剩余有效期。exists 为 false 表示 key 不存在；
	// 未设置过期时间时 ttl 为 0。
	TTL(ctx context.Context, key string) (ttl time.Duration, exists bool, err error)
	// GetMany reads several keys; missing keys are absent from the result.
	// GetMany 批量读取；不存在的 key 不会出现在结果中。
	GetMany(ctx context.Context, keys ...string) (map[string]string, error)
	// SetMany writes several string values; ttl applies to every key.
	// SetMany 批量写入字符串值，ttl 对所有 key 生效。
	SetMany(ctx context.Context, values map[string]string, ttl time.Duration) error
}
