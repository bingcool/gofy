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

	"github.com/bingcool/gofy/support/sqllog"
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
func New(ctx context.Context, cfg Config, lg *zap.Logger, opts ...Option) (*gorm.DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	if ctx == nil {
		ctx = context.Background()
	}
	if lg == nil {
		lg = zap.NewNop()
	}
	o := newOptions(opts...)
	if o.sqlLogger == nil {
		o.sqlLogger = lg
	}

	gormLog := newGormLogger(o, lg, cfg)
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

// Option customises how a connection is opened. Options exist so the sql_log
// policy and the SQL log channel can be injected at the assembly point without
// widening Config with process-wide values.
// Option 定制连接如何打开。选项的存在，是为了让 sql_log 策略与 SQL 日志通道能在装配点
// 注入，而不必把进程级的值塞进 Config。
type Option func(*options)

type options struct {
	policy    *sqllog.Policy
	sqlLogger *zap.Logger
}

func newOptions(opts ...Option) *options {
	o := &options{}
	for _, apply := range opts {
		apply(o)
	}
	return o
}

// WithSQLPolicy attaches the process-wide sql_log policy. Passing the same
// *Policy to every connection is what makes the rate limits process-wide; a nil
// policy keeps the legacy behaviour (log by log_level, no sampling or masking).
// WithSQLPolicy 挂上进程级 sql_log 策略。把同一个 *Policy 传给每个连接，限流才是
// 进程级的；传 nil 则保持旧行为（按 log_level 输出，不采样、不脱敏）。
func WithSQLPolicy(policy *sqllog.Policy) Option {
	return func(o *options) { o.policy = policy }
}

// WithSQLLogger sends SQL entries to a dedicated channel. Without it the entries
// go to the application logger, which is what a project without sql_outputs
// wants.
// WithSQLLogger 把 SQL 条目送到独立通道。不传时条目进应用 logger，这正是没有配置
// sql_outputs 的项目需要的。
func WithSQLLogger(l *zap.Logger) Option {
	return func(o *options) { o.sqlLogger = l }
}

// newGormLogger builds the adapter. Logger selection is decided here, once: the
// SQL channel when configured, the application logger otherwise.
// newGormLogger 构造适配器。日志器在这里一次性选定：配置了 SQL 通道就写通道，否则写
// 应用 logger。
func newGormLogger(o *options, app *zap.Logger, cfg Config) gormlogger.Interface {
	sink := o.sqlLogger
	if sink == nil {
		sink = app
	}
	return sqllog.GormLogger(sink, sqllog.GormLoggerConfig{
		SlowThreshold:             cfg.SlowThreshold,
		LogLevel:                  cfg.LogLevel,
		IgnoreRecordNotFoundError: false,
		ParameterizedQueries:      !cfg.LogParameterValues,
		Policy:                    o.policy,
	})
}
