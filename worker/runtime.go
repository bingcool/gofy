package worker

import (
	"context"

	"github.com/bingcool/gofy/core"
)

type runtimeKey struct{}

// WithRuntime attaches the framework runtime so a worker can reach the logger,
// database and tracer without rebuilding them. It is exported for hosts that
// implement their own runner; applications normally receive it from the worker
// context instead.
// WithRuntime 附加框架 Runtime，worker 因此可以直接使用 logger、数据库与
// tracer，无需自行重建。该方法导出给自行实现 Runner 的宿主；业务代码通常从
// worker context 里取。
func WithRuntime(ctx context.Context, rt *core.Runtime) context.Context {
	if rt == nil {
		return ctx
	}
	return context.WithValue(ctx, runtimeKey{}, rt)
}

// RuntimeFrom returns the runtime the host injected. Workers use it the same way
// a script uses its Runtime argument, which keeps one way to reach the database.
// RuntimeFrom 返回宿主注入的 Runtime。worker 使用它的方式与脚本接收 Runtime
// 参数一致，访问数据库因此只有一种写法。
func RuntimeFrom(ctx context.Context) (*core.Runtime, bool) {
	rt, ok := ctx.Value(runtimeKey{}).(*core.Runtime)
	return rt, ok && rt != nil
}
