// Package lock provides Redis-backed distributed mutexes built on redsync.
//
// A mutex is a key with a value and an expiry: the holder is whoever wrote the
// value, and the expiry is what frees the key when the holder dies. Two rules
// follow from that and cannot be enforced by the library, so they are stated
// here instead:
//
//   - the expiry must outlive the critical section. A lock that expires while
//     the holder still works is not exclusive any more; call Extend for work
//     whose duration is not bounded.
//   - a mutex is not reentrant. Locking the same key twice in one goroutine (or
//     two) waits for the first lock to expire, because the second call sees a
//     value it did not write.
//
// Unlock only deletes the key when the stored value is still the one this
// mutex wrote, so an expired lock that somebody else took over is never
// released by its previous owner.
// 包 lock 基于 redsync 提供 Redis 分布式互斥锁。
//
// 一把锁是一个带值和过期时间的 key：写入该值的持有者就是所有者，过期时间负责在持有者
// 死亡后释放 key。由此有两条库无法强制、只能写在这里的约束：
//
//   - 过期时间必须长于临界区。锁在持有者仍在工作时过期，就不再互斥；耗时不确定的工作
//     请调用 Extend 续租。
//   - 锁不可重入。同一 goroutine（或两个）对同一 key 二次加锁会等到第一次过期，因为
//     第二次看到的值不是它写的。
//
// Unlock 只有在 key 里仍是本锁写入的值时才删除它，因此过期后已被别人接管的锁，绝不会
// 被它的前任释放。
package lock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-redsync/redsync/v4"
	redsyncgoredis "github.com/go-redsync/redsync/v4/redis/goredis/v9"
	goredis "github.com/redis/go-redis/v9"

	gofyredis "github.com/bingcool/gofy/support/redis"
)

const (
	// DefaultInstance is the support/redis alias used when neither a client nor
	// an instance name is given.
	// DefaultInstance 是既未传 client、也未指定实例名时使用的 support/redis 别名。
	DefaultInstance = "default"

	// DefaultExpiry is the lock TTL when no configuration says otherwise. It is
	// deliberately larger than redsync's 8s default: the TTL has to cover the
	// critical section, and 8s is short for anything that touches a database.
	// DefaultExpiry 是没有配置时的锁 TTL。它刻意大于 redsync 默认的 8s：TTL 必须覆盖
	// 临界区，而 8s 对任何碰数据库的操作都偏短。
	DefaultExpiry = 10 * time.Second

	// DefaultTries is how many times a blocking lock retries before it reports
	// ErrAlreadyHeld.
	// DefaultTries 是阻塞式加锁在报 ErrAlreadyHeld 之前的重试次数。
	DefaultTries = 3

	// DefaultRetryDelay is the fixed pause between those retries.
	// DefaultRetryDelay 是上述重试之间的固定间隔。
	DefaultRetryDelay = 100 * time.Millisecond
)

var (
	// ErrClientRequired is returned by WithClient(nil): a nil client would turn
	// every later call into a nil dereference.
	// WithClient(nil) 返回该错误：nil 客户端会让之后的每次调用变成空指针解引用。
	ErrClientRequired = errors.New("lock: redis client must not be nil")
	// ErrKeyRequired is returned when the lock key is empty.
	// 锁 key 为空时返回。
	ErrKeyRequired = errors.New("lock: key is required")
	// ErrAlreadyHeld is returned when the key is held by someone else and the
	// retries ran out. It says nothing about who holds it.
	// 锁被他人持有且重试耗尽时返回；它不透露持有者是谁。
	ErrAlreadyHeld = errors.New("lock: lock is already held")
	// ErrNotHeld is returned by Unlock and Extend when this mutex no longer owns
	// the key: the lock expired, or another caller took it over.
	// Unlock 与 Extend 在本锁不再拥有该 key 时返回：锁已过期，或已被其它调用方接管。
	ErrNotHeld = errors.New("lock: lock is not held by this caller")
	// ErrUnavailable is returned when Redis itself failed (connection, timeout,
	// command error) rather than "the lock is busy".
	// Redis 自身失败（连接、超时、命令错误）而不是"锁被占用"时返回。
	ErrUnavailable = errors.New("lock: redis is unavailable")
)

