// Package serviceclients builds the typed clients this process calls
// downstream, and owns their lifetime.
//
// It sits at the assembly root on purpose: the resolver is process-scoped (its
// cache, round-robin cursor and singleflight table must survive a config
// reload), while the business modules only ever see the narrow interface they
// call. The addressing rule has exactly one home here — a configured base_url
// pins an address, otherwise an enabled Nacos resolves the contract's service
// name, otherwise the client reports ErrNotConfigured instead of pretending to
// work.
// 包 serviceclients 构造本进程调用下游用的类型化客户端，并持有它们的生命周期。
//
// 它放在装配根是刻意的：resolver 是进程级的（缓存、轮询游标与单飞表必须跨配置热更新
// 存活），而业务模块只看到自己调用的那一个窄接口。寻址规则只有这一处落点——配置了
// base_url 就钉死地址；否则 Nacos 启用时按契约服务名解析；两者都没有就返回
// ErrNotConfigured，而不是装作能用。
package serviceclients

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	nacosadapter "github.com/bingcool/gofy-adapter/client/nacos"
	gclient "github.com/bingcool/gofy-interface-api/client"
	nacosconfig "github.com/bingcool/gofy/support/nacos"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/demoserviceclient"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
)

// ErrNotConfigured is returned by the placeholder client when neither a base
// URL nor an enabled Nacos is available. It is a configuration error, not a
// transport error, so callers can tell the two apart.
// ErrNotConfigured 在既没有 base_url、Nacos 也未启用时由占位客户端返回。它是配置
// 错误而不是传输错误，调用方因此能区分两者。
var ErrNotConfigured = errors.New("serviceclients: demoservice is not configured: set clients.demoservice.base_url or enable nacos")

// DemoService is the part of demoserviceclient.Client the demo calls. Keeping
// the interface here, instead of handing out the concrete client, is what lets
// an unconfigured process still build and fail with a clear error at call time.
// DemoService 是 demo 实际用到的 demoserviceclient.Client 子集。把接口留在这里、
// 而不是直接发放具体客户端，未配置的进程才能照常启动，并在调用时给出明确错误。
type DemoService interface {
	GetConfig(ctx context.Context, req *orderrequest.GetConfigRequest, opts ...gclient.CallOption) (*orderresponse.GetConfigResponse, error)
}

type unconfigured struct{}

func (unconfigured) GetConfig(context.Context, *orderrequest.GetConfigRequest, ...gclient.CallOption) (*orderresponse.GetConfigResponse, error) {
	return nil, ErrNotConfigured
}

// Clients owns every downstream client of this process.
// Clients 持有本进程的全部下游客户端。
type Clients struct {
	demo     DemoService
	http     *gclient.Client
	resolver *nacosadapter.Resolver
	source   string
}

// New builds the clients. A nil or empty cfg yields the not-configured
// placeholder; the returned Clients is always usable, so callers never need a
// nil check.
// New 构造客户端。cfg 为空时得到"未配置"占位实现；返回的 Clients 始终可用，
// 调用方不需要判空。
func New(cfg config.ClientsConfig, nacosCfg nacosconfig.Config) (*Clients, error) {
	timeout := cfg.DemoService.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	base := strings.TrimSpace(cfg.DemoService.BaseURL)

	// Explicit address wins: it is the local-development and canary path and
	// must not depend on the registry being reachable.
	// 显式地址优先：它是本地开发与灰度直连的路径，不能依赖注册中心可达。
	if base != "" {
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		c, err := gclient.New(gclient.Config{
			BaseURL: base,
			Service: demoservice.ServiceName,
			Timeout: timeout,
		})
		if err != nil {
			return nil, fmt.Errorf("serviceclients: demoservice base_url: %w", err)
		}
		return &Clients{
			demo:   demoserviceclient.New(c),
			http:   c,
			source: "base_url " + base,
		}, nil
	}

	if !nacosCfg.Enabled {
		return &Clients{demo: unconfigured{}, source: "not configured"}, nil
	}

	resolver, err := nacosadapter.NewFromConfig(nacosCfg)
	if err != nil {
		return nil, fmt.Errorf("serviceclients: demoservice resolver: %w", err)
	}
	c, err := gclient.New(gclient.Config{
		Service:  demoservice.ServiceName,
		Resolver: resolver,
		Timeout:  timeout,
	})
	if err != nil {
		_ = resolver.Close()
		return nil, fmt.Errorf("serviceclients: demoservice client: %w", err)
	}
	return &Clients{
		demo:     demoserviceclient.New(c),
		http:     c,
		resolver: resolver,
		source:   nacosSource(nacosCfg),
	}, nil
}

// DemoService returns the demoservice client. A nil Clients still answers with
// the placeholder, so a container built in a test cannot panic here.
// DemoService 返回 demoservice 客户端。nil Clients 也会给出占位实现，测试里构造的
// 容器不会在这里 panic。
func (c *Clients) DemoService() DemoService {
	if c == nil || c.demo == nil {
		return unconfigured{}
	}
	return c.demo
}

// Source describes how the clients are addressed, for the one startup log line
// that answers "why did it call that address".
// Source 描述客户端的寻址方式，供启动时那一行日志回答"为什么打到了那个地址"。
func (c *Clients) Source() string {
	if c == nil || c.source == "" {
		return "not configured"
	}
	return c.source
}

// Close releases the resolver's and the client's idle connections. It is called
// once, after the application loop returns.
// Close 释放 resolver 与客户端的空闲连接，在应用主循环返回后调用一次。
func (c *Clients) Close() error {
	if c == nil {
		return nil
	}
	var errs []error
	if c.resolver != nil {
		errs = append(errs, c.resolver.Close())
	}
	if c.http != nil {
		errs = append(errs, c.http.Close())
	}
	return errors.Join(errs...)
}

// nacosSource renders the connection and discovery scope actually used. The
// namespace and group are the two values that, when wrong, produce "no
// instances" with no other symptom, so they belong in the log line.
// nacosSource 渲染实际使用的连接与发现范围。namespace 与 group 配错时唯一症状就是
// "查不到实例"，因此它们必须出现在日志里。
func nacosSource(cfg nacosconfig.Config) string {
	addr := cfg.Addr
	if addr == "" {
		addr = "http://" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	}
	namespace := cfg.Discovery.Namespace
	if namespace == "" {
		namespace = "public"
	}
	return fmt.Sprintf("nacos %s namespace=%s group=%s", addr, namespace, cfg.Discovery.Group)
}
