package worker

import gofyworker "github.com/bingcool/gofy/support/worker"

// Group1Config holds the settings of group1: the fast work that scales with
// order volume. It sits here rather than in config.go because the group decides
// what belongs to it: adding a worker to group1 adds a field here, and group2
// does not change.
// Group1Config 保存 group1 的设置：随订单量扩缩的秒级任务。它放在分组自己的文件里
// 而不是 config.go，因为分组决定组内有哪些 worker：给 group1 加 worker 就在这里加
// 字段，group2 不受影响。
type Group1Config struct {
	Scan ScanWorkerConfig `yaml:"scan"`
}

// newGroup1 builds group1: the scan loop and the liveness heartbeat, which share
// one rhythm and therefore one process. Concurrency comes from the worker section
// of config.yaml, so the same image runs with different replica counts per
// environment.
// newGroup1 构造 group1：扫描循环与存活心跳，两者节奏一致，因此同进程运行。并发来自
// config.yaml 的 worker 段，同一个镜像可以在不同环境用不同的实例数运行。
func newGroup1(cfg Group1Config) (*gofyworker.Group, error) {
	return gofyworker.NewGroup(
		"group1",
		newOrderScanWorker(cfg.Scan),
		newOrderHeartbeatWorker(),
	)
}
