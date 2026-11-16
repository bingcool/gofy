package claim

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// This file is the "one callback" layer over Queue. Queue speaks in rows and
// fencing pairs; Runner speaks in one business callback and two outcome hooks.
// A worker supplies its queue knobs, a Handler and (optionally) hooks, then calls
// Run once per tick. Everything between "a row was claimed" and "its outcome is
// durable" lives here, so two workers cannot disagree about when a row is done.
//
// The bookkeeping is not duplicated: Succeed stamps finish_handle_time and Fail
// stamps fail_reason + fail_handle_time (or parks the row as dead). Runner just
// makes sure those calls happen with the exact Task Claim returned — the owner
// and attempts pair that fences a stale handler.
//
// 本文件是 Queue 之上的"单回调"层。Queue 的语言是行与围栏字段；Runner 的语言是一个业务回调
// 加两个结果钩子。worker 只需给出队列参数、一个 Handler 和可选钩子，然后每 tick 调一次 Run。
// 从"行被认领"到"结果落库"之间的所有事都在这里，因此两个 worker 不会对"一行何时算完成"产生
// 分歧。
//
// 落库逻辑并不重复：Succeed 写 finish_handle_time，Fail 写 fail_reason + fail_handle_time
// （或把行置为 dead）。Runner 只保证这些调用用的是 Claim 原样返回的 Task——owner 与 attempts
// 这对围栏，用来挡住过期的 handler。
const (
	// DefaultHandlerTimeout bounds one Handler call when Config leaves it at zero.
	// It must stay below Lease: claim does not renew a lease, and a handler that
	// outlives it can have its row taken over.
	// DefaultHandlerTimeout 是配置为零时单次 Handler 的耗时上限。它必须小于 Lease：claim
	// 不续租，超过租约的 handler 可能被接管。
	DefaultHandlerTimeout = 5 * time.Second
)

var (
	// ErrHandlerRequired means NewRunner was called without a Handler. Running
	// without one would claim rows nobody processes and let their leases expire.
	// ErrHandlerRequired 表示 NewRunner 没有拿到 Handler。没有回调就认领行只会让租约过期。
	ErrHandlerRequired = errors.New("claim: runner handler is required")
)

// Handler processes one claimed row.
//
// data is the row's raw payload; the business decodes it. When a claim round
// finds nothing due, Run calls handler once with data == nil so a worker can
// still keep a heartbeat or idle metric in the same callback. The return values
// on that nil call are ignored — there is no row to succeed or fail — and no
// column is written.
//
// The return values on a real row drive the bookkeeping:
//   - err == nil: Run calls Succeed, which stamps finish_handle_time.
//   - err != nil: Run calls Fail, which stores the bounded reason in
//     fail_reason and the moment in fail_handle_time, then either schedules a
//     retry or parks the row as dead once attempts run out.
//
// taskID is a business identifier the handler extracts from the payload. Runner
// does not interpret it; it only passes it to the outcome hooks and logs, so a
// hook can say "order 42 failed" instead of "row 9128 failed". The claim row id
// is available separately as Result.RowID.
//
// Handler 处理一条已认领的行。
//
// data 是该行的原始 payload，由业务自行解码。当本轮没有到期任务时，Run 会用 data == nil
// 调用一次 handler，让 worker 在同一个回调里也能打心跳或空闲指标。这次 nil 调用的返回值会被
// 忽略——没有行可以成功或失败——也不会写任何列。
//
// 真实行的返回值决定如何收尾：
//   - err == nil：Run 调 Succeed，写入 finish_handle_time。
//   - err != nil：Run 调 Fail，把有界的失败原因写进 fail_reason、失败时刻写进
//     fail_handle_time，然后安排重试；尝试次数用尽则把行置为 dead。
//
// taskID 是 handler 从 payload 里解出的业务标识。Runner 不解释它，只把它透传给结果钩子和日志，
// 于是钩子能说"订单 42 失败"而不是"第 9128 行失败"。claim 行号单独放在 Result.RowID。
type Handler func(ctx context.Context, data []byte) (taskID uint64, err error)

