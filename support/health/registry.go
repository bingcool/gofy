package health

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Entry is the read-only view of a registered check. The host uses it to log
// what a composition actually installed, so a misplaced readiness check is
// visible in the startup log instead of only in an incident.
// Entry 是已注册检查的只读视图。宿主用它记录本次装配实际安装了什么，误放进 readiness
// 的检查因此会出现在启动日志里，而不是只在故障时才被发现。
type Entry struct {
	// Name is the registered check name.
	// Name 是注册的检查名。
	Name string
	// Class is readiness or diagnostic.
	// Class 是 readiness 或 diagnostic。
	Class Class
	// Timeout is the effective per-check timeout after defaults were applied.
	// Timeout 是应用默认值之后生效的单检查超时。
	Timeout time.Duration
}

type entry struct {
	name    string
	class   Class
	timeout time.Duration
	checker Checker
}

// round is one execution shared by every probe that asks for the same class
// while it runs. Waiting on done is how the probes share one result.
// round 是一次执行，被同一类别下同时到达的所有探针共享。等待 done 就是它们共享同一份
// 结果的方式。
type round struct {
	done chan struct{}
	snap Snapshot
}

type slot struct {
	snap     Snapshot
	at       time.Time
	valid    bool
	inflight *round
	// gen counts invalidations. A round captures it when it starts and only
	// publishes its snapshot while the value still matches, so a round that was
	// already running when the caller asked for a fresh answer cannot write the
	// old state back into the cache.
	// gen 记录失效次数。轮次在开始时捕获它，只有在值仍然相同时才发布快照；因此在调用方
	// 要求新答案时已经开始的那一轮，无法把旧状态写回缓存。
	gen uint64
}

// Registry holds the checks of one process. It is safe for concurrent use: probe
// handlers call Ready and Diagnose while a reload may register new checks.
// Registry 持有本进程的检查集合。它是并发安全的：探针处理器调用 Ready 与 Diagnose
// 的同时，Reload 可能注册新的检查。
type Registry struct {
	opts      Options
	startedAt time.Time

	// now is time.Now except in tests, which need to advance the clock without
	// sleeping through the TTLs.
	// now 就是 time.Now，只有测试会替换它，以便不睡过 TTL 也能推进时钟。
	now func() time.Time

	mu      sync.Mutex
	entries []*entry
	slots   [2]slot
}

// New builds an empty registry. Checks are added with Register; an empty
// registry reports ready, because a process with nothing to check is not known
// to be broken.
// New 构造空注册表。检查通过 Register 加入；空注册表报告就绪，因为没有任何检查的进程
// 并未被发现有问题。
func New(opts Options) *Registry {
	return &Registry{
		opts:      opts.withDefaults(),
		startedAt: time.Now(),
		now:       time.Now,
	}
}

// Options returns the effective options after defaults were applied.
// Options 返回应用默认值之后生效的配置。
func (r *Registry) Options() Options { return r.opts }

// Register adds a check. A nil checker, an empty name, an unknown class, a
// negative timeout or a duplicate name is rejected: each of them would either
// panic later or silently shadow a check that is already answering.
// Register 加入一个检查。nil 检查器、空名字、未知类别、负超时与重名都会被拒绝：它们
// 要么在之后 panic，要么静默遮蔽已在应答的检查。
func (r *Registry) Register(c Checker, opts ...CheckOption) error {
	if c == nil {
		return errors.New("health: nil checker")
	}
	name := strings.TrimSpace(c.Name())
	if name == "" {
		return errors.New("health: checker name is empty")
	}

	var co checkOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&co)
		}
	}

	class := co.class
	if class == "" {
		class = ClassReadiness
	}
	if class != ClassReadiness && class != ClassDiagnostic {
		return fmt.Errorf("health: unknown class %q for check %q", class, name)
	}

	timeout := co.timeout
	if timeout == 0 {
		if tp, ok := c.(TimeoutProvider); ok {
			timeout = tp.CheckTimeout()
		}
	}
	// Checked after the provider lookup so a checker that declares a negative
	// timeout is rejected too instead of silently running under the default.
	// 在查询 TimeoutProvider 之后再判断：声明负超时的检查器同样被拒绝，而不是静默按默认值运行。
	if timeout < 0 {
		return fmt.Errorf("health: negative timeout for check %q", name)
	}
	if timeout == 0 {
		timeout = r.opts.Timeout
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.name == name {
			return fmt.Errorf("health: duplicate check %q", name)
		}
	}
	r.entries = append(r.entries, &entry{name: name, class: class, timeout: timeout, checker: c})
	r.invalidateLocked()
	return nil
}

