package nacos

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// MetadataInnerExternalBaseURI is the instance metadata key that carries a base
// URL reachable from outside the cluster. An instance IP is often a pod address
// that a laptop or the office network cannot route to, so a consumer resolving
// this instance can use the base URL instead. The value is this instance's own
// address, in the environment the configuration came from.
// MetadataInnerExternalBaseURI 是实例 metadata 的键，值为集群外可达的 base URL。
// 实例 IP 常常是笔记本或办公网路由不到的 Pod 地址，解析到该实例的调用方可以改用这个
// URL。值是本实例自己的地址，取的是配置文件来源的那套环境。
const MetadataInnerExternalBaseURI = "inner_external_base_uri"

// RegisterConfig describes how the application instance is published in
// Nacos. It maps to application.yaml nacos.service_register.
// RegisterConfig 描述应用实例如何发布到 Nacos，对应 application.yaml 的
// nacos.service_register。
type RegisterConfig struct {
	// Enabled maps to nacos.enable_nacos_register in application.yaml.
	// Enabled 对应 application.yaml 的 nacos.enable_nacos_register。
	Enabled bool

	// ServiceName is the registered service name, for example demo-service.
	// ServiceName 是注册的服务名，例如 demo-service。
	ServiceName string

	// IP is the advertised address. Empty triggers automatic detection;
	// NACOS_SERVICE_REGISTER_HOST and POD_IP take precedence when set.
	// IP 是向外公布的地址。留空时自动探测；若设置了 NACOS_SERVICE_REGISTER_HOST
	// 或 POD_IP，则优先使用它们。
	IP string

	// Port is the advertised port. Zero makes the App use its listener port;
	// NACOS_SERVICE_REGISTER_PORT takes precedence over both.
	// Port 是向外公布的端口。为 0 时由 App 使用自身监听端口；
	// NACOS_SERVICE_REGISTER_PORT 的优先级高于两者。
	Port int

	// Namespace and Group default to the connection namespace and
	// DEFAULT_GROUP.
	// Namespace 和 Group 默认取连接配置中的命名空间与 DEFAULT_GROUP。
	Namespace string
	Group     string

	// Cluster defaults to the Nacos default cluster when empty.
	// Cluster 为空时使用 Nacos 的默认集群。
	Cluster string

	// Weight defaults to 1.
	// Weight 默认值为 1。
	Weight float64

	// HeartbeatInterval is the initial lease renewal interval; Nacos can
	// adjust it through the heartbeat response. Default 5s.
	// HeartbeatInterval 是初始续约间隔，Nacos 可通过心跳响应调整它，默认 5s。
	HeartbeatInterval time.Duration

	// Metadata is stored with the instance and can carry version or protocol
	// hints for discovery clients.
	// Metadata 随实例一起保存，可携带版本或协议提示供服务发现客户端使用。
	Metadata map[string]string
}

func (c RegisterConfig) normalize(nacosNamespace string) RegisterConfig {
	c.ServiceName = strings.TrimSpace(c.ServiceName)
	c.IP = expandEnvPlaceholder(strings.TrimSpace(c.IP))
	c.Namespace = normalizeNamespace(firstNonEmpty(c.Namespace, envValue("NACOS_SERVICE_NAMESPACE_ID"), nacosNamespace))
	c.Group = strings.TrimSpace(c.Group)
	if c.Group == "" {
		c.Group = envValue("NACOS_SERVICE_GROUP_NAME")
	}
	if c.Group == "" {
		c.Group = DefaultGroup
	}
	c.Cluster = strings.TrimSpace(c.Cluster)
	if c.Weight <= 0 {
		c.Weight = 1
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 5 * time.Second
	}
	if c.HeartbeatInterval < time.Second {
		c.HeartbeatInterval = time.Second
	}
	if c.Metadata == nil {
		c.Metadata = map[string]string{}
	}
	return c
}

