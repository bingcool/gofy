package config

import (
	"strings"
	"time"
)

// DefaultClientTimeout applies when the configuration leaves timeout empty.
// DefaultClientTimeout 在配置未写 timeout 时生效。
const DefaultClientTimeout = 5 * time.Second

// ClientConfig describes this service's own HTTP endpoint as seen from another
// environment, plus the timeout for the calls it makes by service name.
//
// The value is deliberately not a table of downstream addresses. A call to
// another service asks Nacos for that service's instances and can read the
// address from the resolved instance's metadata; the one address that cannot be
// discovered is your own, because what a pod registers is a pod IP that a laptop
// or the office network cannot route to. So config.yaml carries exactly one base
// URL — this service's address in the environment the configuration came from —
// and it has two consumers:
//
//  1. registration writes it into the instance metadata under
//     inner_external_base_uri, so a caller resolving this instance can use it in
//     place of the pod IP;
//  2. this process's own client uses it as the degraded target when the call
//     through discovery fails. That is what makes local development work without
//     starting the service locally: pull dev's configuration, fail to reach the
//     unstarted local instance, fall back to dev's deployment.
//
// Empty means neither: no metadata key, no fallback.
//
// The single value works because the demo calls exactly one service name, and
// that name is its own. A service calling several downstreams cannot share one
// URL for all of them — the fallback would have to be configured per downstream,
// which is why it is not shaped as a per-service map here.
// ClientConfig 描述本服务自己的 HTTP 端点（从别的环境看过来），以及按服务名调用时的超时。
//
// 它刻意不是"下游地址表"。调别的服务时服务名交给 Nacos 解析，地址还能从解析到的实例
// metadata 上取；唯一发现不了的是自己——Pod 注册的是办公网路由不到的 Pod IP。因此
// config.yaml 只存一个 base URL：本服务在"配置来源那套环境"里的地址，它有两个使用方：
//
//  1. 服务注册时写进实例 metadata 的 inner_external_base_uri，解析到本实例的调用方
//     可以用它替代 Pod IP；
//  2. 本进程自己的客户端在按服务名调用失败时用它再试一次。本地开发因此不必启动本地
//     服务：拉 dev 的配置，打没起的本地实例失败，降级到 dev 的部署。
//
// 留空表示两者都不做：不写 metadata，也不降级。
//
// 之所以一个值够用，是因为 demo 只按一个服务名调用，而这个服务名就是它自己。一个服务
// 调多个下游时不能共用一个地址——兜底必须按下游分别配置，这也是它没有被做成"每个下游
// 一条"的原因。
type ClientConfig struct {
	// InnerExternalBaseURI is this service's address, for example
	// http://demo-service-dev.example.com:8080. A value without a scheme gets
	// http:// in front; a trailing slash is dropped.
	// InnerExternalBaseURI 是本服务的地址，例如
	// http://demo-service-dev.example.com:8080。没有 scheme 时补 http://，末尾
	// 斜杠会被去掉。
	InnerExternalBaseURI string `yaml:"inner_external_base_uri"`

	// Timeout bounds one call, including the resolution it triggers. A degraded
	// call is a second call, so the worst case is about twice this value.
	// Timeout 限制单次调用（含它触发的寻址）。降级是第二次调用，最坏耗时约为它的两倍。
	Timeout time.Duration `yaml:"timeout"`
}

// Normalize applies the rules both consumers share: a missing scheme becomes
// http, a trailing slash is dropped, a zero timeout means the default. The
// registrar and the client therefore read the same value and cannot disagree
// about what "the address of this service" is.
// Normalize 应用两个使用方共享的规则：缺 scheme 补 http、去掉末尾斜杠、零超时取默认
// 值。注册器与客户端因此读的是同一个值，不会对"本服务的地址"有两种理解。
func (c *ClientConfig) Normalize() {
	c.InnerExternalBaseURI = strings.TrimSpace(c.InnerExternalBaseURI)
	c.InnerExternalBaseURI = strings.TrimRight(c.InnerExternalBaseURI, "/")
	if c.InnerExternalBaseURI != "" && !strings.Contains(c.InnerExternalBaseURI, "://") {
		c.InnerExternalBaseURI = "http://" + c.InnerExternalBaseURI
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultClientTimeout
	}
}
