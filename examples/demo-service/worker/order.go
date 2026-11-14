package worker

import gofyworker "github.com/bingcool/gofy/support/worker"

// OrderGroupConfig holds the settings of the order group. It sits here rather
// than in config.go because the group decides what belongs to it: adding a
// worker to `order` adds a field here, and no other group changes.
// OrderGroupConfig 保存 order 分组的设置。它放在这里而不是 config.go，因为分组自己
// 决定组内有哪些 worker：给 order 加一个 worker 就在这加一个字段，其它分组不受影响。
type OrderGroupConfig struct {
	Scan ScanWorkerConfig `yaml:"scan"`
}

// newOrderGroup builds the order group. Concurrency and rhythm come from the
// worker section of config.yaml, so the same image runs with different replica
// counts per environment.
// newOrderGroup 构造 order 分组。并发与节奏来自 config.yaml 的 worker 段，同一个
// 镜像因此可以在不同环境用不同的实例数运行。
func newOrderGroup(cfg OrderGroupConfig) (*gofyworker.Group, error) {
	return gofyworker.NewGroup(
		"order",
		newOrderScanWorker(cfg.Scan),
		newOrderHeartbeatWorker(),
	)
}
