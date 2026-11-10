package logger

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

// 终端着色模式。auto 沿用 TTY 检测，always 供 IDE 这类非 TTY 控制台强制开启，
// never 完全关闭；三者都只作用于 stdout/stderr，文件 sink 永不着色。
const (
	colorAuto   = "auto"
	colorAlways = "always"
	colorNever  = "never"
)

const (
	ansiRed   = "\x1b[31m"
	ansiReset = "\x1b[0m"
)

// normalizeColor 归一化配置值，非法值返回空串由 Validate 报错。
func normalizeColor(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", colorAuto:
		return colorAuto
	case colorAlways:
		return colorAlways
	case colorNever:
		return colorNever
	default:
		return ""
	}
}

// colorAllowed 判断一个 sink 是否着色。file 为 nil 表示文件 sink（滚动或直接
// 写文件），即使配置 always 也不着色，避免 ANSI 码落进日志文件。
func colorAllowed(mode string, file *os.File) bool {
	if file == nil {
		return false
	}
	switch normalizeColor(mode) {
	case colorNever:
		return false
	case colorAlways:
		return os.Getenv("NO_COLOR") == "" && runtime.GOOS != "windows"
	default:
		return colorEnabled(file)
	}
}

func colorEnabled(f *os.File) bool {
	if f == nil || os.Getenv("NO_COLOR") != "" || runtime.GOOS == "windows" {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// terminalErrorEncoder 在终端 sink 上把 error 及以上条目渲染成红色多行块：
// 首行是时间、级别、caller、消息和其余字段，error 单独成行，stacktrace 逐行
// 展开，不再挤在一行 JSON 里。低于 error 的条目沿用配置的编码器。
type terminalErrorEncoder struct {
	zapcore.Encoder
	block zapcore.Encoder
	// pool 支撑着色后的缓冲；zap 会释放 EncodeEntry 返回的 buffer，
	// 零值 Buffer 无法被释放。
	pool buffer.Pool
}

func newTerminalErrorEncoder(enc zapcore.Encoder, cfg Config) terminalErrorEncoder {
	return terminalErrorEncoder{Encoder: enc, block: newBlockEncoder(cfg), pool: buffer.NewPool()}
}

// newBlockEncoder 复用同一份编码配置，只把分隔符换成空格、级别换成大写，
// 并关掉自带的 stacktrace 输出（由本编码器逐行接管）。
func newBlockEncoder(cfg Config) zapcore.Encoder {
	encCfg := encoderConfig(cfg)
	encCfg.EncodeLevel = zapcore.CapitalLevelEncoder
	encCfg.ConsoleSeparator = " "
	encCfg.StacktraceKey = zapcore.OmitKey
	return zapcore.NewConsoleEncoder(encCfg)
}

func (e terminalErrorEncoder) Clone() zapcore.Encoder {
	return terminalErrorEncoder{Encoder: e.Encoder.Clone(), block: e.block.Clone(), pool: e.pool}
}

// zap 把 BaseFields 和 logger.With 的字段通过 Add* 烘焙进编码器本身，而不是
// 交给 EncodeEntry，所以两个编码器都要收到同样的字段，否则 error 块会丢掉
// service、component 这类固定字段。
var _ zapcore.Encoder = terminalErrorEncoder{}

func (e terminalErrorEncoder) AddArray(key string, m zapcore.ArrayMarshaler) error {
	if err := e.Encoder.AddArray(key, m); err != nil {
		return err
	}
	return e.block.AddArray(key, m)
}

func (e terminalErrorEncoder) AddObject(key string, m zapcore.ObjectMarshaler) error {
	if err := e.Encoder.AddObject(key, m); err != nil {
		return err
	}
	return e.block.AddObject(key, m)
}

func (e terminalErrorEncoder) AddReflected(key string, value any) error {
	if err := e.Encoder.AddReflected(key, value); err != nil {
		return err
	}
	return e.block.AddReflected(key, value)
}

func (e terminalErrorEncoder) AddBinary(key string, value []byte) {
	e.Encoder.AddBinary(key, value)
	e.block.AddBinary(key, value)
}

func (e terminalErrorEncoder) AddByteString(key string, value []byte) {
	e.Encoder.AddByteString(key, value)
	e.block.AddByteString(key, value)
}

func (e terminalErrorEncoder) AddBool(key string, value bool) {
	e.Encoder.AddBool(key, value)
	e.block.AddBool(key, value)
}

func (e terminalErrorEncoder) AddComplex128(key string, value complex128) {
	e.Encoder.AddComplex128(key, value)
	e.block.AddComplex128(key, value)
}

func (e terminalErrorEncoder) AddComplex64(key string, value complex64) {
	e.Encoder.AddComplex64(key, value)
	e.block.AddComplex64(key, value)
}

func (e terminalErrorEncoder) AddDuration(key string, value time.Duration) {
	e.Encoder.AddDuration(key, value)
	e.block.AddDuration(key, value)
}

func (e terminalErrorEncoder) AddFloat64(key string, value float64) {
	e.Encoder.AddFloat64(key, value)
	e.block.AddFloat64(key, value)
}

func (e terminalErrorEncoder) AddFloat32(key string, value float32) {
	e.Encoder.AddFloat32(key, value)
	e.block.AddFloat32(key, value)
}

func (e terminalErrorEncoder) AddInt(key string, value int) {
	e.Encoder.AddInt(key, value)
	e.block.AddInt(key, value)
}

func (e terminalErrorEncoder) AddInt64(key string, value int64) {
	e.Encoder.AddInt64(key, value)
	e.block.AddInt64(key, value)
}

func (e terminalErrorEncoder) AddInt32(key string, value int32) {
	e.Encoder.AddInt32(key, value)
	e.block.AddInt32(key, value)
}

func (e terminalErrorEncoder) AddInt16(key string, value int16) {
	e.Encoder.AddInt16(key, value)
	e.block.AddInt16(key, value)
}

func (e terminalErrorEncoder) AddInt8(key string, value int8) {
	e.Encoder.AddInt8(key, value)
	e.block.AddInt8(key, value)
}

func (e terminalErrorEncoder) AddString(key, value string) {
	e.Encoder.AddString(key, value)
	e.block.AddString(key, value)
}

func (e terminalErrorEncoder) AddTime(key string, value time.Time) {
	e.Encoder.AddTime(key, value)
	e.block.AddTime(key, value)
}

func (e terminalErrorEncoder) AddUint(key string, value uint) {
	e.Encoder.AddUint(key, value)
	e.block.AddUint(key, value)
}

func (e terminalErrorEncoder) AddUint64(key string, value uint64) {
	e.Encoder.AddUint64(key, value)
	e.block.AddUint64(key, value)
}

func (e terminalErrorEncoder) AddUint32(key string, value uint32) {
	e.Encoder.AddUint32(key, value)
	e.block.AddUint32(key, value)
}

func (e terminalErrorEncoder) AddUint16(key string, value uint16) {
	e.Encoder.AddUint16(key, value)
	e.block.AddUint16(key, value)
}

func (e terminalErrorEncoder) AddUint8(key string, value uint8) {
	e.Encoder.AddUint8(key, value)
	e.block.AddUint8(key, value)
}

func (e terminalErrorEncoder) AddUintptr(key string, value uintptr) {
	e.Encoder.AddUintptr(key, value)
	e.block.AddUintptr(key, value)
}

func (e terminalErrorEncoder) OpenNamespace(key string) {
	e.Encoder.OpenNamespace(key)
	e.block.OpenNamespace(key)
}

func (e terminalErrorEncoder) EncodeEntry(ent zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	if ent.Level < zapcore.ErrorLevel {
		return e.Encoder.EncodeEntry(ent, fields)
	}

	rest := make([]zapcore.Field, 0, len(fields))
	var errLines []string
	for _, f := range fields {
		if f.Type == zapcore.ErrorType || f.Key == "error" {
			errLines = append(errLines, errorFieldLine(f))
			continue
		}
		rest = append(rest, f)
	}

	header := ent
	header.Stack = ""
	buf, err := e.block.EncodeEntry(header, rest)
	if err != nil {
		return nil, err
	}
	block := e.pool.Get()
	block.AppendString(ansiRed)
	block.AppendBytes(buf.Bytes())
	buf.Free()
	for _, line := range errLines {
		block.AppendString(line)
		block.AppendString("\n")
	}
	if stack := strings.TrimRight(ent.Stack, "\n"); stack != "" {
		block.AppendString("stacktrace:\n")
		block.AppendString(stack)
		block.AppendString("\n")
	}
	block.AppendString(ansiReset)
	return block, nil
}

// errorFieldLine 把 error 字段渲染成「字段名: 错误文本」，zap.NamedError
// 这类自定义键也按同样的方式单独成行。
func errorFieldLine(f zapcore.Field) string {
	key := f.Key
	if key == "" {
		key = "error"
	}
	return key + ": " + errorFieldText(f)
}

func errorFieldText(f zapcore.Field) string {
	if f.Interface != nil {
		if err, ok := f.Interface.(error); ok {
			return err.Error()
		}
		return fmt.Sprint(f.Interface)
	}
	if f.String != "" {
		return f.String
	}
	return strconv.FormatInt(f.Integer, 10)
}