// Option configures a Locker.
// Option 在 New 中按顺序应用，任一返回错误即中止构造；nil Option 会被跳过。
type Option func(*settings) error

type settings struct {
	client      *goredis.Client
	clientSet   bool
	instance    string
	instanceSet bool
	prefix      string
	expiry      time.Duration
	tries       int
	retryDelay  time.Duration
	onError     func(error)
}

// WithClient pins the locker to an explicit Redis client. It cannot be combined
// with WithInstance. The caller keeps ownership: the locker never closes it.
// WithClient 把锁固定到显式传入的 Redis 客户端，与 WithInstance 互斥。客户端仍归
// 调用方所有，Locker 不会关闭它。
func WithClient(client *goredis.Client) Option {
	return func(s *settings) error {
		if client == nil {
			return ErrClientRequired
		}
		s.client = client
		s.clientSet = true
		return nil
	}
}

// WithInstance resolves the client through the support/redis registry on every
// call, so a reload that installs new instances is picked up without rebuilding
// the locker. It cannot be combined with WithClient.
// WithInstance 在每次调用时通过 support/redis 注册表解析客户端，因此 Reload 安装的
// 新实例无需重建 Locker 即可生效。它与 WithClient 互斥。
func WithInstance(name string) Option {
	return func(s *settings) error {
		s.instance = strings.TrimSpace(name)
		s.instanceSet = true
		return nil
	}
}

// WithPrefix namespaces every key this locker writes, so two applications can
// share one Redis database without colliding. A trailing colon is optional and
// normalized away.
// WithPrefix 为本 Locker 写入的所有 key 加命名空间，两个应用可以共用一个 Redis 库而
// 不撞键。结尾的冒号可写可不写，会被统一去掉。
func WithPrefix(prefix string) Option {
	return func(s *settings) error {
		s.prefix = prefix
		return nil
	}
}

// WithExpiry sets the default lock TTL.
// WithExpiry 设置默认的锁 TTL。
func WithExpiry(expiry time.Duration) Option {
	return func(s *settings) error {
		s.expiry = expiry
		return nil
	}
}

// WithRetry sets the default retry count and pause for blocking locks. A delay
// of zero retries back to back; TryLock ignores both.
// WithRetry 设置阻塞式加锁的默认重试次数与间隔。间隔为 0 表示立即重试；TryLock 忽略
// 这两个值。
func WithRetry(tries int, delay time.Duration) Option {
	return func(s *settings) error {
		s.tries = tries
		s.retryDelay = delay
		return nil
	}
}

// WithErrorHandler receives every error the locker reports internally — the
// ones it also returns, so a caller that only logs at the boundary does not need
// it. Use it to attach a logger without making this package depend on one.
// WithErrorHandler 接收 Locker 内部的每次错误——这些错误同时也会返回，因此只在边界
// 记日志的调用方并不需要它。它的用途是挂日志，而不让本包依赖某个日志库。
func WithErrorHandler(fn func(error)) Option {
	return func(s *settings) error {
		s.onError = fn
		return nil
	}
}

// LockOption adjusts one acquisition.
// LockOption 调整单次加锁。
type LockOption func(*lockSettings)

type lockSettings struct {
	expiry     time.Duration
	tries      int
	retryDelay time.Duration
	token      string
}

// WithLockExpiry overrides the TTL of one lock.
// WithLockExpiry 覆盖单把锁的 TTL。
func WithLockExpiry(expiry time.Duration) LockOption {
	return func(s *lockSettings) { s.expiry = expiry }
}

// WithLockRetry overrides the retry policy of one blocking lock.
// WithLockRetry 覆盖单把阻塞式锁的重试策略。
func WithLockRetry(tries int, delay time.Duration) LockOption {
	return func(s *lockSettings) {
		s.tries = tries
		s.retryDelay = delay
	}
}

