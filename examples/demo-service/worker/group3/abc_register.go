// Package group3 holds the service-discovery check: a worker that calls
// demo-service through the same client the business code would use.
//
// It exists as a separate group so the check can run as its own process:
// `demo-service worker --group=group3`. That is the point — the caller here is
// not the HTTP process that registered itself, so a successful call proves the
// whole chain (Nacos registration → resolver → typed client → HTTP) rather than
// an in-process shortcut.
// 包 group3 是服务发现巡检：用一个与业务代码相同的客户端调用 demo-service。
//
// 单独成组是为了让它能作为独立进程运行：`demo-service worker --group=group3`。这正是
// 关键——这里的调用方不是那个注册自己的 HTTP 进程，因此一次成功调用证明的是整条链路
// （Nacos 注册 → resolver → 类型化客户端 → HTTP），而不是进程内的捷径。
package group3

import (
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/demo-service/serviceclients"
)

// NewGroup builds group3 around the injected clients. The whole client set is
// passed, not just the demoservice interface, so the worker can log the
// addressing mode and discovery scope it is about to use.
// NewGroup 用注入的客户端集合构造 group3。传入整个客户端集合而不是单个 demoservice
// 接口，worker 才能把即将使用的寻址方式与发现范围写进启动日志。
func NewGroup(clients *serviceclients.Clients) (*gofyworker.Group, error) {
	return gofyworker.NewGroup(
		"group3",
		newDiscoveryCheckWorker(clients, discoveryCheckDefaults),
	)
}
