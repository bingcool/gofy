// Package redislease implements lease.Locker on top of Redis.
//
// Two keys back each lease: `lease:{key}` holds the current token with a PX
// expiry, and `fence:{key}` is a monotonic counter that never expires. The
// braces are a Redis Cluster hash tag, so both keys live in one slot and a
// single script can touch them.
// 包 redislease 用 Redis 实现 lease.Locker。
//
// 每个租约由两个键支撑：`lease:{key}` 保存当前 token 并带 PX 过期，`fence:{key}`
// 是永不过期的单调计数器。花括号是 Redis Cluster 的 hash tag，两个键因此落在同一个
// slot，单条脚本可以同时操作。
package redislease

import (
	"context"
	"fmt"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bingcool/gofy/support/lease"
)

// Config names the key space.
// Config 定义键空间。
type Config struct {
	// Prefix is prepended to every key, which lets one Redis instance host
	// several environments. It sits outside the hash tag and must not contain
	// braces. Empty means no prefix.
	// Prefix 会加到每个键前面，使一个 Redis 实例可以承载多套环境。它位于 hash tag
	// 之外，且不能含花括号。为空表示不加前缀。
	Prefix string
}

// Locker acquires leases from a Redis server.
// Locker 从 Redis 获取租约。
type Locker struct {
	client goredis.UniversalClient
	prefix string
}

// acquireScript takes the lease when nobody holds it and only then burns a
// fence token. Returning -1 keeps the counter clean; a burned token would still
// be monotonic, but the numbers would grow with every failed attempt.
// acquireScript 在无人持有时取锁，并且只有成功才消耗一个 fence 号。返回 -1 让
// 计数器保持干净；失败也烧号虽然仍单调，但数字会随每次抢锁失败一直涨。
var acquireScript = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  return -1
end
local token = redis.call('INCR', KEYS[2])
redis.call('SET', KEYS[1], token, 'PX', ARGV[1])
return token
`)

// renewScript compares the token before extending, so a paused holder cannot
// extend the lease of the instance that replaced it.
// renewScript 先比对 token 再续期，暂停后苏醒的旧持有者无法为接替它的实例续约。
var renewScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

// releaseScript compares the token before deleting for the same reason.
// releaseScript 同样先比对 token 再删除。
var releaseScript = goredis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// New builds a locker around an existing client. The client stays owned by the
// caller: this package never closes it.
// New 基于已有客户端构建 Locker。客户端仍由调用方持有，本包不会关闭它。
func New(client goredis.UniversalClient, cfg Config) (*Locker, error) {
	if client == nil {
		return nil, fmt.Errorf("redislease: client is required")
	}
	prefix := strings.TrimSpace(cfg.Prefix)
	if strings.ContainsAny(prefix, "{}") {
		return nil, fmt.Errorf("redislease: prefix %q must not contain braces", cfg.Prefix)
	}
	return &Locker{client: client, prefix: prefix}, nil
}

// Acquire takes the lease or returns lease.ErrHeld.
// Acquire 获取租约，被占用时返回 lease.ErrHeld。
func (l *Locker) Acquire(ctx context.Context, key string, ttl time.Duration) (lease.Lease, error) {
	if err := lease.ValidateKey(key); err != nil {
		return nil, err
	}
	if err := lease.ValidateTTL(ttl); err != nil {
		return nil, err
	}
	res, err := acquireScript.Run(ctx, l.client, []string{l.leaseKey(key), l.fenceKey(key)}, ttl.Milliseconds()).Int64()
	if err != nil {
		return nil, fmt.Errorf("redislease: acquire %q: %w", key, err)
	}
	if res < 0 {
		return nil, lease.ErrHeld
	}
	return &heldLease{locker: l, key: key, token: res, ttl: ttl}, nil
}

func (l *Locker) leaseKey(key string) string { return l.prefix + "lease:{" + key + "}" }
func (l *Locker) fenceKey(key string) string { return l.prefix + "fence:{" + key + "}" }

// heldLease is one successful acquisition.
// heldLease 表示一次成功的获取。
type heldLease struct {
	locker *Locker
	key    string
	token  int64
	ttl    time.Duration
}

func (h *heldLease) Key() string    { return h.key }
func (h *heldLease) Token() int64   { return h.token }
func (h *heldLease) String() string { return fmt.Sprintf("%s#%d", h.key, h.token) }

// Renew extends the lease, reporting ErrLeaseLost when the token no longer
// matches the stored one.
// Renew 延长租约；token 与存储值不再匹配时报告 ErrLeaseLost。
func (h *heldLease) Renew(ctx context.Context) error {
	res, err := renewScript.Run(ctx, h.locker.client,
		[]string{h.locker.leaseKey(h.key)}, h.token, h.ttl.Milliseconds()).Int64()
	if err != nil {
		return fmt.Errorf("redislease: renew %q: %w", h.key, err)
	}
	if res == 0 {
		return fmt.Errorf("%w: %s", lease.ErrLeaseLost, h)
	}
	return nil
}

// Release deletes the lease when we still own it. A lease that already moved on
// is not an error, and the fence counter is left in place so the next token is
// larger than ours.
// Release 在仍持有时删除租约。已经易主的租约不算错误；fence 计数器保留，下一个
// token 仍然比我们的大。
func (h *heldLease) Release(ctx context.Context) error {
	if _, err := releaseScript.Run(ctx, h.locker.client,
		[]string{h.locker.leaseKey(h.key)}, h.token).Int64(); err != nil {
		return fmt.Errorf("redislease: release %q: %w", h.key, err)
	}
	return nil
}
