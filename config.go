package gofy

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/logging"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/mysql"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/otel"
)

// Runtime is the shared runtime passed to modules, scripts and commands.
type Runtime = core.Runtime

// VersionInfo describes the running build.
type VersionInfo = core.VersionInfo

// ModuleFactory builds a Module during startup.
type ModuleFactory = support.ModuleFactory

// Middleware is a gin middleware function.
type Middleware = support.Middleware

// Responder renders HTTP successes and errors.
type Responder = support.Responder

// CORSConfig is the cross-origin policy passed to WithCORS.
type CORSConfig = support.CORSConfig

// DatabaseConfig is the MySQL configuration passed to WithDatabase.
type DatabaseConfig = mysql.Config

// LogConfig is the logger configuration passed to WithLogConfig.
type LogConfig = logging.Config

// NacosConfig is the Nacos connection and service settings passed to
// WithNacosRegistration.
type NacosConfig = nacos.Config

// TelemetryConfig is the OpenTelemetry tracing configuration passed to
// WithTelemetry.
type TelemetryConfig = otel.Config

// HTTPConfig configures the HTTP server.
type HTTPConfig struct {
	Host              string        `yaml:"host" default:"0.0.0.0"`
	Port              int           `yaml:"port" default:"8080"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout" default:"5s"`
	ReadTimeout       time.Duration `yaml:"read_timeout" default:"15s"`
	WriteTimeout      time.Duration `yaml:"write_timeout" default:"30s"`
	IdleTimeout       time.Duration `yaml:"idle_timeout" default:"60s"`
	MaxHeaderBytes    int           `yaml:"max_header_bytes" default:"1048576"`
	Mode              string        `yaml:"mode" default:"release"` // debug|release|test
}

// Validate checks the HTTP configuration.
func (c HTTPConfig) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("gofy: invalid http port %d", c.Port)
	}
	if c.ReadHeaderTimeout < 0 || c.ReadTimeout < 0 || c.WriteTimeout < 0 || c.IdleTimeout < 0 {
		return errors.New("gofy: http timeouts must not be negative")
	}
	if c.MaxHeaderBytes < 0 {
		return errors.New("gofy: max header bytes must not be negative")
	}
	switch strings.ToLower(c.Mode) {
	case "debug", "release", "test":
	default:
		return fmt.Errorf("gofy: invalid http mode %q (want debug, release or test)", c.Mode)
	}
	return nil
}

func defaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		Host:              "0.0.0.0",
		Port:              8080,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		Mode:              "release",
	}
}

func (c HTTPConfig) withDefaults() HTTPConfig {
	out := c
	def := defaultHTTPConfig()
	if strings.TrimSpace(out.Host) == "" {
		out.Host = def.Host
	}
	if out.Port == 0 {
		out.Port = def.Port
	}
	if out.ReadHeaderTimeout == 0 {
		out.ReadHeaderTimeout = def.ReadHeaderTimeout
	}
	if out.ReadTimeout == 0 {
		out.ReadTimeout = def.ReadTimeout
	}
	if out.WriteTimeout == 0 {
		out.WriteTimeout = def.WriteTimeout
	}
	if out.IdleTimeout == 0 {
		out.IdleTimeout = def.IdleTimeout
	}
	if out.MaxHeaderBytes == 0 {
		out.MaxHeaderBytes = def.MaxHeaderBytes
	}
	if strings.TrimSpace(out.Mode) == "" {
		out.Mode = def.Mode
	}
	out.Mode = strings.ToLower(out.Mode)
	return out
}
