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

// RegisterConfig describes how the application instance is published in
// Nacos. It maps to application.yaml nacos.service_register.
type RegisterConfig struct {
	// Enabled maps to nacos.enable_nacos_register in application.yaml.
	Enabled bool

	// ServiceName is the registered service name, for example demo-service.
	ServiceName string

	// IP is the advertised address. Empty triggers automatic detection;
	// NACOS_SERVICE_REGISTER_HOST and POD_IP take precedence when set.
	IP string

	// Port is the advertised port. Zero makes the App use its listener port;
	// NACOS_SERVICE_REGISTER_PORT takes precedence over both.
	Port int

	// Namespace and Group default to the connection namespace and
	// DEFAULT_GROUP.
	Namespace string
	Group     string

	// Cluster defaults to the Nacos default cluster when empty.
	Cluster string

	// Weight defaults to 1.
	Weight float64

	// HeartbeatInterval is the initial lease renewal interval; Nacos can
	// adjust it through the heartbeat response. Default 5s.
	HeartbeatInterval time.Duration

	// Metadata is stored with the instance and can carry version or protocol
	// hints for discovery clients.
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
func (r *Registrar) Instance() Instance { return r.instance }

// Start registers the instance and starts the heartbeat loop. It is
// idempotent: a second call while running returns nil without re-registering.
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
