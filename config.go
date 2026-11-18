package gofy

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/logger"
	"github.com/bingcool/gofy/support"
	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/diagnostics"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/support/otel"
)

// Runtime is the shared runtime passed to modules, scripts and commands.
// Runtime 是传给模块、脚本和命令的共享运行时。
type Runtime = core.Runtime

// VersionInfo describes the running build.
// VersionInfo 描述当前运行的构建版本。
type VersionInfo = core.VersionInfo

// ModuleFactory builds a Module during startup.
// ModuleFactory 在启动期构建 Module，能拿到当代 Runtime。
type ModuleFactory = support.ModuleFactory

// Middleware is a gin middleware function.
// Middleware 是一个 gin 中间件函数。
type Middleware = support.Middleware

// Responder renders HTTP successes and errors.
// Responder 渲染 HTTP 成功与错误响应。
type Responder = support.Responder

// CORSConfig is the cross-origin policy passed to WithCORS.
// CORSConfig 是传给 WithCORS 的跨域策略。
type CORSConfig = support.CORSConfig

// DatabaseConfig is the MySQL-protocol configuration passed to WithDatabase.
// DatabaseConfig 是传给 WithDatabase 的 MySQL 协议配置。
type DatabaseConfig = gofydb.Config

// LogConfig is the logger configuration passed to WithLogConfig.
// LogConfig 是传给 WithLogConfig 的 logger 配置。
type LogConfig = logger.Config

// NacosConfig is the Nacos connection and service settings passed to
// WithNacosRegistration.
// NacosConfig 是传给 WithNacosRegistration 的 Nacos 连接与服务配置。
type NacosConfig = nacos.Config

// TelemetryConfig is the OpenTelemetry tracing configuration passed to
// WithTelemetry.
// TelemetryConfig 是传给 WithTelemetry 的 OpenTelemetry tracing 配置。
type TelemetryConfig = otel.Config

// DiagnosticsConfig is the operational listener configuration passed to
// WithDiagnostics: the Prometheus scrape endpoint and net/http/pprof, served on
// their own port.
// DiagnosticsConfig 是传给 WithDiagnostics 的运维监听配置：Prometheus 抓取端点与
// net/http/pprof，跑在独立端口上。
type DiagnosticsConfig = diagnostics.Config

// HTTPConfig configures the HTTP server.
// HTTPConfig 配置 HTTP 服务器；零值字段会先补成框架默认值再整体校验。
type HTTPConfig struct {
	Host              string        `yaml:"host" default:"0.0.0.0"`
	Port              int           `yaml:"port" default:"8080"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout" default:"5s"`
	ReadTimeout       time.Duration `yaml:"read_timeout" default:"15s"`
	WriteTimeout      time.Duration `yaml:"write_timeout" default:"30s"`
	IdleTimeout       time.Duration `yaml:"idle_timeout" default:"60s"`
	MaxHeaderBytes    int           `yaml:"max_header_bytes" default:"1048576"`
	Mode              string        `yaml:"mode" default:"release"` // debug|release|test；可选值，默认 release，大小写不敏感
}

// Validate checks the HTTP configuration.
// Validate 校验 HTTP 配置：端口范围、超时与请求头上限不能为负，mode 只接受
// debug、release 或 test（大小写不敏感）。
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
