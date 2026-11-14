package logger

import (
	"errors"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func errorEntry() zapcore.Entry {
	return zapcore.Entry{
		Level:   zapcore.ErrorLevel,
		Message: "demo-service exit",
		Caller:  zapcore.NewEntryCaller(0, "demo-service/main.go", 75, true),
		Stack:   "main.main\n\t/src/demo-service/main.go:75\nruntime.main\n\t/src/runtime/proc.go:290",
	}
}

func TestTerminalErrorEncoderRendersRedBlock(t *testing.T) {
	enc := newTerminalErrorEncoder(newEncoder(Config{Encoding: "json"}), Config{})

	buf, err := enc.EncodeEntry(errorEntry(), []zapcore.Field{
		zap.String("service", "demo-service"),
		zap.Error(errors.New(`module "order" register: middleware "audit1" is not registered`)),
	})
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer buf.Free()
	out := buf.String()

	if !strings.HasPrefix(out, ansiRed) || !strings.HasSuffix(out, ansiReset+"\n") {
		t.Fatalf("block is not wrapped in red and terminated by a newline: %q", out)
	}
	if strings.Contains(out, `\n`) || strings.Contains(out, `\t`) {
		t.Fatalf("stacktrace is still escaped on one line: %q", out)
	}
	for _, want := range []string{
		"ERROR demo-service/main.go:75 demo-service exit",
		`{"service": "demo-service"}`,
		"\nerror: module \"order\" register: middleware \"audit1\" is not registered\n",
		"\nstacktrace:\nmain.main\n\t/src/demo-service/main.go:75\nruntime.main\n\t/src/runtime/proc.go:290",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("block missing %q: %q", want, out)
		}
	}
	if strings.Count(out, "\n") != 7 {
		t.Fatalf("unexpected line count in %q", out)
	}
}

func TestTerminalErrorEncoderNamedErrorField(t *testing.T) {
	enc := newTerminalErrorEncoder(newEncoder(Config{}), Config{})
	buf, err := enc.EncodeEntry(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "reload failed"},
		[]zapcore.Field{zap.NamedError("cause", errors.New("nacos unreachable"))})
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer buf.Free()
	out := buf.String()
	if !strings.Contains(out, "\ncause: nacos unreachable") || !strings.HasSuffix(out, ansiReset+"\n") {
		t.Fatalf("named error is not on its own line, or the block is not terminated: %q", out)
	}
}

func TestTerminalErrorEncoderWithoutErrorOrStack(t *testing.T) {
	enc := newTerminalErrorEncoder(newEncoder(Config{}), Config{})
	buf, err := enc.EncodeEntry(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "boom"}, nil)
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer buf.Free()
	out := buf.String()
	if strings.Contains(out, "error:") || strings.Contains(out, "stacktrace:") {
		t.Fatalf("empty sections were rendered: %q", out)
	}
	if !strings.HasPrefix(out, ansiRed) || !strings.HasSuffix(out, ansiReset+"\n") {
		t.Fatalf("block is not wrapped in red and terminated by a newline: %q", out)
	}
}

// BaseFields and logger.With fields are baked into the encoder by zap through the
// Add* methods, so both the error block and plain entries must carry them.
// BaseFields 和 logger.With 的字段由 zap 通过 Add* 烘焙进编码器，error 块和
// 普通条目都必须带上它们。
func TestTerminalErrorEncoderKeepsBakedFields(t *testing.T) {
	enc := newTerminalErrorEncoder(newEncoder(Config{Encoding: "json"}), Config{})
	enc.AddString("service", "demo-service")
	clone := enc.Clone()
	clone.AddString("component", "order.service")

	buf, err := clone.EncodeEntry(errorEntry(), []zapcore.Field{zap.Error(errors.New("boom"))})
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer buf.Free()
	out := buf.String()
	if !strings.Contains(out, `"service": "demo-service"`) || !strings.Contains(out, `"component": "order.service"`) {
		t.Fatalf("error block lost baked fields: %q", out)
	}

	infoBuf, err := clone.EncodeEntry(zapcore.Entry{Level: zapcore.InfoLevel, Message: "hello"}, nil)
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer infoBuf.Free()
	info := infoBuf.String()
	if !strings.Contains(info, `"service":"demo-service"`) || !strings.Contains(info, `"component":"order.service"`) {
		t.Fatalf("info entry lost baked fields: %q", info)
	}
}

