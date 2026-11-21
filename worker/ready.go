package worker

import (
	"context"
	"sync"
)

// ReadyReporter is implemented by a Worker whose Run has to connect and
// subscribe before it can accept work: the four message consumers.
//
// The runner uses it to decide whether "Run was started" already means "this
// instance is ready". For a consumer it does not — the TCP connection, the
// queue declaration and the group join all happen inside Run — so the runner
// keeps the process from reporting ready until every instance of such a worker
// called Ready.
//
// A polling loop (Func, a Ticker) has nothing to wait for and must not
// implement this interface: it is ready as soon as Run starts.
// ReadyReporter 由"Run 内部要先建连与订阅才能接活"的 Worker 实现，即四个消息消费者。
//
// Runner 用它判断"Run 已经开始"是否等价于"实例已就绪"。对消费者来说不等价——TCP
// 连接、队列声明与消费组加入都发生在 Run 里——因此 runner 会等到这类 worker 的每个
// 实例都调用过 Ready 之后，才让进程对外报告就绪。
//
// 轮询任务（Func、Ticker）没有可等的东西，不应实现本接口：Run 一开始就算就绪。
type ReadyReporter interface {
	// ReportsReady marks the worker as one that signals readiness itself. It
	// carries no data; implementing the interface is the whole signal.
	// ReportsReady 标记该 worker 自行上报就绪。方法没有数据，实现这个接口本身就是
	// 全部信号。
	ReportsReady()
}

type readyKey struct{}

// readyGate collects the readiness signals of one Runner, one per instance.
//
// A gate with no pending instance is satisfied immediately, which is what keeps
// workers that do not report readiness on the old "ready when Run starts" path.
// readyGate 收集一个 Runner 内各实例的就绪信号，每个实例一次。
//
// 没有待上报实例时立刻满足，这正是不上报就绪的 worker 保持"Run 开始即就绪"的方式。
type readyGate struct {
	mu      sync.Mutex
	pending map[Identity]struct{}
	closed  bool
	done    chan struct{}
	// onChange reports a snapshot of the pending set after it changes. The
	// runner turns it into an instances.ready gauge; nil keeps standalone gates
	// free of metrics wiring. It is called with the mutex held so snapshots are
	// published in the same order as the state changes they describe.
	// onChange 在待上报集合变化后给出快照。runner 用它更新 instances.ready gauge；
	// nil 让独立使用的闸门无需接指标。调用时持有互斥锁，保证快照的发布顺序与
	// 状态变化顺序一致。
	onChange func([]Identity)
}

func newReadyGate() *readyGate {
	return &readyGate{pending: make(map[Identity]struct{}), done: make(chan struct{})}
}

// add registers the instances that will each signal once. Registering none
// satisfies the gate immediately, which is how a group of polling workers stays
// "ready when Run starts".
// add 登记将各上报一次的实例。没有实例时立刻满足闸门，全由轮询任务组成的分组因此保持
// "Run 开始即就绪"。
func (g *readyGate) add(ids []Identity) {
	g.mu.Lock()
	for _, id := range ids {
		g.pending[id] = struct{}{}
	}
	closeNow := len(g.pending) == 0 && !g.closed
	if closeNow {
		g.closed = true
	}
	// Publish while holding the lock: two concurrent signals must not let a
	// stale snapshot overwrite the final count after the gate closed.
	// 持锁发布：两个并发上报的快照若乱序落盘，旧值会在闸门关闭后覆盖最终计数。
	g.publishLocked()
	g.mu.Unlock()
	if closeNow {
		close(g.done)
	}
}

// publishLocked reports the pending set to the runner's gauge. The caller must
// hold g.mu, and onChange must not call back into the gate; the runner's only
// callback records metrics.
// publishLocked 把待上报集合报给 runner 的 gauge。调用方必须持有 g.mu，且 onChange
// 不得回调闸门；runner 的回调只记录指标。
func (g *readyGate) publishLocked() {
	if g.onChange == nil {
		return
	}
	pending := make([]Identity, 0, len(g.pending))
	for id := range g.pending {
		pending = append(pending, id)
	}
	g.onChange(pending)
}

// signal consumes one pending instance. Signals are keyed by instance, so a
// worker that reports twice — the adapter reconnecting inside one Run, or the
// runner restarting the same instance after an error — does not consume a
// sibling's slot. Unknown keys are ignored, which makes a late signal from a
// superseded run harmless.
// signal 消耗一个待上报实例。信号按实例记账，因此重复上报的 worker——适配器在一次 Run
// 内重连，或 runner 在错误后重启同一实例——不会消耗兄弟实例的名额。未知的键直接忽略，
// 已被取代的那次 Run 迟到的信号因此无害。
func (g *readyGate) signal(id Identity) {
	g.mu.Lock()
	_, ok := g.pending[id]
	if ok {
		delete(g.pending, id)
	}
	closeNow := ok && len(g.pending) == 0 && !g.closed
	if closeNow {
		g.closed = true
	}
	if ok {
		g.publishLocked()
	}
	g.mu.Unlock()
	if closeNow {
		close(g.done)
	}
}

// wait returns the channel closed when every registered instance reported.
// wait 返回在所有登记实例都上报后关闭的通道。
func (g *readyGate) wait() <-chan struct{} { return g.done }

// withReadyGate attaches the runner's gate to ctx. The gate type stays
// unexported because only Runner creates one: a host that needs its own
// readiness wiring uses the exported Ready hook through Runner.
// withReadyGate 把 runner 的闸门附加到 ctx。闸门类型不导出，因为它只由 Runner
// 创建：需要自定义就绪链路的宿主通过 Runner 使用导出的 Ready。
func withReadyGate(ctx context.Context, gate *readyGate) context.Context {
	return context.WithValue(ctx, readyKey{}, gate)
}

// Ready reports that the calling instance can now accept work. It must be
// called once per instance, after the broker connection and the subscription
// are in place and before the consume loop starts.
//
// The instance comes from the identity the runner injected into ctx, so a second
// call from the same instance is a no-op. It is also a no-op when the context
// carries no gate or no identity, which is what lets a consumer call it
// unconditionally — tests and hand-built runners are not forced to install one.
// Ready 上报当前实例已经可以接活。每个实例调用一次，时机是 broker 连接与订阅都已就绪、
// 消费循环开始之前。
//
// 实例来自 runner 注入 ctx 的身份，因此同一实例第二次调用是空操作。ctx 没有携带闸门或
// 身份时同样是空操作，消费者因此可以无条件调用——测试与自建 runner 不必特意安装闸门。
func Ready(ctx context.Context) {
	if ctx == nil {
		return
	}
	gate, ok := ctx.Value(readyKey{}).(*readyGate)
	if !ok || gate == nil {
		return
	}
	id, ok := IdentityFrom(ctx)
	if !ok {
		return
	}
	gate.signal(id)
}
