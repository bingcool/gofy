// Package logger builds zap loggers from an explicit configuration.
// It has no package-level state and never touches time.Local.
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
type Config struct {
	Level              string            `yaml:"level" default:"info"`    // debug|info|warn|error
	Encoding           string            `yaml:"encoding" default:"json"` // json|console
	Color              string            `yaml:"color" default:"auto"`    // auto|always|never，仅作用于 stdout/stderr
	Outputs            []string          `yaml:"outputs"`                 // stdout|stderr|file path
	ErrorOutputs       []string          `yaml:"error_outputs"`           // error level and above only
	SystemErrorOutputs []string          `yaml:"system_error_outputs"`    // framework errors only
	TimeLayout         string            `yaml:"time_layout" default:"2006-01-02 15:04:05"`
	Rotation           RotationConfig    `yaml:"rotation"`
	BaseFields         map[string]string `yaml:"base_fields"`
}

// RotationConfig controls file rotation.
type RotationConfig struct {
	Enabled bool `yaml:"enabled"`
	// Mode selects how Outputs files roll: "size" (default, lumberjack) or
	// "hourly" (info-h0.log ... info-h23.log). ErrorOutputs and
	// SystemErrorOutputs always use size rotation to avoid hourly fragments.
	Mode string `yaml:"mode" default:"size"`
	// DateDir writes each file under a date subdirectory such as 20261106.
	DateDir    bool   `yaml:"date_dir"`
	DateLayout string `yaml:"date_layout" default:"20060102"`
	// DateDirMaxAgeDays keeps date directories for this many days. When a
	// directory expires the whole YYYYMMDD tree is removed. Zero means the
	// default of 7 days.
	DateDirMaxAgeDays int  `yaml:"date_dir_max_age_days" default:"7"`
	MaxSizeMB         int  `yaml:"max_size_mb" default:"100"`
	MaxAgeDays        int  `yaml:"max_age_days" default:"30"`
	MaxBackups        int  `yaml:"max_backups" default:"7"`
	Compress          bool `yaml:"compress" default:"true"`
	LocalTime         bool `yaml:"local_time" default:"true"`
}

// Validate checks the configuration without touching the filesystem.
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
func Nop() *zap.Logger { return zap.NewNop() }

// Sync flushes buffered log entries. Errors caused by unsupported or already
// closed sinks (common for stdout/tty) are ignored.
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
	// 关闭滚动时 {date} 只在打开文件时展开一次，避免把字面量写进文件名。
	target = expandDateToken(target, rotation, time.Now())
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
