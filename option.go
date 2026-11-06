package gofy

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/bingcool/gofy/command"
	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/logging"
	"github.com/bingcool/gofy/script"
	"github.com/bingcool/gofy/support/mysql"
	"github.com/bingcool/gofy/support/nacos"
	"github.com/bingcool/gofy/transport"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Option customizes an App during New. Options are applied in order.
type Option func(*options) error

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type options struct {
	name    string
	env     string
	version core.VersionInfo
	hasVer  bool

	logger    *zap.Logger
	logConfig logging.Config

	http      HTTPConfig
	apiPrefix string

	middleware []transport.Middleware

	pidFile    string
	pidPerm    os.FileMode
	pidRefresh time.Duration
	workDir    string

	dbConfig *mysql.Config
	db       *gorm.DB

	// nacosRegistration publishes this instance in Nacos naming when
	// cfg.Register.Enabled is true.
	nacosRegistration *nacos.Config

	moduleFactories []transport.ModuleFactory
	modules         []transport.Module

	scripts  []script.Script
	commands []command.Command

	shutdownTimeout time.Duration

	responder    transport.Responder
	responderSet bool
	recovery     bool
	accessLog    bool
	cors         *transport.CORSConfig
	health       bool
	maxBody      int64
	trusted      []string
	alwaysOK     bool
	serverConf   func(*http.Server) error

	jwt             *transport.JWTAuthConfig
	rateLimit       *transport.RateLimitConfig
	securityHeaders *transport.SecurityHeadersConfig
}

func defaultOptions() options {
	return options{
		name: "app",
		env:  "dev",
		logConfig: logging.Config{
			Level:    "info",
			Encoding: "console",
			Outputs:  []string{"stdout"},
		},
		http:            defaultHTTPConfig(),
		apiPrefix:       "/api/v1",
		pidPerm:         0o644,
		shutdownTimeout: 10 * time.Second,
		responder:       transport.NewResponder(transport.ResponderConfig{}),
		recovery:        true,
		accessLog:       true,
		maxBody:         4 << 20,
	}
}

// WithName sets the application name, used by logs, pid files and version.
func WithName(name string) Option {
	return func(o *options) error {
		name = strings.TrimSpace(name)
		if !namePattern.MatchString(name) {
			return fmt.Errorf("gofy: invalid app name %q", name)
		}
		o.name = name
		return nil
	}
}

// WithEnv sets the environment marker exposed as Runtime.Env.
func WithEnv(env string) Option {
	return func(o *options) error {
		env = strings.TrimSpace(env)
		if env == "" {
			return fmt.Errorf("gofy: env must not be empty")
		}
		o.env = env
		return nil
	}
}

// WithLogger injects a logger. The App does not sync or close it.
func WithLogger(logger *zap.Logger) Option {
	return func(o *options) error {
		if logger == nil {
			return fmt.Errorf("gofy: logger must not be nil")
		}
		o.logger = logger
		return nil
	}
}

// WithLogConfig configures the logger built by the App when no logger is
// injected.
func WithLogConfig(cfg logging.Config) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		o.logConfig = cfg
		return nil
	}
}

// WithHTTP configures the HTTP server. Zero fields keep their defaults.
func WithHTTP(cfg HTTPConfig) Option {
	return func(o *options) error {
		cfg = cfg.withDefaults()
		if err := cfg.Validate(); err != nil {
			return err
		}
		o.http = cfg
		return nil
	}
}

// WithAPIPrefix sets the prefix shared by all modules. An empty string
// disables the prefix.
func WithAPIPrefix(prefix string) Option {
	return func(o *options) error {
		o.apiPrefix = transport.NormalizePrefix(prefix)
		return nil
	}
}

// WithMiddleware appends business-wide middleware after framework middleware.
func WithMiddleware(mw ...transport.Middleware) Option {
	return func(o *options) error {
		for _, m := range mw {
			if m == nil {
				return fmt.Errorf("gofy: middleware must not be nil")
			}
		}
		o.middleware = append(o.middleware, mw...)
		return nil
	}
}

// WithPIDFile enables a pid file at path. An empty path disables it.
// Relative paths are resolved against WithWorkDir.
func WithPIDFile(path string) Option {
	return func(o *options) error {
		o.pidFile = strings.TrimSpace(path)
		return nil
	}
}

// WithPIDFilePerm sets the permissions used when writing the pid file.
func WithPIDFilePerm(perm os.FileMode) Option {
	return func(o *options) error {
		if perm&0o200 == 0 {
			return fmt.Errorf("gofy: pid file perm %#o must be writable by the owner", perm)
		}
		o.pidPerm = perm
		return nil
	}
}

// WithPIDRefreshInterval rewrites the pid file periodically. Zero disables
// refreshing.
func WithPIDRefreshInterval(interval time.Duration) Option {
	return func(o *options) error {
		if interval < 0 {
			return fmt.Errorf("gofy: pid refresh interval must not be negative")
		}
		o.pidRefresh = interval
		return nil
	}
}

// WithWorkDir sets the base directory for pid files. It must exist at startup.
func WithWorkDir(dir string) Option {
	return func(o *options) error {
		o.workDir = strings.TrimSpace(dir)
		return nil
	}
}

// WithDatabase lets the App create, own and close a MySQL connection.
// It is mutually exclusive with WithDB.
func WithDatabase(cfg mysql.Config) Option {
	return func(o *options) error {
		c := cfg
		if err := c.Validate(); err != nil {
			return err
		}
		o.dbConfig = &c
		return nil
	}
}

// WithDB injects an existing connection. The App does not ping or close it.
func WithDB(db *gorm.DB) Option {
	return func(o *options) error {
		if db == nil {
			return fmt.Errorf("gofy: db must not be nil")
		}
		o.db = db
		return nil
	}
}

