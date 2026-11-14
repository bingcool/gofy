package worker

import gofyworker "github.com/bingcool/gofy/support/worker"

// Group2Config holds the settings of group2: reporting, which runs on an hourly
// rhythm. It is a group of its own so a report restart never interrupts the scan
// loop in group1.
// Group2Config 保存 group2 的设置：报表按小时节奏运行。它单独成组，报表重启因此不会
// 打断 group1 的扫描循环。
type Group2Config struct {
	Summary SummaryWorkerConfig `yaml:"summary"`
}

// newGroup2 builds group2.
// newGroup2 构造 group2。
func newGroup2(cfg Group2Config) (*gofyworker.Group, error) {
	return gofyworker.NewGroup("group2", newReportSummaryWorker(cfg.Summary))
}