func TestTerminalErrorEncoderKeepsLowerLevels(t *testing.T) {
	enc := newTerminalErrorEncoder(newEncoder(Config{Encoding: "json"}), Config{})
	buf, err := enc.EncodeEntry(zapcore.Entry{Level: zapcore.WarnLevel, Message: "slow"}, nil)
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer buf.Free()
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("warn entry contains ANSI codes: %q", out)
	}
	if !strings.HasPrefix(out, `{"level":"warn"`) {
		t.Fatalf("warn entry is not the configured JSON encoding: %q", out)
	}
}

func TestTerminalErrorEncoderClone(t *testing.T) {
	enc := newTerminalErrorEncoder(newEncoder(Config{Encoding: "json"}), Config{})
	clone, ok := enc.Clone().(terminalErrorEncoder)
	if !ok {
		t.Fatalf("Clone returned %T, want terminalErrorEncoder", enc.Clone())
	}
	buf, err := clone.EncodeEntry(errorEntry(), []zapcore.Field{zap.Error(errors.New("boom"))})
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer buf.Free()
	out := buf.String()
	if !strings.HasPrefix(out, ansiRed) || !strings.Contains(out, "\nerror: boom") ||
		!strings.HasSuffix(out, ansiReset+"\n") {
		t.Fatalf("cloned encoder lost the block rendering: %q", out)
	}
}

func TestColorEnabledIgnoresNonTerminals(t *testing.T) {
	if colorEnabled(nil) {
		t.Fatal("colorEnabled(nil) = true")
	}
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()
	if colorEnabled(f) {
		t.Fatal("colorEnabled(regular file) = true")
	}
}

func TestColorAllowedModes(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()

	cases := []struct {
		mode string
		file *os.File
		want bool
	}{
		{mode: "", file: f, want: false},
		{mode: "auto", file: f, want: false},
		{mode: "always", file: f, want: true},
		{mode: "ALWAYS", file: f, want: true},
		{mode: "never", file: f, want: false},
		// A nil file means a file sink, which stays uncolored in every mode.
		// 文件 sink 传 nil，任何模式都不着色。
		{mode: "always", file: nil, want: false},
		{mode: "auto", file: nil, want: false},
	}
	for _, tc := range cases {
		if got := colorAllowed(tc.mode, tc.file); got != tc.want {
			t.Fatalf("colorAllowed(%q, file=%v) = %v, want %v", tc.mode, tc.file != nil, got, tc.want)
		}
	}
}

func TestColorAllowedRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()
	if colorAllowed("always", f) {
		t.Fatal("NO_COLOR did not disable color=always")
	}
}

func TestNewSinkEncoderColorsOnlyConsoleSinks(t *testing.T) {
	entry := zapcore.Entry{Level: zapcore.ErrorLevel, Message: "boom"}
	consoleBuf, err := newSinkEncoder(Config{Color: "always"}, os.Stdout).EncodeEntry(entry, nil)
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer consoleBuf.Free()
	if !strings.HasPrefix(consoleBuf.String(), ansiRed) {
		t.Fatalf("console sink was not colored: %q", consoleBuf.String())
	}

	fileBuf, err := newSinkEncoder(Config{Color: "always"}, nil).EncodeEntry(entry, nil)
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer fileBuf.Free()
	if strings.Contains(fileBuf.String(), "\x1b[") {
		t.Fatalf("file sink contains ANSI codes: %q", fileBuf.String())
	}
}