// SetInnerExternalBaseURI records uri as MetadataInnerExternalBaseURI. The value
// usually comes from a config section the assembly root reads, not from
// application.yaml, so it is applied after Load and therefore normalizes here:
// a trailing slash is dropped and a missing scheme becomes http://. An empty
// uri leaves the metadata untouched, so "not configured" never publishes an
// empty address that a consumer would then try to dial.
// SetInnerExternalBaseURI 把 uri 写进 MetadataInnerExternalBaseURI。该值通常来自装配根
// 读取的配置段而不是 application.yaml，因此在 Load 之后才写入，归一化也放在这里：去掉
// 末尾斜杠，缺少 scheme 时补 http://。uri 为空时不动 metadata，"没配"因此不会发布一个
// 空地址让调用方去拨。
func (c *RegisterConfig) SetInnerExternalBaseURI(uri string) {
	if c == nil {
		return
	}
	uri = strings.TrimSpace(uri)
	uri = strings.TrimRight(uri, "/")
	if uri == "" {
		return
	}
	if !strings.Contains(uri, "://") {
		uri = "http://" + uri
	}
	if c.Metadata == nil {
		c.Metadata = map[string]string{}
	}
	c.Metadata[MetadataInnerExternalBaseURI] = uri
}

func (c RegisterConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.ServiceName == "" {
		return fmt.Errorf("nacos: service_register.service_name is required")
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("nacos: invalid service_register.port %d", c.Port)
	}
	return nil
}

// Registrar publishes one instance and renews its lease until stopped.
// Registrar 发布一个实例并持续续约，直到被停止。
type Registrar struct {
	client   *Client
	cfg      RegisterConfig
	logger   *zap.Logger
	instance Instance

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewRegistrar builds a registrar from the Nacos connection config and the
// service_register block. The instance is not registered until Start.
// NewRegistrar 根据 Nacos 连接配置和 service_register 块创建注册器；实例在调用
// Start 之前不会注册。
func NewRegistrar(cfg Config, logger *zap.Logger) (*Registrar, error) {
	reg := cfg.Register.normalize(cfg.Namespace)
	if !reg.Enabled {
		return nil, ErrDisabled
	}
	if err := reg.validate(); err != nil {
		return nil, err
	}
	if v, ok := lookupEnv("NACOS_SERVICE_REGISTER_PORT"); ok {
		if port, err := strconv.Atoi(v); err == nil && port > 0 && port <= 65535 {
			reg.Port = port
		}
	}
	if reg.Port <= 0 || reg.Port > 65535 {
		return nil, fmt.Errorf("nacos: service_register.port is required")
	}
	client, err := NewNamingClient(cfg)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	ip := ResolveRegisterIP(reg.IP, client.cfg.Host)
	return &Registrar{
		client: client,
		cfg:    reg,
		logger: logger,
		instance: Instance{
			IP:          ip,
			Port:        reg.Port,
			Weight:      reg.Weight,
			Healthy:     true,
			Enabled:     true,
			Ephemeral:   true,
			ClusterName: reg.Cluster,
			ServiceName: reg.ServiceName,
			Group:       reg.Group,
			Namespace:   reg.Namespace,
			Metadata:    reg.Metadata,
		},
	}, nil
}

// Instance returns the published instance description.
// Instance 返回已发布的实例描述。
func (r *Registrar) Instance() Instance { return r.instance }

// Start registers the instance and starts the heartbeat loop. It is
// idempotent: a second call while running returns nil without re-registering.
// Start 注册实例并启动心跳循环。它是幂等的：运行期间重复调用直接返回 nil，
// 不会重复注册。
func (r *Registrar) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = true
	r.mu.Unlock()

	if err := r.client.RegisterInstance(ctx, r.instance); err != nil {
		r.mu.Lock()
		r.started = false
		r.mu.Unlock()
		return fmt.Errorf("nacos: register %s: %w", r.instance.ServiceName, err)
	}

	hbCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.mu.Lock()
	r.cancel = cancel
	r.done = done
	r.mu.Unlock()

	r.logger.Info("nacos service registered",
		zap.String("service", r.instance.ServiceName),
		zap.String("ip", r.instance.IP),
		zap.Int("port", r.instance.Port),
		zap.String("namespace", r.instance.Namespace),
		zap.String("group", r.instance.Group),
		zap.String("cluster", r.instance.ClusterName),
	)
	go r.heartbeat(hbCtx, done)
	return nil
}

