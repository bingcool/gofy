// Package nacos pulls configuration from a Nacos config server into a local
// YAML file and watches it for changes.
//
// It talks to the Nacos Open API directly over HTTP (including long polling),
// so it adds no SDK dependency. A typical application pulls the config before
// loading it and optionally keeps it fresh at runtime:
//
//	nx, err := nacos.LoadConfig("nacos.yaml") // or nacos.FromEnv()
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
//	if nx.Enabled {
//		go nacos.Watch(ctx, nx, nacos.WithRestartOnChange(cfg.PIDFile))
//	}
//
// Sync validates that the remote content is YAML before replacing the local
// file, and the replacement is atomic so a running application never reads a
// partially written config.
package nacos

import (
	"fmt"
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

	// Timeout bounds a single config or auth request.
	Timeout time.Duration
	// PollTimeout is how long the server holds a listener request open.
	PollTimeout time.Duration
	// RetryDelay is the pause after a failed listener request or a failed
	// config apply.
	RetryDelay time.Duration
}

// DefaultConfig returns a config pointing at 127.0.0.1:8848, writing
// config.yaml, with the feature disabled.
func DefaultConfig() Config {
	return Config{
		Host:        "127.0.0.1",
		Port:        8848,
		Group:       DefaultGroup,
		File:        DefaultFile,
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
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("nacos: read %s: %w", path, err)
	}
	var file struct {
		Nacos rawConfig `yaml:"nacos"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return cfg, fmt.Errorf("nacos: decode %s: %w", path, err)
	}

	n := file.Nacos
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
	return cfg.normalize(), nil
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
