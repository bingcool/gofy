// Package uuidint generates distributed, trend-increasing int64 IDs backed by
// Redis. It ports the swoolefy PHP Uuid library: a Lua script combines a
// millisecond time prefix from Redis TIME with a segment counter and checks for
// same-millisecond collisions, so PHP and Go services can share the same
// Redis key and produce the same ID layout.
// 包 uuidint 基于 Redis 生成分布式、趋势递增的 int64 ID，移植自 swoolefy PHP 的
// Uuid 库：Lua 脚本把 Redis TIME 得到的毫秒前缀与段内序号拼接，并处理同毫秒冲突，
// 因此 PHP 与 Go 服务可以共用同一个 Redis key 并生成相同布局的 ID。
//
// ID 只在单进程内趋势递增，跨实例的严格单调需要调用方另外保证；取号结果依赖
// Redis 可用性，Redis 全部失败时返回包装了 ErrUnavailable 的错误。
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

// MaxIncr is the largest counter allowed within one millisecond; beyond it the
// Lua script rolls incrKey.
// MaxIncr 是同一毫秒内允许的最大序号；超过后 Lua 会滚动 incrKey。
const MaxIncr = 999990

// defaultFallbackDelay keeps a handover off the primary's last millisecond.
// IDs are a millisecond prefix plus at most MaxIncr, and the counter never
// carries into the prefix, so two IDs can only collide inside one millisecond.
// A fallback Redis owns another counter entirely; waiting past one boundary is
// what separates it from the primary's last ID.
// defaultFallbackDelay 让切换避开主实例最后一个毫秒。ID 由毫秒前缀加最多 MaxIncr
// 的序号组成，序号不会进位到前缀，因此只有同一毫秒内才可能重复。备用 Redis 有自己
// 独立的一套计数器，跨过一个毫秒边界就能把它与主的最后一个 ID 分开。
const defaultFallbackDelay = 10 * time.Millisecond

var (
	// ErrKeyRequired is returned when the Redis key is empty.
	// Redis key 为空时由 New 返回，可用 errors.Is 比较。
	ErrKeyRequired = errors.New("uuidint: key is required")
	// ErrUnavailable is returned when every retry and fallback failed.
	// 主连接重试与全部备用连接都失败时返回，可用 errors.Is 判定；原始错误通过
	// errors.Join 保留在链上，告警回调也会先收到一次同样的错误。
	ErrUnavailable = errors.New("uuidint: no id is available")
)

// Option configures a Generator.
// Option 在 New 中按顺序应用，任一返回错误即中止构造；nil Option 会被跳过。
type Option func(*settings) error

type settings struct {
	client        *goredis.Client
	clientSet     bool
	instance      string
	instanceSet   bool
	fallbacks     []source
	ttl           time.Duration
	retryTimes    int
	retryDelay    time.Duration
	fallbackDelay time.Duration
	onError       func(error)
	poolSize      int
	poolInterval  time.Duration
}

// WithClient uses an explicit Redis client as the primary source. It cannot be
// combined with WithInstance.
// 用显式传入的 client 作为主连接；client 为 nil 时构造报错，与 WithInstance
// 互斥。调用方仍持有该 client，Generator 不负责关闭它。
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
// 用 support/redis 中的命名实例作为主连接。别名在每次取号时经包级 redis.NewIO
// 解析，跟随进程内最后一次 Install 的 Registry，而不是固定某一代；它是单 App
// 脚本的兼容路径，按代持有连接的组件应改用 WithClient 传入该代的 client。
// 两者都不指定时使用别名 "default"，与 WithClient 互斥。
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
// 追加显式 client 作为备用连接，按传入顺序在主连接重试全部失败后各尝试一次；
// 任一为 nil 时构造报错。
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
// 追加命名实例作为备用连接，按传入顺序在主连接重试全部失败后各尝试一次；
// 名称去空白后为空时构造报错。
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
// 设置计数器 key 的 TTL，默认 15s；小于 1s 的非零值直接报错，因为 Redis EXPIRE
// 只接受整秒，传 0 表示沿用默认值。
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

