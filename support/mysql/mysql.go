// Package mysql opens and closes a GORM MySQL connection.
// It owns no global state: callers pass a context, a config and a logger.
package mysql

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
var ErrDSNRequired = errors.New("mysql: dsn is required")

// Config describes a MySQL connection pool.
type Config struct {
	DSN             string        `yaml:"dsn"`
	MaxIdleConns    int           `yaml:"max_idle_conns" default:"10"`
	MaxOpenConns    int           `yaml:"max_open_conns" default:"50"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime" default:"1h"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time" default:"10m"`
	PingTimeout     time.Duration `yaml:"ping_timeout" default:"5s"`
	SlowThreshold   time.Duration `yaml:"slow_threshold" default:"200ms"`
	LogLevel        string        `yaml:"log_level" default:"warn"` // silent|error|warn|info
	PrepareStmt     bool          `yaml:"prepare_stmt" default:"false"`
}

// Validate checks the config without connecting.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DSN) == "" {
		return ErrDSNRequired
	}
	if c.MaxIdleConns < 0 || c.MaxOpenConns < 0 {
		return errors.New("mysql: pool sizes must not be negative")
	}
	if c.MaxIdleConns > 0 && c.MaxOpenConns > 0 && c.MaxIdleConns > c.MaxOpenConns {
		return errors.New("mysql: max_idle_conns must not exceed max_open_conns")
	}
	if c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 || c.PingTimeout < 0 || c.SlowThreshold < 0 {
		return errors.New("mysql: durations must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "", "silent", "error", "warn", "warning", "info":
	default:
		return fmt.Errorf("mysql: invalid log level %q", c.LogLevel)
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

// New opens a MySQL connection, configures the pool and verifies it with a
// ping. It never panics and never logs the DSN.
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
		return nil, fmt.Errorf("mysql: open %s: %w", redactDSN(cfg.DSN), err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("mysql: get sql.DB: %w", err)
	}
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.PingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("mysql: ping %s: %w", redactDSN(cfg.DSN), err)
	}
	return db, nil
}

// Close releases the underlying connection pool. A nil db is a no-op.
func Close(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("mysql: get sql.DB: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("mysql: close: %w", err)
	}
	return nil
}

// redactDSN hides the password portion of a DSN before it reaches a log or error.
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
	parameterized bool
}

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
	}
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
	if g.parameterized {
		sql = ""
	}
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
