package worker

import (
	"fmt"
	"strings"
)

// DefaultDBRequirer is implemented by a Worker that cannot run without the
// framework-managed db.default.
//
// "The process has a database" and "this worker needs the framework's database"
// are different questions. A worker that resolves its handles from a named
// instance (db.order, db.product) works in a deployment that configures no
// db.default at all — that is exactly what the database-only worker generation
// exists for. The consumer idempotency ledger is the other case: it is a
// process-wide table with its own DDL, so it is bound to db.default by design,
// and such a deployment has to fail at startup instead of restarting the
// consumer forever behind a permanently red readiness probe.
//
// The interface mirrors ReadyReporter: optional, discovered by the host, and
// implemented by the workers that need it rather than declared in configuration.
//
// DefaultDBRequirer 由"没有框架托管的 db.default 就跑不起来"的 Worker 实现。
//
// "进程有数据库"与"这个 worker 需要框架的数据库"是两个问题。从命名实例
// （db.order、db.product）取句柄的 worker，在一个完全没有 db.default 的部署里照样工作
// ——这正是"只装数据库"的 worker 装配代存在的理由。消费者幂等台账是另一种情况：它是
// 进程级、自带 DDL 的表，因此按设计绑定 db.default；这样的部署必须在启动期失败，而不是
// 让消费者无限重启、就绪探针永远红着。
//
// 本接口与 ReadyReporter 同形：可选、由宿主发现，并且由需要它的 worker 实现，而不是在
// 配置里声明。
type DefaultDBRequirer interface {
	// RequiresDefaultDB reports whether this worker needs db.default under its
	// current settings. It takes no argument because the answer is a property
	// of the worker itself (a consumer's idempotency switch), not of the
	// deployment being asked about.
	// RequiresDefaultDB 报告该 worker 在当前设置下是否需要 db.default。它不接参数，
	// 因为答案来自 worker 自身（消费者的幂等开关），而不是被问到的部署。
	RequiresDefaultDB() bool
}

// RequireDefaultDB fails when a worker that needs db.default would run without
// one. available is the caller's rt.DB != nil; it is a bool rather than a
// *gorm.DB so this package stays free of a driver dependency.
//
// Call it once the runtime exists and before the first group starts. There the
// answer is still a startup error, which reaches the terminal and the daemon
// parent with a reason. Left to Run, the same static misconfiguration becomes a
// consumer that restarts on the default RestartOnError policy forever while
// readiness never closes — a process that looks alive and serves nothing.
//
// A group with no such worker passes even when available is false: named
// instances are enough for it, and demanding db.default would reject a valid
// deployment.
// RequireDefaultDB 在"需要 db.default 的 worker 却没有它"时返回错误。available 就是
// 调用方的 rt.DB != nil；用布尔而不是 *gorm.DB，是为了让本包不依赖驱动。
//
// 调用点在 runtime 就绪之后、第一个分组启动之前。此时这仍是一个启动错误，能带着原因抵达
// 终端与 daemon 父进程。留到 Run 里，同一个静态配置错误就会变成"按默认 RestartOnError
// 无限重启、就绪永不放行"的消费者——一个看着活着、却什么也不干的进程。
//
// 不含这类 worker 的分组在 available 为 false 时照样通过：命名实例对它已经够用，
// 强行要求 db.default 会误杀合法部署。
func RequireDefaultDB(groups []*Group, available bool) error {
	if available {
		return nil
	}
	var missing []string
	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, w := range g.Workers() {
			req, ok := w.(DefaultDBRequirer)
			if !ok || !req.RequiresDefaultDB() {
				continue
			}
			missing = append(missing, g.Name()+"/"+w.Name())
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"workers requiring db.default: %s. The framework-managed database holds process-wide "+
			"infrastructure such as the consumer idempotency ledger, and named instances such as "+
			"db.order cannot substitute for it. Configure db.default or drop the requirement",
		strings.Join(missing, ", "))
}
