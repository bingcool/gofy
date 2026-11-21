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
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config describes a zap logger.
// Config 描述一个 zap 日志器；字段在构建前会先经 Validate 校验，非法值不会被静默纠正。
type Config struct {
	Level              string   `yaml:"level" default:"info"`    // debug|info|warn|error；可选值，默认 info
	Encoding           string   `yaml:"encoding" default:"json"` // json|console；可选值，默认 json
	Color              string   `yaml:"color" default:"auto"`    // auto|always|never，仅作用于 stdout/stderr
	Outputs            []string `yaml:"outputs"`                 // stdout|stderr|file path；可混用多个目标，文件路径的父目录由框架创建
	ErrorOutputs       []string `yaml:"error_outputs"`           // error level and above only；只接收 error 及以上级别
	SystemErrorOutputs []string `yaml:"system_error_outputs"`    // framework errors only；只接收框架自身产生的错误
	AccessOutputs      []string `yaml:"access_outputs"`          // HTTP access entries only；只接收 HTTP 访问日志条目，为空时复用应用 logger
	// SQLOutputs receives SQL entries only; empty means "reuse the application
	// logger". It sits next to AccessOutputs because both are destinations:
	// log: says where entries go, sql_log: says what a SQL entry contains.
	// SQLOutputs 只接收 SQL 条目；为空表示复用应用 logger。它和 AccessOutputs 并列，
	// 因为两者都是"去哪"：`log:` 管日志去哪，`sql_log:` 管 SQL 条目记什么。
	SQLOutputs []string `yaml:"sql_outputs"`
	TimeLayout string   `yaml:"time_layout" default:"2006-01-02 15:04:05"`
	// FilePerm is the octal mode for log files, for example "0600". It applies
	// to files that already exist too, because os.OpenFile only honors perm
	// when it creates one. Empty uses 0600.
	// FilePerm 是日志文件的八进制权限，例如 "0600"。它同样作用于已存在的文件：
	// os.OpenFile 的 perm 只在创建时生效。留空表示 0600。
	FilePerm   string            `yaml:"file_perm" default:"0600"`
	Rotation   RotationConfig    `yaml:"rotation"`
	BaseFields map[string]string `yaml:"base_fields"`
}

// RotationConfig controls file rotation.
// RotationConfig 控制文件滚动；只对文件路径生效，stdout/stderr 与空路径不受影响。
type RotationConfig struct {
	Enabled bool `yaml:"enabled"`
	// Mode selects how Outputs and AccessOutputs files roll: "size" (default,
	// lumberjack) or "hourly" (info-h0.log ... info-h23.log). ErrorOutputs and
	// SystemErrorOutputs always use size rotation to avoid hourly fragments.
	// Mode 决定 Outputs 与 AccessOutputs 文件的滚动方式："size"（默认，交给
	// lumberjack）或 "hourly"（info-h0.log ... info-h23.log）。ErrorOutputs 与
	// SystemErrorOutputs 始终使用 size 滚动，避免错误日志被切成小时碎片而难以排查。
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
	if _, err := parseFilePerm(c.FilePerm); err != nil {
		return err
	}
	for _, out := range append(append(append(append(append([]string{}, c.Outputs...), c.ErrorOutputs...), c.SystemErrorOutputs...), c.AccessOutputs...), c.SQLOutputs...) {
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
		sink, file, err := openSink(out, cfg, os.Stdout, "")
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(newSinkEncoder(cfg, file), sink, level))
	}
	for _, out := range cfg.ErrorOutputs {
		sink, file, err := openSink(out, cfg, os.Stderr, rotationModeSize)
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
		sink, file, err := openSink(out, cfg, os.Stderr, rotationModeSize)
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(newSinkEncoder(cfg, file), sink, zap.ErrorLevel))
	}
	return zap.New(zapcore.NewTee(cores...), newOptions(cfg)...), nil
}

// NewAccess builds the logger for HTTP access entries. It writes Info and above
// to AccessOutputs and returns a nop logger when the list is empty, so callers
// can wire it unconditionally. Access entries use Info/Warn/Error by status and
// latency, so the logger ignores Config.Level and always accepts Info.
// NewAccess 构建 HTTP 访问日志使用的 logger：把 Info 及以上写到 AccessOutputs，
// 列表为空时返回 nop logger，调用方无需判空。访问条目按状态码和耗时分流到
// Info/Warn/Error，因此该 logger 忽略 Config.Level，始终接收 Info。
func NewAccess(cfg Config) (*zap.Logger, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.AccessOutputs) == 0 {
		return Nop(), nil
	}
	cores := make([]zapcore.Core, 0, len(cfg.AccessOutputs))
	for _, out := range cfg.AccessOutputs {
		sink, file, err := openSink(out, cfg, os.Stdout, "")
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(newSinkEncoder(cfg, file), sink, zap.InfoLevel))
	}
	return zap.New(zapcore.NewTee(cores...), newAccessOptions(cfg)...), nil
}

