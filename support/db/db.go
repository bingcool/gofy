// Package db opens and closes GORM connections for MySQL-protocol servers:
// MySQL, MariaDB, TiDB, OceanBase and anything else that speaks the protocol
// through the go-sql-driver DSN. It owns no global state: callers pass a
// context, a config and a logger.
// 包 db 为 MySQL 协议服务器打开并关闭 GORM 连接：MySQL、MariaDB、TiDB、OceanBase
// 以及任何通过 go-sql-driver DSN 使用该协议的实现。它不持有全局状态：调用方
// 传入 context、配置和 logger。
package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ErrDSNRequired is returned when Config.DSN is empty.
// ErrDSNRequired 在 Config.DSN 为空时返回。
var ErrDSNRequired = errors.New("db: dsn is required")

// Config describes a MySQL-protocol connection pool.
// Config 描述 MySQL 协议的连接 pool；零值字段在 New 中填充默认值，Validate
// 只检查显式传入的非法值。
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
		return errors.New("db: pool sizes must not be negative")
	}
	if c.MaxIdleConns > 0 && c.MaxOpenConns > 0 && c.MaxIdleConns > c.MaxOpenConns {
		return errors.New("db: max_idle_conns must not exceed max_open_conns")
	}
	if c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 || c.PingTimeout < 0 || c.SlowThreshold < 0 {
		return errors.New("db: durations must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "", "silent", "error", "warn", "warning", "info":
	default:
		return fmt.Errorf("db: invalid log level %q", c.LogLevel)
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

// New opens a MySQL-protocol connection, configures the pool and verifies it
// with a ping. It never panics and never logs the DSN.
// New 打开 MySQL 协议连接、配置 pool 并用 ping 验证；不会 panic，也不会在日志或
// 错误里输出完整 DSN。ping 失败时会先关闭已打开的连接再返回错误。
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
	db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{
		Logger:                                   gormLog,
		PrepareStmt:                              cfg.PrepareStmt,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", redactDSN(cfg.DSN), err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("db: get sql.DB: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: ping %s: %w", redactDSN(cfg.DSN), err)
	}
	return db, nil
}

// Close releases the underlying connection pool. A nil db is a no-op.
// Close 释放底层连接 pool；db 为 nil 时是空操作，关闭后该句柄不应再使用。
func Close(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("db: get sql.DB: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("db: close: %w", err)
	}
	return nil
}

// redactDSN hides the password portion of a DSN before it reaches a log or error.
// redactDSN 在 DSN 进入日志或错误前隐藏密码部分；没有 userinfo 形态时原样返回。
func redactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return dsn
	}
	colon := strings.Index(dsn, ":")
	if colon < 0 || colon > at {
		return dsn
	}
	return dsn[:colon+1] + "***" + dsn[at:]
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
