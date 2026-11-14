package group1

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// ScanWorkerConfig controls the scan worker's rhythm and replica count. It lives
// here because it is this worker's own knob: the group passes one in, and nothing
// outside the package reads it. No yaml tags — the values are set in
// abc_register.go, not in config.yaml.
// ScanWorkerConfig 控制扫描任务的节奏与实例数。放在这里是因为它只属于这个 worker：
// 分组传一个进来，包外没人读它。没有 yaml 标签——值在 abc_register.go 里给，
// 不来自 config.yaml。
type ScanWorkerConfig struct {
	// Concurrency is how many scan instances run inside the worker process.
	// Concurrency 是 worker 进程内同时运行的扫描实例数。
	Concurrency int
	// Interval is the pause between two scans.
	// Interval 是两次扫描之间的间隔。
	Interval time.Duration
}

// scanDefaults is what group1's scan worker runs with. The numbers sit in the
// same file as the loop they control, so "how many instances, how often" is
// answered without leaving the worker; `--concurrency` overrides the count for
// one run without touching code.
// scanDefaults 是 group1 扫描任务的运行参数。数字和它控制的循环在同一个文件里，
// "几个实例、多久一次"不必离开这个 worker 就能回答；临时改实例数用 `--concurrency`，
// 不必动代码。
var scanDefaults = ScanWorkerConfig{
	Concurrency: 2,
	Interval:    5 * time.Second,
}

// newScanWorker scans pending orders on an interval. Demo's orders table has no
// claim columns (status is a business value, not a task state), so this only
// counts; a real queue declares a job table with claim.DDL and calls
// claim.Queue.Claim, which is what makes several replicas safe.
// newScanWorker 按间隔扫描待处理订单。demo 的 orders 表没有认领所需的状态列（status
// 是业务值，不是任务状态），因此这里只统计；真实的队列用 claim.DDL 建任务表，再调
// claim.Queue.Claim，多副本才安全。
func newScanWorker(cfg ScanWorkerConfig) gofyworker.Worker {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return gofyworker.Func("order-scan", uint8(cfg.Concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok || rt.DB == nil {
			return errors.New("order-scan: runtime database is required")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := rt.Logger.With(zap.String("worker", id.String()))

		lg.Info("order scan started", zap.Duration("interval", interval))
		defer lg.Info("order scan stopped")

		return gofyworker.Ticker(ctx, interval, func(ctx context.Context) error {
			// The count uses the drain context: a shutdown must not abort a
			// query that is already talking to the database.
			// 计数使用 drain context：停机不应该中断已经在与数据库交互的查询。
			queryCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), 3*time.Second)
			defer cancel()
			var pending int64
			if err := rt.DB.WithContext(queryCtx).
				Table("orders").
				Where("status = ?", string(orderenum.OrderStatusPending)).
				Count(&pending).Error; err != nil {
				return err
			}
			lg.Info("order scan tick", zap.Int64("pending", pending))
			return nil
		})
	})
}
