// Package group1 holds the first worker group: the fast work that scales with
// order volume.
//
// The package boundary is the deployment boundary: group1 is started by
// `demo-service worker --group=group1` and owns its own settings, so a change
// here never reaches group2.
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
	gofyworker "github.com/bingcool/gofy/support/worker"
)

// NewGroup builds group1: the scan loop and the liveness heartbeat, which share
// one rhythm and therefore one process. Each worker passes its own defaults,
// declared next to the loop they control.
// NewGroup 构造 group1：扫描循环与存活心跳，两者节奏一致，因此同进程运行。每个 worker
// 传入自己的 defaults，声明就写在它控制的那个循环旁边。
func NewGroup() (*gofyworker.Group, error) {
	return gofyworker.NewGroup(
		"group1",
		newScanWorker(scanDefaults),
		newHeartbeatWorker(),
	)
}