// WithLockToken uses a caller-supplied value instead of a random one. It is
// what makes a lock recognizable across processes — for example to carry a
// request ID into Redis for debugging — and it must be unique per acquisition:
// two mutexes with the same value can both believe they own the key.
// WithLockToken 用调用方提供的值代替随机值。它让跨进程可以识别一次持锁（例如把请求
// ID 带进 Redis 便于排查），但每次获取必须唯一：两个值相同的锁会同时认为自己持有 key。
func WithLockToken(token string) LockOption {
	return func(s *lockSettings) { s.token = token }
}

// Locker creates mutexes against one Redis deployment.
// Locker 针对一个 Redis 部署创建互斥锁。
type Locker struct {
	prefix     string
	expiry     time.Duration
	tries      int
	retryDelay time.Duration
	onError    func(error)

	// pinned is built once when the caller supplied a client. Without one the
	// Redsync handle is rebuilt per call from the alias resolved through
	// support/redis, which is what keeps a reloaded registry usable.
	// 调用方给了客户端时 pinned 只构建一次；没给时每次调用都用 support/redis 解析出的
	// 别名重建 Redsync 句柄，Reload 后的注册表因此仍然可用。
	pinned   *redsync.Redsync
	instance string
}

// New builds a locker. With neither WithClient nor WithInstance it uses the
// "default" support/redis instance, so a project that configured Redis once does
// not have to wire anything else.
// New 构造 Locker。既没有 WithClient 也没有 WithInstance 时使用 support/redis 的
// "default" 实例：已经配过 Redis 的项目不需要再接任何东西。
func New(opts ...Option) (*Locker, error) {
	s := settings{expiry: DefaultExpiry, tries: DefaultTries, retryDelay: DefaultRetryDelay}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&s); err != nil {
			return nil, err
		}
	}
	if s.clientSet && s.instanceSet {
		return nil, errors.New("lock: WithClient and WithInstance are mutually exclusive")
	}
	if s.expiry <= 0 {
		return nil, fmt.Errorf("lock: expiry must be positive, got %s", s.expiry)
	}
	if s.tries < 1 {
		return nil, fmt.Errorf("lock: tries must be at least 1, got %d", s.tries)
	}
	if s.retryDelay < 0 {
		return nil, fmt.Errorf("lock: retry delay must not be negative, got %s", s.retryDelay)
	}

	l := &Locker{
		prefix:     normalizePrefix(s.prefix),
		expiry:     s.expiry,
		tries:      s.tries,
		retryDelay: s.retryDelay,
		onError:    s.onError,
	}
	if s.clientSet {
		l.pinned = redsync.New(redsyncgoredis.NewPool(s.client))
	} else {
		l.instance = s.instance
		if l.instance == "" {
			l.instance = DefaultInstance
		}
	}
	return l, nil
}

// Prefix returns the normalized key namespace, empty when none is configured.
// Prefix 返回规范化后的键命名空间，未配置时为空串。
func (l *Locker) Prefix() string { return l.prefix }

// Lock acquires the key, retrying according to the configured policy. It
// returns ErrAlreadyHeld when the retries run out and the context error when the
// caller gave up first.
// Lock 按配置的重试策略获取 key。重试耗尽返回 ErrAlreadyHeld；调用方先放弃则返回
// ctx 的错误。
func (l *Locker) Lock(ctx context.Context, key string, opts ...LockOption) (*Mutex, error) {
	return l.acquire(ctx, key, true, opts...)
}

// TryLock attempts the key exactly once, which is the shape a request handler
// wants: answer "busy" now instead of holding a connection.
// TryLock 只尝试一次，这正是请求处理想要的形态：立刻回答"忙"，而不是占着连接等待。
func (l *Locker) TryLock(ctx context.Context, key string, opts ...LockOption) (*Mutex, error) {
	return l.acquire(ctx, key, false, opts...)
}

