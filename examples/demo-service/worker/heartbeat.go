package worker

import (
	"context"
	"time"

	"go.uber.org/zap"

	gofyworker "github.com/bingcool/gofy/support/worker"
)

// newOrderHeartbeatWorker proves a group can host tasks that need no database;
// it also gives operators a per-instance liveness line to grep for.
// newOrderHeartbeatWorker 说明分组里可以放不依赖数据库的任务，同时给运维留下一行
// 可 grep 的实例存活日志。
func newOrderHeartbeatWorker() gofyworker.Worker {
	return gofyworker.Func("order-heartbeat", func(ctx context.Context) error {
		rt, _ := gofyworker.RuntimeFrom(ctx)
		id, _ := gofyworker.IdentityFrom(ctx)

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if rt != nil {
					rt.Logger.Info("worker heartbeat",
						zap.String("worker", id.String()),
						zap.String("host", id.Host),
					)
				}
			}
		}
	})
}