// NewSQL builds the SQL log channel. It mirrors NewAccess: a dedicated file (or
// files), info level, and no stacktrace — a SQL entry records a statement, not a
// failure site. An empty SQLOutputs returns a no-op logger, which makes "not
// configured" mean "fall back to the application logger" at the call site
// instead of here.
// NewSQL 构建 SQL 日志通道。与 NewAccess 同构：独立文件、info 级别、不带调用栈——
// SQL 条目记录的是语句而不是出错位置。SQLOutputs 为空时返回 no-op logger，于是
// "未配置 = 回落到应用 logger"由调用方决定，而不是在这里猜。
func NewSQL(cfg Config) (*zap.Logger, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.SQLOutputs) == 0 {
		return Nop(), nil
	}
	cores := make([]zapcore.Core, 0, len(cfg.SQLOutputs))
	for _, out := range cfg.SQLOutputs {
		sink, file, err := openSink(out, cfg, os.Stdout, "")
		if err != nil {
			return nil, err
		}
		cores = append(cores, zapcore.NewCore(newSinkEncoder(cfg, file), sink, zap.InfoLevel))
	}
	return zap.New(zapcore.NewTee(cores...), newAccessOptions(cfg)...), nil
}

func newOptions(cfg Config) []zap.Option {
	opts := []zap.Option{
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
	}
	return append(opts, baseFieldOptions(cfg)...)
}

// newAccessOptions leaves out AddStacktrace: a 5xx access entry records a
// request outcome, not a failure site, so a stack of the middleware would only
// bloat the access file.
// newAccessOptions 不加 AddStacktrace：5xx 访问条目记录的是请求结果而不是出错
// 位置，带上中间件调用栈只会撑大访问日志。
func newAccessOptions(cfg Config) []zap.Option {
	return append([]zap.Option{zap.AddCaller()}, baseFieldOptions(cfg)...)
}

func baseFieldOptions(cfg Config) []zap.Option {
	if len(cfg.BaseFields) == 0 {
		return nil
	}
	return []zap.Option{zap.Fields(baseFields(cfg.BaseFields)...)}
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

// defaultFilePerm keeps log files private. Access logs can record queries and
// SQL logs can carry statements, so group and other read access is opt-in
// through file_perm rather than the default.
// defaultFilePerm 让日志文件默认私有。访问日志可能记录 query，SQL 日志可能带语句，
// 因此同组与其他用户的读权限要通过 file_perm 显式开启，而不是默认给予。
const defaultFilePerm os.FileMode = 0o600

func parseFilePerm(value string) (os.FileMode, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultFilePerm, nil
	}
	digits := strings.TrimPrefix(strings.TrimPrefix(value, "0o"), "0O")
	n, err := strconv.ParseUint(digits, 8, 32)
	if err != nil || n > 0o777 {
		return 0, fmt.Errorf("logger: invalid file_perm %q (want octal like \"0600\")", value)
	}
	return os.FileMode(n), nil
}

func (c Config) filePerm() os.FileMode {
	perm, err := parseFilePerm(c.FilePerm)
	if err != nil {
		return defaultFilePerm
	}
	return perm
}

// openSink builds one output. mode is "" for info outputs (they follow
// Rotation.Mode) and "size" for error streams. The second result is the console
// file (stdout/stderr) or nil for file sinks, which never get ANSI colors.
// openSink 构建单个输出。mode 为空表示 info 输出（跟随 Rotation.Mode），传 "size"
// 表示错误流。第二个返回值是控制台文件（stdout/stderr），文件 sink 时为 nil，
// 调用方据此判断该输出是否允许着色。
func openSink(target string, cfg Config, fallback *os.File, mode string) (zapcore.WriteSyncer, *os.File, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "stdout":
		return zapcore.AddSync(os.Stdout), os.Stdout, nil
	case "stderr":
		return zapcore.AddSync(os.Stderr), os.Stderr, nil
	case "":
		return zapcore.AddSync(fallback), fallback, nil
	}
	perm := cfg.filePerm()
	rotation := cfg.Rotation
	if cfg.Rotation.Enabled {
		return zapcore.AddSync(newRotatingFileSink(target, cfg.Rotation, mode, perm)), nil, nil
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
	f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, perm)
	if err != nil {
		return nil, nil, fmt.Errorf("logger: open %s: %w", target, err)
	}
	// The mode above only applies to a newly created file; an existing one
	// keeps whatever it had, including a 0644 left by an older release.
	// 上面的 perm 只对新建文件生效；已存在的文件保留原有权限，包括旧版本留下的 0644。
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("logger: chmod %s: %w", target, err)
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
