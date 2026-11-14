// Package worker builds the long-running group started by
// `demo-service worker --group=order`.
//
// One file per task keeps each loop readable: scan.go and heartbeat.go own their
// own retry, ticker and logging, worker.go only decides what belongs to the
// group. The workers call the same repository-level data as the HTTP handlers
// instead of reimplementing queries, and they never touch support.Router: a
// worker process has no HTTP stack.
// 包 worker 构造 `demo-service worker --group=order` 启动的常驻分组。
//
// 一个任务一个文件，各自的 ticker、日志与错误处理都留在自己的文件里；worker.go
// 只决定谁属于这个分组。worker 复用与 HTTP 处理器相同的数据访问，而不是另写一套
// 查询；它们也不碰 support.Router：worker 进程里没有 HTTP 栈。
package worker

import gofyworker "github.com/bingcool/gofy/support/worker"

// NewGroup builds the order group. Concurrency and rhythm come from the worker
// section of config.yaml, so the same image runs with different replica counts
// per environment.
// NewGroup 构造 order 分组。并发与节奏来自 config.yaml 的 worker 段，同一个镜像
// 因此可以在不同环境用不同的实例数运行。
func NewGroup(cfg WorkerConfig) (*gofyworker.Group, error) {
	return gofyworker.NewGroup(
		"order",
		newOrderScanWorker(cfg.Scan),
		newOrderHeartbeatWorker(),
	)
}
