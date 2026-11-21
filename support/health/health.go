// Package health provides the check registry behind a gofy process's liveness,
// readiness and diagnostic endpoints.
//
// The registry answers one question per class: can this process serve traffic
// (readiness), and is each dependency reachable right now (diagnostic). It runs
// checks with a per-check timeout, a round budget, a concurrency cap and a TTL
// cache, so a slow dependency cannot turn the probe itself into an outage.
//
// 包 health 提供 gofy 进程 liveness / readiness / diagnostic 端点背后的检查注册表。
//
// 注册表按类别回答两个问题：本进程现在能否接活（readiness），以及各依赖当前是否可达
// （diagnostic）。它用单检查超时、整轮预算、并发上限与 TTL 缓存执行检查，因此一个慢依赖
// 不会把探针本身变成故障源。
package health

import (
	"context"
	"time"
)

// Defaults for Options. The numbers are confirmed in the design document: a
// check slower than two seconds no longer describes "available now", and the
// round budget has to stay below the five seconds a probe usually allows.
// Options 的默认值。设计文档已确认这些数字：超过两秒的检查已不能描述"当前可用"，
// 整轮预算必须小于探针通常允许的五秒。
const (
	DefaultTimeout       = 2 * time.Second
	DefaultTotalTimeout  = 3 * time.Second
	DefaultConcurrency   = 4
	DefaultReadinessTTL  = time.Second
	DefaultDiagnosticTTL = 2 * time.Second
)

// Class separates checks that may remove this process from traffic from checks
// that only fill in the diagnostic detail.
// Class 区分"可能把本进程摘出流量"的检查与只补诊断明细的检查。
type Class string

const (
	// ClassReadiness failures make /readyz answer 503: the process should not
	// receive new work.
	// ClassReadiness 的失败让 /readyz 返回 503：本进程不应再接收新任务。
	ClassReadiness Class = "readiness"
	// ClassDiagnostic failures are reported on /health only. They never pull the
	// process out of traffic.
	// ClassDiagnostic 的失败只上报到 /health，绝不摘流。
	ClassDiagnostic Class = "diagnostic"
)

// Status is the outcome of one check in a snapshot.
// Status 是一个检查在快照中的结果。
type Status string

const (
	// StatusHealthy means the check returned nil.
	// StatusHealthy 表示检查返回 nil。
	StatusHealthy Status = "healthy"
	// StatusFailed means the check returned an error and the class is readiness:
	// the process is not fit to take new work.
	// StatusFailed 表示检查返回错误且类别为 readiness：本进程不适合接收新任务。
	StatusFailed Status = "failed"
	// StatusUnknown means the check produced no answer: the round budget ran out,
	// the probe waiting on the snapshot gave up, or a diagnostic check panicked.
	// StatusUnknown 表示检查没有给出结论：整轮预算耗尽、等待快照的探针放弃，或诊断类
	// 检查 panic。
	StatusUnknown Status = "unknown"
)

// Reasons attached to Result.Reason. They are fixed strings so dashboards can
// key on them; the error text stays free-form.
// 附在 Result.Reason 上的原因。它们是固定字符串，看板可以据此取值；错误文本保持自由格式。
const (
	ReasonTimeout  = "timeout"
	ReasonPanic    = "panic"
	ReasonBudget   = "budget"
	ReasonCanceled = "canceled"
)

// Checker is one dependency probe.
// Checker 是一个依赖探测。
type Checker interface {
	// Name identifies the check in the snapshot and on /health. It must be
	// unique inside a registry and stable across reloads, because dashboards and
	// alerts key on it.
	// Name 在快照与 /health 上标识该检查。它在同一个注册表内必须唯一，并在 Reload
	// 之间保持稳定，因为看板与告警都以它为准。
	Name() string
	// Check reports nil when the dependency is usable now.
	// Check 在依赖当前可用时返回 nil。
	Check(ctx context.Context) error
}

// CheckFunc adapts a function to Checker.
// CheckFunc 把函数适配成 Checker。
type CheckFunc func(ctx context.Context) error

// Named wraps fn as a Checker called name.
// Named 把 fn 包装成名为 name 的 Checker。
func Named(name string, fn CheckFunc) Checker { return &funcChecker{name: name, fn: fn} }

