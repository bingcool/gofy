// Package lease implements the global-singleton primitive: a lock only one
// instance may hold, plus a monotonic token that lets the protected resource
// reject a stale holder.
//
// A lease is the last resort, not the default. Use claim when the work can be
// split, and a consumer group when the work arrives as messages. Reach for a
// lease only when the action must not run twice at all (settlement, day
// rollover, cleanup), which also means the switching window is acceptable.
// 包 lease 实现"全局单例"原语：同一时刻只有一个实例能持有的锁，外加一个单调递增
// 的 token，让被保护的资源能拒绝过期持有者的写入。
//
// lease 是最后手段而不是默认选择：工作可拆分时用 claim，消息型工作交给消费组；
// 只有"绝不能执行两次"的动作（对账、日切、清理）才需要它，这也意味着它能接受
// 切换窗口。
package lease

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"
)

var (
	// ErrHeld means another instance holds the lease right now.
	// ErrHeld 表示当前由其他实例持有该租约。
	ErrHeld = errors.New("lease: already held")

	// ErrLeaseLost means the lease expired or was taken over. The caller must
	// stop writing: its token is now behind the new holder's, so the protected
	// resource will reject it anyway.
	// ErrLeaseLost 表示租约已过期或被接管。调用方必须停止写入：它的 token 已经
	// 落后于新持有者，被保护的资源本来也会拒绝它。
	ErrLeaseLost = errors.New("lease: lease lost")

	// ErrNilLocker means Run was called without a locker.
	// ErrNilLocker 表示调用 Run 时没有提供 locker。
	ErrNilLocker = errors.New("lease: locker is required")

	// ErrNilFunc means Run was called without a function to guard.
	// ErrNilFunc 表示调用 Run 时没有提供受保护的操作。
	ErrNilFunc = errors.New("lease: run func is required")

	// ErrInvalidKey means the key is empty, too long, or carries characters
	// that would break the implementation's key naming.
	// ErrInvalidKey 表示 key 为空、过长，或含有会破坏实现方命名规则的字符。
	ErrInvalidKey = errors.New("lease: invalid key")

	// ErrInvalidTTL means the ttl is shorter than MinTTL.
	// ErrInvalidTTL 表示 ttl 短于 MinTTL。
	ErrInvalidTTL = errors.New("lease: invalid ttl")
)

const (
	// DefaultTTL is the lease lifetime used when none is given. It must stay
	// meaningfully larger than a stop-the-world pause; the classic mistake is a
	// one-second lease that expires during an ordinary GC cycle.
	// DefaultTTL 是未指定时的租约时长。它必须显著大于一次 stop-the-world 停顿；
	// 典型的错误是设成一秒，然后在一轮普通的 GC 里就过期了。
	DefaultTTL = 15 * time.Second

	// MinTTL is the shortest ttl accepted. It exists to catch unit mistakes
	// (milliseconds passed as seconds), not to bless short leases: below ten
	// seconds a single scheduling pause already produces two holders.
	// MinTTL 是允许的最短租约，用来拦住单位写错（把毫秒当秒）而不是为短租约背书：
	// 低于十秒时，一次调度停顿就足以造出两个持有者。
	MinTTL = 100 * time.Millisecond
)

// Locker acquires leases by key. Implementations must be safe across
// processes and across hosts. Callers must Release every acquired Lease:
// implementations may keep an in-process reservation per key, and dropping a
// handle without Release can leave that key blocked inside the process.
// Locker 按 key 获取租约，实现必须跨进程、跨主机安全。调用方必须 Release 每个已
// 获取的 Lease：实现可能按 key 保留进程内占用，丢弃句柄而不 Release 会让该 key 在
// 本进程内一直处于占用状态。
type Locker interface {
	// Acquire takes the lease for key. It returns ErrHeld when another holder
	// owns it, and a Lease with a strictly larger token on success.
	// Acquire 获取 key 的租约。被他人持有时返回 ErrHeld；成功时返回的 Lease
	// 带有一个严格更大的 token。
	Acquire(ctx context.Context, key string, ttl time.Duration) (Lease, error)
}