// Result is the full context handed to the success and failure hooks. It carries
// both the business task id (from Handler) and the claim-side identity, so a
// hook can correlate an outcome with both the business record and the table row.
// Result 是交给成功/失败钩子的完整上下文。它同时带业务 task id（来自 Handler）与认领侧的标识，
// 因此钩子能把一次结果与业务记录和表行都对应起来。
type Result struct {
	// TaskID is the business identifier returned by Handler.
	// TaskID 是 Handler 返回的业务标识。
	TaskID uint64
	// RowID is the claim table's primary key for this row.
	// RowID 是该行在 claim 表里的主键。
	RowID uint64
	// Job is the configured job name.
	// Job 是配置的 job 名。
	Job string
	// Attempts is the claim number this handler ran under.
	// Attempts 是本次 handler 对应的第几次认领。
	Attempts int
	// Data is the raw payload handed to Handler.
	// Data 是交给 Handler 的原始 payload。
	Data []byte
	// Err is the handler error; nil on the success path.
	// Err 是 handler 的错误；成功路径上为 nil。
	Err error
	// Dead is true when a failed handler exhausted its attempts and the row was
	// parked as dead rather than rescheduled.
	// Dead 表示失败的 handler 已用尽尝试次数、行被置为 dead 而非重新排期。
	Dead bool
	// LeaseLost is true when Succeed/Fail found the row had been taken over, so
	// nothing was written and the side effects may have already run elsewhere.
	// LeaseLost 表示 Succeed/Fail 发现行已被接管，什么都没写入，副作用可能已在别处发生。
	LeaseLost bool
	// Elapsed is how long Handler took, excluding the bookkeeping write.
	// Elapsed 是 Handler 的耗时，不含收尾写库。
	Elapsed time.Duration
}

// RunnerConfig is everything a Runner needs. Config supplies the queue knobs and
// the queue-level hooks (OnClaim/OnReap/OnLeaseLost); the remaining fields are
// this layer's own.
// RunnerConfig 是 Runner 所需的全部参数。Config 提供队列参数与队列级钩子
// （OnClaim/OnReap/OnLeaseLost），其余字段是本层自有。
type RunnerConfig struct {
	Config

	// HandlerTimeout bounds one Handler call; zero means DefaultHandlerTimeout.
	// Keep it well below Lease so a slow handler finishes before its lease does.
	// HandlerTimeout 限制单次 Handler 耗时；零表示 DefaultHandlerTimeout。要远小于 Lease，
	// 保证慢 handler 在租约到期前完成。
	HandlerTimeout time.Duration

	// Handler is the only mandatory callback. A nil Handler is rejected by NewRunner.
	// Handler 是唯一必须提供的回调；为 nil 时 NewRunner 会拒绝。
	Handler Handler

	// OnSuccess fires after Succeed commits. OnFailure fires after Fail commits,
	// and also on lease loss (with Result.LeaseLost set) and on a handler error.
	// Both run on the calling goroutine, right after the database write, so they
	// can log or emit metrics without a second source of truth.
	// OnSuccess 在 Succeed 提交后触发；OnFailure 在 Fail 提交后触发，也会在租约丢失
	// （Result.LeaseLost 置位）与 handler 报错时触发。两者都在调用方 goroutine 上、紧跟写库
	// 之后执行，因此日志与指标不需要第二个真相来源。
	OnSuccess func(Result)
	OnFailure func(Result)
}

// Runner turns one claim round into one callback plus two outcome hooks.
// Runner 把一轮认领变成一个回调加两个结果钩子。
type Runner struct {
	queue          *Queue
	handler        Handler
	handlerTimeout time.Duration
	maxAttempts    int
	onSuccessHook  func(Result)
	onFailureHook  func(Result)
}

// NewRunner validates the config and builds the queue. The queue is built here,
// not once per process, because it holds this generation's *gorm.DB: a reload
// restarts the worker with a fresh pool and therefore a fresh Runner.
// NewRunner 校验配置并构建队列。队列在这里构建而不是进程级单例，因为它持有这一代的 *gorm.DB：
// 热更新后 worker 会用新连接池、也就是新的 Runner 重新启动。
func NewRunner(db *gorm.DB, cfg RunnerConfig) (*Runner, error) {
	if cfg.Handler == nil {
		return nil, ErrHandlerRequired
	}
	q, err := New(db, cfg.Config)
	if err != nil {
		return nil, err
	}
	if cfg.HandlerTimeout <= 0 {
		cfg.HandlerTimeout = DefaultHandlerTimeout
	}
	// Normalize nil hooks to no-ops once, so the hot path can call them without
	// a nil check on every outcome.
	// 把 nil 钩子一次性归一化为 no-op，热路径上就不必每次结果都判空。
	successHook, failureHook := cfg.OnSuccess, cfg.OnFailure
	if successHook == nil {
		successHook = func(Result) {}
	}
	if failureHook == nil {
		failureHook = func(Result) {}
	}
	return &Runner{
		queue:          q,
		handler:        cfg.Handler,
		handlerTimeout: cfg.HandlerTimeout,
		maxAttempts:    q.maxAttempts,
		onSuccessHook:  successHook,
		onFailureHook:  failureHook,
	}, nil
}

