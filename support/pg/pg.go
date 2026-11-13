// Package pg opens and closes GORM connections for PostgreSQL-protocol
// servers through gorm.io/driver/postgres (jackc/pgx/v5). It owns no global
// state: callers pass a context, a config and a logger.
// 包 pg 通过 gorm.io/driver/postgres（jackc/pgx/v5）为 PostgreSQL 协议服务器打开
// 并关闭 GORM 连接。它不持有全局状态：调用方传入 context、配置和 logger。
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
// ErrDSNRequired 在 Config.DSN 为空时返回。
var ErrDSNRequired = errors.New("pg: dsn is required")

// Config describes a PostgreSQL connection pool. DSN accepts both pgx forms:
// URL ("postgres://user:pass@host:5432/db?sslmode=disable") and keyword/value
// ("host=127.0.0.1 user=app password=secret dbname=app sslmode=disable").
// Config 描述 PostgreSQL 连接 pool。DSN 接受 pgx 的两种形式：URL 与
// keyword/value；其余零值字段在 New 中填充默认值。
type Config struct {
	DSN             string        `yaml:"dsn"`
	MaxIdleConns    int           `yaml:"max_idle_conns" default:"10"`
	MaxOpenConns    int           `yaml:"max_open_conns" default:"50"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime" default:"1h"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time" default:"10m"`
	PingTimeout     time.Duration `yaml:"ping_timeout" default:"5s"`
	SlowThreshold   time.Duration `yaml:"slow_threshold" default:"200ms"`
	LogLevel        string        `yaml:"log_level" default:"warn"` // silent|error|warn|info；可选值，默认 warn
	// LogParameterValues logs expanded SQL arguments instead of placeholders.
	// The default false keeps phone numbers, emails, tokens and other values
	// out of slow and failed query logs; enable it only for local debugging.
	// LogParameterValues 为 true 时日志记录展开后的 SQL 参数而不是占位符。默认
	// false 可避免手机号、邮箱、token 等进入慢查询与失败日志，仅本地调试时开启。
	LogParameterValues bool `yaml:"log_parameter_values" default:"false"`
	PrepareStmt        bool `yaml:"prepare_stmt" default:"false"`
}

// Validate checks the config without connecting.
// Validate 在不建立连接的前提下检查配置，拒绝负的 pool 大小与时长、非法的日志级别；
// 应在启动期调用而不是等首个查询失败。
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
// New 打开 PostgreSQL 连接、配置 pool 并用 ping 验证；不会 panic，也不会在日志
// 或错误里输出完整 DSN。ping 失败时会先关闭已打开的连接再返回错误。
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
// Close 释放底层连接 pool；conn 为 nil 时是空操作，关闭后该句柄不应再使用。
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

// passwordKV matches password=xxx (including a single-quoted value) in a
// keyword/value DSN.
// passwordKV 匹配 keyword/value 形式 DSN 中的 password=xxx（含单引号包裹）。
var passwordKV = regexp.MustCompile(`(?i)(password\s*=\s*)('[^']*'|[^\s]+)`)

// redactDSN hides the password portion of a DSN before it reaches a log or
// error. Both pgx forms are covered: URL userinfo and password= keyword.
// redactDSN 在 DSN 进入日志或错误前隐藏密码部分；URL 的 userinfo 与
// password= 两种 pgx 形式都已覆盖。
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
// ParamsFilter 实现 gorm.ParamsFilter。返回 nil 参数列表会让 dialector 跳过展开，
// 因此 Trace 记录的是带占位符的语句模板而不是实际参数。
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
