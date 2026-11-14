// Package group1 holds the first worker group: the fast work that scales with
// order volume.
//
// The package boundary is the deployment boundary: group1 is started by
// `demo-service worker --group=group1` and owns its own config, so a change here
// never reaches group2.
//
// The file is named abc_register.go so the group's entry point (Config and
// NewGroup) sorts above its member files: "a" beats heartbeat.go and scan.go,
// whereas a plain register.go sinks below both. The prefix has to be a real
// character, not an underscore — Go ignores files starting with "_" or ".", so
// `_register.go` would leave the package without its Config and NewGroup.
//
// 包 group1 是第一个 worker 分组：随订单量扩缩的秒级任务。
//
// 包边界就是部署边界：group1 由 `demo-service worker --group=group1` 启动，配置也归
// 它自己，因此改动这里不会波及 group2。
//
// 文件名是 abc_register.go，让分组的入口（Config 与 NewGroup）排在成员文件之前："a" 在
// heartbeat.go、scan.go 前面，而裸的 register.go 会沉到两者之后。前缀必须是真实字符而
// 不是下划线——Go 忽略以 "_" 或 "." 开头的文件，`_register.go` 会让这个包丢掉 Config 与
// NewGroup。
package group1

import (
	"fmt"
	"math"

	gofyworker "github.com/bingcool/gofy/support/worker"
)

// Config holds the settings of group1. It sits next to the group rather than in
// the parent worker package, because the group decides what belongs to it:
// adding a worker to group1 adds a field here, and group2 does not change.
// Config 保存 group1 的设置。它跟着分组走而不是放在父包 worker 里，因为分组决定组内
// 有哪些 worker：给 group1 加 worker 就在这里加字段，group2 不受影响。
type Config struct {
	Scan ScanWorkerConfig `yaml:"scan"`
}

// NewGroup builds group1: the scan loop and the liveness heartbeat, which share
// one rhythm and therefore one process. A count the worker cannot carry as a
// uint8 is rejected here instead of being silently truncated by the conversion.
// NewGroup 构造 group1：扫描循环与存活心跳，两者节奏一致，因此同进程运行。worker
// 用 uint8 承载实例数，配置值超出范围时在这里直接报错，而不是被转换悄悄截断。
func NewGroup(cfg Config) (*gofyworker.Group, error) {
	if c := cfg.Scan.Concurrency; c < 1 || c > math.MaxUint8 {
		return nil, fmt.Errorf("group1: scan.concurrency must be between 1 and %d, got %d", math.MaxUint8, c)
	}
	return gofyworker.NewGroup(
		"group1",
		newScanWorker(cfg.Scan),
		newHeartbeatWorker(),
	)
}