func (l *Locker) acquire(ctx context.Context, key string, retry bool, opts ...LockOption) (*Mutex, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, ErrKeyRequired
	}

	s := lockSettings{expiry: l.expiry, tries: l.tries, retryDelay: l.retryDelay}
	for _, opt := range opts {
		if opt != nil {
			opt(&s)
		}
	}
	if s.expiry <= 0 {
		return nil, fmt.Errorf("lock: expiry must be positive, got %s", s.expiry)
	}
	// TryLock is "one attempt" by contract, so the retry settings cannot leak
	// into it.
	// TryLock 的契约就是"只试一次"，重试配置不能渗进来。
	tries := 1
	if retry {
		tries = max(s.tries, 1)
	}

	rs, err := l.redsyncFor(ctx)
	if err != nil {
		l.report(err)
		return nil, err
	}

	redisKey := l.redisKey(key)
	ropts := []redsync.Option{
		redsync.WithExpiry(s.expiry),
		redsync.WithTries(tries),
		redsync.WithRetryDelayFunc(func(int) time.Duration { return s.retryDelay }),
	}
	if s.token != "" {
		// WithValue only seeds the value; LockContext generates a fresh one on
		// success and would overwrite it. Replacing the generator is the only
		// hook that makes a caller-supplied token stick.
		// WithValue 只是预置值；LockContext 成功时会生成新值并覆盖它。换掉生成器才是
		// 让调用方提供的 token 真正生效的唯一入口。
		ropts = append(ropts, redsync.WithGenValueFunc(func() (string, error) { return s.token, nil }))
	}
	m := rs.NewMutex(redisKey, ropts...)
	if retry {
		err = m.LockContext(ctx)
	} else {
		err = m.TryLockContext(ctx)
	}
	if err != nil {
		return nil, l.acquireFailure(ctx, key, err)
	}
	return &Mutex{key: key, redisKey: redisKey, expiry: s.expiry, mutex: m, onError: l.onError}, nil
}

// acquireFailure translates redsync's errors. The context check comes first on
// purpose: redsync folds a canceled context into ErrFailed while waiting between
// retries, and "shutting down" must not reach the caller as "lock is busy".
//
// A Redis failure wins over "taken": when part of the keyspace is unreachable,
// the honest report is the broken infrastructure, not a busy lock. With a single
// Redis either one appears alone, so this ordering only matters for the quorum
// shapes.
// acquireFailure 翻译 redsync 的错误。ctx 检查刻意放在最前面：redsync 会把重试等待
// 期间的取消折叠成 ErrFailed，而"正在停机"不能以"锁被占用"的形式传给调用方。
//
// Redis 故障优先于"已被占用"：一部分键空间不可达时，诚实的原因是基础设施坏了，而不是
// 锁忙。单个 Redis 时两者只会单独出现，这个顺序只在 quorum 形态下有意义。
func (l *Locker) acquireFailure(ctx context.Context, key string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("lock: acquire %q: %w", key, ctxErr)
	}
	var redisErr *redsync.RedisError
	if errors.As(err, &redisErr) {
		l.report(err)
		return fmt.Errorf("%w: acquire %q: %v", ErrUnavailable, key, err)
	}
	if isTaken(err) || errors.Is(err, redsync.ErrFailed) {
		return fmt.Errorf("%w: %q", ErrAlreadyHeld, key)
	}
	l.report(err)
	return fmt.Errorf("lock: acquire %q: %w", key, err)
}

// isTaken matches the two shapes redsync uses for "somebody else has it":
// ErrTaken when a quorum answered "taken", ErrNodeTaken on a single node.
// isTaken 匹配 redsync 表示"别人持有"的两种形态：达到 quorum 时是 ErrTaken，单个节点
// 上则是 ErrNodeTaken。
func isTaken(err error) bool {
	var taken *redsync.ErrTaken
	if errors.As(err, &taken) {
		return true
	}
	var nodeTaken *redsync.ErrNodeTaken
	return errors.As(err, &nodeTaken)
}

func (l *Locker) redsyncFor(ctx context.Context) (*redsync.Redsync, error) {
	if l.pinned != nil {
		return l.pinned, nil
	}
	client, err := gofyredis.NewIO(ctx, l.instance)
	if err != nil {
		return nil, fmt.Errorf("lock: resolve redis instance %q: %w", l.instance, err)
	}
	return redsync.New(redsyncgoredis.NewPool(client)), nil
}

