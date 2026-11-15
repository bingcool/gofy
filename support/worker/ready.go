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

// readyGate collects the readiness signals of one Runner. A gate with no
// pending instance is satisfied immediately, which is what keeps workers that
// do not report readiness on the old "ready when Run starts" path.
// readyGate 收集一个 Runner 内各实例的就绪信号。没有待上报实例时立刻满足，这正是
// 不上报就绪的 worker 保持"Run 开始即就绪"的方式。
type readyGate struct {
	mu      sync.Mutex
	pending int
	closed  bool
	done    chan struct{}
}

func newReadyGate() *readyGate {
	return &readyGate{done: make(chan struct{})}
}

// add registers n instances that will each signal once. Registering zero
// instances satisfies the gate immediately, which is how a group of polling
// workers stays "ready when Run starts".
// add 登记 n 个将各上报一次的实例。登记 0 个实例会立刻满足闸门，全由轮询任务组成的
// 分组因此保持"Run 开始即就绪"。
func (g *readyGate) add(n int) {
	g.mu.Lock()
	if n > 0 {
		g.pending += n
	}
	closeNow := g.pending == 0 && !g.closed
	if closeNow {
		g.closed = true
	}
	g.mu.Unlock()
	if closeNow {
		close(g.done)
	}
}

// signal consumes one pending instance. Extra signals are ignored: a worker
// that reports twice must not close the gate for its siblings.
// signal 消耗一个待上报实例。多余的信号被忽略：重复上报的 worker 不能替同组其他
// 实例把闸门关掉。
func (g *readyGate) signal() {
	g.mu.Lock()
	if g.pending > 0 {
		g.pending--
	}
	closeNow := g.pending == 0 && !g.closed
	if closeNow {
		g.closed = true
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
// called once, after the broker connection and the subscription are in place
// and before the consume loop starts.
//
// It is a no-op when the context carries no gate, so a consumer can call it
// unconditionally — tests and hand-built runners are not forced to install one.
// Ready 上报当前实例已经可以接活。它只调用一次，时机是 broker 连接与订阅都已就绪、
// 消费循环开始之前。
//
// ctx 没有携带闸门时它是空操作，因此消费者可以无条件调用——测试与自建 runner 不必
// 特意安装闸门。
func Ready(ctx context.Context) {
	if ctx == nil {
		return
	}
	gate, ok := ctx.Value(readyKey{}).(*readyGate)
	if !ok || gate == nil {
		return
	}
	gate.signal()
}