// Run performs one poll round: reap expired leases, claim a batch, then run the
// Handler over every claimed row and persist each outcome. Call it from a ticker;
// an empty result is normal and calls the Handler once with nil data.
//
// Errors from individual rows are joined and returned, so one failed bookkeeping
// write does not stop the rest of the batch. A lease loss is not returned as an
// error to the ticker — retrying it would repeat side effects — but is reported
// through OnFailure with LeaseLost set.
//
// Run 执行一轮轮询：回收过期租约、认领一批、对每条认领行运行 Handler 并持久化结果。请从 ticker
// 里调用它；空结果是常态，此时用 nil data 调一次 Handler。
//
// 单行错误会合并返回，因此一条收尾写失败不会拖住批次里其余任务。租约丢失不作为错误上抛给
// ticker——重试会重复副作用——而是通过 OnFailure 并置 LeaseLost 来上报。
func (r *Runner) Run(ctx context.Context, owner string) error {
	// Reap first: a row left by a crashed instance is invisible to Claim until
	// its lease expires and somebody collects it. Every replica reaping at once
	// is harmless — the UPDATE only touches rows whose lease really expired.
	// 先回收：崩溃实例留下的行在租约过期并被回收之前对 Claim 不可见。多个副本同时回收也无害，
	// UPDATE 只碰租约确已过期的行。
	_, _, reapErr := r.queue.Reap(ctx)

	tasks, err := r.queue.Claim(ctx, owner, 0)
	if err != nil {
		return errors.Join(reapErr, err)
	}
	if len(tasks) == 0 {
		// No due row: give the worker one callback with nil data for heartbeats,
		// ignore its return values, and still surface a reap failure if any.
		// 没有到期行：用 nil data 调一次回调供打点，忽略返回值；若回收失败仍上抛。
		_, _ = r.handler(ctx, nil)
		return reapErr
	}

	var errs []error
	if reapErr != nil {
		errs = append(errs, reapErr)
	}
	for _, task := range tasks {
		if err := r.process(ctx, task); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// process runs one handler under a timeout and records its outcome. The timeout
// applies only to the handler; Succeed/Fail use the caller's context so a slow
// business call cannot abort the very write that records its failure.
// process 在超时约束下运行一次 handler 并记录结果。超时只作用于 handler；Succeed/Fail 使用调用方
// 的 context，因此慢业务调用不会打断记录失败的那次写库。
func (r *Runner) process(ctx context.Context, task Task) error {
	started := time.Now()
	handleCtx, cancel := context.WithTimeout(ctx, r.handlerTimeout)
	taskID, handleErr := r.handler(handleCtx, task.Payload)
	cancel()

	res := Result{
		TaskID:   taskID,
		RowID:    task.ID,
		Job:      task.Job,
		Attempts: task.Attempts,
		Data:     task.Payload,
		Err:      handleErr,
		Elapsed:  time.Since(started),
	}

	if handleErr != nil {
		// Fail stores the reason and time, then retries or parks as dead. Pass the
		// Task through unchanged: owner + attempts are the fencing pair.
		// Fail 写原因与时间，然后重试或置 dead。Task 原样传下去：owner + attempts 是围栏。
		failErr := r.queue.Fail(ctx, task, handleErr)
		if errors.Is(failErr, ErrLeaseLost) {
			res.LeaseLost = true
			r.onFailureHook(res)
			return nil
		}
		if failErr != nil {
			return failErr
		}
		res.Dead = task.Attempts >= r.maxAttempts
		r.onFailureHook(res)
		return nil
	}

	// Handler succeeded: Succeed stamps finish_handle_time and clears fail_reason.
	// Handler 成功：Succeed 写 finish_handle_time 并清掉 fail_reason。
	if succErr := r.queue.Succeed(ctx, task); succErr != nil {
		if errors.Is(succErr, ErrLeaseLost) {
			res.LeaseLost = true
			r.onFailureHook(res)
			return nil
		}
		// The work ran but the row still says claimed. Returning the error leaves
		// repair to Reap, which is why a handler must be idempotent.
		// 活已经干了但行仍是 claimed。上抛错误把修复交给 Reap，这正是 handler 必须幂等的原因。
		return succErr
	}
	if r.onSuccessHook != nil {
		r.onSuccessHook(res)
	}
	return nil
}
