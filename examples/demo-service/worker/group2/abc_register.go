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
	gofyworker "github.com/bingcool/gofy/worker"
)

// NewGroup builds group2. The summary worker passes its own defaults, declared
// next to the loop they control.
//
// The tenant plugin is installed by the process assembly, not here: group2 is
// its own deployment, and its process always assembles the same io-db
// generation before any group starts. That is what makes "the report is
// filtered" independent of which group happens to run.
// NewGroup 构造 group2。汇总任务传入自己的 defaults，声明就写在它控制的循环旁边。
//
// 租户插件由进程装配安装，不在这里：group2 是独立部署，它的进程同样会在任何分组启动
// 之前装配那一代 io-db。因此"报表被过滤"不取决于本进程恰好跑了哪个分组。
func NewGroup() (*gofyworker.Group, error) {
	return gofyworker.NewGroup(
		"group2",
		newSummaryWorker(summaryDefaults),
	)
}
