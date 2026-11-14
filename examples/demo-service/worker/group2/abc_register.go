// Package group2 holds the second worker group: reporting, which runs on an
// hourly rhythm.
//
// It is a package of its own so the deployment boundary is visible in the tree:
// group1 and group2 start as separate processes, scale separately, and a report
// restart never interrupts the scan loop.
//
// The file is abc_register.go for the same reason as in group1: the prefix pins
// the group's entry point (Config and NewGroup) above summary.go, which a plain
// register.go would sit below.
//
// 包 group2 是第二个 worker 分组：按小时节奏运行的报表任务。
//
// 单独成包是为了让部署边界在目录结构里就看得见：group1 与 group2 各自启动独立进程、
// 各自扩缩，报表重启不会打断扫描循环。
//
// 文件名同样是 abc_register.go：前缀把分组的入口（Config 与 NewGroup）钉在 summary.go
// 之前，裸的 register.go 会落在它后面。
package group2

import (
	"fmt"
	"math"

	gofyworker "github.com/bingcool/gofy/support/worker"
)

// Config holds the settings of group2.
// Config 保存 group2 的设置。
type Config struct {
	Summary SummaryWorkerConfig `yaml:"summary"`
}

// NewGroup builds group2. A count the worker cannot carry as a uint8 is rejected
// here instead of being silently truncated by the conversion.
// NewGroup 构造 group2。worker 用 uint8 承载实例数，配置值超出范围时在这里直接报错，
// 而不是被转换悄悄截断。
func NewGroup(cfg Config) (*gofyworker.Group, error) {
	if c := cfg.Summary.Concurrency; c < 1 || c > math.MaxUint8 {
		return nil, fmt.Errorf("group2: summary.concurrency must be between 1 and %d, got %d", math.MaxUint8, c)
	}
	return gofyworker.NewGroup(
		"group2",
		newSummaryWorker(cfg.Summary),
	)
}
