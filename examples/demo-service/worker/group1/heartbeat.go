package group1

import (
	"context"

	"go.uber.org/zap"

	gofyworker "github.com/bingcool/gofy/worker"
)

// newHeartbeatWorker proves a group can host tasks that need no database; it
// also gives operators a per-instance liveness line to grep for.
// newHeartbeatWorker 说明分组里可以放不依赖数据库的任务，同时给运维留下一行可 grep
// 的实例存活日志。
func newHeartbeatWorker() gofyworker.Worker {
	return gofyworker.Func("order-heartbeat", 1, func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := zap.NewNop()
		if ok && rt.Logger != nil {
			lg = rt.Logger
		}
		return gofyworker.Ticker(ctx, 10_000, func(context.Context) error {
			lg.Info("worker heartbeat",
				zap.String("worker", id.String()),
				zap.String("host", id.Host),
			)
			return nil
		})
	})
}
