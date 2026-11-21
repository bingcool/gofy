package worker

import (
	"context"
	"fmt"
)

// Identity describes one worker instance. The runner injects it into the
// context, and it is also the natural owner value for a claimed task row.
// Identity 描述一个 worker 实例。Runner 把它注入 context，它同时也可直接用作
// 认领式任务行的 owner 值。
type Identity struct {
	// Group is the group name.
	// Group 是分组名。
	Group string
	// Name is the worker name.
	// Name 是 worker 名。
	Name string
	// Index is the 0-based instance index; it is always 0 when the worker runs
	// with concurrency 1.
	// Index 是从 0 开始的实例序号；并发为 1 时恒为 0。
	Index int
	// Total is how many instances of this worker run in the group.
	// Total 是该 worker 在本分组内的实例总数。
	Total int
	// Host is the hostname, so an owner value can be traced back to a pod.
	// Host 是主机名，便于把 owner 值追溯到具体 Pod。
	Host string
}

// String renders the identity as group/name#index, which is what logs and
// metrics labels use.
// String 输出 group/name#index，日志与指标标签都用这个形式。
func (i Identity) String() string {
	return fmt.Sprintf("%s/%s#%d", i.Group, i.Name, i.Index)
}

// Owner renders the identity as a claim owner: group/name#index@host when the
// hostname is known, and the same value as String otherwise.
//
// Use it for claim.Queue.Claim instead of String: two pods running the same
// group, worker and instance index are different instances, and their owner
// values must differ so an operator can tell them apart in the task table.
//
// Owner identifies the instance, not the claim: it does not change when the
// same instance claims the same row again after a reap. That is why claim
// fences completions with attempts as well — see claim.Task.
// Owner 把身份渲染成认领用的 owner：知道主机名时是 group/name#index@host，否则与
// String 相同。
//
// 调用 claim.Queue.Claim 时应当用它而不是 String：两个 Pod 跑同样的分组、worker
// 与实例序号时仍是不同实例，owner 值必须不同，运维才能在任务表里区分它们。
//
// Owner 标识的是实例，不是某一次认领：同一实例在 Reap 之后重新认领同一行时，它的值
// 不变。这正是 claim 的收尾还要用 attempts 做围栏的原因——见 claim.Task。
func (i Identity) Owner() string {
	if i.Host == "" {
		return i.String()
	}
	return i.String() + "@" + i.Host
}

type identityKey struct{}
type drainKey struct{}

// WithIdentity attaches an identity to ctx. It is exported for hosts that
// implement their own runner; applications normally do not call it.
// WithIdentity 把实例身份附加到 ctx。该方法导出给自行实现 Runner 的宿主使用，
// 业务代码通常不需要调用。
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity injected by the runner.
// IdentityFrom 返回 Runner 注入的实例身份。
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// WithDrainContext attaches the drain context: it survives the cancellation
// that stops new work, so in-flight handlers can finish within a budget.
// WithDrainContext 附加 drain context：它不随"停止拉取"的取消而失效，在途
// 处理因此能在预算内完成。
func WithDrainContext(ctx context.Context, drain context.Context) context.Context {
	if drain == nil {
		return ctx
	}
	return context.WithValue(ctx, drainKey{}, drain)
}

// DrainContext returns the drain context, or ctx when the host injected none.
// Consumer adapters must use it for handler calls instead of the per-delivery
// context, otherwise a shutdown aborts in-flight work that could have finished.
// DrainContext 返回 drain context；宿主未注入时返回 ctx 本身。
// 消费者适配器调用 handler 时必须用它，而不是每次投递自带的 context，否则停机
// 会中断本可完成的在途处理。
func DrainContext(ctx context.Context) context.Context {
	if drain, ok := ctx.Value(drainKey{}).(context.Context); ok && drain != nil {
		return drain
	}
	return ctx
}
