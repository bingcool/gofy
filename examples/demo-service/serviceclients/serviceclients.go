// Package serviceclients builds the typed clients this process calls by service
// name, and owns their lifetime.
//
// It sits at the assembly root on purpose: the resolver is process-scoped (its
// cache, round-robin cursor and singleflight table must survive a config
// reload), while the business modules only ever see the narrow interface they
// call. The addressing rule has exactly one home here:
//
//   - Nacos enabled: resolve the contract's service name through the resolver;
//   - plus client.inner_external_base_uri (the service's own deployment address
//     in the environment this configuration came from) as the degraded target
//     when that call fails;
//   - Nacos disabled: use that base URL directly, which is the offline and CI
//     path;
//   - neither: report ErrNotConfigured at call time instead of pretending to
//     work.
//
// 包 serviceclients 构造本进程按服务名调用的类型化客户端，并持有它们的生命周期。
//
// 它放在装配根是刻意的：resolver 是进程级的（缓存、轮询游标与单飞表必须跨配置热更新
// 存活），而业务模块只看到自己调用的那一个窄接口。寻址规则只有这一处落点：
//
//   - Nacos 启用：按契约里的服务名通过 resolver 解析；
//   - 再加上 client.inner_external_base_uri（本服务在"配置来源那套环境"里的部署地址）
//     作为调用失败时的降级目标；
//   - Nacos 未启用：直接用它，这是离线与 CI 的路径；
//   - 两者都没有：调用时返回 ErrNotConfigured，而不是装作能用。
package serviceclients

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	nacosadapter "github.com/bingcool/gofy-adapter/client/nacos"
	gclient "github.com/bingcool/gofy-interface-api/client"
	nacosconfig "github.com/bingcool/gofy/support/nacos"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/interface-service/appinterface/demoservice"
	"github.com/yourorg/interface-service/appinterface/demoservice/demoserviceclient"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
)

// ErrNotConfigured is returned by the placeholder client when neither an
// enabled Nacos nor an inner_external_base_uri is available. It is a
// configuration error, not a transport error, so callers can tell the two
// apart — and it is deliberately not degraded, because there is nowhere to
// degrade to.
// ErrNotConfigured 在 Nacos 未启用、也没有 inner_external_base_uri 时由占位客户端
// 返回。它是配置错误而不是传输错误，调用方因此能区分两者——它也刻意不触发降级，因为
// 没有可降级的目标。
var ErrNotConfigured = errors.New("serviceclients: demoservice is not configured: set client.inner_external_base_uri or enable nacos")

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

// Clients owns every downstream client of this process. A nil *Clients answers
// like a process with no downstream at all, so every method stays callable.
// Clients 持有本进程的全部下游客户端。nil *Clients 的行为等同于一个没有任何下游的
// 进程，因此每个方法都必须仍可调用。
type Clients struct {
	demo        DemoService
	http        []*gclient.Client
	resolver    *nacosadapter.Resolver
	source      string
	fallbackURL string
	timeout     time.Duration

	// lg arrives after construction: the clients are built before the
	// application exists, and only the application owns the configured logger.
	// It is held as a getter, not a logger, because App.Logger() answers a no-op
	// logger until Execute builds the real one — storing that value would
	// swallow every degradation without a trace.
	// lg 在构造之后才到位：客户端先于 application 构建，而配置好的 logger 属于
	// application。它保存的是 getter 而不是 logger：Execute 之前 App.Logger() 返回
	// no-op logger，把这个值存下来会让每次降级都无声无息。
	lgMu sync.RWMutex
	lg   func() *zap.Logger
}

