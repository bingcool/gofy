// Package uuidint generates distributed, trend-increasing int64 IDs backed by
// Redis. It ports the swoolefy PHP Uuid library: a Lua script combines a
// millisecond time prefix from Redis TIME with a segment counter and checks for
// same-millisecond collisions, so PHP and Go services can share the same
// Redis key and produce the same ID layout.
package uuidint

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gofyredis "github.com/bingcool/gofy/support/redis"
	goredis "github.com/redis/go-redis/v9"
)

// MaxIncr 是同一毫秒内允许的最大序号；超过后 Lua 会滚动 incrKey。
const MaxIncr = 999990

var (
	// ErrKeyRequired is returned when the Redis key is empty.
	ErrKeyRequired = errors.New("uuidint: key is required")
	// ErrUnavailable is returned when every retry and fallback failed.
	ErrUnavailable = errors.New("uuidint: no id is available")
)

// Option configures a Generator.
type Option func(*settings) error

type settings struct {
	client       *goredis.Client
	clientSet    bool
	instance     string
	instanceSet  bool
	fallbacks    []source
	ttl          time.Duration
	retryTimes   int
	retryDelay   time.Duration
	onError      func(error)
	poolSize     int
	poolInterval time.Duration
}

// WithClient uses an explicit Redis client as the primary source. It cannot be
// combined with WithInstance.
func WithClient(client *goredis.Client) Option {
	return func(s *settings) error {
		if client == nil {
			return errors.New("uuidint: redis client must not be nil")
		}
		s.client = client
		s.clientSet = true
		return nil
	}
}

// WithInstance uses a named instance from support/redis as the primary source.
// The alias is resolved on every call through the package-level redis.NewIO, so
// it follows the last Registry installed in the process, not a specific
// generation. It is the compatibility path for single-App scripts; components
// that own per-generation connections should pass WithClient with that
// generation's client instead. Without WithClient or WithInstance the alias
// "default" is used. It cannot be combined with WithClient.
func WithInstance(name string) Option {
	return func(s *settings) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("uuidint: instance name must not be empty")
		}
		s.instance = name
		s.instanceSet = true
		return nil
	}
}

// WithFallbackClients appends explicit clients tried, in order, after the
// primary source fails all its retries.
func WithFallbackClients(clients ...*goredis.Client) Option {
	return func(s *settings) error {
		for _, client := range clients {
			if client == nil {
				return errors.New("uuidint: fallback client must not be nil")
			}
			s.fallbacks = append(s.fallbacks, source{client: client})
		}
		return nil
	}
}

// WithFallbackInstances appends named support/redis instances tried, in order,
// after the primary source fails all its retries.
func WithFallbackInstances(names ...string) Option {
	return func(s *settings) error {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				return errors.New("uuidint: fallback instance name must not be empty")
			}
			s.fallbacks = append(s.fallbacks, source{instance: name})
		}
		return nil
	}
}

// WithTTL sets the lifetime of the counter keys. Defaults to 15s; values below
// one second are rejected because Redis EXPIRE needs whole seconds.
func WithTTL(ttl time.Duration) Option {
	return func(s *settings) error {
		if ttl < 0 {
			return errors.New("uuidint: ttl must not be negative")
		}
		if ttl > 0 && ttl < time.Second {
			return errors.New("uuidint: ttl must be at least 1s")
		}
		s.ttl = ttl
		return nil
	}
}

// WithRetry sets how many attempts the primary source gets (default 3) and the
// delay between them (default 150ms). Retries also cover same-millisecond
// collisions reported by the Lua script.
func WithRetry(times int, delay time.Duration) Option {
	return func(s *settings) error {
		if times < 0 {
			return errors.New("uuidint: retry times must not be negative")
		}
		if delay < 0 {
			return errors.New("uuidint: retry delay must not be negative")
		}
		s.retryTimes = times
		s.retryDelay = delay
		return nil
	}
}

// WithErrorHandler installs a callback invoked once when the primary source
// fails all its retries, before the fallbacks are tried.
func WithErrorHandler(fn func(error)) Option {
	return func(s *settings) error {
		s.onError = fn
		return nil
	}
}

