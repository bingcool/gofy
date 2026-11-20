package sqllog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// GormLoggerConfig configures the zap-backed GORM logger.
//
// Mode and Policy are the sql_log-aware fields. When Policy is nil the logger
// keeps the legacy contract (ParameterizedQueries decides whether values are
// expanded); a non-empty Mode wins over ParameterizedQueries.
// GormLoggerConfig 配置基于 zap 的 GORM 日志器。
//
// Mode 与 Policy 是感知 sql_log 的字段。Policy 为 nil 时保持旧契约（由
// ParameterizedQueries 决定是否展开参数）；Mode 非空时优先于 ParameterizedQueries。
type GormLoggerConfig struct {
	SlowThreshold             time.Duration
	LogLevel                  string // silent|error|warn|info；可选值，非法值退化为 warn
	IgnoreRecordNotFoundError bool
	ParameterizedQueries      bool
	// Mode overrides ParameterizedQueries: off|placeholder|full.
	// Mode 优先于 ParameterizedQueries：off|placeholder|full。
	Mode string
	// Policy is the process-wide sql_log policy. It carries sampling, rate
	// limits, masking and the mode; handing the same pointer to every
	// connection is what keeps those limits process-wide.
	// Policy 是进程级 sql_log 策略，携带采样、限流、脱敏与档位；把同一个指针传给每个
	// 连接，这些上限才是进程级的。
	Policy *Policy
}

// GormLogger adapts a zap logger to gorm's logger interface.
// GormLogger 把 zap logger 适配成 gorm 的日志接口；传入 nil 时使用 nop logger，
// 因此调用方无需保证已有日志器。
func GormLogger(l *zap.Logger, cfg GormLoggerConfig) gormlogger.Interface {
	if l == nil {
		l = zap.NewNop()
	}
	level, silent, err := parseLevel(cfg.LogLevel)
	if err != nil {
		level, silent = zap.WarnLevel, false
	}
	gormLevel := toGormLevel(level)
	if silent {
		// "silent" is gorm's "log nothing", which has no zap equivalent; it is
		// carried separately so the level comparisons below cannot let a slow
		// or failed statement through.
		// "silent" 是 gorm 的"什么都不打"，在 zap 里没有对应级别；它单独承载，下面按
		// 级别判断的分支因此不会漏出慢语句或失败语句。
		gormLevel = gormlogger.Silent
	}
	return &gormLogger{
		logger:         l.With(zap.String("component", "gorm")),
		level:          gormLevel,
		silent:         silent,
		slowThreshold:  cfg.SlowThreshold,
		ignoreNotFound: cfg.IgnoreRecordNotFoundError,
		parameterized:  cfg.ParameterizedQueries,
		mode:           strings.ToLower(strings.TrimSpace(cfg.Mode)),
		policy:         cfg.Policy,
	}
}

// toGormLevel maps a zap level onto gorm's scale. The two scales look alike but
// are compared with gorm's own constants, so the mapping is explicit.
// toGormLevel 把 zap 级别映射到 gorm 的刻度。两套刻度看起来相似，但比较时用的是 gorm
// 自己的常量，因此显式映射。
// parseLevel turns the configured log_level into a zap level. It is a local
// copy of the logger package's parser: support/sqllog must not import logger,
// or every database package that imports sqllog would pull the framework's
// logging setup back in through the dependency graph.
// parseLevel 把配置里的 log_level 转成 zap 级别。它是 logger 包同名解析器的本地副本：
// support/sqllog 不能 import logger，否则每个引用 sqllog 的数据库包都会经由依赖图把
// 框架的日志装配重新拉回来。
func parseLevel(level string) (zapcore.Level, bool, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return zap.InfoLevel, false, nil
	case "debug":
		return zap.DebugLevel, false, nil
	case "warn", "warning":
		return zap.WarnLevel, false, nil
	case "error":
		return zap.ErrorLevel, false, nil
	case "silent":
		// Every caller's config validator accepts "silent", and the field
		// documents it, so rejecting it here silently rewrote the operator's
		// intent: the constructor fell back to warn and a pool that asked for no
		// SQL logging kept emitting slow and failed statements.
		// 各调用方的配置校验都接受 "silent"，字段注释也写着它，因此在这里拒收等于静默
		// 改写运维的意图：构造函数退化成 warn，声明"不要打 SQL"的连接池照样输出慢语句
		// 与失败语句。
		return zap.ErrorLevel, true, nil
	default:
		return zap.InfoLevel, false, fmt.Errorf("sqllog: invalid level %q (want debug, info, warn, error or silent)", level)
	}
}