const defaultRetryTimes = 3

// WithRetry sets how many attempts the primary source gets (default 3) and the
// delay between them (default 150ms). Retries also cover same-millisecond
// collisions reported by the Lua script. Zero attempts means the default: a
// caller that only wants to tune the delay must not accidentally disable the
// primary source.
// 设置主连接的总尝试次数（默认 3）与间隔（默认 150ms）；Lua 报同毫秒冲突时也走
// 这套重试。次数为 0 表示使用默认值：只想调整间隔的调用方不应因此关掉主连接。
// 等待可被 ctx 取消，取消后立即返回 ctx.Err() 而不进入备用连接。
func WithRetry(times int, delay time.Duration) Option {
	return func(s *settings) error {
		if times < 0 {
			return errors.New("uuidint: retry times must not be negative")
		}
		if delay < 0 {
			return errors.New("uuidint: retry delay must not be negative")
		}
		if times == 0 {
			times = defaultRetryTimes
		}
		s.retryTimes = times
		s.retryDelay = delay
		return nil
	}
}

// WithFallbackDelay sets the pause after the primary fails all its retries and
// before the first fallback is tried. It defaults to defaultFallbackDelay; zero
// disables it. The pause is skipped when no fallback is configured, and a
// cancelled ctx cuts it short instead of waiting out the timer.
//
// Two independent Redis instances keep separate counters, so the millisecond
// prefix is the only thing that distinguishes their IDs. Waiting past one
// boundary makes a handover land in a different millisecond from the primary's
// last ID. It does not correct a clock offset: if the fallback's TIME lags the
// primary's by more than this delay, the two can still share a millisecond.
// WithFallbackDelay 设置主连接重试全部失败后、尝试第一个备用之前的停顿，默认
// defaultFallbackDelay，设为 0 关闭。没有配置备用时不等待；ctx 取消会立即中断等待。
//
// 两个独立 Redis 各自维护计数器，ID 之间只靠毫秒前缀区分。跨过一个毫秒边界，切换
// 就不会落在主实例最后一个 ID 的同一毫秒里。它不能修正时钟偏差：若备用 Redis 的
// TIME 比主慢超过这个时长，两边仍可能落在同一毫秒。
func WithFallbackDelay(delay time.Duration) Option {
	return func(s *settings) error {
		if delay < 0 {
			return errors.New("uuidint: fallback delay must not be negative")
		}
		s.fallbackDelay = delay
		return nil
	}
}

// WithErrorHandler installs a callback invoked once when the primary source
// fails all its retries, before the fallbacks are tried.
// 主连接重试全部失败、尚未尝试备用连接时回调一次，用于告警；回调在取号路径上
// 同步执行，应保持快速且不 panic。
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
// 启用本地预取池：StartPool 立即预取一批 size 个 ID，之后按 interval 定时补充，
// Get 与 GetBatch 先消费池内缓冲再访问 Redis。池内 ID 是提前分配的，所以空闲三个
// 周期后（缓冲被清空）或 StopPool 丢弃时会产生空洞，但绝不会重复。
//
// size 必须在 [1, MaxIncr] 内；interval 至少 10ms（1s–2s 是合理区间，与 PHP
// 版建议一致）。
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

// source is the Redis connection used for one allocation: an explicit client or
// an alias resolved lazily.
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
// Generator 从同一个 Redis 计数器 key 取号；Get、GetBatch 可并发调用，
// 启动预取池后由内部定时器补充，StopPool 会停止该定时器。
type Generator struct {
	key           string
	ttl           time.Duration
	retryTimes    int
	retryDelay    time.Duration
	fallbackDelay time.Duration
	primary       source
	fallbacks     []source
	onError       func(error)
	pool          *idPool
}

