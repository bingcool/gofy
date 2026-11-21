// Package diagnostics serves the operational endpoints of a gofy process —
// Prometheus metrics and net/http/pprof — on a listener of their own.
//
// They deliberately do not share the application listener. pprof exposes heap
// contents, goroutine stacks and command lines, so it must never sit behind the
// same routing, middleware and public address as business traffic; metrics are
// scraped by an infrastructure component that has no business presenting a JWT.
// A separate port also keeps both reachable when the application is saturated,
// which is exactly when an operator needs them.
//
// 包 diagnostics 在独立监听上提供 gofy 进程的运维端点：Prometheus 指标与
// net/http/pprof。
//
// 它们刻意不共用应用监听。pprof 会暴露堆内容、协程栈与命令行，绝不能和业务流量
// 处在同一套路由、中间件与公网地址之后；指标由基础设施组件抓取，它既没有业务身份，
// 也不该为此持有 JWT。独立端口还保证应用被打满时两者仍然可达——而那正是运维最需要
// 它们的时刻。
package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// DefaultAddr is the loopback address the diagnostics server binds to when the
// configuration does not name one. Loopback is the safe default: an operator has
// to opt in explicitly before anything is reachable from another host.
// DefaultAddr 是配置未指定时诊断服务器绑定的回环地址。回环是安全默认值：运维必须
// 显式选择，其他主机才可能访问到任何端点。
const DefaultAddr = "127.0.0.1:6060"

// Config describes the diagnostics listener.
// Config 描述诊断监听。
type Config struct {
	// Enabled turns the listener on. When false no socket is opened at all.
	// Enabled 打开监听。为 false 时完全不创建 socket。
	Enabled bool `yaml:"enabled"`

	// Addr is the listen address, host:port. Defaults to 127.0.0.1:6060.
	// Prometheus scrapes from another Pod, so a container deployment normally
	// sets 0.0.0.0:6060 and lets the network policy do the isolation.
	// Addr 是监听地址 host:port，默认 127.0.0.1:6060。Prometheus 从别的 Pod 抓取，
	// 容器部署通常设为 0.0.0.0:6060，由网络策略负责隔离。
	Addr string `yaml:"addr" default:"127.0.0.1:6060"`

	// DisableMetrics omits /metrics. Both endpoints are on by default, so a
	// config that only turns Enabled on exposes both; the inverted flag avoids
	// the "enabled: true but everything still off" trap that plain booleans
	// produce in YAML.
	// DisableMetrics 关闭 /metrics。两个端点默认都开，因此只写 Enabled 的配置会
	// 同时暴露两者；反向开关避免了裸布尔在 YAML 里造成的"enabled: true 但什么都没开"。
	DisableMetrics bool `yaml:"disable_metrics"`

	// MetricsPath is where the scrape handler is mounted. Empty means /metrics.
	// It exists because a proxy or an existing scrape config may already own
	// the conventional path.
	// MetricsPath 是抓取 handler 的挂载路径，空表示 /metrics。留出这个字段是因为
	// 反向代理或既有的抓取配置可能已经占用了约定路径。
	MetricsPath string `yaml:"metrics_path"`

	// DisablePprof omits /debug/pprof/*. Turn it off where heap dumps are not
	// allowed to leave the process, for example in a regulated environment.
	// DisablePprof 关闭 /debug/pprof/*。在不允许堆转储离开进程的环境（例如受监管
	// 场景）应关掉它。
	DisablePprof bool `yaml:"disable_pprof"`

	// DisableHealth omits /health, the per-check detail endpoint. Readiness and
	// liveness stay on the application listener; this endpoint only carries the
	// detail an operator reads, so it may be turned off where the diagnostics port
	// is exposed to a wider network than the probes.
	// DisableHealth 关闭 /health（逐检查明细端点）。readiness 与 liveness 在应用监听上；
	// 这个端点只承载运维查看的明细，因此当代诊断端口暴露给比探针更宽的网络时可以关掉。
	DisableHealth bool `yaml:"disable_health"`

	// HealthPath is where the detail handler is mounted. Empty means /health. It
	// exists for the same reason as MetricsPath: an existing route or proxy may
	// already own the conventional path.
	// HealthPath 是明细 handler 的挂载路径，空表示 /health。它与 MetricsPath 同理：
	// 既有路由或代理可能已经占用了约定路径。
	HealthPath string `yaml:"health_path"`
}

