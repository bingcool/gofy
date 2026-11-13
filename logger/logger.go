// Package logger builds zap loggers from an explicit configuration.
// It has no package-level state and never touches time.Local.
// 包 logger 依据显式配置构建 zap 日志器。
// 它不持有包级状态，也不会改写 time.Local，因此不会影响调用方依赖的本地时区行为。
package logger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config describes a zap logger.
// Config 描述一个 zap 日志器；字段在构建前会先经 Validate 校验，非法值不会被静默纠正。
type Config struct {
	Level              string            `yaml:"level" default:"info"`    // debug|info|warn|error；可选值，默认 info
	Encoding           string            `yaml:"encoding" default:"json"` // json|console；可选值，默认 json
	Color              string            `yaml:"color" default:"auto"`    // auto|always|never，仅作用于 stdout/stderr
	Outputs            []string          `yaml:"outputs"`                 // stdout|stderr|file path；可混用多个目标，文件路径的父目录由框架创建
	ErrorOutputs       []string          `yaml:"error_outputs"`           // error level and above only；只接收 error 及以上级别
	SystemErrorOutputs []string          `yaml:"system_error_outputs"`    // framework errors only；只接收框架自身产生的错误
	TimeLayout         string            `yaml:"time_layout" default:"2006-01-02 15:04:05"`
	Rotation           RotationConfig    `yaml:"rotation"`
	BaseFields         map[string]string `yaml:"base_fields"`
}

// RotationConfig controls file rotation.
// RotationConfig 控制文件滚动；只对文件路径生效，stdout/stderr 与空路径不受影响。
type RotationConfig struct {
	Enabled bool `yaml:"enabled"`
	// Mode selects how Outputs files roll: "size" (default, lumberjack) or
	// "hourly" (info-h0.log ... info-h23.log). ErrorOutputs and
	// SystemErrorOutputs always use size rotation to avoid hourly fragments.
	// Mode 决定 Outputs 文件的滚动方式："size"（默认，交给 lumberjack）或
	// "hourly"（info-h0.log ... info-h23.log）。ErrorOutputs 与 SystemErrorOutputs
	// 始终使用 size 滚动，避免错误日志被切成小时碎片而难以排查。
	Mode string `yaml:"mode" default:"size"`
	// DateDir writes each file under a date subdirectory such as 20261106.
	// DateDir 为 true 时把每个文件写到日期子目录（如 20261106）下；子目录名由 DateLayout 格式化得出。
	DateDir    bool   `yaml:"date_dir"`
	DateLayout string `yaml:"date_layout" default:"20060102"`
	// DateDirMaxAgeDays keeps date directories for this many days. When a
	// directory expires the whole YYYYMMDD tree is removed. Zero means the
	// default of 7 days.
	// DateDirMaxAgeDays 是日期目录的保留天数；目录过期时整棵 YYYYMMDD 树会被删除，
	// 因此该清理是破坏性操作，不要指向非日志目录。0 表示使用默认的 7 天。
	DateDirMaxAgeDays int  `yaml:"date_dir_max_age_days" default:"7"`
	MaxSizeMB         int  `yaml:"max_size_mb" default:"100"`
	MaxAgeDays        int  `yaml:"max_age_days" default:"30"`
	MaxBackups        int  `yaml:"max_backups" default:"7"`
	Compress          bool `yaml:"compress" default:"true"`
	LocalTime         bool `yaml:"local_time" default:"true"`
}