// Entries returns a copy of the registered checks, in registration order.
// Entries 按注册顺序返回已注册检查的副本。
func (r *Registry) Entries() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.entries))
	for i, e := range r.entries {
		out[i] = Entry{Name: e.name, Class: e.class, Timeout: e.timeout}
	}
	return out
}

// Invalidate drops both cached snapshots and detaches any round that is still
// running. The worker host calls it when the group ready flag flips, so the
// first probe after readiness does not answer from the 503 cached before it —
// and does not join a round that started before the flip either. A reload that
// replaces the pool builds a new registry instead, so its checks never see the
// retired connection.
// Invalidate 丢弃两个类别的缓存快照，并摘掉仍在运行的轮次。worker 宿主在分组就绪标志
// 翻起时调用它，就绪之后的第一次探测因此不会用之前缓存下来的 503 作答，也不会加入在
// 翻转之前就开始的那一轮。替换连接池的 Reload 会重建注册表，它的检查因此不会看到已退役
// 的连接。
func (r *Registry) Invalidate() {
	r.mu.Lock()
	r.invalidateLocked()
	r.mu.Unlock()
}

func (r *Registry) invalidateLocked() {
	for i := range r.slots {
		s := &r.slots[i]
		s.valid = false
		s.at = time.Time{}
		// Detaching the in-flight round matters as much as dropping the cached
		// snapshot: a probe that joined it would still wait for an answer that was
		// decided before the invalidation. The detached round keeps its own
		// waiters and finishes normally; the next probe starts a new one.
		// 摘掉在途轮次与丢弃缓存快照同样重要：加入它的探针仍会等到一个在失效之前就已
		// 决定的答案。被摘掉的轮次保留自己的等待者并正常结束，下一个探针会开启新一轮。
		s.inflight = nil
		s.gen++
	}
}

// Ready returns the readiness snapshot, running one round when the cache
// expired. The returned Ready bit is fail-closed: an unknown readiness check
// counts as not ready.
// Ready 返回 readiness 快照，缓存过期时执行一轮。返回的 Ready 位是 fail-closed 的：
// 未知的 readiness 检查按未就绪计。
func (r *Registry) Ready(ctx context.Context) Snapshot { return r.snapshot(ctx, ClassReadiness) }

// Diagnose returns the diagnostic snapshot, which runs readiness checks too: an
// operator reading /health needs the traffic decision and the detail together.
// Diagnose 返回诊断快照，它也会执行 readiness 检查：读 /health 的运维需要同时看到接活
// 结论与明细。
func (r *Registry) Diagnose(ctx context.Context) Snapshot {
	return r.snapshot(ctx, ClassDiagnostic)
}

func (r *Registry) snapshot(ctx context.Context, c Class) Snapshot {
	idx := classIndex(c)
	ttl := r.ttl(c)

	r.mu.Lock()
	s := &r.slots[idx]
	if ttl > 0 && s.valid && r.now().Sub(s.at) < ttl {
		snap := s.snap
		r.mu.Unlock()
		return snap
	}
	if s.inflight != nil {
		rt := s.inflight
		r.mu.Unlock()
		return r.await(ctx, rt, c)
	}
	rt := &round{done: make(chan struct{})}
	s.inflight = rt
	gen := s.gen
	r.mu.Unlock()

	// The round runs in its own goroutine on purpose: a probe that times out and
	// gives up must not cancel checks that are already in flight, and the result
	// still has to be cached for the probes that follow.
	// 本轮刻意跑在自己的 goroutine 里：超时放弃的探针不能取消已经发出的检查，结果也必须
	// 仍然为之后的探针写入缓存。
	go r.execute(c, rt, gen)
	return r.await(ctx, rt, c)
}

func (r *Registry) await(ctx context.Context, rt *round, c Class) Snapshot {
	select {
	case <-rt.done:
		return rt.snap
	case <-ctx.Done():
		return Snapshot{
			Class:     c,
			Ready:     false,
			CheckedAt: r.now(),
			Reason:    ReasonCanceled,
			Results:   []Result{},
		}
	}
}

