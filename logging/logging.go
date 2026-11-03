// Package logging builds zap loggers from an explicit configuration.
// It has no package-level state and never touches time.Local.
package logging

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Config describes a zap logger.
type Config struct {
	Level        string            `yaml:"level" default:"info"`    // debug|info|warn|error
	Encoding     string            `yaml:"encoding" default:"json"` // json|console
	Outputs      []string          `yaml:"outputs"`                 // stdout|stderr|file path
	ErrorOutputs []string          `yaml:"error_outputs"`           // error level and above only
	TimeLayout   string            `yaml:"time_layout" default:"2006-01-02 15:04:05"`
	Rotation     RotationConfig    `yaml:"rotation"`
	BaseFields   map[string]string `yaml:"base_fields"`
}

// RotationConfig controls file rotation handled by lumberjack.
type RotationConfig struct {
	Enabled    bool `yaml:"enabled"`
	MaxSizeMB  int  `yaml:"max_size_mb" default:"100"`
	MaxAgeDays int  `yaml:"max_age_days" default:"30"`
	MaxBackups int  `yaml:"max_backups" default:"7"`
	Compress   bool `yaml:"compress" default:"true"`
	LocalTime  bool `yaml:"local_time" default:"true"`
}

// Validate checks the configuration without touching the filesystem.
func (c Config) Validate() error {
	if _, err := parseLevel(c.Level); err != nil {
		return err
	}
	switch c.Encoding {
	case "", "json", "console":
	default:
		return fmt.Errorf("logging: invalid encoding %q (want json or console)", c.Encoding)
	}
	if c.Rotation.MaxSizeMB < 0 || c.Rotation.MaxAgeDays < 0 || c.Rotation.MaxBackups < 0 {
		return errors.New("logging: rotation values must not be negative")
	}
	for _, out := range append(append([]string{}, c.Outputs...), c.ErrorOutputs...) {
		if strings.TrimSpace(out) == "" {
			return errors.New("logging: output path must not be empty")
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
	encoder := newEncoder(cfg)

	outputs := cfg.Outputs
	if len(outputs) == 0 {
		outputs = []string{"stdout"}
	}

	cores := make([]zapcore.Core, 0, len(outputs)+len(cfg.ErrorOutputs))
	for _, out := range outputs {
		sink, err := openSink(out, cfg.Rotation, os.Stdout)
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(encoder, sink, level))
	}
	for _, out := range cfg.ErrorOutputs {
		sink, err := openSink(out, cfg.Rotation, os.Stderr)
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(encoder, sink, zap.ErrorLevel))
	}

	opts := []zap.Option{
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
	}
	if len(cfg.BaseFields) > 0 {
		opts = append(opts, zap.Fields(baseFields(cfg.BaseFields)...))
	}
	return zap.New(zapcore.NewTee(cores...), opts...), nil
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
		return zap.InfoLevel, fmt.Errorf("logging: invalid level %q (want debug, info, warn or error)", level)
	}
}

func newEncoder(cfg Config) zapcore.Encoder {
	layout := cfg.TimeLayout
	if layout == "" {
		layout = "2006-01-02 15:04:05"
	}
	encCfg := zapcore.EncoderConfig{
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
	if strings.EqualFold(cfg.Encoding, "console") {
		return zapcore.NewConsoleEncoder(encCfg)
	}
	return zapcore.NewJSONEncoder(encCfg)
}

func openSink(target string, rotation RotationConfig, fallback *os.File) (zapcore.WriteSyncer, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "stdout":
		return zapcore.AddSync(os.Stdout), nil
	case "stderr":
		return zapcore.AddSync(os.Stderr), nil
	case "":
		return zapcore.AddSync(fallback), nil
	}
	if rotation.Enabled {
		return zapcore.AddSync(&lumberjack.Logger{
			Filename:   target,
			MaxSize:    rotation.MaxSizeMB,
			MaxAge:     rotation.MaxAgeDays,
			MaxBackups: rotation.MaxBackups,
			Compress:   rotation.Compress,
			LocalTime:  rotation.LocalTime,
		}), nil
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("logging: open %s: %w", target, err)
	}
	return zapcore.AddSync(f), nil
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
