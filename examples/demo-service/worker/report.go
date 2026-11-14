package worker

import gofyworker "github.com/bingcool/gofy/support/worker"

// ReportGroupConfig holds the settings of the report group. Reporting runs on an
// hourly rhythm while the order group ticks every few seconds, which is why they
// are two groups: separate deployments, separate scaling, no shared restart.
// ReportGroupConfig 保存 report 分组的设置。报表按小时节奏运行，order 分组每几秒
// 一跳，这正是它们分成两个分组的原因：各自部署、各自扩缩、互不牵连重启。
type ReportGroupConfig struct {
	Summary SummaryWorkerConfig `yaml:"summary"`
}

// newReportGroup builds the report group.
// newReportGroup 构造 report 分组。
func newReportGroup(cfg ReportGroupConfig) (*gofyworker.Group, error) {
	return gofyworker.NewGroup("report", newReportSummaryWorker(cfg.Summary))
}