// Validate checks the configuration without touching the filesystem.
// Validate 只做配置校验，不访问文件系统，因此可在启动早期调用，把非法配置尽早暴露为错误。
func (c Config) Validate() error {
	if _, err := parseLevel(c.Level); err != nil {
		return err
	}
	switch c.Encoding {
	case "", "json", "console":
	default:
		return fmt.Errorf("logger: invalid encoding %q (want json or console)", c.Encoding)
	}
	if normalizeColor(c.Color) == "" {
		return fmt.Errorf("logger: invalid color %q (want auto, always or never)", c.Color)
	}
	if c.Rotation.MaxSizeMB < 0 || c.Rotation.MaxAgeDays < 0 || c.Rotation.MaxBackups < 0 || c.Rotation.DateDirMaxAgeDays < 0 {
		return errors.New("logger: rotation values must not be negative")
	}
	if mode := normalizeRotationMode(c.Rotation.Mode); mode != rotationModeSize && mode != rotationModeHourly {
		return fmt.Errorf("logger: invalid rotation mode %q (want size or hourly)", c.Rotation.Mode)
	}
	if strings.ContainsAny(c.Rotation.DateLayout, `/\`) {
		return fmt.Errorf("logger: invalid date_layout %q (must not contain path separators)", c.Rotation.DateLayout)
	}
	for _, out := range append(append(append([]string{}, c.Outputs...), c.ErrorOutputs...), c.SystemErrorOutputs...) {
		if strings.TrimSpace(out) == "" {
			return errors.New("logger: output path must not be empty")
		}
	}
	return nil
}

// New builds a logger. The caller owns the returned logger and should call
// Sync before exiting.
// New 构建日志器。返回的 logger 由调用方持有，进程退出前必须调用 Sync，
// 否则缓冲区中尚未刷盘的日志可能丢失；框架内由外层统一负责刷盘时无需重复调用。
func New(cfg Config) (*zap.Logger, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	level, _ := parseLevel(cfg.Level)

	outputs := cfg.Outputs
	if len(outputs) == 0 {
		outputs = []string{"stdout"}
	}

	cores := make([]zapcore.Core, 0, len(outputs)+len(cfg.ErrorOutputs))
	for _, out := range outputs {
		sink, file, err := openSink(out, cfg.Rotation, os.Stdout, "")
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(newSinkEncoder(cfg, file), sink, level))
	}
	for _, out := range cfg.ErrorOutputs {
		sink, file, err := openSink(out, cfg.Rotation, os.Stderr, rotationModeSize)
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(newSinkEncoder(cfg, file), sink, zap.ErrorLevel))
	}

	return zap.New(zapcore.NewTee(cores...), newOptions(cfg)...), nil
}

// NewSystem builds the logger for framework-level failures such as recovered
// panics. It writes error entries to SystemErrorOutputs only and returns a nop
// logger when the list is empty, so callers can always use the result.
// NewSystem 构建用于框架级失败（例如 recover 到的 panic）的日志器。它只把 error
// 级别写入 SystemErrorOutputs；该列表为空时返回 nop logger，所以调用方可以直接
// 使用结果而不必判空，也不会因为缺少系统错误输出而丢掉 panic 记录。
func NewSystem(cfg Config) (*zap.Logger, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.SystemErrorOutputs) == 0 {
		return Nop(), nil
	}
	cores := make([]zapcore.Core, 0, len(cfg.SystemErrorOutputs))
	for _, out := range cfg.SystemErrorOutputs {
		sink, file, err := openSink(out, cfg.Rotation, os.Stderr, rotationModeSize)
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(newSinkEncoder(cfg, file), sink, zap.ErrorLevel))
	}
	return zap.New(zapcore.NewTee(cores...), newOptions(cfg)...), nil
}

func newOptions(cfg Config) []zap.Option {
	opts := []zap.Option{
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
	}
	if len(cfg.BaseFields) > 0 {
		opts = append(opts, zap.Fields(baseFields(cfg.BaseFields)...))
	}
	return opts
}

// Nop returns a logger that discards everything.
// Nop 返回丢弃所有日志的 logger，适合作为默认值或测试替身，调用方无需判空。
func Nop() *zap.Logger { return zap.NewNop() }

// Sync flushes buffered log entries. Errors caused by unsupported or already
// closed sinks (common for stdout/tty) are ignored.
// Sync 刷新缓冲的日志条目。stdout/tty 这类 sink 常见的“不支持 sync”“已关闭”错误会被
// 忽略，避免退出流程因终端无关的 sync 失败而中断；其余错误仍会返回给调用方。
func Sync(l *zap.Logger) error {
	if l == nil {
		return nil
	}
	err := l.Sync()
	if err == nil || ignorableSyncError(err) {
		return nil
	}
	return err
}

func ignorableSyncError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"invalid argument", "inappropriate ioctl", "not a terminal", "already closed", "bad file descriptor"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func parseLevel(level string) (zapcore.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return zap.InfoLevel, nil
	case "debug":
		return zap.DebugLevel, nil
	case "warn", "warning":
		return zap.WarnLevel, nil
	case "error":
		return zap.ErrorLevel, nil
	default:
		return zap.InfoLevel, fmt.Errorf("logger: invalid level %q (want debug, info, warn or error)", level)
	}
}

func newEncoder(cfg Config) zapcore.Encoder {
	encCfg := encoderConfig(cfg)
	if strings.EqualFold(cfg.Encoding, "console") {
		return zapcore.NewConsoleEncoder(encCfg)
	}
	return zapcore.NewJSONEncoder(encCfg)
}

func encoderConfig(cfg Config) zapcore.EncoderConfig {
	layout := cfg.TimeLayout
	if layout == "" {
		layout = "2006-01-02 15:04:05"
	}
	return zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.TimeEncoderOfLayout(layout),
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
}

// newSinkEncoder builds the encoder for one output. Console sinks render error
// entries as a red multi-line block, which the encoder owns so files stay free
// of ANSI codes even when the same process writes to both.
// newSinkEncoder 为单个输出构建 encoder。控制台输出会把 error 条目渲染成红色多行块；
// 着色逻辑由 encoder 自身承担，这样同一进程同时写文件时，文件内容不会混入 ANSI 转义码。
func newSinkEncoder(cfg Config, file *os.File) zapcore.Encoder {
	encoder := newEncoder(cfg)
	if colorAllowed(cfg.Color, file) {
		return newTerminalErrorEncoder(encoder, cfg)
	}
	return encoder
}

// openSink builds one output. mode is "" for info outputs (they follow
// Rotation.Mode) and "size" for error streams. The second result is the console
// file (stdout/stderr) or nil for file sinks, which never get ANSI colors.
// openSink 构建单个输出。mode 为空表示 info 输出（跟随 Rotation.Mode），传 "size"
// 表示错误流。第二个返回值是控制台文件（stdout/stderr），文件 sink 时为 nil，
// 调用方据此判断该输出是否允许着色。
func openSink(target string, rotation RotationConfig, fallback *os.File, mode string) (zapcore.WriteSyncer, *os.File, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "stdout":
		return zapcore.AddSync(os.Stdout), os.Stdout, nil
	case "stderr":
		return zapcore.AddSync(os.Stderr), os.Stderr, nil
	case "":
		return zapcore.AddSync(fallback), fallback, nil
	}
	if rotation.Enabled {
		return zapcore.AddSync(newRotatingFileSink(target, rotation, mode)), nil, nil
	}
	// With rotation off, {date} is expanded only once at open time so the literal
	// token never ends up in the file name.
	// 关闭滚动时 {date} 只在打开文件时展开一次，避免把字面量写进文件名。
	target = expandDateToken(target, rotation, time.Now())
	// In date-directory mode directories such as storage/log/http may not exist
	// yet; lumberjack creates its own, so create it here for the direct-write path.
	// 按启动模式分目录时，storage/log/http 等目录可能还不存在；lumberjack
	// 会自己建目录，这里补齐直接写文件的分支。
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, nil, fmt.Errorf("logger: create dir for %s: %w", target, err)
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("logger: open %s: %w", target, err)
	}
	return zapcore.AddSync(f), nil, nil
}

func baseFields(fields map[string]string) []zap.Field {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]zap.Field, 0, len(keys))
	for _, k := range keys {
		out = append(out, zap.String(k, fields[k]))
	}
	return out
}
