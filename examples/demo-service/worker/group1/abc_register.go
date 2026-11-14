// Package group1 holds the first worker group: the fast work that scales with
// order volume.
//
// The package boundary is the deployment boundary: group1 is started by
// `demo-service worker --group=group1` and owns its own config, so a change here
// never reaches group2.
//
// The file is named abc_register.go so the group's entry point sorts above its
// member files: "a" beats heartbeat.go and scan.go, whereas a plain register.go
// sinks below both. The prefix has to be a real character, not an underscore —
// Go ignores files starting with "_" or ".", so `_register.go` would leave the
// package without its NewGroup.
//
// 包 group1 是第一个 worker 分组：随订单量扩缩的秒级任务。
//
// 包边界就是部署边界：group1 由 `demo-service worker --group=group1` 启动，参数也归
// 它自己，因此改动这里不会波及 group2。
//
// 文件名是 abc_register.go，让分组的入口排在成员文件之前："a" 在 heartbeat.go、
// scan.go 前面，而裸的 register.go 会沉到两者之后。前缀必须是真实字符而不是下划线——
// Go 忽略以 "_" 或 "." 开头的文件，`_register.go` 会让这个包丢掉 NewGroup。
package group1

import (
	"time"

	gofyworker "github.com/bingcool/gofy/support/worker"
)

// defaults is what group1 runs with. It sits here rather than in config.yaml so
// the numbers live next to the worker they belong to: one file answers "how many
// instances, how often". Use --concurrency to override the count for a run
// without editing code.
// defaults 是 group1 运行时的参数。放在这里而不是 config.yaml：数字紧挨它配置的
// worker，一个文件就能回答"几个实例、多久一次"。临时改实例数用 `--concurrency`，
// 不必改代码。
var defaults = ScanWorkerConfig{
	Concurrency: 2,
	Interval:    5 * time.Second,
}

// NewGroup builds group1: the scan loop and the liveness heartbeat, which share
// one rhythm and therefore one process.
// NewGroup 构造 group1：扫描循环与存活心跳，两者节奏一致，因此同进程运行。
func NewGroup() (*gofyworker.Group, error) {
	return gofyworker.NewGroup(
		"group1",
		newScanWorker(defaults),
		newHeartbeatWorker(),
	)
}