type funcChecker struct {
	name string
	fn   CheckFunc
}

func (c *funcChecker) Name() string { return c.name }

func (c *funcChecker) Check(ctx context.Context) error {
	if c.fn == nil {
		return nil
	}
	return c.fn(ctx)
}

// TimeoutProvider lets a built-in checker declare its own default timeout. The
// database, HTTP and Redis checkers use one second: a connection pool answered
// within a second or it did not answer.
// TimeoutProvider 让内置检查器声明自己的默认超时。数据库、HTTP 与 Redis 检查器都用
// 一秒：连接池要么一秒内应答，要么根本没应答。
type TimeoutProvider interface {
	CheckTimeout() time.Duration
}

// Options configures a Registry. Zero values fall back to the defaults above.
// Options 配置 Registry。零值回退到上面的默认值。
type Options struct {
	// Timeout bounds one check when neither the checker nor its registration
	// declares a timeout.
	// Timeout 在检查器与其注册都没有声明超时时约束单个检查。
	Timeout time.Duration

	// TotalTimeout bounds one round of checks. When it expires, checks that did
	// not finish are reported with ReasonBudget instead of waiting forever.
	// TotalTimeout 约束一整轮检查。它到期时，未完成的检查标注 ReasonBudget，而不是
	// 无限等待。
	TotalTimeout time.Duration

	// Concurrency caps how many checks run at once. The default of four keeps a
	// twenty-dependency service from opening twenty connections at once.
	// Concurrency 限制同时执行的检查数。默认值四避免一个有二十个依赖的服务同时打开
	// 二十个连接。
	Concurrency int

	// ReadinessTTL caches the readiness snapshot for this long, merging the
	// repeated probes of one replica into one round. A negative value disables
	// the cache; zero means the default.
	// ReadinessTTL 缓存 readiness 快照，把同一副本的重复探测合并成一轮。负值关闭缓存，
	// 零值表示默认值。
	ReadinessTTL time.Duration

	// DiagnosticTTL caches the diagnostic snapshot; detail may be slightly older
	// than the readiness answer. A negative value disables the cache.
	// DiagnosticTTL 缓存诊断快照；明细可以比 readiness 结论略旧。负值关闭缓存。
	DiagnosticTTL time.Duration

	// StartupGrace keeps readiness true for this long after the registry is
	// created. A dependency that connects asynchronously would otherwise make
	// every replica fail readiness at the same moment during a rollout.
	// StartupGrace 在注册表创建后的这段时间内保持 readiness 为真。否则一个异步建连的
	// 依赖会让每次滚动发布时所有副本在同一时刻 readiness 失败。
	StartupGrace time.Duration
}

// withDefaults fills every zero field. Negative values keep their intent (no
// cache) and are clamped where a negative would be meaningless.
// withDefaults 填充所有零值字段。负值保留其意图（不缓存），并在负值无意义处收敛。
func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.TotalTimeout <= 0 {
		o.TotalTimeout = DefaultTotalTimeout
	}
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	if o.ReadinessTTL < 0 {
		o.ReadinessTTL = 0
	} else if o.ReadinessTTL == 0 {
		o.ReadinessTTL = DefaultReadinessTTL
	}
	if o.DiagnosticTTL < 0 {
		o.DiagnosticTTL = 0
	} else if o.DiagnosticTTL == 0 {
		o.DiagnosticTTL = DefaultDiagnosticTTL
	}
	if o.StartupGrace < 0 {
		o.StartupGrace = 0
	}
	return o
}

// Result is one check's outcome inside a Snapshot.
// Result 是一个检查在 Snapshot 中的结果。
type Result struct {
	// Name is Checker.Name() at registration time.
	// Name 是注册时的 Checker.Name()。
	Name string
	// Class is the class the check was registered with.
	// Class 是检查注册时的类别。
	Class Class
	// Status is healthy, failed or unknown.
	// Status 是 healthy、failed 或 unknown。
	Status Status
	// Error carries the error text, empty when the check passed.
	// Error 携带错误文本，检查通过时为空。
	Error string
	// Reason is a fixed string from the Reason* constants for degraded results:
	// timeout, panic, budget or canceled.
	// Reason 是降级结果的固定原因串（timeout、panic、budget、canceled）。
	Reason string
	// Latency is how long the check function itself ran, measured from the
	// moment it got a concurrency slot. Queueing for that slot is the registry's
	// own cost, so it is deliberately excluded: latency_ms answers "is the
	// dependency slow", not "is the probe listener busy".
	// Latency 是检查函数自身的耗时，从拿到并发槽位开始计时。排队等槽位是注册表自身的
	// 开销，因此刻意不计入：latency_ms 回答的是"依赖是否慢"，而不是"探针 listener 是否
	// 繁忙"。
	Latency time.Duration
	// CheckedAt is when the check started.
	// CheckedAt 是检查开始的时间。
	CheckedAt time.Time
}