func (r *Registry) execute(c Class, rt *round, gen uint64) {
	snap := r.run(c)

	idx := classIndex(c)
	r.mu.Lock()
	s := &r.slots[idx]
	// Publish only if no invalidation happened while the checks ran. A stale
	// round must not mark the slot valid again (that is what kept /readyz on the
	// pre-ready answer for a whole TTL), and it must not detach the newer round
	// that replaced it.
	// 只有在检查运行期间没有发生失效时才发布。过期轮次不能把槽位重新标记为有效
	// （正是这一点让 /readyz 在整个 TTL 内继续返回就绪之前的答案），也不能把取代它的
	// 新一轮从 inflight 上摘掉。
	if s.gen == gen {
		s.snap = snap
		s.at = snap.CheckedAt
		s.valid = true
		if s.inflight == rt {
			s.inflight = nil
		}
	}
	r.mu.Unlock()

	// snap is written before done is closed, so every goroutine that returns from
	// <-rt.done reads a complete snapshot.
	// snap 在 done 关闭之前写入，因此任何从 <-rt.done 返回的 goroutine 读到的都是完整快照。
	rt.snap = snap
	close(rt.done)
}

func (r *Registry) run(c Class) Snapshot {
	started := r.now()

	if c == ClassReadiness && r.opts.StartupGrace > 0 && started.Sub(r.startedAt) < r.opts.StartupGrace {
		return Snapshot{Class: c, Ready: true, CheckedAt: started, Reason: "grace", Results: []Result{}}
	}

	entries := r.entriesFor(c)
	if len(entries) == 0 {
		return Snapshot{Class: c, Ready: true, CheckedAt: started, Results: []Result{}}
	}

	// The budget is detached from the caller's context: the round bounds itself,
	// and a probe that gives up only stops waiting, as documented in §3.3.
	// 预算与调用方的 context 分离：本轮自行设界，放弃的探针只是不再等待（设计文档 §3.3）。
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.TotalTimeout)
	defer cancel()

	results := make([]Result, len(entries))
	sem := make(chan struct{}, r.opts.Concurrency)
	var wg sync.WaitGroup
	for i := range entries {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = r.checkOne(ctx, entries[i], sem)
		}(i)
	}
	wg.Wait()

	ready := true
	for _, res := range results {
		if res.Class != ClassReadiness {
			continue
		}
		if res.Status != StatusHealthy {
			ready = false
			break
		}
	}
	return Snapshot{Class: c, Ready: ready, CheckedAt: started, Results: results}
}

func (r *Registry) entriesFor(c Class) []*entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*entry, 0, len(r.entries))
	for _, e := range r.entries {
		if c == ClassDiagnostic || e.class == ClassReadiness {
			out = append(out, e)
		}
	}
	return out
}

func (r *Registry) checkOne(ctx context.Context, e *entry, sem chan struct{}) Result {
	res := Result{Name: e.name, Class: e.class, CheckedAt: r.now()}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return r.degraded(e, ReasonBudget, "round budget exhausted before the check started", res)
	}

	cctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	start := r.now()
	res.CheckedAt = start
	err := safeCheck(cctx, e)
	res.Latency = r.now().Sub(start)

	if err == nil {
		res.Status = StatusHealthy
		return res
	}

	var panicked *panicError
	if errors.As(err, &panicked) {
		return r.degraded(e, ReasonPanic, err.Error(), res)
	}
	if ctx.Err() != nil {
		return r.degraded(e, ReasonBudget, "round budget exhausted while the check was running", res)
	}
	if cctx.Err() != nil {
		res.Reason = ReasonTimeout
	}
	res.Error = err.Error()
	if e.class == ClassDiagnostic {
		res.Status = StatusUnknown
		return res
	}
	res.Status = StatusFailed
	return res
}

// degraded maps a check that produced no answer onto the class: readiness fails
// closed, diagnostic reports unknown and never pulls traffic.
// degraded 把"没有给出结论"的检查按类别映射：readiness fail-closed，diagnostic 记
// unknown 且绝不摘流。
func (r *Registry) degraded(e *entry, reason, msg string, res Result) Result {
	res.Reason = reason
	res.Error = msg
	if e.class == ClassReadiness {
		res.Status = StatusFailed
		return res
	}
	res.Status = StatusUnknown
	return res
}

func (r *Registry) ttl(c Class) time.Duration {
	if classIndex(c) == 1 {
		return r.opts.DiagnosticTTL
	}
	return r.opts.ReadinessTTL
}

func classIndex(c Class) int {
	if c == ClassDiagnostic {
		return 1
	}
	return 0
}

// panicError marks a recovered panic so checkOne can report ReasonPanic instead
// of a plain failure.
// panicError 标记被恢复的 panic，让 checkOne 报告 ReasonPanic 而不是普通失败。
type panicError struct {
	name  string
	value any
}

func (e *panicError) Error() string {
	return fmt.Sprintf("%s: panic: %v", e.name, e.value)
}

func safeCheck(ctx context.Context, e *entry) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &panicError{name: e.name, value: v}
		}
	}()
	return e.checker.Check(ctx)
}
