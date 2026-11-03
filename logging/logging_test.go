package logging_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/logging"
	"go.uber.org/zap"
)

func TestValidateRejectsInvalidConfig(t *testing.T) {
	cases := map[string]logging.Config{
		"bad level":         {Level: "verbose"},
		"bad encoding":      {Encoding: "xml"},
		"negative rotation": {Rotation: logging.RotationConfig{MaxSizeMB: -1}},
		"empty output":      {Outputs: []string{" "}},
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
	logger, err := logging.New(logging.Config{
		Level:      "debug",
		Encoding:   "json",
		Outputs:    []string{path},
		BaseFields: map[string]string{"service": "order-service", "env": "test"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("hello", zap.String("order_id", "42"))
	if err := logging.Sync(logger); err != nil {
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

func TestLevelFiltering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	logger, err := logging.New(logging.Config{Level: "warn", Outputs: []string{path}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("dropped")
	logger.Warn("kept")
	if err := logging.Sync(logger); err != nil {
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
	logger, err := logging.New(logging.Config{
		Level:    "info",
		Encoding: "console",
		Outputs:  []string{path},
		Rotation: logging.RotationConfig{
			Enabled:    true,
			MaxSizeMB:  1,
			MaxAgeDays: 1,
			MaxBackups: 1,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logger.Info("rotated")
	if err := logging.Sync(logger); err != nil {
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

func TestNopAndSync(t *testing.T) {
	if logging.Nop() == nil {
		t.Fatal("Nop returned nil")
	}
	if err := logging.Sync(nil); err != nil {
		t.Fatalf("Sync(nil) = %v", err)
	}
	if err := logging.Sync(logging.Nop()); err != nil {
		t.Fatalf("Sync(Nop) = %v", err)
	}
}
