// Package sqlite opens and closes GORM SQLite connections.
//
// The dialector is github.com/glebarez/sqlite (modernc.org/sqlite): pure Go,
// no cgo. It is a drop-in replacement for gorm.io/driver/sqlite, which builds
// on mattn/go-sqlite3 and therefore requires cgo. It owns no global state:
// callers pass a context, a config and a logger.
// 包 sqlite 打开并关闭 GORM SQLite 连接。
//
// dialector 使用 github.com/glebarez/sqlite（modernc.org/sqlite）：纯 Go、无需
// cgo，可直接替换基于 mattn/go-sqlite3 因而需要 cgo 的 gorm.io/driver/sqlite。
// 它不持有全局状态：调用方传入 context、配置和 logger。
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ErrDSNRequired is returned when Config.DSN is empty.
// ErrDSNRequired 在 Config.DSN 为空时返回。
var ErrDSNRequired = errors.New("sqlite: dsn is required")

// Config describes a SQLite connection pool. DSN is a file path (app.db),
// ":memory:", or a URI with driver pragmas, for example
// "file:app.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)".
// Config 描述 SQLite 连接 pool。DSN 可以是文件路径（app.db）、":memory:"，
// 或带 driver pragma 的 URI；零值字段在 New 中填充默认值。
type Config struct {
	DSN string `yaml:"dsn"`
	// SQLite is single-writer with many readers: the default keeps one
	// connection, which avoids write-lock contention inside one process.
	// Raising concurrency requires the DSN to enable WAL and busy_timeout;
	// :memory: also needs shared cache, otherwise every connection sees its own
	// separate database.
	// SQLite 是单写多读：默认只保留一个连接，避免同进程内的写锁竞争。
	// 提高并发需要 DSN 打开 WAL 和 busy_timeout；:memory: 还需 shared cache，
	// 否则每个连接会看到各自独立的库。
	MaxIdleConns    int           `yaml:"max_idle_conns" default:"1"`
	MaxOpenConns    int           `yaml:"max_open_conns" default:"1"`
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
		return errors.New("sqlite: pool sizes must not be negative")
	}
	if c.MaxIdleConns > 0 && c.MaxOpenConns > 0 && c.MaxIdleConns > c.MaxOpenConns {
		return errors.New("sqlite: max_idle_conns must not exceed max_open_conns")
	}
	if c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 || c.PingTimeout < 0 || c.SlowThreshold < 0 {
		return errors.New("sqlite: durations must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "", "silent", "error", "warn", "warning", "info":
	default:
		return fmt.Errorf("sqlite: invalid log level %q", c.LogLevel)
	}
	return nil
}

func (c Config) withDefaults() Config {
	out := c
	out.DSN = strings.TrimSpace(out.DSN)
	if out.MaxIdleConns == 0 {
		out.MaxIdleConns = 1
	}
	if out.MaxOpenConns == 0 {
		out.MaxOpenConns = 1
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

// New opens a SQLite connection, configures the pool and verifies it with a
// ping. It never panics and never logs the DSN.
// New 打开 SQLite 连接、配置 pool 并用 ping 验证；不会 panic，也不会在日志或错误
// 里输出完整 DSN。ping 失败时会先关闭已打开的连接再返回错误。
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
	conn, err := gorm.Open(sqlite.Open(cfg.DSN), &gorm.Config{
		Logger:                                   gormLog,
		PrepareStmt:                              cfg.PrepareStmt,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", cfg.DSN, err)
	}

	sqlDB, err := conn.DB()
	if err != nil {
		return nil, fmt.Errorf("sqlite: get sql.DB: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("sqlite: ping %s: %w", cfg.DSN, err)
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
		return fmt.Errorf("sqlite: get sql.DB: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("sqlite: close: %w", err)
	}
	return nil
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
