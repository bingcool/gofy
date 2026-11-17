package config

import "time"

// ClientsConfig describes the downstream services this process calls over HTTP.
//
// It answers one question per service: how to find it. An empty base_url means
// "resolve the contract's service name through Nacos"; a non-empty one pins a
// fixed address and never contacts Nacos, which is how local development and
// canary checks bypass the registry. The two modes are mutually exclusive by
// construction, so the wiring layer never has to decide between them.
// ClientsConfig 描述本进程通过 HTTP 调用的下游服务。
//
// 它对每个服务只回答一个问题：怎么找到它。base_url 为空表示"按契约里的服务名走
// Nacos 解析"；非空则钉死一个地址、完全不碰 Nacos，这也是本地开发与灰度直连绕过
// 注册中心的方式。两种寻址方式在结构上互斥，接线层因此不需要再做判断。
type ClientsConfig struct {
	// DemoService points at the demo-service contract (demoservice.ServiceName).
	// DemoService 指向 demo-service 契约（demoservice.ServiceName）。
	DemoService DownstreamConfig `yaml:"demoservice"`
}

// DownstreamConfig is one downstream service's addressing settings.
// DownstreamConfig 是一个下游服务的寻址设置。
type DownstreamConfig struct {
	// BaseURL pins a fixed address such as http://10.0.0.5:8080. Empty means
	// Nacos discovery. A value without a scheme gets http:// in front.
	// BaseURL 钉死一个固定地址（如 http://10.0.0.5:8080）；留空表示走 Nacos
	// 发现。没有 scheme 时自动补 http://。
	BaseURL string `yaml:"base_url"`

	// Timeout bounds one call, including the resolution it triggers. It is
	// deliberately per-client rather than per-call so a slow downstream cannot
	// hold a worker tick indefinitely.
	// Timeout 限制单次调用（包含它触发的寻址）。它按客户端而不是按调用设置，
	// 慢下游因此无法无限期占住一个 worker tick。
	Timeout time.Duration `yaml:"timeout" default:"5s"`
}
