// Package group3 holds the checks that need the outside world: a worker that
// calls demo-service through the same client the business code would use, and a
// worker that calls the configured LLM alias.
//
// It exists as a separate group so a check can run as its own process:
// `demo-service worker --group=group3`. That is the point — the caller here is
// not the HTTP process that registered itself, so a successful call proves the
// whole chain (Nacos registration → resolver → typed client → HTTP) rather than
// an in-process shortcut. The LLM check proves the same for a different chain:
// config → credential → provider endpoint.
// 包 group3 放需要"外部世界"的巡检：一个用与业务代码相同的客户端调用 demo-service，
// 一个调用配置里的 LLM alias。
//
// 单独成组是为了让它能作为独立进程运行：`demo-service worker --group=group3`。这正是
// 关键——这里的调用方不是那个注册自己的 HTTP 进程，因此一次成功调用证明的是整条链路
// （Nacos 注册 → resolver → 类型化客户端 → HTTP），而不是进程内的捷径。LLM 巡检证明的
// 是另一条链路：配置 → 凭证 → provider 端点。
package group3

import (
	supportai "github.com/bingcool/gofy/support/ai"
	gofyworker "github.com/bingcool/gofy/support/worker"
	"github.com/yourorg/demo-service/serviceclients"
)

// NewGroup builds group3 around the injected clients and the llm_ai
// configuration. The whole client set is passed, not just the demoservice
// interface, so the worker can log the addressing mode and discovery scope it
// is about to use.
//
// The AI check only exists when llm_ai.enabled is true: without a client there
// is nothing to pace, and a deployment that does not use an LLM should not see
// a worker that can only fail. Nothing is constructed here — the client is
// built when the worker actually runs.
// NewGroup 用注入的客户端集合与 llm_ai 配置构造 group3。传入整个客户端集合而不是单个
// demoservice 接口，worker 才能把即将使用的寻址方式与发现范围写进启动日志。
//
// 只有 llm_ai.enabled 为 true 才有 LLM 巡检：没有客户端就没有可巡检的东西，不接大模型
// 的部署也不该看到一个只会失败的 worker。这里不构造任何东西——客户端在 worker 真正
// 运行时才建。
func NewGroup(clients *serviceclients.Clients, llm supportai.Config) (*gofyworker.Group, error) {
	workers := []gofyworker.Worker{
		newDiscoveryCheckWorker(clients, discoveryCheckDefaults),
	}
	if llm.Enabled {
		workers = append(workers, newAICheckWorker(llm, aiCheckDefaults))
	}
	return gofyworker.NewGroup("group3", workers...)
}
