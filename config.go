package gofy

import (
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

// MiddlewareFunc is a gin middleware function; the semantics live with the
// definition in support.MiddlewareFunc.
// MiddlewareFunc 是 gin 中间件函数；语义写在 support.MiddlewareFunc 的定义处，
// 这里不重复描述，避免两份注释各说一半而漂移。
type MiddlewareFunc = support.MiddlewareFunc

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
