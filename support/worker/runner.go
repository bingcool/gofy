package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

var (
	// ErrNilGroup means NewRunner was called without a group.
	// ErrNilGroup 表示调用 NewRunner 时没有提供分组。
	ErrNilGroup = errors.New("worker: group is required")

	// ErrInvalidConcurrency means a concurrency entry is below one.
	// ErrInvalidConcurrency 表示某个并发配置小于 1。
	ErrInvalidConcurrency = errors.New("worker: concurrency must be at least 1")

	// ErrWorkerExited reports a Worker whose Run returned nil before the
	// context was cancelled, which breaks the blocking contract.
	// ErrWorkerExited 表示 Worker 在 ctx 取消前就返回了 nil，违反了阻塞约定。
	ErrWorkerExited = errors.New("worker: run returned without cancellation")

	// ErrDrainTimeout means the group did not finish draining inside the
	// budget. The process should still exit; the error only marks the log.
	// ErrDrainTimeout 表示分组未在预算内完成 drain；进程仍会退出，该错误只用于
	// 标记日志。
	ErrDrainTimeout = errors.New("worker: drain budget exhausted")
)

// RunConfig configures one Runner.
// RunConfig 配置一个 Runner。
type RunConfig struct {
	// Policy controls restarts; the zero value restarts with defaults.
	// Policy 控制重启；零值表示按默认策略重启。
	Policy Policy
	// Concurrency maps a worker name to its instance count and overrides the
	// count the worker declares through Func. A missing entry keeps the
	// worker's own default (one for workers that declare nothing); values below
	// one are rejected.
	// Concurrency 把 worker 名映射到实例数，并覆盖 worker 通过 Func 声明的计数；未配置
	// 时沿用 worker 自己的默认值（没声明过的按 1 个），小于 1 的值会被拒绝。
	Concurrency map[string]int
	// DrainTimeout is how long in-flight work may run after the context is
	// cancelled; zero means 20s. It must stay below the process shutdown budget
	// so the host can still close the database and flush telemetry.
	// DrainTimeout 是 ctx 取消后在途任务还能运行多久，0 表示 20s。它必须小于
	// 进程关闭总预算，宿主才有时间关闭数据库与 flush 遥测。
	DrainTimeout time.Duration
	// Logger receives lifecycle events; nil discards them.
	// Logger 接收生命周期事件；nil 表示丢弃。
	Logger *zap.Logger
}

const defaultDrainTimeout = 20 * time.Second

func (c RunConfig) withDefaults() RunConfig {
	c.Policy = c.Policy.withDefaults()
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = defaultDrainTimeout
	}
	return c
}

func (c RunConfig) instances(w Worker) int {
	if n, ok := c.Concurrency[w.Name()]; ok && n > 0 {
		return n
	}
	// Func carries its own count; anything else that declares one through
	// Concurrent gets the same treatment, so the host does not have to repeat
	// what the worker already knows.
	// Func 自带计数；其它实现 Concurrent 的 worker 同样处理，宿主不必重复 worker
	// 已经知道的数字。
	if cw, ok := w.(Concurrent); ok && cw.Concurrency() > 0 {
		return int(cw.Concurrency())
	}
	return 1
}

// Runner starts a group, supervises restarts and drains it on shutdown.
// Runner 启动分组、监督重启，并在停机时执行 drain。
type Runner struct {
	group *Group
	cfg   RunConfig
}

// NewRunner validates the group and its concurrency map.
// NewRunner 校验分组与并发配置。
func NewRunner(group *Group, cfg RunConfig) (*Runner, error) {
	if group == nil {
		return nil, ErrNilGroup
	}
	for name, n := range cfg.Concurrency {
		if n < 1 {
			return nil, fmt.Errorf("%w: %s=%d", ErrInvalidConcurrency, name, n)
		}
	}
	return &Runner{group: group, cfg: cfg.withDefaults()}, nil
}

// Group returns the group being run.
// Group 返回当前运行的分组。
func (r *Runner) Group() *Group { return r.group }

