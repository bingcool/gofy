package worker

import "time"

// WorkerConfig configures the long-running workers started by
// `demo-service worker --group=<group>`.
// WorkerConfig 配置由 `demo-service worker --group=<分组>` 启动的常驻任务。
type WorkerConfig struct {
	Scan ScanWorkerConfig `yaml:"scan"`
	// HealthAddr exposes /healthz on the worker process; empty disables it.
	// HealthAddr 在 worker 进程上暴露 /healthz；为空表示关闭。
	HealthAddr string `yaml:"health_addr"`
}

// ScanWorkerConfig controls the order scan worker's rhythm and replica count.
// ScanWorkerConfig 控制订单扫描任务的节奏与实例数。
type ScanWorkerConfig struct {
	// Concurrency is how many scan instances run inside the worker process.
	// Concurrency 是 worker 进程内同时运行的扫描实例数。
	Concurrency int `yaml:"concurrency" default:"2"`
	// Interval is the pause between two scans.
	// Interval 是两次扫描之间的间隔。
	Interval time.Duration `yaml:"interval" default:"5s"`
}