// New builds a Generator for key. The primary source is WithClient, or
// WithInstance, or the "default" named support/redis instance when neither is
// given.
// 为 key 构造 Generator，key 去空白后为空返回 ErrKeyRequired。主连接优先取
// WithClient，其次 WithInstance，都没有时使用别名 "default"；同时传入两者会报错。
// New 不建立连接，启用预取池时需再调用 StartPool。
func New(key string, opts ...Option) (*Generator, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrKeyRequired
	}

	s := settings{
		ttl:           15 * time.Second,
		retryTimes:    defaultRetryTimes,
		retryDelay:    150 * time.Millisecond,
		fallbackDelay: defaultFallbackDelay,
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
		key:           key,
		ttl:           s.ttl,
		retryTimes:    s.retryTimes,
		retryDelay:    s.retryDelay,
		fallbackDelay: s.fallbackDelay,
		fallbacks:     s.fallbacks,
		onError:       s.onError,
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
// 返回单个 ID，等价于 GetBatch(ctx, 1)；错误语义与 GetBatch 相同。
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
// 分配 n 个 ID 并按升序返回。启用池时先消费缓冲，再向 Redis 取剩余部分（闭区间
// [max-remain+1, max]）；n 超过 MaxIncr 会被钳制，返回长度可能小于请求值，
// n <= 0 直接返回空结果且不访问 Redis。出错且池内已取出部分 ID 时，这些 ID 随
// 错误一起返回，不会静默丢弃。
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

// generate retries the primary source retryTimes times; if it still fails it
// fires the error callback, waits out the fallback delay and then tries each
// fallback once, matching the PHP version.
// generate 先在主连接上重试 retryTimes 次；仍失败则回调告警、等待切换停顿，
// 再依次尝试备用连接各一次（与 PHP 版一致）。
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
	if lastErr == nil {
		// No attempt ran or none reported a reason: return the bare sentinel
		// instead of wrapping a nil cause and alerting with a nil error.
		// 一次尝试都没执行、或没有任何原因可报时，直接返回哨兵错误，避免包装 nil
		// 原因、也不要用 nil 触发告警回调。
		return 0, ErrUnavailable
	}
	if g.onError != nil {
		g.onError(lastErr)
	}
	// A fallback owns a different counter, so let the millisecond prefix move
	// on before its first allocation. Without any fallback there is no handover
	// and no reason to pay the delay.
	// 备用用的是另一套计数器，先让毫秒前缀往前走一步再取号。没有备用就没有切换，
	// 不必付这段延迟。
	if len(g.fallbacks) > 0 {
		if err := sleepCtx(ctx, g.fallbackDelay); err != nil {
			return 0, err
		}
	}
	for _, fallback := range g.fallbacks {
		id, err := g.try(ctx, fallback, step)
		if err == nil {
			return id, nil
		}
		errs = append(errs, err)
	}
	joined := errors.Join(errs...)
	if joined == nil {
		return 0, ErrUnavailable
	}
	return 0, fmt.Errorf("%w: %w", ErrUnavailable, joined)
}

func (g *Generator) wait(ctx context.Context) error {
	return sleepCtx(ctx, g.retryDelay)
}

// sleepCtx waits for d but returns as soon as ctx is cancelled; a non-positive
// d is a no-op.
// sleepCtx 等待 d，ctx 取消时立即返回；d 非正时不等待。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// try resolves the connection and runs the Lua script once. An empty array from
// Lua means a same-millisecond collision or a counter rollover, which is
// returned as a retryable error.
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

// clampStep mirrors the clamping done in Lua; expanding a batch must use the
// same step.
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

// expandRange returns [maxID-count+1, maxID]; maxID is the right end of this
// INCRBY, so count itself must not be subtracted or the previous ID would be
// included and the current maximum lost.
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

// uuidScript keeps the same semantics as PHP LuaScripts::getUuidLuaScript():
//  1. INCRBY the in-segment counter; above MaxIncr delete the key and retry, and
//     discard the segment if it is still over the limit;
//  2. build a 19-digit prefix from Redis TIME seconds + milliseconds (seconds +
//     milliseconds + six zeros);
//  3. on the first write of a segment compare the previous millisecond stamp and
//     return an empty result on a same-millisecond collision so the caller retries.
//
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