// Lease is a held lock. Token is monotonic and must be passed to the resource
// being protected, so a paused holder's late write can be rejected.
// Lease 是一个已持有的锁。Token 单调递增，必须传给被保护的资源，使暂停后苏醒
// 的旧持有者的迟到写入能被拒绝。
type Lease interface {
	// Key is the lease key as given to Acquire.
	// Key 返回传给 Acquire 的租约键。
	Key() string

	// Token is the fencing token. Pass it to the protected resource and compare
	// it there: `UPDATE ... WHERE fence < :token` keeps a stale holder out.
	// Token 是 fencing token。把它传给被保护的资源并在那里比较：
	// `UPDATE ... WHERE fence < :token` 就能挡住过期持有者。
	Token() int64

	// Renew extends the lease. It returns ErrLeaseLost when the lease already
	// expired or was taken over.
	// Renew 延长租约。租约已过期或被接管时返回 ErrLeaseLost。
	Renew(ctx context.Context) error

	// Release drops the lease immediately. Releasing a lease that was already
	// lost is not an error: shutdown paths call it unconditionally, and a
	// second failure there would only hide the first.
	// Release 立即释放租约。释放一个已经丢失的租约不算错误：停机路径会无条件调用
	// 它，这时候再报一次错只会掩盖真正的原因。
	Release(ctx context.Context) error
}

// RunConfig tunes Run.
// RunConfig 配置 Run 的行为。
type RunConfig struct {
	// TTL is the lease lifetime; zero means DefaultTTL. Renewal runs at a third
	// of it, so two renewals may fail before the lease is actually gone.
	// TTL 是租约时长，0 表示 DefaultTTL。续约按三分之一节奏进行，因此在租约真正
	// 失效前允许两次续约失败。
	TTL time.Duration

	// Interval is the pause between two calls of the guarded function. Zero
	// means the function is called once and Run returns when it returns: pass
	// zero only for a function that blocks until the context is cancelled.
	// Interval 是受保护操作的两次调用之间的间隔。为 0 表示只调用一次、函数返回
	// Run 就返回，因此只有自己阻塞到 ctx 取消的函数才该传 0。
	Interval time.Duration

	// Name labels lease metrics. Empty means the aggregate series only, so a
	// deployment that runs one lease does not need a name to get counters.
	// Name 是 lease 指标的标签。为空时只发聚合序列，因此只跑一个租约的部署
	// 不必为了拿到计数器而特意起名。
	Name string
	// MeterProvider creates this Run's instruments; nil disables creation.
	// MeterProvider 创建本次 Run 的 instrument；nil 表示不创建。
	MeterProvider metric.MeterProvider
	// Metrics reports whether the provider is real; when false hot paths skip
	// attribute construction entirely.
	// The two fields must be set together: the provider alone with Metrics=false
	// is silently ignored, and Metrics=true with a nil provider is rejected by
	// Run. Wire both from core.Runtime (rt.MeterProvider, rt.Metrics).
	// Metrics 表示 provider 是否真实可用；为 false 时热路径不构造标签。
	// 两个字段必须成对设置：只给 provider 而留 false 会被静默忽略；true 配 nil
	// provider 会被 Run 拒绝。请一并从 core.Runtime 接线（rt.MeterProvider、
	// rt.Metrics）。
	Metrics bool
}

// Run holds key for the duration of fn and renews the lease behind it.
//
// While another instance holds the lease, Run waits and retries instead of
// returning, which is what a worker's blocking contract wants. fn receives a
// context that dies as soon as the lease is lost, so a long-running action
// stops writing before its token goes stale. A periodic action is expressed
// with Interval; the lease stays held between calls, so no other instance can
// insert an extra run in the gap.
// Run 在 fn 执行期间持有 key，并在后台续约。
//
// 租约被其他实例持有时，Run 等待并重试而不是返回，正好满足 worker 的阻塞约定。
// fn 收到的 context 在租约丢失时立刻取消，因此长耗时操作会在 token 过期之前停止
// 写入。周期性动作通过 Interval 表达；租约在两次调用之间不释放，其他实例也就无法
// 在间隙里插进一次额外的执行。
func Run(ctx context.Context, l Locker, key string, cfg RunConfig, fn func(context.Context, int64) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if l == nil {
		return ErrNilLocker
	}
	if fn == nil {
		return ErrNilFunc
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	ttl := cfg.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if err := ValidateTTL(ttl); err != nil {
		return err
	}
	metrics, err := newLeaseMetrics(cfg.MeterProvider, cfg.Metrics, cfg.Name)
	if err != nil {
		return err
	}

	retry := ttl / 3
	if retry < time.Second {
		retry = time.Second
	}
	for {
		held, err := l.Acquire(ctx, key, ttl)
		switch {
		case err == nil:
			return hold(ctx, held, ttl, cfg.Interval, fn, metrics)
		case errors.Is(err, ErrHeld):
			// Standing by is the normal state of a standby: poll at a third of
			// the ttl so a dead leader is picked up within roughly one term.
			// 待命是备实例的常态：按 ttl 的三分之一轮询，前一个持有者失联后大致
			// 一个租期内就能接管。
			if !sleep(ctx, retry) {
				return nil
			}
		default:
			metrics.recordAcquireFailure()
			return fmt.Errorf("lease: acquire %q: %w", key, err)
		}
	}
}

// hold runs fn under a renewing lease and settles the outcome.
// hold 在续约中的租约下运行 fn，并结算最终结果。
func hold(ctx context.Context, held Lease, ttl, interval time.Duration, fn func(context.Context, int64) error, metrics *leaseMetrics) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	renewed := make(chan error, 1)
	go func() {
		err := keep(runCtx, held, ttl, metrics)
		// Losing the lease cancels fn: it must stop before its token is
		// rejected by the resource, not after.
		// 丢失租约时取消 fn：它必须在 token 被资源拒绝之前停下，而不是之后。
		if err != nil {
			cancel()
		}
		renewed <- err
	}()

	fnErr := call(runCtx, interval, fn, held.Token())

	cancel()
	renewErr := <-renewed
	releaseErr := release(ctx, held)

	switch {
	case errors.Is(renewErr, ErrLeaseLost):
		return renewErr
	case ctx.Err() != nil:
		return nil
	case fnErr != nil:
		return fnErr
	default:
		return releaseErr
	}
}

