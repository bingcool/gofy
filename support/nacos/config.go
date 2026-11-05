// Package nacos pulls configuration from a Nacos config server into a local
// YAML file and watches it for changes.
//
// It talks to the Nacos Open API directly over HTTP (including long polling),
// so it adds no SDK dependency. The recommended layout mirrors swoolefy:
// nacos.yaml holds the server connection and application.yaml holds the
// config-center entry and monitoring switches.
//
//	nx, err := nacos.Load("nacos.yaml", "application.yaml")
//	if err != nil {
//		return err
//	}
//	if nx.Enabled {
//		if _, err := nacos.Sync(ctx, nx); err != nil {
//			return err
//		}
//	}
//	cfg, err := gofyconfig.Load[AppConfig](ctx, nx.File)
//	// ...
//	if nx.Enabled && nx.Watch {
//		go nacos.Watch(ctx, nx, nacos.WithRestartOnChange(cfg.PIDFile))
//	}
//
// Sync validates that the remote content is YAML before replacing the local
// file, and the replacement is atomic so a running application never reads a
// partially written config.
package nacos

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultGroup is used when Config.Group is empty.
	DefaultGroup = "DEFAULT_GROUP"
	// DefaultFile is used when Config.File is empty.
	DefaultFile = "config.yaml"
)

// Config describes the Nacos connection and the local file to generate.
//
// Addr takes precedence over Host/Port when both are set. Namespace is the
// namespace ID (not its display name); "public" and an empty value both mean
// the default namespace.
type Config struct {
	// Enabled gates Sync and Watch. Disabled configs return ErrDisabled.
	Enabled bool

	// Addr is the Nacos server base URL, for example http://127.0.0.1:8848.
	// A value without a scheme is treated as http.
	Addr string
	// Host and Port are used when Addr is empty.
	Host string
	Port int

	Username  string
	Password  string
	Namespace string

	// DataID and Group identify the configuration in Nacos.
	DataID string
	Group  string

	// File is the local path written by Sync and Watch.
	File string

	// Watch keeps the local file in sync after startup. It maps to
	// application.yaml nacos.enable_nacos_config_change_restart in the
	// two-file layout and defaults to true.
	Watch bool

	// Timeout bounds a single config or auth request.
	Timeout time.Duration
	// PollTimeout is how long the server holds a listener request open.
	PollTimeout time.Duration
	// RetryDelay is the pause after a failed listener request or a failed
	// config apply.
	RetryDelay time.Duration

	// Register describes how this process is published in Nacos naming.
	Register RegisterConfig

	// Discovery describes how clients load-balance instances of a service.
	Discovery DiscoveryConfig
}

// DiscoveryConfig mirrors application.yaml nacos.discovery_service_client. It
// is used by clients that resolve a service name through Nacos naming.
type DiscoveryConfig struct {
	// Group and Namespace default to the service_register values.
	Group     string
	Namespace string

	// LoadBalancer is random, round_robin or weight. Default random.
	LoadBalancer string

	// CacheTTL is how long a resolved instance list stays fresh.
	CacheTTL time.Duration
	// StaleTTL allows serving a stale list when Nacos is unreachable.
	StaleTTL time.Duration

	// HealthyOnly drops unhealthy instances. Default true.
	HealthyOnly bool

	// Clusters restricts discovery to the listed clusters.
	Clusters []string

	healthyOnlySet bool
}

