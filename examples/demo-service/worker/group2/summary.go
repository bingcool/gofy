package group2

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/bingcool/gofy/support/tenant"
	gofyworker "github.com/bingcool/gofy/worker"
	"github.com/yourorg/demo-service/app/appio"
	appdb "github.com/yourorg/demo-service/app/appio/db"
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
	// IntervalMS is the pause between two aggregations, in milliseconds.
	// IntervalMS 是两次汇总之间的间隔，单位毫秒。
	IntervalMS uint32
}

// summaryDefaults is what group2's summary worker runs with: one instance per
// hour. More than one would write the same report twice, so the count is
// deliberately one and lives here, beside the aggregation it paces.
// summaryDefaults 是 group2 汇总任务的运行参数：每小时一个实例。多实例会把同一份报表
// 写两遍，因此计数刻意就是 1，和它控制的聚合逻辑放在一起。
var summaryDefaults = SummaryWorkerConfig{
	Concurrency: 1,
	IntervalMS:  3_600_000,
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
	intervalMS := cfg.IntervalMS
	if intervalMS == 0 {
		intervalMS = 3_600_000
	}
	return gofyworker.Func("report-summary", uint8(cfg.Concurrency), func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		if !ok {
			return errors.New("report-summary: runtime is required")
		}
		// Same entry shape as group1: the process assembled one io-db generation
		// before any group started, and this loop resolves its handle through
		// DBConnect. group2 is a separate deployment, but its own process assembled
		// the same generation, so nothing here depends on group1 having run.
		// 与 group1 相同的入口形状：本进程在任何分组启动之前已装配一代 io-db，本循环通过
		// DBConnect 解析句柄。group2 是独立部署，但它自己的进程装配了同一代，因此这里
		// 不依赖 group1 是否运行过。
		db, err := appio.WithContext(ctx).DBConnect(ctx, appdb.DefaultInstance)
		if err != nil {
			return err
		}
		id, _ := gofyworker.IdentityFrom(ctx)
		lg := rt.Logger.With(zap.String("worker", id.String()))

		lg.Info("report summary started", zap.Uint32("interval_ms", intervalMS))
		defer lg.Info("report summary stopped")

		return gofyworker.Ticker(ctx, intervalMS, func(ctx context.Context) error {
			// The aggregation uses the drain context: a shutdown must not cut
			// a query that is already on the wire.
			// 汇总使用 drain context：停机不应该中断已经在执行的查询。
			// The report aggregates every tenant; it is the privileged shape the
			// bypass exists for. Without it, a tenant-aware deployment would
			// either fail closed or silently report one tenant's totals.
			// 报表聚合所有租户，正是绕过存在的那类特权形状。不声明的话，开了租户隔离的
			// 部署要么失败关闭，要么只报某一个租户的合计。
			queryCtx, cancel := context.WithTimeout(tenant.Without(gofyworker.DrainContext(ctx)), 10*time.Second)
			defer cancel()
			var rows []statusCount
			if err := db.WithContext(queryCtx).
				Table("orders").
				Select("status, COUNT(*) AS total").
				Group("status").
				Scan(&rows).Error; err != nil {
				return err
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
			return nil
		})
	})
}
