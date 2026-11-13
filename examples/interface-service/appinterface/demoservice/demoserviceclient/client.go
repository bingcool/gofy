// Package demoserviceclient is the typed API client for the demo service.
//
// Files mirror the route descriptors under demoservice/orderroute: each
// *_route.go has a matching *_client.go, so a route and its client method stay
// next to each other conceptually without sharing a file:
//
//	order_route.go   -> order_client.go
//	config_route.go  -> config_client.go
//
// client.go owns the shared Client type and its three addressing modes.
// 包 demoserviceclient 是 demo 服务的类型化 API Client。文件与
// demoservice/orderroute 下的路由描述符一一对应：每个 *_route.go 都有匹配的
// *_client.go，路由与对应的 Client 方法概念上相邻但不共享文件。client.go
// 承载共享的 Client 类型及其三种寻址方式。
package demoserviceclient

import (
	"errors"
	"strings"

	"github.com/bingcool/gofy-interface-api/client"
	"github.com/yourorg/interface-service/appinterface/demoservice"
)

// Client wraps the generic client with demo-service methods.
//
// demo-service can be addressed in three ways, mirroring
// interface-api-service BaseClientApi::makeService:
//
//	NewWithBaseURL("10.0.0.5:8080") // fixed domain or IP, never contacts Nacos
//	NewWithResolver(resolver)       // Nacos discovery by ServiceName
//	New(c)                          // custom client: timeout, token, transport
//
// A fixed BaseURL takes precedence over a resolver, so the first mode performs
// no service discovery even if the resolver also knows ServiceName.
// Client 在通用 client 之上封装 demo-service 的方法。demo-service 支持三种寻址
// 方式，与 interface-api-service 的 BaseClientApi::makeService 保持一致：固定
// 域名或 IP 时不访问 Nacos；按 ServiceName 做 Nacos 发现；或注入自定义 client
// （超时、token、transport）。固定 BaseURL 优先于 resolver，因此第一种方式即使
// resolver 也知道 ServiceName 也不做服务发现。
type Client struct {
	c *client.Client
}

// New wraps an existing client. Build it with client.Config{BaseURL: ...} to
// call a fixed domain or IP, or with client.Config{Service: ServiceName,
// Resolver: ...} to discover instances; it is also the way to inject a custom
// *http.Client, timeout or token source.
// New 包装一个已构建好的 client：用 client.Config{BaseURL: ...} 调固定域名或 IP，
// 用 client.Config{Service: ServiceName, Resolver: ...} 走服务发现；需要注入
// 自定义 *http.Client、超时或 token 来源时也用它。
func New(c *client.Client) *Client { return &Client{c: c} }

// NewWithResolver builds a demo-service client that discovers
// demoservice.ServiceName through resolver, so calls follow the registered
// instances instead of a fixed host. The resolver owns caching and load
// balancing; this client re-resolves after a transport error, so a retry can
// land on a different instance.
// NewWithResolver 构造通过 resolver 发现 demoservice.ServiceName 的客户端，调用
// 跟随已注册实例而不是固定主机。缓存与负载均衡由 resolver 负责；本 client 在
// transport 错误后重新选址，因此重试可能落到不同实例。
func NewWithResolver(resolver client.Resolver) (*Client, error) {
	c, err := client.New(client.Config{Service: demoservice.ServiceName, Resolver: resolver})
	if err != nil {
		return nil, err
	}
	return New(c), nil
}

// NewWithBaseURL builds a demo-service client that always calls baseURL, for
// example "http://10.0.0.5:8080" or "orders.example.com". A missing scheme
// defaults to http. Nacos is not contacted in this mode, and retries go to the
// same address.
// NewWithBaseURL 构造始终调用 baseURL 的客户端，例如 "http://10.0.0.5:8080"。
// 缺少 scheme 时默认 http；该模式不访问 Nacos，重试也发往同一地址。
func NewWithBaseURL(baseURL string) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, errors.New("demoserviceclient: base URL is required")
	}
	// Accept "host:port" and bare hosts; client.Config.BaseURL requires a
	// scheme, so add the same default the rest of gofy uses.
	// 接受 "host:port" 与裸主机名；client.Config.BaseURL 要求带 scheme，因此
	// 补上与 gofy 其他部分一致的默认值 http。
	if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	// BaseURL wins over Service: the name is kept for symmetry with the other
	// constructors, but no resolver is involved.
	// BaseURL 优先于 Service：保留名字只为与其他构造器对称，此处不涉及
	// resolver，也不会做服务发现。
	c, err := client.New(client.Config{BaseURL: baseURL, Service: demoservice.ServiceName})
	if err != nil {
		return nil, err
	}
	return New(c), nil
}

// NewWithBaseURL builds a demo-service client that always calls baseURL, for
// example "http://10.0.0.5:8080" or "orders.example.com". A missing scheme
// defaults to http. Nacos is not contacted in this mode, and retries go to the
// same address.
// NewWithBaseURL 构造始终调用 baseURL 的客户端，例如 "https://10.0.0.5:8080"。
// 缺少 scheme 时默认 https；该模式不访问 Nacos，重试也发往同一地址。
func NewWithBaseURLSSl(baseURL string) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, errors.New("demoserviceclient: base URL is required")
	}
	// Accept "host:port" and bare hosts; client.Config.BaseURL requires a
	// scheme, so add the same default the rest of gofy uses.
	// 接受 "host:port" 与裸主机名；client.Config.BaseURL 要求带 scheme，因此
	// 补上与 gofy 其他部分一致的默认值 http。
	if !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	// BaseURL wins over Service: the name is kept for symmetry with the other
	// constructors, but no resolver is involved.
	// BaseURL 优先于 Service：保留名字只为与其他构造器对称，此处不涉及
	// resolver，也不会做服务发现。
	c, err := client.New(client.Config{BaseURL: baseURL, Service: demoservice.ServiceName})
	if err != nil {
		return nil, err
	}
	return New(c), nil
}
