package worker

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// SummaryWorkerConfig controls the report summary worker: how many instances
// run inside the process and how often each one aggregates.
// SummaryWorkerConfig 控制报表汇总任务：进程内跑几个实例，每个实例多久汇总一次。
type SummaryWorkerConfig struct {
	// Concurrency is how many summary instances run inside the worker process.
	// Concurrency 是 worker 进程内同时运行的汇总实例数。
	Concurrency int `yaml:"concurrency" default:"1"`
	// Interval is the pause between two aggregations.
	// Interval 是两次汇总之间的间隔。
	Interval time.Duration `yaml:"interval" default:"1h"`
}

// statusCount is one row of the status breakdown.
// statusCount 是状态分布中的一行。
type statusCount struct {
	Status string `gorm:"column:status"`
	Total  int64  `gorm:"column:total"`
}

// newReportSummaryWorker aggregates orders by status on an interval. It is the
// second group's only member, which is enough to show the point: it reads the
// same database as the order group without sharing a process, because the two
// have different rhythms and are deployed separately.
// newReportSummaryWorker 按间隔统计各状态订单数。它是 report 分组唯一的成员，足以
// 说明问题：它读的是与 order 分组相同的库，却不同进程，因为两者节奏不同、分开部署。
func newReportSummaryWorker(cfg SummaryWorkerConfig) gofyworker.Worker {
	interval := cfg.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	return gofyworker.Func("report-summary", func(ctx context.Context) error {
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