func (c DiscoveryConfig) normalize(reg RegisterConfig) DiscoveryConfig {
	c.Group = firstNonEmpty(c.Group, reg.Group, envValue("NACOS_SERVICE_GROUP_NAME"), DefaultGroup)
	c.Namespace = normalizeNamespace(firstNonEmpty(c.Namespace, reg.Namespace))
	c.LoadBalancer = strings.ToLower(strings.TrimSpace(firstNonEmpty(c.LoadBalancer, envValue("NACOS_DISCOVERY_LOAD_BALANCER"))))
	switch c.LoadBalancer {
	case "", "random":
		c.LoadBalancer = "random"
	case "round_robin", "round-robin", "roundrobin":
		c.LoadBalancer = "round_robin"
	case "weight", "weighted":
		c.LoadBalancer = "weight"
	}
	if c.CacheTTL <= 0 {
		c.CacheTTL = envSeconds("NACOS_DISCOVERY_CACHE_TTL", 60*time.Second)
	}
	if c.StaleTTL <= 0 {
		c.StaleTTL = envSeconds("NACOS_DISCOVERY_STALE_TTL", max(3*c.CacheTTL, 180*time.Second))
	}
	if c.StaleTTL < c.CacheTTL {
		c.StaleTTL = max(3*c.CacheTTL, 180*time.Second)
	}
	if !c.healthyOnlySet {
		if v, ok := lookupEnv("NACOS_DISCOVERY_HEALTHY_ONLY"); ok {
			c.HealthyOnly, _ = strconv.ParseBool(v)
			c.healthyOnlySet = true
		}
	}
	if !c.healthyOnlySet {
		c.HealthyOnly = true
	}
	if len(c.Clusters) == 0 {
		if v := envValue("NACOS_DISCOVERY_CLUSTERS"); v != "" {
			c.Clusters = splitList(v)
		}
	}
	return c
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envSeconds(key string, fallback time.Duration) time.Duration {
	v, ok := lookupEnv(key)
	if !ok {
		return fallback
	}
	seconds, err := strconv.Atoi(v)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// DefaultConfig returns a config pointing at 127.0.0.1:8848, writing
// config.yaml, with the feature disabled.
func DefaultConfig() Config {
	return Config{
		Host:        "127.0.0.1",
		Port:        8848,
		Group:       DefaultGroup,
		File:        DefaultFile,
		Watch:       true,
		Timeout:     10 * time.Second,
		PollTimeout: 30 * time.Second,
		RetryDelay:  3 * time.Second,
	}
}

// FromEnv builds a config from defaults overridden by environment variables:
//
//	NACOS_ENABLED, NACOS_ADDR, NACOS_HOST, NACOS_PORT,
//	NACOS_USERNAME, NACOS_PASSWORD, NACOS_NAMESPACE,
//	NACOS_DATA_ID, NACOS_GROUP, NACOS_FILE,
//	NACOS_TIMEOUT_MS, NACOS_LISTENER_TIMEOUT_MS, NACOS_LISTENER_FAILED_MS
func FromEnv() Config {
	cfg := DefaultConfig()
	if v, ok := lookupEnv("NACOS_ENABLED"); ok {
		cfg.Enabled, _ = strconv.ParseBool(v)
	}
	if v, ok := lookupEnv("NACOS_ADDR"); ok {
		cfg.Addr = v
	}
	if v, ok := lookupEnv("NACOS_HOST"); ok {
		cfg.Host = v
	}
	if v, ok := lookupEnv("NACOS_PORT"); ok {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Port = port
		}
	}
	if v, ok := lookupEnv("NACOS_USERNAME"); ok {
		cfg.Username = v
	}
	if v, ok := lookupEnv("NACOS_PASSWORD"); ok {
		cfg.Password = v
	}
	if v, ok := lookupEnv("NACOS_NAMESPACE"); ok {
		cfg.Namespace = v
	}
	if v, ok := lookupEnv("NACOS_DATA_ID"); ok {
		cfg.DataID = v
	}
	if v, ok := lookupEnv("NACOS_GROUP"); ok {
		cfg.Group = v
	}
	if v, ok := lookupEnv("NACOS_FILE"); ok {
		cfg.File = v
	}
	cfg.Timeout = envDurationMS("NACOS_TIMEOUT_MS", cfg.Timeout)
	cfg.PollTimeout = envDurationMS("NACOS_LISTENER_TIMEOUT_MS", cfg.PollTimeout)
	cfg.RetryDelay = envDurationMS("NACOS_LISTENER_FAILED_MS", cfg.RetryDelay)
	return cfg.normalize()
}

// LoadConfig reads a nacos.yaml file and applies environment overrides for
// values the file leaves empty. Precedence is file > environment > default.
//
//	nacos:
//	  enabled: true
//	  addr: http://127.0.0.1:8848
//	  namespace: dev
//	  data_id: gofy-service
//	  group: GO_GROUP
//	  file: config.yaml
func LoadConfig(path string) (Config, error) {
	cfg := FromEnv()
	n, err := readNacosFile(path)
	if err != nil {
		return cfg, err
	}
	applyNacosFile(&cfg, n)
	return cfg.normalize(), nil
}

// Load reads the swoolefy-style pair of files: nacosPath holds the server
// connection, appPath (usually application.yaml) holds the config-center entry
// and the change-monitoring switches.
//
//	nacos.yaml         nacos.host/port/username/password/namespace
//	application.yaml   nacos.service_config.data_id/group/tenant
//	                   nacos.enable_nacos_config_change_restart
//	                   nacos.monitor_config_change.listener_timeout_ms
//	                   nacos.monitor_config_change.failed_wait_ms
//
// Precedence is file > environment > default. The tenant resolves to
// nacos.yaml namespace (or NACOS_NAMESPACE), then application.yaml tenant (or
// NACOS_TENANT), then the public namespace. application.yaml is optional; when
// it is missing, the legacy single-file keys keep working.
//
// In this layout Nacos is enabled by default, mirroring swoolefy where the
// presence of nacos.yaml means the configuration is managed remotely. Set
// nacos.enabled: false or NACOS_ENABLED=0 to opt out.
func Load(nacosPath, appPath string) (Config, error) {
	cfg := FromEnv()
	n, err := readNacosFile(nacosPath)
	if err != nil {
		return cfg, err
	}

	// Only an explicit switch (env or either file) overrides the layout default.
	explicitEnabled := envSet("NACOS_ENABLED")
	applyNacosFile(&cfg, n)
	if n.Enabled != nil {
		explicitEnabled = true
	}

	if appPath != "" {
		// application.yaml is optional: a missing file keeps the nacos.yaml
		// result, while any other read or parse error is fatal.
		app, appErr := readApplicationFile(appPath)
		switch {
		case appErr == nil:
			applyApplicationFile(&cfg, n, app)
			if app.Nacos.Enabled != nil {
				explicitEnabled = true
			}
		case !errors.Is(appErr, fs.ErrNotExist):
			return cfg, appErr
		}
	}

	if !explicitEnabled {
		cfg.Enabled = true
	}
	return cfg.normalize(), nil
}

func readNacosFile(path string) (rawConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rawConfig{}, fmt.Errorf("nacos: read %s: %w", path, err)
	}
	var file struct {
		Nacos rawConfig `yaml:"nacos"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return rawConfig{}, fmt.Errorf("nacos: decode %s: %w", path, err)
	}
	return file.Nacos, nil
}

func applyNacosFile(cfg *Config, n rawConfig) {
	if n.Enabled != nil {
		cfg.Enabled = *n.Enabled
	}
	if v := strings.TrimSpace(n.Addr); v != "" {
		cfg.Addr = v
	} else if n.Host != "" || n.Port > 0 {
		// The file describes the endpoint with host/port, so drop an addr
		// inherited from the environment.
		cfg.Addr = ""
	}
	if v := strings.TrimSpace(n.Host); v != "" {
		cfg.Host = v
	}
	if n.Port > 0 {
		cfg.Port = n.Port
	}
	if v := strings.TrimSpace(n.Username); v != "" {
		cfg.Username = v
	}
	if v := strings.TrimSpace(n.Password); v != "" {
		cfg.Password = v
	}
	if v := firstNonEmpty(n.Namespace, n.NamespaceID); v != "" {
		cfg.Namespace = v
	}
	if v := strings.TrimSpace(n.DataID); v != "" {
		cfg.DataID = v
	}
	if v := strings.TrimSpace(n.Group); v != "" {
		cfg.Group = v
	}
	if v := strings.TrimSpace(n.File); v != "" {
		cfg.File = v
	}
	if n.TimeoutMS > 0 {
		cfg.Timeout = time.Duration(n.TimeoutMS) * time.Millisecond
	}
	if n.ListenerTimeoutMS > 0 {
		cfg.PollTimeout = time.Duration(n.ListenerTimeoutMS) * time.Millisecond
	}
	if n.FailedWaitMS > 0 {
		cfg.RetryDelay = time.Duration(n.FailedWaitMS) * time.Millisecond
	}
}

type rawApplication struct {
	Nacos struct {
		Enabled       *bool `yaml:"enabled"`
		ServiceConfig struct {
			DataID string `yaml:"data_id"`
			Group  string `yaml:"group"`
			Tenant string `yaml:"tenant"`
		} `yaml:"service_config"`
		EnableNacosRegister bool `yaml:"enable_nacos_register"`
		ServiceRegister     struct {
			IP                string            `yaml:"ip"`
			Port              int               `yaml:"port"`
			ServiceName       string            `yaml:"service_name"`
			NamespaceID       string            `yaml:"namespace_id"`
			GroupName         string            `yaml:"group_name"`
			ClusterName       string            `yaml:"cluster_name"`
			Weight            float64           `yaml:"weight"`
			HeartbeatInterval int               `yaml:"heartbeat_interval"`
			Metadata          map[string]string `yaml:"metadata"`
		} `yaml:"service_register"`
		DiscoveryServiceClient struct {
			LoadBalancer string `yaml:"load_balancer"`
			CacheTTL     int    `yaml:"cache_ttl"`
			StaleTTL     int    `yaml:"stale_ttl"`
			HealthyOnly  *bool  `yaml:"healthy_only"`
			NamespaceID  string `yaml:"namespace_id"`
			GroupName    string `yaml:"group_name"`
			Clusters     string `yaml:"clusters"`
		} `yaml:"discovery_service_client"`
		EnableNacosConfigChangeRestart *bool `yaml:"enable_nacos_config_change_restart"`
		MonitorConfigChange            struct {
			ListenerTimeoutMS int `yaml:"listener_timeout_ms"`
			FailedWaitMS      int `yaml:"failed_wait_ms"`
		} `yaml:"monitor_config_change"`
	} `yaml:"nacos"`
}

func readApplicationFile(path string) (rawApplication, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rawApplication{}, fmt.Errorf("nacos: read %s: %w", path, err)
	}
	var app rawApplication
	if err := yaml.Unmarshal(data, &app); err != nil {
		return rawApplication{}, fmt.Errorf("nacos: decode %s: %w", path, err)
	}
	return app, nil
}

func applyApplicationFile(cfg *Config, n rawConfig, app rawApplication) {
	if app.Nacos.Enabled != nil {
		cfg.Enabled = *app.Nacos.Enabled
	}
	svc := app.Nacos.ServiceConfig
	if v := strings.TrimSpace(svc.DataID); v != "" {
		cfg.DataID = v
	}
	if v := strings.TrimSpace(svc.Group); v != "" {
		cfg.Group = v
	}
	if app.Nacos.EnableNacosConfigChangeRestart != nil {
		// The key keeps swoolefy's name; it now enables watching, and the
		// callback decides whether a change reloads or restarts.
		cfg.Watch = *app.Nacos.EnableNacosConfigChangeRestart
	}
	monitor := app.Nacos.MonitorConfigChange
	if monitor.ListenerTimeoutMS > 0 {
		cfg.PollTimeout = time.Duration(monitor.ListenerTimeoutMS) * time.Millisecond
	}
	if monitor.FailedWaitMS > 0 {
		cfg.RetryDelay = time.Duration(monitor.FailedWaitMS) * time.Millisecond
	}

	reg := app.Nacos.ServiceRegister
	cfg.Register = RegisterConfig{
		Enabled:     app.Nacos.EnableNacosRegister,
		IP:          reg.IP,
		Port:        reg.Port,
		ServiceName: reg.ServiceName,
		Namespace:   reg.NamespaceID,
		Group:       reg.GroupName,
		Cluster:     reg.ClusterName,
		Weight:      reg.Weight,
		Metadata:    reg.Metadata,
	}
	if reg.HeartbeatInterval > 0 {
		cfg.Register.HeartbeatInterval = time.Duration(reg.HeartbeatInterval) * time.Second
	}

	disc := app.Nacos.DiscoveryServiceClient
	cfg.Discovery = DiscoveryConfig{
		Group:        disc.GroupName,
		Namespace:    disc.NamespaceID,
		LoadBalancer: disc.LoadBalancer,
		Clusters:     splitList(disc.Clusters),
	}
	if disc.CacheTTL > 0 {
		cfg.Discovery.CacheTTL = time.Duration(disc.CacheTTL) * time.Second
	}
	if disc.StaleTTL > 0 {
		cfg.Discovery.StaleTTL = time.Duration(disc.StaleTTL) * time.Second
	}
	if disc.HealthyOnly != nil {
		cfg.Discovery.HealthyOnly = *disc.HealthyOnly
		cfg.Discovery.healthyOnlySet = true
	}

	// The connection file wins: service_config.tenant is only a fallback when
	// nacos.yaml neither set a namespace nor NACOS_NAMESPACE did.
	if namespaceFromNacos(n) {
		return
	}
	tenant := strings.TrimSpace(svc.Tenant)
	if tenant == "" {
		tenant, _ = lookupEnv("NACOS_TENANT")
	}
	if tenant != "" {
		cfg.Namespace = tenant
	}
}

// namespaceFromNacos reports whether the namespace came from nacos.yaml or
// NACOS_NAMESPACE. In that case application.yaml service_config.tenant is
// ignored, matching swoolefy.
func namespaceFromNacos(n rawConfig) bool {
	if firstNonEmpty(n.Namespace, n.NamespaceID) != "" {
		return true
	}
	return envSet("NACOS_NAMESPACE")
}

type rawConfig struct {
	Enabled           *bool  `yaml:"enabled"`
	Addr              string `yaml:"addr"`
	Host              string `yaml:"host"`
	Port              int    `yaml:"port"`
	Username          string `yaml:"username"`
	Password          string `yaml:"password"`
	Namespace         string `yaml:"namespace"`
	NamespaceID       string `yaml:"namespace_id"`
	DataID            string `yaml:"data_id"`
	Group             string `yaml:"group"`
	File              string `yaml:"file"`
	TimeoutMS         int    `yaml:"timeout_ms"`
	ListenerTimeoutMS int    `yaml:"listener_timeout_ms"`
	FailedWaitMS      int    `yaml:"failed_wait_ms"`
}

func (c Config) normalize() Config {
	c.Addr = strings.TrimSpace(c.Addr)
	if c.Addr != "" {
		if !strings.Contains(c.Addr, "://") {
			c.Addr = "http://" + c.Addr
		}
		c.Addr = strings.TrimRight(c.Addr, "/")
	}
	c.Host = strings.TrimSpace(c.Host)
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port <= 0 {
		c.Port = 8848
	}
	c.Username = strings.TrimSpace(c.Username)
	c.Password = strings.TrimSpace(c.Password)
	c.Namespace = normalizeNamespace(c.Namespace)
	c.DataID = strings.TrimSpace(c.DataID)
	c.Group = strings.TrimSpace(c.Group)
	if c.Group == "" {
		c.Group = DefaultGroup
	}
	c.File = strings.TrimSpace(c.File)
	if c.File == "" {
		c.File = DefaultFile
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.PollTimeout <= 0 {
		c.PollTimeout = 30 * time.Second
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = 3 * time.Second
	}
	c.Register = c.Register.normalize(c.Namespace)
	c.Discovery = c.Discovery.normalize(c.Register)
	return c
}

func (c Config) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.DataID == "" {
		return fmt.Errorf("nacos: data_id is required")
	}
	if c.File == "" {
		return fmt.Errorf("nacos: file is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("nacos: invalid port %d", c.Port)
	}
	if c.Addr != "" {
		u, err := url.Parse(c.Addr)
		if err != nil || u.Host == "" {
			return fmt.Errorf("nacos: invalid addr %q", c.Addr)
		}
	}
	return nil
}

func (c Config) baseURL() string {
	if c.Addr != "" {
		return c.Addr
	}
	return "http://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

func normalizeNamespace(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if strings.EqualFold(namespace, "public") {
		return ""
	}
	return namespace
}

func lookupEnv(key string) (string, bool) {
	value, ok := os.LookupEnv(key)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

func envSet(key string) bool {
	_, ok := lookupEnv(key)
	return ok
}

func envDurationMS(key string, fallback time.Duration) time.Duration {
	v, ok := lookupEnv(key)
	if !ok {
		return fallback
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