func toGormLevel(level zapcore.Level) gormlogger.LogLevel {
	switch {
	case level <= zapcore.InfoLevel:
		return gormlogger.Info
	case level <= zapcore.WarnLevel:
		return gormlogger.Warn
	default:
		return gormlogger.Error
	}
}

type gormLogger struct {
	logger         *zap.Logger
	level          gormlogger.LogLevel
	slowThreshold  time.Duration
	ignoreNotFound bool
	parameterized  bool
	mode           string
	policy         *Policy
	silent         bool
}

// ParamsFilter implements gorm.ParamsFilter. Returning a nil argument list makes
// the dialector skip expansion, so Trace sees the template with its placeholders
// instead of the actual arguments — that is what keeps values out of the log in
// off and placeholder mode.
// ParamsFilter 实现 gorm.ParamsFilter。返回 nil 参数列表会让 dialector 跳过展开，
// Trace 因此拿到带占位符的模板而不是实际参数——这正是 off/placeholder 档不让值进入
// 日志的方式。
func (g *gormLogger) ParamsFilter(_ context.Context, sql string, params ...interface{}) (string, []interface{}) {
	if g.sqlMode() == ModeFull {
		return sql, params
	}
	return sql, nil
}

// LogMode returns a copy with another severity. The policy pointer stays shared
// on purpose: rate limits belong to the process, not to one logger handle.
// LogMode 返回改了严重级别的副本。policy 指针刻意保持共享：限流属于进程，不属于某个
// logger 句柄。
func (g *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	cp := *g
	cp.silent = level == gormlogger.Silent
	cp.level = level
	return &cp
}

func (g *gormLogger) Info(_ context.Context, msg string, args ...interface{}) {
	if !g.enabled() || g.level < gormlogger.Info {
		return
	}
	g.logger.Sugar().Infof(msg, args...)
}

func (g *gormLogger) Warn(_ context.Context, msg string, args ...interface{}) {
	if !g.enabled() || g.level < gormlogger.Warn {
		return
	}
	g.logger.Sugar().Warnf(msg, args...)
}

func (g *gormLogger) Error(_ context.Context, msg string, args ...interface{}) {
	if !g.enabled() || g.level < gormlogger.Error {
		return
	}
	g.logger.Sugar().Errorf(msg, args...)
}

// Trace records one statement. The order of the checks is what keeps the hot
// path cheap: an off-mode success returns before fc() runs, so the dialector's
// string interpolation never happens for statements that produce no log line.
// Trace 记录一条语句。检查顺序决定了热路径的开销：off 档的成功语句在调用 fc() 之前
// 返回，因此不产生日志的语句连 dialector 的字符串插值都不做。
func (g *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if !g.enabled() {
		return
	}
	elapsed := time.Since(begin)
	mode := g.sqlMode()
	slow := g.slowThreshold > 0 && elapsed > g.slowThreshold
	if mode == ModeOff && err == nil && !slow {
		return
	}

	sql, rows := fc()
	if !g.logSchemaQueries() && isSchemaQuery(sql) {
		return
	}

	fields := sqlTraceFields(ctx)
	fields = append(fields,
		zap.Duration("elapsed", elapsed),
		zap.Int64("rows", rows),
		zap.String("sql_mode", string(mode)),
	)
	fields = append(fields, g.statementFields(sql, mode)...)

	switch {
	case err != nil && !(g.ignoreNotFound && errors.Is(err, gorm.ErrRecordNotFound)):
		if g.level < gormlogger.Error {
			return
		}
		ok, dropped := g.allowError()
		if !ok {
			return
		}
		g.logSuppressed("sql error suppressed", dropped)
		g.logger.Error("sql error", append(fields, zap.Error(err))...)
	case slow && g.level >= gormlogger.Warn:
		ok, dropped := g.allowSlow()
		if !ok {
			return
		}
		g.logSuppressed("slow sql suppressed", dropped)
		g.logger.Warn("slow sql", append(fields,
			zap.String("threshold", g.slowThreshold.String()))...)
	case g.level >= gormlogger.Info && g.sample():
		g.logger.Info("sql", fields...)
	}
}