// WithPool enables the local prefetch pool: StartPool allocates one batch of
// size IDs immediately and a ticker refills it every interval, while Get and
// GetBatch consume buffered IDs before calling Redis. Pooled IDs are allocated
// up front, so unused ones become gaps after three idle intervals (the buffer
// is cleared) or when StopPool discards it; uniqueness is never affected.
//
// size must be in [1, MaxIncr]; interval must be at least 10ms (1s–2s is a
// sensible range, mirroring the PHP recommendation).
func WithPool(size int, interval time.Duration) Option {
	return func(s *settings) error {
		if size < 1 || size > MaxIncr {
			return fmt.Errorf("uuidint: pool size must be between 1 and %d", MaxIncr)
		}
		if interval < 10*time.Millisecond {
			return errors.New("uuidint: pool interval must be at least 10ms")
		}
		s.poolSize = size
		s.poolInterval = interval
		return nil
	}
}

// source 是一次取号要用的 Redis 连接：显式客户端，或按别名懒解析。
type source struct {
	client   *goredis.Client
	instance string
}

func (s source) resolve(ctx context.Context) (*goredis.Client, error) {
	if s.client != nil {
		return s.client, nil
	}
	return gofyredis.NewIO(ctx, s.instance)
}

// Generator issues IDs from one Redis counter key.
type Generator struct {
	key        string
	ttl        time.Duration
	retryTimes int
	retryDelay time.Duration
	primary    source
	fallbacks  []source
	onError    func(error)
	pool       *idPool
}

// New builds a Generator for key. The primary source is WithClient, or
// WithInstance, or the "default" named support/redis instance when neither is
// given.
func New(key string, opts ...Option) (*Generator, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrKeyRequired
	}

	s := settings{
		ttl:        15 * time.Second,
		retryTimes: 3,
		retryDelay: 150 * time.Millisecond,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&s); err != nil {
			return nil, err
		}
	}
	if s.clientSet && s.instanceSet {
		return nil, errors.New("uuidint: WithClient and WithInstance are mutually exclusive")
	}

	g := &Generator{
		key:        key,
		ttl:        s.ttl,
		retryTimes: s.retryTimes,
		retryDelay: s.retryDelay,
		fallbacks:  s.fallbacks,
		onError:    s.onError,
	}
	switch {
	case s.client != nil:
		g.primary = source{client: s.client}
	case s.instance != "":
		g.primary = source{instance: s.instance}
	default:
		g.primary = source{instance: "default"}
	}
	if s.poolSize > 0 {
		g.pool = newIDPool(s.poolSize, s.poolInterval)
	}
	return g, nil
}

