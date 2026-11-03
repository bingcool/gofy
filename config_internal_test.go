package gofy

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestHTTPConfigDefaultsAndValidate(t *testing.T) {
	cfg := defaultHTTPConfig()
	if cfg.Host != "0.0.0.0" || cfg.Port != 8080 {
		t.Fatalf("default host/port = %s:%d", cfg.Host, cfg.Port)
	}
	if cfg.ReadHeaderTimeout != 5*time.Second || cfg.ReadTimeout != 15*time.Second ||
		cfg.WriteTimeout != 30*time.Second || cfg.IdleTimeout != 60*time.Second {
		t.Fatalf("unexpected default timeouts: %+v", cfg)
	}
	if cfg.Mode != "release" {
		t.Fatalf("default mode = %q, want release", cfg.Mode)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestHTTPConfigValidation(t *testing.T) {
	cases := map[string]func(*HTTPConfig){
		"port zero":        func(c *HTTPConfig) { c.Port = 0 },
		"port too high":    func(c *HTTPConfig) { c.Port = 70000 },
		"negative timeout": func(c *HTTPConfig) { c.ReadTimeout = -time.Second },
		"negative header":  func(c *HTTPConfig) { c.MaxHeaderBytes = -1 },
		"bad mode":         func(c *HTTPConfig) { c.Mode = "staging" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := defaultHTTPConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid config")
			}
		})
	}
}

func TestHTTPConfigEmptyHostUsesWildcard(t *testing.T) {
	cfg := HTTPConfig{Port: 9000}
	if got := cfg.withDefaults().Host; got != "0.0.0.0" {
		t.Fatalf("host = %q, want 0.0.0.0", got)
	}
}

func TestOptionValidation(t *testing.T) {
	cases := map[string]Option{
		"invalid name":        WithName("Invalid Name"),
		"empty env":           WithEnv("  "),
		"nil logger":          WithLogger(nil),
		"nil middleware":      WithMiddleware(nil),
		"nil db":              WithDB(nil),
		"nil factory":         WithModuleFactories(nil),
		"nil module":          WithModules(nil),
		"nil responder":       WithResponder(nil),
		"negative refresh":    WithPIDRefreshInterval(-time.Second),
		"zero shutdown":       WithShutdownTimeout(0),
		"zero max body":       WithMaxBodyBytes(0),
		"unwritable pid mode": WithPIDFilePerm(0o444),
		"invalid proxy":       WithTrustedProxies("not-an-ip"),
		"nil configurator":    WithServerConfigurator(nil),
		"invalid log level":   WithLogConfig(LogConfig{Level: "verbose"}),
		"invalid http mode":   WithHTTP(HTTPConfig{Port: 8080, Mode: "staging"}),
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(opt); err == nil {
				t.Fatal("New accepted an invalid option")
			}
		})
	}
}

func TestDatabaseOptionsAreMutuallyExclusive(t *testing.T) {
	_, err := New(
		WithDatabase(DatabaseConfig{DSN: "user:pass@tcp(localhost:3306)/db"}),
		WithDB(&gorm.DB{}),
	)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("New error = %v, want mutually exclusive error", err)
	}
}

func TestCORSConfigValidation(t *testing.T) {
	cfg := CORSConfig{AllowOrigins: []string{"*"}, AllowCredentials: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("CORS accepted credentials with a wildcard origin")
	}
}
