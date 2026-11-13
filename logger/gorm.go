package logger

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// GormLoggerConfig configures the zap-backed GORM logger.
// GormLoggerConfig 配置基于 zap 的 GORM 日志器；LogLevel 非法时会退化为 warn，不会中断启动。
type GormLoggerConfig struct {
	SlowThreshold             time.Duration
	LogLevel                  string // silent|error|warn|info；可选值，非法值退化为 warn
	IgnoreRecordNotFoundError bool
	ParameterizedQueries      bool
}

// GormLogger adapts a zap logger to gorm's logger interface.
// GormLogger 把 zap logger 适配成 gorm 的日志接口；传入 nil 时使用 nop logger，
// 因此调用方无需保证已有日志器。ParameterizedQueries 为 true 时 Trace 中不再输出 SQL 文本。
func GormLogger(l *zap.Logger, cfg GormLoggerConfig) gormlogger.Interface {
	if l == nil {
		l = zap.NewNop()
	}
	level, err := parseLevel(cfg.LogLevel)
	if err != nil {
		level = zap.WarnLevel
	}
	return &gormLogger{
		logger:         l.With(zap.String("component", "gorm")),
		level:          level,
		slowThreshold:  cfg.SlowThreshold,
		ignoreNotFound: cfg.IgnoreRecordNotFoundError,
		parameterized:  cfg.ParameterizedQueries,
	}
}

type gormLogger struct {
	logger         *zap.Logger
	level          zapcore.Level
	slowThreshold  time.Duration
	ignoreNotFound bool
	parameterized  bool
	silent         bool
}

func (g *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	cp := *g
	cp.silent = false
	switch level {
	case gormlogger.Silent:
		cp.silent = true
	case gormlogger.Error:
		cp.level = zapcore.ErrorLevel
	case gormlogger.Warn:
		cp.level = zapcore.WarnLevel
	default:
		cp.level = zapcore.InfoLevel
	}
	return &cp
}

func (g *gormLogger) Info(_ context.Context, msg string, args ...interface{}) {
	if g.silent || g.level > zapcore.InfoLevel {
		return
	}
	g.logger.Sugar().Infof(msg, args...)
}

func (g *gormLogger) Warn(_ context.Context, msg string, args ...interface{}) {
	if g.silent || g.level > zapcore.WarnLevel {
		return
	}
	g.logger.Sugar().Warnf(msg, args...)
}

func (g *gormLogger) Error(_ context.Context, msg string, args ...interface{}) {
	if g.silent {
		return
	}
	g.logger.Sugar().Errorf(msg, args...)
}

func (g *gormLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.silent {
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
	case err != nil && !(g.ignoreNotFound && errors.Is(err, gorm.ErrRecordNotFound)):
		g.logger.Error("sql error", append(fields, zap.Error(err))...)
	case g.slowThreshold > 0 && elapsed > g.slowThreshold && g.level <= zapcore.WarnLevel:
		g.logger.Warn("slow sql", append(fields, zap.String("threshold", g.slowThreshold.String()))...)
	case g.level <= zapcore.InfoLevel:
		g.logger.Info("sql", fields...)
	}
}