// Stop cancels the heartbeat and deregisters the instance. It is idempotent.
// Stop 取消心跳并注销实例；该调用是幂等的。
func (r *Registrar) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = false
	cancel, done := r.cancel, r.done
	r.cancel, r.done = nil, nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}

	// Deregistration should still run when the caller's context was already
	// cancelled by shutdown.
	// 即使调用方的 ctx 已因关闭被取消，注销仍要执行，因此这里剥离取消信号
	// 并单独设置 5s 超时。
	deregCtx, deregCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer deregCancel()
	if err := r.client.DeregisterInstance(deregCtx, r.instance); err != nil {
		return fmt.Errorf("nacos: deregister %s: %w", r.instance.ServiceName, err)
	}
	r.logger.Info("nacos service deregistered",
		zap.String("service", r.instance.ServiceName),
		zap.String("ip", r.instance.IP),
		zap.Int("port", r.instance.Port),
	)
	return nil
}

func (r *Registrar) heartbeat(ctx context.Context, done chan struct{}) {
	defer close(done)

	interval := r.cfg.HeartbeatInterval
	light := false
	failures := 0
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		result, err := r.client.BeatInstance(ctx, r.instance, light)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			r.logger.Warn("nacos heartbeat failed",
				zap.String("service", r.instance.ServiceName),
				zap.Int("failures", failures),
				zap.Error(err),
			)
			// The lease may have expired after repeated failures: fall back to
			// a full beat, then register again if that does not help.
			// 连续失败后租约可能已过期：先退回完整心跳，若仍无效则重新注册。
			light = false
			if failures >= 2 {
				if regErr := r.client.RegisterInstance(ctx, r.instance); regErr != nil {
					r.logger.Error("nacos re-register failed", zap.Error(regErr))
				} else {
					r.logger.Info("nacos service re-registered", zap.String("service", r.instance.ServiceName))
					failures = 0
				}
			}
			continue
		}

		failures = 0
		light = result.LightBeatEnabled
		if result.ClientBeatInterval > 0 {
			interval = clampBeatInterval(result.ClientBeatInterval)
		}
	}
}

func clampBeatInterval(interval time.Duration) time.Duration {
	if interval < time.Second {
		return time.Second
	}
	if interval > 30*time.Second {
		return 30 * time.Second
	}
	return interval
}

// ResolveRegisterIP picks the address advertised to Nacos. Precedence:
// NACOS_SERVICE_REGISTER_HOST, POD_IP, configured IP, the local address used
// to reach the Nacos host, then any non-loopback IPv4.
// ResolveRegisterIP 选择向 Nacos 公布的地址，优先级依次为：
// NACOS_SERVICE_REGISTER_HOST、POD_IP、配置的 IP、访问 Nacos 所用的本机地址，
// 最后是任意非回环 IPv4 地址。
func ResolveRegisterIP(configured, nacosHost string) string {
	if v, ok := lookupEnv("NACOS_SERVICE_REGISTER_HOST"); ok {
		if ip := parseIPOrHost(v); ip != "" {
			return ip
		}
	}
	if v, ok := lookupEnv("POD_IP"); ok {
		if ip := net.ParseIP(v); ip != nil && !ip.IsLoopback() {
			return ip.String()
		}
	}
	if ip := parseIPOrHost(configured); ip != "" {
		return ip
	}
	if ip := outboundIP(nacosHost); ip != "" {
		return ip
	}
	if ip := firstNonLoopbackIPv4(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

func parseIPOrHost(value string) string {
	value = expandEnvPlaceholder(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	addrs, err := net.LookupHost(value)
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip != nil {
			return ip.String()
		}
	}
	return ""
}

func outboundIP(nacosHost string) string {
	host := strings.TrimSpace(nacosHost)
	if host == "" || host == "0.0.0.0" {
		return ""
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, "8848"), 300*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsLoopback() {
		return addr.IP.String()
	}
	return ""
}

func firstNonLoopbackIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}

// expandEnvPlaceholder resolves ${VAR} in configuration values; unset
// variables become empty, matching the swoolefy application.yaml behavior.
// expandEnvPlaceholder 解析配置值中的 ${VAR}；未设置的变量替换为空串，与
// swoolefy 的 application.yaml 行为一致。
func expandEnvPlaceholder(value string) string {
	if !strings.Contains(value, "${") {
		return value
	}
	return strings.TrimSpace(os.Expand(value, func(key string) string {
		v, _ := lookupEnv(key)
		return v
	}))
}

func envValue(key string) string {
	v, _ := lookupEnv(key)
	return v
}
