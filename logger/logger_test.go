package logger_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/logger"
	"go.uber.org/zap"
)

func TestValidateRejectsInvalidConfig(t *testing.T) {
	cases := map[string]logger.Config{
		"bad level":             {Level: "verbose"},
		"bad encoding":          {Encoding: "xml"},
		"bad color":             {Color: "rainbow"},
		"negative rotation":     {Rotation: logger.RotationConfig{MaxSizeMB: -1}},
		"bad rotation mode":     {Rotation: logger.RotationConfig{Mode: "weekly"}},
		"bad date layout":       {Rotation: logger.RotationConfig{DateLayout: "2006/01/02"}},
		"negative date dir age": {Rotation: logger.RotationConfig{DateDirMaxAgeDays: -1}},
		"empty output":          {Outputs: []string{" "}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid config")
			}
		})
	}
}

func TestNewWritesJSONWithBaseFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	lg, err := logger.New(logger.Config{
		Level:      "debug",
		Encoding:   "json",
		Outputs:    []string{path},
		BaseFields: map[string]string{"service": "order-service", "env": "test"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Info("hello", zap.String("order_id", "42"))
	if err := logger.Sync(lg); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	line := strings.TrimSpace(string(data))
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	if entry["msg"] != "hello" {
		t.Fatalf("msg = %v", entry["msg"])
	}
	if entry["level"] != "info" {
		t.Fatalf("level = %v", entry["level"])
	}
	if entry["service"] != "order-service" || entry["env"] != "test" {
		t.Fatalf("base fields missing: %v", entry)
	}
	if entry["order_id"] != "42" {
		t.Fatalf("order_id = %v", entry["order_id"])
	}
	if _, ok := entry["ts"]; !ok {
		t.Fatal("timestamp is missing")
	}
}

// color=always only affects stdout/stderr; error entries written to a file stay
// plain JSON without ANSI codes, and the stacktrace stays single-line escaped so
// log collectors can parse it.
// color=always 只影响 stdout/stderr；写进文件的 error 仍是无 ANSI 的 JSON，
// stacktrace 保持单行转义，方便日志采集。
func TestColorAlwaysKeepsFileOutputPlain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	lg, err := logger.New(logger.Config{Color: "always", Outputs: []string{path}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Error("boom", zap.Error(errors.New("database unreachable")))
	if err := logger.Sync(lg); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	line := strings.TrimSpace(readFile(t, path))
	if strings.Contains(line, "\x1b[") {
		t.Fatalf("file output contains ANSI codes: %q", line)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	if entry["error"] != "database unreachable" {
		t.Fatalf("error field = %v", entry["error"])
	}
	if _, ok := entry["stacktrace"]; !ok {
		t.Fatalf("stacktrace missing: %v", entry)
	}
}

func TestLevelFiltering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	lg, err := logger.New(logger.Config{Level: "warn", Outputs: []string{path}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Info("dropped")
	lg.Warn("kept")
	if err := logger.Sync(lg); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if strings.Contains(string(data), "dropped") {
		t.Fatalf("info entry was written below the configured level: %s", data)
	}
	if !strings.Contains(string(data), "kept") {
		t.Fatalf("warn entry missing: %s", data)
	}
}

func TestRotationWritesThroughLumberjack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotate", "app.log")
	lg, err := logger.New(logger.Config{
		Level:    "info",
		Encoding: "console",
		Outputs:  []string{path},
		Rotation: logger.RotationConfig{
			Enabled:    true,
			MaxSizeMB:  1,
			MaxAgeDays: 1,
			MaxBackups: 1,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Info("rotated")
	if err := logger.Sync(lg); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "rotated") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("rotated log entry was not written")
}

func TestHourlyInfoAndSizeErrorOutputsUseDateDir(t *testing.T) {
	dir := t.TempDir()
	infoPath := filepath.Join(dir, "http", "info.log")
	errorPath := filepath.Join(dir, "http", "error.log")
	lg, err := logger.New(logger.Config{
		Level:        "info",
		Encoding:     "json",
		Outputs:      []string{infoPath},
		ErrorOutputs: []string{errorPath},
		Rotation: logger.RotationConfig{
			Enabled:    true,
			Mode:       "hourly",
			DateDir:    true,
			DateLayout: "20060102",
			MaxAgeDays: 30,
			MaxSizeMB:  1,
			MaxBackups: 7,
			Compress:   true,
			LocalTime:  true,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Info("business info")
	lg.Error("business error")
	if err := logger.Sync(lg); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	now := time.Now()
	infoFile := filepath.Join(dir, "http", now.Format("20060102"), "info-h"+strconv.Itoa(now.Hour())+".log")
	if got := readFile(t, infoFile); !strings.Contains(got, "business info") {
		t.Fatalf("hourly info log = %q", got)
	}
	errorFile := filepath.Join(dir, "http", now.Format("20060102"), "error.log")
	if got := readFile(t, errorFile); !strings.Contains(got, "business error") {
		t.Fatalf("size error log = %q", got)
	}
}

func TestDateTokenExpandsWithoutRotation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "script", "{date}", "info", "fix-order.log")
	lg, err := logger.New(logger.Config{
		Level:    "info",
		Encoding: "json",
		Outputs:  []string{target},
		Rotation: logger.RotationConfig{LocalTime: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Info("script info")
	if err := logger.Sync(lg); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	path := filepath.Join(dir, "script", time.Now().Format("20060102"), "info", "fix-order.log")
	if got := readFile(t, path); !strings.Contains(got, "script info") {
		t.Fatalf("log = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "script", "{date}")); !os.IsNotExist(err) {
		t.Fatalf("{date} should not stay literal in the path, stat err = %v", err)
	}
}

func TestNewSystemWritesOnlyFrameworkErrors(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "info.log")
	errorPath := filepath.Join(dir, "error.log")
	systemPath := filepath.Join(dir, "system", "system_error.log")

	cfg := logger.Config{
		Level:              "info",
		Outputs:            []string{mainPath},
		ErrorOutputs:       []string{errorPath},
		SystemErrorOutputs: []string{systemPath},
	}
	main, err := logger.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	system, err := logger.NewSystem(cfg)
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}

	main.Info("business info")
	main.Error("business error")
	system.Error("panic recovered")
	if err := logger.Sync(main); err != nil {
		t.Fatalf("Sync main: %v", err)
	}
	if err := logger.Sync(system); err != nil {
		t.Fatalf("Sync system: %v", err)
	}

	mainLog := readFile(t, mainPath)
	if !strings.Contains(mainLog, "business info") || !strings.Contains(mainLog, "business error") {
		t.Fatalf("main log = %q", mainLog)
	}
	if strings.Contains(mainLog, "panic recovered") {
		t.Fatalf("main log captured a framework error: %q", mainLog)
	}

	errorLog := readFile(t, errorPath)
	if !strings.Contains(errorLog, "business error") || strings.Contains(errorLog, "panic recovered") {
		t.Fatalf("error log = %q", errorLog)
	}

	systemLog := readFile(t, systemPath)
	if !strings.Contains(systemLog, "panic recovered") || strings.Contains(systemLog, "business") {
		t.Fatalf("system log = %q", systemLog)
	}
}

func TestNewSystemWithoutOutputsIsNop(t *testing.T) {
	lg, err := logger.NewSystem(logger.Config{})
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}
	if lg == nil {
		t.Fatal("NewSystem returned nil")
	}
	lg.Error("discarded")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestNopAndSync(t *testing.T) {
	if logger.Nop() == nil {
		t.Fatal("Nop returned nil")
	}
	if err := logger.Sync(nil); err != nil {
		t.Fatalf("Sync(nil) = %v", err)
	}
	if err := logger.Sync(logger.Nop()); err != nil {
		t.Fatalf("Sync(Nop) = %v", err)
	}
}
