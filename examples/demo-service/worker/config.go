package worker

// WorkerConfig holds the settings every worker in the process shares, so they
// are set once for `demo-service worker` rather than per task. Settings only one
// worker understands live next to that worker: ScanWorkerConfig is in scan.go.
// WorkerConfig 保存进程内所有 worker 共用的设置，对 `demo-service worker` 配置一次
// 即可，不必按任务重复；只有某一个 worker 才懂的设置放在它自己的文件里，例如
// ScanWorkerConfig 在 scan.go。
type WorkerConfig struct {
	// HealthAddr exposes /healthz on the worker process; empty disables it.
	// One process runs one group, so a shared address is still group-scoped at
	// runtime; give co-located groups different ports.
	// HealthAddr 在 worker 进程上暴露 /healthz；为空表示关闭。一个进程只跑一个
	// 分组，因此这个"公共"地址在运行时仍然属于某个分组；同机跑多个分组要换端口。
	HealthAddr string `yaml:"health_addr"`
}

// Config is the `worker` section of config.yaml: the shared settings inlined at
// the top, then one block per group, named after the group, so the file reads as
// `worker.health_addr`, `worker.order.scan.*` and `worker.report.summary.*`.
//
// Group blocks are struct fields rather than a map: strict decoding then rejects
// a misspelled key inside a block, and `default` tags still apply. A map would
// silently accept both.
// Config 是 config.yaml 的 `worker` 段：公共设置内联在顶层，其下按分组名给每个分组
// 一个配置块，因此配置文件里读作 `worker.health_addr`、`worker.order.scan.*` 与
// `worker.report.summary.*`。
//
// 分组用结构体字段而不是 map：严格解码才能拒绝块内拼错的键，`default` 标签也才会
// 生效；用 map 两者都会静默放过。
type Config struct {
	WorkerConfig `yaml:",inline"`
	Order        OrderGroupConfig  `yaml:"order"`
	Report       ReportGroupConfig `yaml:"report"`
}

// Concurrency maps every configured worker to its instance count, keyed the way
// WithWorkerConcurrency expects: "group/worker". It lives here so a group's name
// is written once, next to the group, instead of being repeated in main.
// Concurrency 按 WithWorkerConcurrency 要求的 "group/worker" 键，把每个配置过的
// worker 映射到实例数。放在这里是为了让分组名只写在自己的文件里，不必在 main 里
// 再抄一遍。
func Concurrency(cfg Config) map[string]int {
	out := make(map[string]int, 2)
	if n := cfg.Order.Scan.Concurrency; n > 0 {
		out["order/order-scan"] = n
	}
	if n := cfg.Report.Summary.Concurrency; n > 0 {
		out["report/report-summary"] = n
	}
	return out
}