func (l *Locker) redisKey(key string) string {
	if l.prefix == "" {
		return key
	}
	return l.prefix + ":" + key
}

func (l *Locker) report(err error) {
	if l.onError != nil {
		l.onError(err)
	}
}

// Mutex is one held lock. It is not safe to use a Mutex after Unlock.
// Mutex 是一把已持有的锁；Unlock 之后不应再使用它。
type Mutex struct {
	key      string
	redisKey string
	expiry   time.Duration
	mutex    *redsync.Mutex
	onError  func(error)
}

// Key returns the key the caller passed, without the prefix.
// Key 返回调用方传入的 key，不含前缀。
func (m *Mutex) Key() string { return m.key }

// RedisKey returns the key actually written to Redis, prefix included.
// RedisKey 返回真正写入 Redis 的 key，含前缀。
func (m *Mutex) RedisKey() string { return m.redisKey }

// Token returns the value stored in Redis. It identifies this acquisition and
// is what later Extend and Unlock calls are fenced by.
// Token 返回存进 Redis 的值。它标识这一次获取，也是之后 Extend 与 Unlock 的围栏。
func (m *Mutex) Token() string { return m.mutex.Value() }

// Expiry returns the TTL this lock was acquired with.
// Expiry 返回获取这把锁时使用的 TTL。
func (m *Mutex) Expiry() time.Duration { return m.expiry }

// Until returns the conservative expiry instant: the TTL minus the drift and
// command time redsync accounts for. Treat it as "stop working before this".
// Until 返回保守的失效时刻：TTL 减去 redsync 计入的漂移与命令耗时。把它当作"必须在此
// 之前停止工作"的界线。
func (m *Mutex) Until() time.Time { return m.mutex.Until() }

// Extend resets the TTL to the one the lock was acquired with. An expired or
// taken-over lock is not extended: that returns ErrNotHeld.
// Extend 把 TTL 重置为获取时的值。已过期或已被接管的锁不会续租，返回 ErrNotHeld。
func (m *Mutex) Extend(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := m.mutex.ExtendContext(ctx)
	switch {
	case err == nil && ok:
		return nil
	case errors.Is(err, redsync.ErrExtendFailed):
		return fmt.Errorf("%w: extend %q expired before the call", ErrNotHeld, m.key)
	case err != nil:
		m.report(err)
		return fmt.Errorf("%w: extend %q: %v", ErrUnavailable, m.key, err)
	default:
		return fmt.Errorf("%w: extend %q", ErrNotHeld, m.key)
	}
}

// Unlock releases the key. It only deletes it when the stored value is still
// this mutex's token, so a lock that expired and was taken over stays with its
// new owner. Releasing twice returns ErrNotHeld; there is nothing left to do,
// and treating it as an error is what makes a double Unlock visible in tests.
// Unlock 释放 key。只有当存储值仍是本锁的 token 时才删除，因此过期后已被接管的锁仍
// 属于新持有者。重复释放返回 ErrNotHeld：此时无事可做，而把它当错误正是让"重复
// Unlock"在测试里可见的方式。
func (m *Mutex) Unlock(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ok, err := m.mutex.UnlockContext(ctx)
	switch {
	case err == nil && ok:
		return nil
	case errors.Is(err, redsync.ErrLockAlreadyExpired):
		return fmt.Errorf("%w: unlock %q already expired", ErrNotHeld, m.key)
	case err != nil:
		m.report(err)
		return fmt.Errorf("%w: unlock %q: %v", ErrUnavailable, m.key, err)
	default:
		return fmt.Errorf("%w: unlock %q was taken over or already released", ErrNotHeld, m.key)
	}
}

func (m *Mutex) report(err error) {
	if m.onError != nil {
		m.onError(err)
	}
}

// normalizePrefix trims spaces and the trailing separator so WithPrefix("a:")
// and WithPrefix("a") produce the same keys.
// normalizePrefix 去掉空白与结尾分隔符，`WithPrefix("a:")` 与 `WithPrefix("a")`
// 因此产生相同的键。
func normalizePrefix(prefix string) string {
	return strings.Trim(strings.TrimSpace(prefix), ":")
}