// statementFields renders the statement for the mode and reports whether it was
// masked. An off-mode statement contributes no fields at all — "no sql field"
// is a state the log platform can check, an empty one is not.
// statementFields 按档位渲染语句，并带上是否脱敏。off 档不贡献任何字段——"没有 sql
// 字段"是日志平台可以判断的状态，空串不是。
func (g *gormLogger) statementFields(sql string, mode Mode) []zap.Field {
	if mode == ModeOff || sql == "" {
		return nil
	}
	rendered := sanitizeControl(g.maskSQL(sql))
	rendered, truncated := g.truncate(rendered)
	fields := []zap.Field{zap.String("sql", rendered)}
	if truncated {
		fields = append(fields, zap.Bool("truncated", true))
	}
	if g.policy != nil && g.policy.MaskEnabled() {
		fields = append(fields, zap.Bool("sql_masked", true))
	}
	return fields
}

// enabled reports whether any output is possible at all.
// enabled 报告是否可能产生任何输出。
func (g *gormLogger) enabled() bool {
	if g.silent {
		return false
	}
	if g.policy == nil {
		return true
	}
	return !g.policy.Disabled()
}

// sqlMode resolves the mode: an active policy wins, then an explicit Mode, then
// the legacy boolean.
// sqlMode 解析档位：生效的策略优先，其次是显式 Mode，最后才是旧的布尔字段。
func (g *gormLogger) sqlMode() Mode {
	if g.policy != nil {
		return g.policy.Mode()
	}
	if g.mode != "" {
		if mode, err := ParseMode(g.mode); err == nil {
			return mode
		}
	}
	if g.parameterized {
		return ModePlaceholder
	}
	return ModeFull
}

func (g *gormLogger) logSchemaQueries() bool {
	return g.policy == nil || g.policy.LogSchemaQueries()
}

func (g *gormLogger) allowError() (bool, int) {
	if g.policy == nil {
		return true, 0
	}
	return g.policy.AllowError()
}

func (g *gormLogger) allowSlow() (bool, int) {
	if g.policy == nil {
		return true, 0
	}
	return g.policy.AllowSlow()
}

func (g *gormLogger) sample() bool {
	if g.policy == nil {
		return true
	}
	return g.policy.SampleInfo()
}

func (g *gormLogger) maskSQL(sql string) string {
	if g.policy == nil {
		return sql
	}
	return g.policy.MaskSQL(sql)
}

func (g *gormLogger) truncate(sql string) (string, bool) {
	if g.policy == nil {
		return sql, false
	}
	return g.policy.Truncate(sql)
}

// logSuppressed writes one summary line when a limiter dropped entries in the
// window that just closed, so "throttled" never looks like "nothing happened".
// logSuppressed 在上一个窗口有丢弃时写一条汇总，让"被限流"不会看起来像"什么都没发生"。
func (g *gormLogger) logSuppressed(msg string, dropped int) {
	if dropped > 0 {
		g.logger.Warn(msg, zap.Int("suppressed", dropped), zap.String("window", "1s"))
	}
}

// isSchemaQuery reports whether the statement is metadata introspection, which
// gorm emits in bulk during AutoMigrate and which nobody debugs.
// isSchemaQuery 判断语句是否为元数据探查——AutoMigrate 会成批发出这类语句，而且没人
// 会去排查它们。
func isSchemaQuery(sql string) bool {
	if sql == "" {
		return false
	}
	upper := strings.ToUpper(strings.TrimSpace(sql))
	if strings.HasPrefix(upper, "SHOW ") {
		return true
	}
	return strings.Contains(strings.ToLower(sql), "information_schema")
}