// call invokes fn once, or repeatedly when interval is positive.
// call 调用 fn 一次；interval 为正数时重复调用。
func call(ctx context.Context, interval time.Duration, fn func(context.Context, int64) error, token int64) error {
	for {
		err := fn(ctx, token)
		if err != nil || interval <= 0 || ctx.Err() != nil {
			return err
		}
		if !sleep(ctx, interval) {
			return nil
		}
	}
}

// keep renews the lease until the context ends or the lease is gone.
// keep 持续续约，直到 context 结束或租约消失。
func keep(ctx context.Context, l Lease, ttl time.Duration, metrics *leaseMetrics) error {
	interval := ttl / 3
	deadline := time.Now().Add(ttl)
	for {
		if !sleep(ctx, interval) {
			return nil
		}
		err := l.Renew(ctx)
		if err == nil {
			deadline = time.Now().Add(ttl)
			continue
		}
		if errors.Is(err, ErrLeaseLost) {
			metrics.recordRenewFailure(true)
			return err
		}
		metrics.recordRenewFailure(false)
		// A failed round trip is not proof the lease is gone: the store may be
		// unreachable while the key still points at us. Keep trying until the
		// term we already paid for runs out, then report the loss.
		// 一次往返失败并不等于租约消失：存储可能只是暂时不可达，而键仍指向我们。
		// 继续重试直到已经买到的那段租期用完，再报告丢失。
		if !time.Now().Before(deadline) {
			// The stored error was counted above; this is the moment it turns
			// into a loss, so the lost outcome is counted separately.
			metrics.recordRenewFailure(true)
			return fmt.Errorf("%w: renew: %v", ErrLeaseLost, err)
		}
	}
}

// release drops the lease with a bounded context of its own, because the caller
// is usually shutting down and its context is already cancelled.
// release 用独立的有限 context 释放租约：调用方通常在停机，它自己的 context 已经
// 被取消。
func release(ctx context.Context, l Lease) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return l.Release(rctx)
}

// ValidateKey rejects keys that are empty, unreasonably long, or contain the
// characters implementations use for their own key layout.
// ValidateKey 拒绝空、过长或含有实现方命名分隔符的 key。
func ValidateKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	case len(key) > 256:
		return fmt.Errorf("%w: %d bytes exceeds 256", ErrInvalidKey, len(key))
	case strings.ContainsAny(key, "{} \t\r\n"):
		return fmt.Errorf("%w: %q contains a reserved character", ErrInvalidKey, key)
	}
	return nil
}

// ValidateTTL rejects ttls shorter than MinTTL.
// ValidateTTL 拒绝短于 MinTTL 的 ttl。
func ValidateTTL(ttl time.Duration) error {
	if ttl < MinTTL {
		return fmt.Errorf("%w: %s is below %s", ErrInvalidTTL, ttl, MinTTL)
	}
	return nil
}

// sleep waits for d and reports whether the context survived.
// sleep 等待 d，并返回 ctx 是否仍然有效。
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