// Get returns one ID. It is GetBatch with n=1.
func (g *Generator) Get(ctx context.Context) (int64, error) {
	ids, err := g.GetBatch(ctx, 1)
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// GetBatch allocates n IDs and returns them in ascending order. With a pool it
// first drains buffered IDs, then asks Redis for the remainder (the closed
// range [max-remain+1, max]). n above MaxIncr is clamped, so the returned
// length can be smaller than requested. A non-positive n returns an empty
// result without calling Redis.
//
// 取号失败且已经从池里取出部分 ID 时，这些 ID 会随错误一起返回，不会静默丢弃。
func (g *Generator) GetBatch(ctx context.Context, n int) ([]int64, error) {
	if n <= 0 {
		return nil, nil
	}
	ids := g.drainPool(n)
	if remain := n - len(ids); remain > 0 {
		step := clampStep(remain)
		maxID, err := g.generate(ctx, step)
		if err != nil {
			return ids, err
		}
		ids = append(ids, expandRange(maxID, step)...)
	}
	return ids, nil
}

// generate 先在主连接上重试 retryTimes 次；仍失败则回调告警，再依次尝试
// 备用连接各一次（与 PHP 版一致）。
func (g *Generator) generate(ctx context.Context, step int) (int64, error) {
	var errs []error
	for attempt := 0; attempt < g.retryTimes; attempt++ {
		if attempt > 0 {
			if err := g.wait(ctx); err != nil {
				return 0, err
			}
		}
		id, err := g.try(ctx, g.primary, step)
		if err == nil {
			return id, nil
		}
		errs = append(errs, err)
	}

	lastErr := errors.Join(errs...)
	if g.onError != nil {
		g.onError(lastErr)
	}
	for _, fallback := range g.fallbacks {
		id, err := g.try(ctx, fallback, step)
		if err == nil {
			return id, nil
		}
		errs = append(errs, err)
	}
	return 0, fmt.Errorf("%w: %w", ErrUnavailable, errors.Join(errs...))
}

func (g *Generator) wait(ctx context.Context) error {
	if g.retryDelay <= 0 {
		return nil
	}
	timer := time.NewTimer(g.retryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// try 解析连接并执行一次 Lua。Lua 返回空数组表示同一毫秒冲突或段号回绕，
// 这里作为可重试错误返回。
func (g *Generator) try(ctx context.Context, src source, step int) (int64, error) {
	client, err := src.resolve(ctx)
	if err != nil {
		return 0, err
	}
	res, err := uuidScript.Run(ctx, client, []string{g.key}, step, int(g.ttl/time.Second)).Result()
	if err != nil {
		return 0, err
	}
	values, ok := res.([]interface{})
	if !ok || len(values) != 2 {
		return 0, errors.New("uuidint: redis returned no id")
	}
	prefix, err := toInt64(values[0])
	if err != nil {
		return 0, fmt.Errorf("uuidint: invalid prefix: %w", err)
	}
	incr, err := toInt64(values[1])
	if err != nil {
		return 0, fmt.Errorf("uuidint: invalid counter: %w", err)
	}
	return prefix + incr, nil
}

func toInt64(v interface{}) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case string:
		return strconv.ParseInt(n, 10, 64)
	case []byte:
		return strconv.ParseInt(string(n), 10, 64)
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}

// clampStep 与 Lua 的钳制保持一致，批量展开必须用同一个步长。
func clampStep(n int) int {
	if n > MaxIncr {
		return MaxIncr
	}
	if n < 1 {
		return 1
	}
	return n
}

// expandRange 返回 [maxID-count+1, maxID]；maxID 是本次 INCRBY 的右端，
// 不能减 count 本身，否则会包含上一号并丢掉本段最大号。
func expandRange(maxID int64, count int) []int64 {
	count = clampStep(count)
	start := maxID - int64(count) + 1
	if start <= 0 {
		return nil
	}
	ids := make([]int64, count)
	for i := range ids {
		ids[i] = start + int64(i)
	}
	return ids
}

// uuidScript 与 PHP LuaScripts::getUuidLuaScript() 保持同一语义：
//  1. INCRBY 段内序号，超过 MaxIncr 先删 key 重试，仍超限则本段作废；
//  2. 用 Redis TIME 的秒 + 毫秒拼 19 位前缀（秒 + 毫秒 + 6 个 0）；
//  3. 新段首次写入时比对上一毫秒戳，同一毫秒冲突返回空由调用方重试。
var uuidScript = goredis.NewScript(`
local incrKey = KEYS[1];
local step = ARGV[1];
local expireTime = ARGV[2];

if step == nil then
    step = 1;
end;
-- 0 或负数会让 EXPIRE 立刻删 key
if expireTime == nil or tonumber(expireTime) == nil or tonumber(expireTime) < 1 then
    expireTime = 20;
end;

step = tonumber(step);
expireTime = tonumber(expireTime);
if step == nil or step < 1 then
    step = 1;
end;
if step > 999990 then
    step = 999990;
end;

local incrId = redis.call('IncrBy', incrKey, step);
local preTimeStampKey = table.concat({incrKey,'time_stamp'},'_');

-- 回绕后仍超过上限说明写入异常，本段作废，避免序号进位到下一毫秒
if incrId > 999990 then
    redis.call('Del', incrKey);
    incrId = redis.call('IncrBy', incrKey, step);
    if incrId > 999990 then
        redis.call('Del', incrKey);
        return {};
    end;
end;

local timeStamp = redis.call('TIME');
local second = timeStamp[1];
-- 微秒必须补零到 6 位，否则 51ms 会被截成 51
local micro = string.format('%06d', tonumber(timeStamp[2]));
local msecond = string.sub(micro, 1, 3);
local nowTimeValue = table.concat({second, msecond});
local preTimeValue;

if incrId == step then
    redis.call('expire', incrKey, expireTime);
    preTimeValue = redis.call('Get', preTimeStampKey);

    if preTimeValue == nowTimeValue then
         return {};
    end;
end;

redis.call('Set', preTimeStampKey, nowTimeValue);
-- SET 会丢掉原 TTL，必须重新续期
redis.call('Expire', preTimeStampKey, expireTime);

return {table.concat({second, msecond, '000000'}), incrId};
`)