// New builds the clients. An empty configuration yields the not-configured
// placeholder; the returned Clients is always usable, so callers never need a
// nil check.
// New 构造客户端。配置为空时得到"未配置"占位实现；返回的 Clients 始终可用，调用方
// 不需要判空。
func New(cfg config.ClientConfig, nacosCfg nacosconfig.Config) (*Clients, error) {
	// Normalize here as well as at Load: this constructor is also called from
	// tests and scripts with a hand-built struct, and "10.0.0.5:8080" without a
	// scheme must not become a different address depending on the entry point.
	// 这里也归一化一次：测试与脚本会用手工构造的结构体调用它，没有 scheme 的
	// "10.0.0.5:8080" 不能因为入口不同而变成另一个地址。
	cfg.Normalize()

	timeout := cfg.Timeout
	fallbackURL := strings.TrimSpace(cfg.InnerExternalBaseURI)

	clients := &Clients{source: "not configured", fallbackURL: fallbackURL, timeout: timeout}

	// Without Nacos the base URL is not a fallback but the only address there
	// is: offline development, CI and a canary all take this path.
	// 没有 Nacos 时这个地址不是兜底而是唯一地址：离线开发、CI 与灰度都走这里。
	if !nacosCfg.Enabled {
		if fallbackURL == "" {
			clients.demo = unconfigured{}
			return clients, nil
		}
		typed, raw, err := newTyped(nil, fallbackURL, timeout)
		if err != nil {
			return nil, fmt.Errorf("serviceclients: demoservice inner_external_base_uri: %w", err)
		}
		clients.demo = typed
		clients.http = append(clients.http, raw)
		clients.source = "inner_external_base_uri " + fallbackURL
		return clients, nil
	}

	resolver, err := nacosadapter.NewFromConfig(nacosCfg)
	if err != nil {
		return nil, fmt.Errorf("serviceclients: demoservice resolver: %w", err)
	}
	// A lookup answered by the deployment group is the one event nobody can see
	// afterwards: the call succeeds, but against another environment. The
	// resolver reports it once per refresh; without this line it would be silent.
	// 由部署分组作答的解析是事后唯一看不见的事件：调用成功了，但打的是另一个环境。resolver
	// 每次刷新上报一次；没有这一行，它就是静默的。
	resolver.SetOnFallback(func(ev nacosadapter.FallbackEvent) {
		lg := clients.logger()
		if lg == nil {
			return
		}
		lg.Warn("demoservice resolved in the deployment group",
			zap.String("service", ev.Service),
			zap.String("local_group", ev.LocalGroup),
			zap.String("fallback_group", ev.FallbackGroup),
			zap.String("base_url", ev.BaseURL),
			zap.Bool("from_metadata", ev.FromMetadata),
		)
	})
	primary, raw, err := newTyped(resolver, "", timeout)
	if err != nil {
		_ = resolver.Close()
		return nil, fmt.Errorf("serviceclients: demoservice client: %w", err)
	}
	clients.demo = primary
	clients.http = append(clients.http, raw)
	clients.resolver = resolver
	clients.source = nacosSource(nacosCfg)

	if fallbackURL == "" {
		return clients, nil
	}
	// Both addresses exist, so a failed lookup or an unreachable instance can
	// be retried once against the deployment address. Only reads are wrapped:
	// retrying a write against another instance is not a fallback, it is a
	// second write.
	// 两个地址都在，因此查不到实例或实例不可达时可以改打部署地址再试一次。只有读操作
	// 会被包一层：把写操作重试到另一个实例不是兜底，是写两次。
	fallback, rawFallback, err := newTyped(nil, fallbackURL, timeout)
	if err != nil {
		_ = clients.Close()
		return nil, fmt.Errorf("serviceclients: demoservice inner_external_base_uri: %w", err)
	}
	clients.http = append(clients.http, rawFallback)
	clients.demo = &degradable{primary: primary, fallback: fallback, owner: clients}
	clients.source = clients.source + " (fallback " + fallbackURL + ")"
	return clients, nil
}

// newTyped builds one typed client. Exactly one of resolver and baseURL is set:
// an empty baseURL means "resolve the service name", which is what the
// contract's demoservice.ServiceName is for.
// newTyped 构造一个类型化客户端。resolver 与 baseURL 二选一：baseURL 为空表示按服务名
// 解析，这也是契约里 demoservice.ServiceName 的用途。
func newTyped(resolver *nacosadapter.Resolver, baseURL string, timeout time.Duration) (*demoserviceclient.Client, *gclient.Client, error) {
	c, err := gclient.New(gclient.Config{
		BaseURL:  baseURL,
		Service:  demoservice.ServiceName,
		Resolver: resolver,
		Timeout:  timeout,
	})
	if err != nil {
		return nil, nil, err
	}
	return demoserviceclient.New(c), c, nil
}

// SetLogger installs the getter for the logger used to report a degraded call.
// It takes a getter rather than a logger on purpose: the clients are built
// before the application, and App.Logger() is a no-op until Execute. Called at
// log time, the getter yields the real logger without the client knowing when
// it appeared.
// SetLogger 设置"取 logger"的 getter，用于报告降级。它要 getter 而不是 logger 是
// 有意的：客户端先于 application 构建，而 Execute 之前 App.Logger() 是 no-op。在打日志
// 的那一刻才调用 getter，客户端因此不需要知道 logger 是什么时候就位的。
func (c *Clients) SetLogger(fn func() *zap.Logger) {
	if c == nil {
		return
	}
	c.lgMu.Lock()
	c.lg = fn
	c.lgMu.Unlock()
}

