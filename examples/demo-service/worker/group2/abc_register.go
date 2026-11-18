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
	"github.com/bingcool/gofy/support/tenant"
	gofyworker "github.com/bingcool/gofy/support/worker"

	"github.com/yourorg/demo-service/worker/tenantguard"
)

// NewGroup builds group2. The summary worker passes its own defaults, declared
// next to the loop they control.
//
// The tenant section is taken for the same reason as group1: group2 is its own
// deployment, so its process may contain no group1 worker at all. Without this
// the report would read the pool before any plugin was installed and never
// notice it is running unfiltered.
// NewGroup 构造 group2。汇总任务传入自己的 defaults，声明就写在它控制的循环旁边。
//
// 接收租户段的原因与 group1 相同：group2 是独立部署，它的进程里可能没有任何 group1
// 的 worker。不接的话，报表会在插件安装之前就读连接池，而且永远不知道自己没被过滤。
func NewGroup(tenantEnabled bool, tenantOpts tenant.Options) (*gofyworker.Group, error) {
	tenantguard.Configure(tenantEnabled, tenantOpts)
	return gofyworker.NewGroup(
		"group2",
		newSummaryWorker(summaryDefaults),
	)
}