// Snapshot is what /readyz and /health render. Ready is the single bit a
// readiness probe needs; Results is the detail an operator reads on /health.
// Snapshot 是 /readyz 与 /health 渲染的内容。Ready 是 readiness 探针需要的单个位，
// Results 是运维在 /health 上读的明细。
type Snapshot struct {
	// Class is readiness or diagnostic.
	// Class 是 readiness 或 diagnostic。
	Class Class
	// Ready reports whether every readiness check passed. A snapshot of the
	// diagnostic class sets it from the readiness checks it ran, so a caller that
	// only asks for detail still sees the traffic decision.
	// Ready 报告所有 readiness 检查是否通过。诊断类快照用它执行过的 readiness 检查
	// 计算该位，因此只要明细的调用方也能看到接活结论。
	Ready bool
	// CheckedAt is when the round started.
	// CheckedAt 是本轮开始的时间。
	CheckedAt time.Time
	// Reason explains a Ready value that was not computed from check results:
	// "grace" during StartupGrace, "canceled" when the waiting probe gave up.
	// Reason 解释不是由检查结果直接得出的 Ready 值："grace"（启动宽限期）、
	// "canceled"（等待的探针放弃）。
	Reason string
	// Results holds one entry per executed check, ordered as registered.
	// Results 按注册顺序保存每个已执行检查的结果。
	Results []Result
}

// Failed returns the names of readiness checks that did not pass, in result
// order. It is the list an error message or a log line needs.
// Failed 按结果顺序返回未通过的 readiness 检查名，供错误信息或日志直接使用。
func (s Snapshot) Failed() []string {
	names := make([]string, 0, len(s.Results))
	for _, r := range s.Results {
		if r.Class != ClassReadiness {
			continue
		}
		if r.Status != StatusHealthy {
			names = append(names, r.Name)
		}
	}
	return names
}

// CheckOptions customise a single registration.
// CheckOptions 定制单次注册。
type checkOptions struct {
	class   Class
	timeout time.Duration
	weight  int
}

// CheckOption customises a single registration.
// CheckOption 定制单次注册。
type CheckOption func(*checkOptions)

// WithClass puts the check in the given class. Readiness is the default: a
// registered check is a statement that the dependency matters, and a check that
// silently does nothing is harder to notice than an unexpected drain.
// WithClass 把检查放进指定类别。默认是 readiness：注册检查本身就是"这个依赖重要"的
// 表态，而一个静默不生效的检查比一次意外的摘流更难发现。
func WithClass(c Class) CheckOption {
	return func(o *checkOptions) {
		if c != "" {
			o.class = c
		}
	}
}

// WithCheckTimeout overrides the per-check timeout for this registration.
//
// A negative value is recorded rather than ignored so Register can reject it:
// silently falling back to the default would leave the caller believing the
// check runs under the budget it asked for.
// WithCheckTimeout 覆盖本次注册的单检查超时。
//
// 负值会被记录而不是被忽略，让 Register 能拒绝它：静默回退到默认值会让调用方以为
// 检查运行在自己要求的预算下。
func WithCheckTimeout(d time.Duration) CheckOption {
	return func(o *checkOptions) {
		if d != 0 {
			o.timeout = d
		}
	}
}

// WithWeight is reserved for future weighted aggregation; it is recorded but
// currently unused. It exists so callers can prepare without a breaking change.
// WithWeight 预留给未来的加权聚合；当前只记录不使用，让调用方无需在将来做破坏性改动。
func WithWeight(w int) CheckOption {
	return func(o *checkOptions) {
		o.weight = w
	}
}