func (c *Clients) logger() *zap.Logger {
	if c == nil {
		return nil
	}
	c.lgMu.RLock()
	fn := c.lg
	c.lgMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
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

// CallBudget is the time a caller should allow for one logical call that may
// degrade: two full attempts plus a one-second margin for the fallback's
// lookup and connect. A shorter caller deadline is not just tighter — the
// first attempt consumes it, so shouldDegrade sees a cancelled context and the
// fallback never runs.
// CallBudget 是允许一次可能降级的逻辑调用使用的时长：两次完整尝试，再加一秒余量
// 给兜底的寻址与建连。调用方 deadline 更短不只是更紧——第一次尝试就会耗尽它，
// shouldDegrade 看到已取消的 context，兜底根本不会执行。
func (c *Clients) CallBudget() time.Duration {
	if c == nil || c.timeout <= 0 {
		return 2*config.DefaultClientTimeout + time.Second
	}
	return 2*c.timeout + time.Second
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

// Close releases the resolver's and the clients' idle connections. It is called
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
	for _, client := range c.http {
		errs = append(errs, client.Close())
	}
	return errors.Join(errs...)
}

// degradable retries a failed read once against the service's own deployment
// address. It exists for one scenario: a local process that resolved an
// instance it cannot reach — usually the instance it would have started itself
// — can still serve the request from the environment the configuration came
// from.
// degradable 在调用失败后改打本服务自己的部署地址，重试一次。它只为一个场景存在：
// 本地进程解析到一个它到不了的实例（通常就是它本该自己起的那个），仍能用配置来源那套
// 环境把请求处理掉。
type degradable struct {
	primary  DemoService
	fallback DemoService
	owner    *Clients
}

func (d *degradable) GetConfig(ctx context.Context, req *orderrequest.GetConfigRequest, opts ...gclient.CallOption) (*orderresponse.GetConfigResponse, error) {
	res, err := d.primary.GetConfig(ctx, req, opts...)
	if err == nil || !shouldDegrade(ctx, err) {
		return res, err
	}
	if lg := d.owner.logger(); lg != nil {
		lg.Warn("demoservice call degraded to inner_external_base_uri",
			zap.String("fallback", d.owner.fallbackURL),
			zap.Error(err),
		)
	}
	res, fallbackErr := d.fallback.GetConfig(ctx, req, opts...)
	if fallbackErr != nil {
		// Both errors matter: the first says why the discovered address was
		// not used, the second why the fallback failed too. Joining them keeps
		// errors.Is working for either one.
		// 两个错误都要留：前一个说明为什么没用解析到的地址，后一个说明兜底为什么也
		// 失败。用 Join 合并，errors.Is 对两者都仍然有效。
		return nil, errors.Join(err, fmt.Errorf("degraded to %s: %w", d.owner.fallbackURL, fallbackErr))
	}
	return res, nil
}

// shouldDegrade decides whether a failure is worth a second attempt somewhere
// else. The rule is a whitelist, not "anything that is not an HTTP answer":
// only a transport failure or a failed lookup can be fixed by another address.
// A request that never left the process — encoding, validation, credential
// errors — fails the same way twice and only turns the error into a two-layer
// join, so it is deliberately excluded. A response that arrived, even a 500, is
// excluded too: the target is up and answered, another opinion is not wanted.
// shouldDegrade 判断一次失败是否值得换个地址再试。规则是白名单，而不是"凡不是 HTTP
// 答复就算"：只有传输失败或寻址失败可能被另一个地址解决。根本没发出进程的失败——编码、
// 校验、凭据错误——第二次会以同样方式失败，只会把错误变成两层 Join，因此刻意排除。
// 已经到达的响应（哪怕是 500）同样排除：目标活着并答复了，不需要第二个结论。
func shouldDegrade(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	// A cancelled caller is not a reason to spend another timeout: the work is
	// already unwanted. This check comes first because cancellation surfaces
	// through the transport error below.
	// 调用方已取消不是再花一个超时的理由：这次工作已经没人要了。这个判断必须在前，
	// 因为取消会以传输错误的形式冒出来。
	if ctx.Err() != nil {
		return false
	}
	// Connection refused, DNS failure, connect/read/write timeout. *url.Error
	// satisfies net.Error, so it covers the client's own deadline as well.
	// 连接拒绝、DNS 失败、连接/读写超时。*url.Error 满足 net.Error，因此客户端自身
	// 的 deadline 也在其中。
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// A deadline that surfaced without a net.Error (for example a resolver
	// timeout wrapped by the caller's context).
	// 没有以 net.Error 形式出现的 deadline（例如被调用方 context 包装的寻址超时）。
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Discovery failed: wrong namespace/group, every instance offline, or the
	// stale window ran out. This is the local-development path the fallback
	// exists for, so it must stay in the set.
	// 寻址失败：namespace/group 配错、实例全下线、或 stale 窗口用尽。这正是兜底存在的
	// 本地开发场景，必须留在集合里。
	if errors.Is(err, nacosadapter.ErrNoInstances) || errors.Is(err, nacosadapter.ErrStaleExpired) {
		return true
	}
	return false
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
	source := fmt.Sprintf("nacos %s namespace=%s group=%s", addr, namespace, cfg.Discovery.Group)
	// The fallback group is only reachable when the switch is on and the two
	// groups differ; showing it otherwise would suggest a fallback that cannot
	// happen.
	// 只有开关打开且两个分组不同时才可能兜底；其它情况把它写出来，会让人以为存在一条
	// 走不到的兜底路径。
	if cfg.Discovery.LocalAutoSwitch &&
		cfg.Register.Group != "" &&
		cfg.Register.Group != cfg.Discovery.Group {
		source += " fallback_group=" + cfg.Register.Group
	}
	return source
}
