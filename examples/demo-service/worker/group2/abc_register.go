// Package group2 holds the second worker group: reporting, which runs on an
// hourly rhythm.
//
// It is a package of its own so the deployment boundary is visible in the tree:
// group1 and group2 start as separate processes, scale separately, and a report
// restart never interrupts the scan loop.
//
// The file is abc_register.go for the same reason as in group1: the prefix pins
// the group's entry point above summary.go, which a plain register.go would sit
// below.
//
// 包 group2 是第二个 worker 分组：按小时节奏运行的报表任务。
//
// 单独成包是为了让部署边界在目录结构里就看得见：group1 与 group2 各自启动独立进程、
// 各自扩缩，报表重启不会打断扫描循环。
//
// 文件名同样是 abc_register.go：前缀把分组的入口钉在 summary.go 之前，裸的
// register.go 会落在它后面。
package group2

import (
	"time"

	gofyworker "github.com/bingcool/gofy/support/worker"
)

// defaults is what group2 runs with: one instance per hour. More than one would
// write the same report twice, so the number is deliberately one.
// defaults 是 group2 运行时的参数：每小时一个实例。多实例会把同一份报表写两遍，因此
// 这里刻意就是 1。
var defaults = SummaryWorkerConfig{
	Concurrency: 1,
	Interval:    time.Hour,
}

// NewGroup builds group2.
// NewGroup 构造 group2。
func NewGroup() (*gofyworker.Group, error) {
	return gofyworker.NewGroup(
		"group2",
		newSummaryWorker(defaults),
	)
}
