// Package pg opens and closes GORM connections for PostgreSQL-protocol
// servers through gorm.io/driver/postgres (jackc/pgx/v5). It owns no global
// state: callers pass a context, a config and a logger.
package pg

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ErrDSNRequired is returned when Config.DSN is empty.
var ErrDSNRequired = errors.New("pg: dsn is required")

// Config describes a PostgreSQL connection pool. DSN accepts both pgx forms:
// URL ("postgres://user:pass@host:5432/db?sslmode=disable") and keyword/value
// ("host=127.0.0.1 user=app password=secret dbname=app sslmode=disable").
type Config struct {
	DSN             string        `yaml:"dsn"`
	MaxIdleConns    int           `yaml:"max_idle_conns" default:"10"`
	MaxOpenConns    int           `yaml:"max_open_conns" default:"50"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime" default:"1h"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time" default:"10m"`
	PingTimeout     time.Duration `yaml:"ping_timeout" default:"5s"`
	SlowThreshold   time.Duration `yaml:"slow_threshold" default:"200ms"`
	LogLevel        string        `yaml:"log_level" default:"warn"` // silent|error|warn|info
	// LogParameterValues logs expanded SQL arguments instead of placeholders.
	// The default false keeps phone numbers, emails, tokens and other values
	// out of slow and failed query logs; enable it only for local debugging.
	LogParameterValues bool `yaml:"log_parameter_values" default:"false"`
	PrepareStmt        bool `yaml:"prepare_stmt" default:"false"`
}

// Validate checks the config without connecting.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DSN) == "" {
		return ErrDSNRequired
	}
	if c.MaxIdleConns < 0 || c.MaxOpenConns < 0 {
		return errors.New("pg: pool sizes must not be negative")
	}
	if c.MaxIdleConns > 0 && c.MaxOpenConns > 0 && c.MaxIdleConns > c.MaxOpenConns {
		return errors.New("pg: max_idle_conns must not exceed max_open_conns")
	}
	if c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 || c.PingTimeout < 0 || c.SlowThreshold < 0 {
		return errors.New("pg: durations must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "", "silent", "error", "warn", "warning", "info":
	default:
		return fmt.Errorf("pg: invalid log level %q", c.LogLevel)
	}
	return nil
}

func (c Config) withDefaults() Config {
	out := c
	out.DSN = strings.TrimSpace(out.DSN)
	if out.MaxIdleConns == 0 {
		out.MaxIdleConns = 10
	}
	if out.MaxOpenConns == 0 {
		out.MaxOpenConns = 50
	}
	if out.ConnMaxLifetime == 0 {
		out.ConnMaxLifetime = time.Hour
	}
	if out.ConnMaxIdleTime == 0 {
		out.ConnMaxIdleTime = 10 * time.Minute
	}
	if out.PingTimeout == 0 {
		out.PingTimeout = 5 * time.Second
	}
	if out.SlowThreshold == 0 {
		out.SlowThreshold = 200 * time.Millisecond
	}
	if strings.TrimSpace(out.LogLevel) == "" {
		out.LogLevel = "warn"
	}
	return out
}

// New opens a PostgreSQL connection, configures the pool and verifies it with
// a ping. It never panics and never logs the DSN.
func New(ctx context.Context, cfg Config, logger *zap.Logger) (*gorm.DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = zap.NewNop()
	}

	gormLog := newGormLogger(logger, cfg)
	conn, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
		Logger:                                   gormLog,
		PrepareStmt:                              cfg.PrepareStmt,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("pg: open %s: %w", redactDSN(cfg.DSN), err)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		return nil, fmt.Errorf("pg: get sql.DB: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("pg: ping %s: %w", redactDSN(cfg.DSN), err)
	}
	return conn, nil
}

// Close releases the underlying connection pool. A nil conn is a no-op.
func Close(conn *gorm.DB) error {
	if conn == nil {
		return nil
	}
	sqlDB, err := conn.DB()
	if err != nil {
		return fmt.Errorf("pg: get sql.DB: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("pg: close: %w", err)
	}
	return nil
}

// passwordKV 匹配 keyword/value 形式 DSN 中的 password=xxx（含单引号包裹）。
var passwordKV = regexp.MustCompile(`(?i)(password\s*=\s*)('[^']*'|[^\s]+)`)

// redactDSN hides the password portion of a DSN before it reaches a log or
// error. Both pgx forms are covered: URL userinfo and password= keyword.
func redactDSN(dsn string) string {
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		at := strings.LastIndex(rest, "@")
		if at < 0 {
			return dsn
		}
		userinfo := rest[:at]
		colon := strings.Index(userinfo, ":")
		if colon < 0 {
			return dsn
		}
		return dsn[:i+3] + userinfo[:colon+1] + "***" + rest[at:]
	}
	return passwordKV.ReplaceAllString(dsn, "${1}***")
}

type gormLogger struct {
	logger        *zap.Logger
	level         gormlogger.LogLevel
	slowThreshold time.Duration
	logParameters bool
}

var _ gorm.ParamsFilter = (*gormLogger)(nil)

func newGormLogger(logger *zap.Logger, cfg Config) gormlogger.Interface {
	level := gormlogger.Warn
	switch strings.ToLower(strings.TrimSpace(cfg.LogLevel)) {
	case "silent":
		level = gormlogger.Silent
	case "error":
		level = gormlogger.Error
	case "info":
		level = gormlogger.Info
	}
	return &gormLogger{
		logger:        logger.With(zap.String("component", "gorm")),
		level:         level,
		slowThreshold: cfg.SlowThreshold,
		logParameters: cfg.LogParameterValues,
	}
}

// ParamsFilter implements gorm.ParamsFilter. Returning a nil var list makes the
// dialector skip expansion, so Trace logs the statement template with its
// placeholders instead of the actual arguments.
func (g *gormLogger) ParamsFilter(_ context.Context, sql string, params ...interface{}) (string, []interface{}) {
	if g.logParameters {
		return sql, params
	}
	return sql, nil
}

func (g *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	cp := *g
	cp.level = level
	return &cp
}

func (g *gormLogger) Info(_ context.Context, msg string, args ...interface{}) {
	if g.level >= gormlogger.Info {
		g.logger.Sugar().Infof(msg, args...)
	}
}

func (g *gormLogger) Warn(_ context.Context, msg string, args ...interface{}) {
	if g.level >= gormlogger.Warn {
		g.logger.Sugar().Warnf(msg, args...)
	}
}

func (g *gormLogger) Error(_ context.Context, msg string, args ...interface{}) {
	if g.level >= gormlogger.Error {
		g.logger.Sugar().Errorf(msg, args...)
	}
}

func (g *gormLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.level == gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	sql, rows := fc()
	fields := []zap.Field{
		zap.Duration("elapsed", elapsed),
		zap.Int64("rows", rows),
		zap.String("sql", sql),
	}
	switch {
	case err != nil && g.level >= gormlogger.Error:
		g.logger.Error("sql error", append(fields, zap.Error(err))...)
	case g.slowThreshold > 0 && elapsed > g.slowThreshold && g.level >= gormlogger.Warn:
		g.logger.Warn("slow sql", append(fields, zap.String("threshold", g.slowThreshold.String()))...)
	case g.level >= gormlogger.Info:
		g.logger.Info("sql", fields...)
	}
}
