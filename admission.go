package gofy

import "sync"

// admissionGate is the process-wide door every HTTP request passes through
// before it may enter a generation. StopAccepting closes it; ResumeAccepting
// reopens it when a restart or exit turned out to be impossible.
//
// The gate check and the lease acquisition happen under one read lock. That is
// what turns "closed means no new request enters a handler" into a guarantee
// instead of a narrow race: close takes the write lock, so it waits for the
// readers already inside to finish their admission decision, and every reader
// after it observes a closed gate.
//
// A read lock per request is cheap when uncontended and scales with the number
// of cores, which is why the gate is not a plain mutex: the hot path stays
// parallel, and only the two lifecycle transitions serialize.
// admissionGate 是进程级入口，每个 HTTP 请求进入 generation 之前都要穿过它。
// StopAccepting 关闭它；restart / exit 失败时 ResumeAccepting 重新打开。
//
// 闸门检查与 lease 获取在同一把读锁内完成。这才让"关闭后不再有新请求进入 handler"
// 成为保证而不是窄竞态：close 取写锁，因此会等已经进入临界区的读者做完准入决定，
// 之后的每个读者都会看到已关闭。
//
// 每请求一把读锁在无竞争时开销很低、且随核数扩展，所以闸门没用普通互斥锁：热路径
// 保持并行，只有两次生命周期状态切换需要串行。
type admissionGate struct {
	mu     sync.RWMutex
	closed bool
}

// newAdmissionGate returns an open gate, which is the state a process starts in.
// newAdmissionGate 返回一个打开状态的闸门，这也是进程启动时的状态。
func newAdmissionGate() *admissionGate {
	return &admissionGate{}
}

// admitResult is the outcome of one admission attempt.
// admitResult 是一次准入尝试的结果。
type admitResult int

const (
	// admitted means the request owns a lease on the generation it loaded.
	// admitted 表示请求已持有它载入那一代的 lease。
	admitted admitResult = iota
	// admitRetired means that generation was retired between the load and the
	// acquire; the caller retries with the current one.
	// admitRetired 表示该代在载入与获取之间被退役，调用方改用当前代重试。
	admitRetired
	// admitClosed means the gate is closed: answer 503 without entering any
	// generation.
	// admitClosed 表示闸门已关闭：不进入任何代，直接返回 503。
	admitClosed
)

// admit checks the gate and acquires the lease as one step.
// admit 把闸门检查与 lease 获取合成一步。
func (g *admissionGate) admit(lease *generationLease) admitResult {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		return admitClosed
	}
	if !lease.acquire() {
		return admitRetired
	}
	return admitted
}

// close shuts the gate and reports whether this call changed the state, so the
// caller logs and deregisters exactly once. Requests that already passed admit
// keep their lease and finish.
// close 关闭闸门，并报告本次调用是否改变了状态，调用方因此只记一次日志、只注销
// 一次。已经通过 admit 的请求保留 lease 并跑完。
func (g *admissionGate) close() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.closed = true
	return true
}

// resume reopens the gate and reports whether this call changed the state.
// resume 重新打开闸门，并报告本次调用是否改变了状态。
func (g *admissionGate) resume() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		return false
	}
	g.closed = false
	return true
}

// isOpen reports the current state. It is for logging and for the startup-time
// registration check, never for admission — admitting through it would
// reintroduce the race close exists to remove.
// isOpen 报告当前状态。它只用于日志和启动期注册检查，绝不用于准入：用它做准入
// 等于把 close 好不容易消除的竞态又加回来。
func (g *admissionGate) isOpen() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return !g.closed
}