// Run blocks until the context is cancelled, a worker fails fatally, or every
// instance exits on its own (which is a contract violation and reported as an
// error). On cancellation it stops new work, waits for in-flight work inside
// the drain budget and returns nil.
// Run 阻塞到 ctx 取消、某个 worker 致命失败，或全部实例自行退出（视为违反约定
// 并报错）。ctx 取消时它先停止新任务，在 drain 预算内等待在途任务，然后返回 nil。
func (r *Runner) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := r.cfg

	runCtx, stopRuns := context.WithCancel(ctx)
	defer stopRuns()
	// The drain context survives cancellation on purpose: handlers that already
	// started may finish, but only until the budget expires.
	// drain context 故意不随取消失效：已经开始的处理可以跑完，但不超过预算。
	drainCtx, stopDrain := context.WithTimeout(context.WithoutCancel(ctx), cfg.DrainTimeout)
	defer stopDrain()

	var (
		wg      sync.WaitGroup
		once    sync.Once
		fatal   error
		fatalCh = make(chan struct{})
	)
	fail := func(err error) {
		once.Do(func() {
			fatal = err
			close(fatalCh)
		})
	}

	host := cfg.hostname()
	for _, w := range r.group.Workers() {
		total := cfg.instances(w)
		for index := 0; index < total; index++ {
			id := Identity{
				Group: r.group.Name(),
				Name:  w.Name(),
				Index: index,
				Total: total,
				Host:  host,
			}
			wg.Add(1)
			go func(w Worker, id Identity) {
				defer wg.Done()
				r.supervise(runCtx, drainCtx, w, id, fail)
			}(w, id)
		}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
	case <-fatalCh:
	case <-done:
	}

	// Stop pulling first, then wait: the order matters because cancelling the
	// drain context before the run context would abort work that still had
	// budget left.
	// 先停止拉取再等待：顺序很重要，先取消 drain context 会中断仍有预算的在途
	// 任务。
	stopRuns()
	drained := r.wait(done, cfg.DrainTimeout+time.Second)
	stopDrain()

	if fatal != nil {
		return fatal
	}
	if ctx.Err() != nil {
		if !drained {
			r.warn("worker group drain budget exhausted", zap.Duration("drain_timeout", cfg.DrainTimeout))
		}
		return nil
	}
	if !drained {
		return fmt.Errorf("%w: group %q", ErrDrainTimeout, r.group.Name())
	}
	// Every instance returned on its own; supervise already reported that as a
	// contract violation through fail, so this is only reachable if that report
	// lost the race with the shutdown signal.
	// 全部实例自行退出；supervise 已把它作为违反约定上报，这里只会在该上报与
	// 停机信号竞争失败时到达。
	return nil
}

// supervise runs one instance, restarting it according to the policy.
// supervise 运行单个实例，并按策略重启。
func (r *Runner) supervise(ctx, drain context.Context, w Worker, id Identity, fail func(error)) {
	policy := r.cfg.Policy
	restarts := 0
	for {
		// Identity and the drain context travel together so a consumer adapter
		// can keep handling in-flight messages after the run context is
		// cancelled.
		// 身份与 drain context 一起下发，消费者适配器因此能在 run context 取消
		// 后继续处理在途消息。
		runCtx := WithDrainContext(WithIdentity(ctx, id), drain)
		err := w.Run(runCtx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			fail(fmt.Errorf("%w: %s", ErrWorkerExited, id))
			return
		}
		if policy.Mode == ExitOnError {
			fail(fmt.Errorf("worker %s: %w", id, err))
			return
		}
		restarts++
		if policy.MaxRestarts > 0 && restarts > policy.MaxRestarts {
			fail(fmt.Errorf("worker %s: %w (restart limit %d reached)", id, err, policy.MaxRestarts))
			return
		}
		delay := policy.backoff(restarts)
		r.warn("worker exited with error, restarting",
			zap.String("group", id.Group),
			zap.String("worker", id.Name),
			zap.Int("instance", id.Index),
			zap.Int("restarts", restarts),
			zap.Duration("delay", delay),
			zap.Error(err),
		)
		if !sleep(ctx, delay) {
			return
		}
	}
}

// wait blocks until done or the timeout expires.
// wait 阻塞到 done 关闭或超时。
func (r *Runner) wait(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (r *Runner) warn(msg string, fields ...zap.Field) {
	if r.cfg.Logger == nil {
		return
	}
	r.cfg.Logger.Warn(msg, fields...)
}

func (c RunConfig) hostname() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
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
