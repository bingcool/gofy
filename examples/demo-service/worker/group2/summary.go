package group2

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// SummaryWorkerConfig controls the report summary worker: how many instances
// run inside the process and how often each one aggregates. No yaml tags — the
// values are set in abc_register.go, not in config.yaml.
// SummaryWorkerConfig 控制报表汇总任务：进程内跑几个实例，每个实例多久汇总一次。
// 没有 yaml 标签——值在 abc_register.go 里给，不来自 config.yaml。
type SummaryWorkerConfig struct {
	// Concurrency is how many summary instances run inside the worker process.
	// Concurrency 是 worker 进程内同时运行的汇总实例数。
	Concurrency int
	// Interval is the pause between two aggregations.
	// Interval 是两次汇总之间的间隔。
	Interval time.Duration
}

// summaryDefaults is what group2's summary worker runs with: one instance per
// hour. More than one would write the same report twice, so the count is
// deliberately one and lives here, beside the aggregation it paces.
// summaryDefaults 是 group2 汇总任务的运行参数：每小时一个实例。多实例会把同一份报表
// 写两遍，因此计数刻意就是 1，和它控制的聚合逻辑放在一起。
var summaryDefaults = SummaryWorkerConfig{
	Concurrency: 1,
	Interval:    time.Hour,
}

// statusCount is one row of the status breakdown.
// statusCount 是状态分布中的一行。
type statusCount struct {
	Status string `gorm:"column:status"`
	Total  int64  `gorm:"column:total"`
}

// newSummaryWorker aggregates orders by status on an interval. It reads the same
// database as group1 without sharing a process, which is the point of splitting
// the two groups: different rhythms, independent deployments.
// newSummaryWorker 按间隔统计各状态订单数。它与 group1 读同一个库却不同进程，这正是
// 拆成两个分组的意义：节奏不同、部署独立。
func newSummaryWorker(cfg SummaryWorkerConfig) gofyworker.Worker {
	interval := cfg.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	return gofyworker.Func("report-summary", uint8(cfg.Concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok || rt.DB == nil {
			return errors.New("report-summary: runtime database is required")
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := rt.Logger.With(zap.String("worker", id.String()))

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		lg.Info("report summary started", zap.Duration("interval", interval))
		for {
			select {
			case <-ctx.Done():
				lg.Info("report summary stopped")
				return nil
			case <-ticker.C:
				// The aggregation uses the drain context: a shutdown must not cut
				// a query that is already on the wire.
				// 汇总使用 drain context：停机不应该中断已经在执行的查询。
				queryCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), 10*time.Second)
				var rows []statusCount
				err := rt.DB.WithContext(queryCtx).
					Table("orders").
					Select("status, COUNT(*) AS total").
					Group("status").
					Scan(&rows).Error
				cancel()
				if err != nil {
					lg.Warn("report summary failed", zap.Error(err))
					continue
				}

				// Seed every declared status so a zero count still shows up as a
				// field: a report that drops empty rows looks like missing data.
				// 先把每个已声明的状态置零，计数为 0 时字段仍然存在：报表丢掉空行会
				// 像是数据缺失。
				counts := make(map[string]int64, len(orderenum.AllOrderStatus))
				for _, status := range orderenum.AllOrderStatus {
					counts[string(status)] = 0
				}
				for _, row := range rows {
					counts[row.Status] = row.Total
				}
				fields := make([]zap.Field, 0, len(orderenum.AllOrderStatus))
				for _, status := range orderenum.AllOrderStatus {
					fields = append(fields, zap.Int64(string(status), counts[string(status)]))
				}
				lg.Info("report summary tick", fields...)
			}
		}
	})
}