// WithNacosRegistration publishes the running instance in Nacos using the
// Register block of cfg, and deregisters it during shutdown. The advertised
// port defaults to the HTTP listener port; the service is registered after the
// listener starts and is not re-registered by Reload.
//
// It is a no-op when cfg.Enabled or cfg.Register.Enabled is false.
func WithNacosRegistration(cfg nacos.Config) Option {
	return func(o *options) error {
		if cfg.Register.Enabled && strings.TrimSpace(cfg.Register.ServiceName) == "" {
			return fmt.Errorf("gofy: nacos service_register.service_name is required")
		}
		c := cfg
		o.nacosRegistration = &c
		return nil
	}
}

// WithModuleFactories registers factories invoked during startup.
func WithModuleFactories(factories ...transport.ModuleFactory) Option {
	return func(o *options) error {
		for _, f := range factories {
			if f == nil {
				return fmt.Errorf("gofy: module factory must not be nil")
			}
		}
		o.moduleFactories = append(o.moduleFactories, factories...)
		return nil
	}
}

// WithModules injects module instances. They do not receive a Runtime; use
// WithModuleFactories for modules that need one.
func WithModules(modules ...transport.Module) Option {
	return func(o *options) error {
		for _, m := range modules {
			if m == nil {
				return fmt.Errorf("gofy: module must not be nil")
			}
		}
		o.modules = append(o.modules, modules...)
		return nil
	}
}

// WithScripts registers maintenance scripts.
func WithScripts(scripts ...script.Script) Option {
	return func(o *options) error {
		if err := script.Validate(scripts); err != nil {
			return err
		}
		o.scripts = append(o.scripts, scripts...)
		return nil
	}
}

// WithCommands registers custom CLI commands.
func WithCommands(cmds ...command.Command) Option {
	return func(o *options) error {
		if err := command.Validate(cmds); err != nil {
			return err
		}
		o.commands = append(o.commands, cmds...)
		return nil
	}
}

// WithShutdownTimeout sets the total budget for HTTP shutdown and module stop.
func WithShutdownTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return fmt.Errorf("gofy: shutdown timeout must be positive")
		}
		o.shutdownTimeout = timeout
		return nil
	}
}

// WithResponder replaces the default response renderer.
func WithResponder(r transport.Responder) Option {
	return func(o *options) error {
		if r == nil {
			return fmt.Errorf("gofy: responder must not be nil")
		}
		o.responder = r
		o.responderSet = true
		return nil
	}
}

// WithRecovery enables or disables the panic recovery middleware.
func WithRecovery(enabled bool) Option {
	return func(o *options) error { o.recovery = enabled; return nil }
}

// WithAccessLog enables or disables the access log middleware.
func WithAccessLog(enabled bool) Option {
	return func(o *options) error { o.accessLog = enabled; return nil }
}

// WithCORS enables CORS with the given policy.
func WithCORS(cfg transport.CORSConfig) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.cors = &c
		return nil
	}
}

// WithHealthEndpoints registers /healthz and /readyz.
func WithHealthEndpoints(enabled bool) Option {
	return func(o *options) error { o.health = enabled; return nil }
}

// WithJWTAuth requires a valid bearer token on module routes. Health checks,
// static handlers registered outside modules and unmatched paths are not
// affected.
func WithJWTAuth(cfg transport.JWTAuthConfig) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.jwt = &c
		return nil
	}
}

// WithRateLimit limits requests to module routes per client, method and route.
// A nil Store or non-positive Limit/Window is rejected at startup.
func WithRateLimit(cfg transport.RateLimitConfig) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.rateLimit = &c
		return nil
	}
}

// WithSecurityHeaders adds hardening headers such as nosniff, frame options
// and referrer policy to every response. Zero fields use the defaults from
// transport.DefaultSecurityHeadersConfig.
func WithSecurityHeaders(cfg transport.SecurityHeadersConfig) Option {
	return func(o *options) error {
		if err := cfg.Validate(); err != nil {
			return err
		}
		c := cfg
		o.securityHeaders = &c
		return nil
	}
}

// WithMaxBodyBytes caps request body size. Requests above the limit get 413.
func WithMaxBodyBytes(n int64) Option {
	return func(o *options) error {
		if n <= 0 {
			return fmt.Errorf("gofy: max body bytes must be positive")
		}
		o.maxBody = n
		return nil
	}
}

// WithTrustedProxies sets gin's trusted proxy list. By default no proxy is
// trusted.
func WithTrustedProxies(proxies ...string) Option {
	return func(o *options) error {
		for _, p := range proxies {
			if net.ParseIP(p) == nil {
				if _, _, err := net.ParseCIDR(p); err != nil {
					return fmt.Errorf("gofy: invalid trusted proxy %q", p)
				}
			}
		}
		o.trusted = append(o.trusted, proxies...)
		return nil
	}
}

// WithAlwaysOK makes every response use HTTP 200 while keeping the business
// code in the envelope. NoContent responses stay 204.
func WithAlwaysOK(enabled bool) Option {
	return func(o *options) error { o.alwaysOK = enabled; return nil }
}

// WithServerConfigurator customizes the http.Server before it starts.
func WithServerConfigurator(fn func(*http.Server) error) Option {
	return func(o *options) error {
		if fn == nil {
			return fmt.Errorf("gofy: server configurator must not be nil")
		}
		o.serverConf = fn
		return nil
	}
}

// WithVersionInfo sets explicit build information.
func WithVersionInfo(info core.VersionInfo) Option {
	return func(o *options) error {
		o.version = info
		o.hasVer = true
		return nil
	}
}