// ErrNothingEnabled is returned when the listener is enabled but every endpoint
// is disabled: it would open a port that answers 404 to everything.
// ErrNothingEnabled 在监听开启但所有端点都关闭时返回：否则会打开一个对一切请求都
// 回 404 的端口。
var ErrNothingEnabled = errors.New("diagnostics: listener is enabled but metrics, pprof and health are all disabled")

// Validate checks the configuration without opening a socket.
// Validate 只校验配置，不创建 socket。
func (c Config) Validate() error {
	addr := strings.TrimSpace(c.Addr)
	if addr == "" {
		addr = DefaultAddr
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("diagnostics: invalid addr %q: %w", c.Addr, err)
	}
	// The rule is "all three off", not "metrics and pprof off": a health-only
	// listener is a legitimate deployment, and rejecting it would leave the
	// operator with no diagnostics port at all.
	// 规则是"三个端点全关"，而不是"metrics 与 pprof 关"：只开 health 是合法部署，
	// 拒绝它会让运维彻底没有诊断端口。
	if c.Enabled && c.DisableMetrics && c.DisablePprof && c.DisableHealth {
		return ErrNothingEnabled
	}
	if path := strings.TrimSpace(c.MetricsPath); path != "" && !strings.HasPrefix(path, "/") {
		return fmt.Errorf("diagnostics: metrics_path %q must start with /", c.MetricsPath)
	}
	if path := strings.TrimSpace(c.HealthPath); path != "" && !strings.HasPrefix(path, "/") {
		return fmt.Errorf("diagnostics: health_path %q must start with /", c.HealthPath)
	}
	return nil
}

func (c Config) withDefaults() Config {
	out := c
	out.Addr = strings.TrimSpace(out.Addr)
	if out.Addr == "" {
		out.Addr = DefaultAddr
	}
	return out
}

// Option customizes New.
// Option 自定义 New 的行为。
type Option func(*serverOptions)

type serverOptions struct {
	logger *zap.Logger
}

// WithLogger sets the logger used for serve failures and the exposure warning.
// WithLogger 设置用于记录 serve 失败与暴露警告的 logger。
func WithLogger(logger *zap.Logger) Option {
	return func(o *serverOptions) {
		if logger != nil {
			o.logger = logger
		}
	}
}

// Endpoints carries what the caller wants mounted. A nil Metrics handler is
// rejected when metrics are enabled, because a silently missing /metrics route
// looks exactly like a scrape that returns no series.
// Endpoints 携带调用方要挂载的内容。指标开启时 nil 的 Metrics handler 会被拒绝：
// 静默缺失的 /metrics 路由和"抓取回来没有序列"看起来完全一样。
type Endpoints struct {
	// MetricsPath is where the handler is mounted; empty means /metrics.
	// MetricsPath 是 handler 的挂载路径，空表示 /metrics。
	MetricsPath string
	// Metrics is the scrape handler, usually otel.Metrics.Handler().
	// Metrics 是抓取 handler，通常是 otel.Metrics.Handler()。
	Metrics http.Handler

	// HealthPath is where the detail handler is mounted; empty means /health.
	// HealthPath 是明细 handler 的挂载路径，空表示 /health。
	HealthPath string

	// Health is the per-check detail handler. A nil handler skips the route: the
	// hosts build it only when they have a registry, and a health-only listener
	// without one would otherwise fail to start.
	// Health 是逐检查明细 handler。nil 时跳过该路由：宿主只在有注册表时才构建它，否则
	// 只开 health 的监听会无法启动。
	Health http.Handler
}

