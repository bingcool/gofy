package worker

// WorkerConfig holds the settings every worker in the process shares, so they
// are set once for `demo-service worker` rather than per task. Settings only one
// worker understands live next to that worker: ScanWorkerConfig is in scan.go.
// WorkerConfig 保存进程内所有 worker 共用的设置，对 `demo-service worker` 配置一次
// 即可，不必按任务重复；只有某一个 worker 才懂的设置放在它自己的文件里，例如
// ScanWorkerConfig 在 scan.go。
type WorkerConfig struct {
	// HealthAddr exposes /healthz on the worker process; empty disables it.
	// HealthAddr 在 worker 进程上暴露 /healthz；为空表示关闭。
	HealthAddr string `yaml:"health_addr"`
}

// Config is the `worker` section of config.yaml: the shared settings inlined at
// the top, then one block per worker, so the file reads as
// `worker.health_addr` and `worker.scan.*`.
// Config 是 config.yaml 的 `worker` 段：公共设置内联在顶层，其下是每个 worker 各自
// 的配置块，因此配置文件里读作 `worker.health_addr` 与 `worker.scan.*`。
type Config struct {
	WorkerConfig `yaml:",inline"`
	Scan         ScanWorkerConfig `yaml:"scan"`
}