// Server is the diagnostics listener. It is created by New and started by Start;
// a nil Server is not valid, but Shutdown on a server that was never started is
// a no-op.
// Server 是诊断监听。由 New 创建、Start 启动；nil 的 *Server 不合法，但对未启动的
// server 调用 Shutdown 是 no-op。
type Server struct {
	cfg    Config
	logger *zap.Logger

	mu       sync.Mutex
	listener net.Listener
	srv      *http.Server
	started  bool
}

// New validates cfg and returns a server that has not opened a socket yet.
// New 校验 cfg，返回尚未创建 socket 的 server。
func New(cfg Config, opts ...Option) (*Server, error) {
	var so serverOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&so)
		}
	}
	if so.logger == nil {
		so.logger = zap.NewNop()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg.withDefaults(), logger: so.logger}, nil
}

// Enabled reports whether the listener is configured to run.
// Enabled 报告监听是否配置为运行。
func (s *Server) Enabled() bool { return s != nil && s.cfg.Enabled }

// Addr returns the configured listen address.
// Addr 返回配置的监听地址。
func (s *Server) Addr() string {
	if s == nil {
		return DefaultAddr
	}
	if s.cfg.Addr == "" {
		return DefaultAddr
	}
	return s.cfg.Addr
}

// Handler builds the mux this server serves. It is exported for tests and for
// embedders that want to mount the endpoints somewhere else; Start uses it too,
// so the routes cannot drift between the two.
// Handler 构建本服务器使用的 mux。导出是为了测试，以及让嵌入方把端点挂到别处；
// Start 也用它，两条路径因此不会漂移。
func (s *Server) Handler(ep Endpoints) (http.Handler, error) {
	mux := http.NewServeMux()

	if !s.cfg.DisableMetrics {
		if ep.Metrics == nil {
			return nil, errors.New("diagnostics: metrics endpoint is enabled but its handler is nil")
		}
		path := strings.TrimSpace(ep.MetricsPath)
		if path == "" {
			path = strings.TrimSpace(s.cfg.MetricsPath)
		}
		if path == "" {
			path = "/metrics"
		}
		mux.Handle(path, ep.Metrics)
		// The formatted view reuses the scrape handler and parses its output;
		// it is for curl, not for Prometheus — scrapers must keep using path.
		// 格式化视图复用抓取 handler 并解析其输出；它是给 curl 用的，不是给
		// Prometheus——抓取器必须继续用 path。
		mux.Handle(path+"-format", prettyMetrics(ep.Metrics))
	}

	if !s.cfg.DisableHealth && ep.Health != nil {
		path := strings.TrimSpace(ep.HealthPath)
		if path == "" {
			path = strings.TrimSpace(s.cfg.HealthPath)
		}
		if path == "" {
			path = "/health"
		}
		mux.Handle(path, ep.Health)
	}

	if !s.cfg.DisablePprof {
		registerPprof(mux)
	}
	return mux, nil
}

// Start binds the configured address and serves in the background. A bind
// failure is returned to the caller, so a port conflict stops the application at
// startup instead of leaving an operator without the endpoints it was told to
// expect.
// Start 绑定配置地址并在后台提供服务。绑定失败返回给调用方，端口冲突因此在启动期
// 就让应用停下，而不是让运维在需要时发现端点并不存在。
func (s *Server) Start(ep Endpoints) error {
	if !s.Enabled() {
		return nil
	}
	handler, err := s.Handler(ep)
	if err != nil {
		return err
	}

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", s.Addr())
	if err != nil {
		return fmt.Errorf("diagnostics: listen %s: %w", s.Addr(), err)
	}

	srv := &http.Server{
		Handler: handler,
		// ReadHeaderTimeout bounds slow-header clients. ReadTimeout and
		// WriteTimeout stay unset because /debug/pprof/profile and trace stream
		// for as long as the profile duration, which is 30s by default, and a
		// write timeout would truncate the very artifact being collected.
		// ReadHeaderTimeout 限制慢头客户端。ReadTimeout 与 WriteTimeout 不设置，
		// 因为 /debug/pprof/profile 与 trace 在采样期间持续输出（默认 30s），
		// 写超时会截断正在收集的产物本身。
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	s.mu.Lock()
	s.listener = listener
	s.srv = srv
	s.started = true
	s.mu.Unlock()

	if !s.cfg.DisablePprof && !isLoopbackAddr(s.Addr()) {
		// Not fatal: metrics need a routable address in a container, and the
		// same listener carries pprof. Saying it once at startup is what lets
		// someone notice that heap dumps are reachable from the network.
		// 不致命：容器里指标需要一个可路由的地址，而 pprof 共用同一监听。启动时说一次，
		// 才能有人注意到堆转储可以从网络访问。
		s.logger.Warn("pprof is exposed on a non-loopback address",
			zap.String("addr", s.Addr()),
		)
	}

	s.logger.Info("diagnostics listening",
		zap.String("addr", s.Addr()),
		zap.Bool("metrics", !s.cfg.DisableMetrics),
		zap.Bool("pprof", !s.cfg.DisablePprof),
	)

	go func() {
		// http.ErrServerClosed is the normal shutdown path, not a failure.
		// http.ErrServerClosed 是正常关闭路径，不是失败。
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("diagnostics server stopped", zap.Error(err))
		}
	}()
	return nil
}

// BoundAddr returns the address actually bound, which matters when the config
// used port 0. Before Start it returns the configured address.
// BoundAddr 返回实际绑定的地址（配置用 0 端口时有意义）；Start 之前返回配置地址。
func (s *Server) BoundAddr() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.Addr()
}

// Shutdown stops the listener and waits for in-flight requests until ctx is
// done. Calling it on a server that never started is a no-op.
// Shutdown 停止监听并等待在途请求，直到 ctx 结束。对未启动的 server 调用是 no-op。
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	srv, started := s.srv, s.started
	s.srv, s.listener, s.started = nil, nil, false
	s.mu.Unlock()

	if !started || srv == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := srv.Shutdown(ctx); err != nil {
		// Close the listener even when the graceful path timed out; otherwise a
		// stuck pprof request keeps the port open past process shutdown.
		// 优雅路径超时也要关掉监听，否则一个卡住的 pprof 请求会让端口活过进程退出。
		_ = srv.Close()
		return fmt.Errorf("diagnostics: shutdown: %w", err)
	}
	return nil
}

// registerPprof mounts the standard handlers on a private mux.
//
// net/http/pprof also registers the same handlers on http.DefaultServeMux from
// its init, and an ordinary import runs that init exactly like a blank import
// does — no import form suppresses it. What keeps them off the wire is that
// nothing in this process serves DefaultServeMux: the diagnostics listener, the
// application server and the worker health endpoint each build their own mux.
// A future listener that serves DefaultServeMux would expose /debug/pprof on
// its own address, so it must not; the alternative is to drop this package and
// re-implement the handlers, which buys isolation at the cost of tracking the
// standard library's pprof wiring by hand.
// registerPprof 把标准 handler 挂到私有 mux。
//
// net/http/pprof 的 init 同样会把一组 handler 注册到 http.DefaultServeMux，而普通
// import 与空导入一样会执行该 init——没有任何 import 形式能跳过它。真正让这些端点
// 不出网的是：本进程没有任何监听使用 DefaultServeMux——诊断监听、应用 server 与
// worker 健康端各自新建 mux。将来若有人用 DefaultServeMux 提供服务，/debug/pprof
// 就会出现在那个地址上，因此不能这么做；另一条路是弃用本包、自行实现这些 handler，
// 那能换来隔离，代价是要手工跟进标准库的 pprof 接线。
func registerPprof(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// isLoopbackAddr reports whether addr is provably loopback-only. A hostname that
// cannot be resolved statically counts as non-loopback: the warning is cheaper
// than a missed one, and it is only a warning.
// isLoopbackAddr 报告 addr 是否确定为仅回环。无法静态解析的主机名按非回环处理：
// 多报一次警告的代价小于漏报，而且它只是警告。
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		// ":6060" binds every interface.
		// ":6060" 绑定所有接口。
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
