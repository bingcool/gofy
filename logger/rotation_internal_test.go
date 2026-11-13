package logger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotatingSinkHourlySwitchesDirectoryAndFile(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "http", "info.log")
	sink := newRotatingFileSink(base, RotationConfig{
		Enabled:    true,
		Mode:       "hourly",
		DateDir:    true,
		DateLayout: "20060102",
		MaxAgeDays: 30,
		LocalTime:  true,
		MaxSizeMB:  100,
		MaxBackups: 7,
		Compress:   true,
	}, "")
	now := time.Date(2026, 11, 6, 23, 10, 0, 0, time.Local)
	sink.now = func() time.Time { return now }

	if _, err := sink.Write([]byte("first\n")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	first := filepath.Join(dir, "http", "20261106", "info-h23.log")
	if got := readLogFile(t, first); got != "first\n" {
		t.Fatalf("first file = %q", got)
	}

	now = now.Add(time.Hour)
	if _, err := sink.Write([]byte("second\n")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	second := filepath.Join(dir, "http", "20261107", "info-h0.log")
	if got := readLogFile(t, second); got != "second\n" {
		t.Fatalf("second file = %q", got)
	}
	if got := readLogFile(t, first); got != "first\n" {
		t.Fatalf("first file changed after rollover: %q", got)
	}
}

func TestRotatingSinkDatePlaceholderOrdersDateBeforeStreamDir(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "script", "{date}", "info", "fix-order.log")
	sink := newRotatingFileSink(base, RotationConfig{
		Enabled:           true,
		Mode:              "hourly",
		DateDir:           true,
		DateLayout:        "20060102",
		DateDirMaxAgeDays: 7,
		MaxSizeMB:         100,
		LocalTime:         true,
	}, "")
	now := time.Date(2026, 11, 6, 23, 10, 0, 0, time.Local)
	sink.now = func() time.Time { return now }

	if _, err := sink.Write([]byte("first\n")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	first := filepath.Join(dir, "script", "20261106", "info", "fix-order-h23.log")
	if got := readLogFile(t, first); got != "first\n" {
		t.Fatalf("first file = %q", got)
	}

	now = now.Add(time.Hour)
	if _, err := sink.Write([]byte("second\n")); err != nil {
		t.Fatalf("write second: %v", err)
	}
	second := filepath.Join(dir, "script", "20261107", "info", "fix-order-h0.log")
	if got := readLogFile(t, second); got != "second\n" {
		t.Fatalf("second file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "script", "20261107", "20261107")); !os.IsNotExist(err) {
		t.Fatalf("date directory was appended twice (err=%v)", err)
	}
}

func TestRotatingSinkPlaceholderPrunesDateRoot(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "script")
	expired := filepath.Join(root, "20261029", "info")
	boundary := filepath.Join(root, "20261030", "info")
	unrelated := filepath.Join(root, "keep")
	for _, p := range []string{expired, boundary, unrelated} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	sink := newRotatingFileSink(filepath.Join(root, "{date}", "info", "fix-order.log"), RotationConfig{
		Enabled:           true,
		Mode:              "size",
		DateDir:           true,
		DateLayout:        "20060102",
		DateDirMaxAgeDays: 7,
		MaxSizeMB:         1,
		LocalTime:         true,
	}, rotationModeSize)
	sink.now = func() time.Time {
		return time.Date(2026, 11, 6, 9, 0, 0, 0, time.Local)
	}
	if _, err := sink.Write([]byte("x\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired dir %s still exists (err=%v)", expired, err)
	}
	for _, p := range []string{boundary, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("dir %s was removed: %v", p, err)
		}
	}
}

func TestRotatingSinkSizeModeUsesDateDirectory(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "script", "error.log")
	sink := newRotatingFileSink(base, RotationConfig{
		Enabled:    true,
		Mode:       "size",
		DateDir:    true,
		DateLayout: "20060102",
		MaxAgeDays: 30,
		MaxSizeMB:  1,
		MaxBackups: 7,
		Compress:   true,
		LocalTime:  true,
	}, rotationModeSize)
	sink.now = func() time.Time {
		return time.Date(2026, 11, 6, 9, 0, 0, 0, time.Local)
	}

	if _, err := sink.Write([]byte("boom\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := filepath.Join(dir, "script", "20261106", "error.log")
	if got := readLogFile(t, path); got != "boom\n" {
		t.Fatalf("log file = %q", got)
	}
}

func TestRotatingSinkPrunesExpiredDateDirs(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "http")
	// Seven days before 2026-11-06 is 2026-10-30: anything earlier must be removed.
	// 2026-11-06 往前 7 天是 2026-10-30：更早的目录应删除。
	expired := filepath.Join(parent, "20261029")
	boundary := filepath.Join(parent, "20261030")
	kept := filepath.Join(parent, "20261101")
	unrelated := filepath.Join(parent, "keep")
	for _, p := range []string{expired, boundary, kept, unrelated} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}

	sink := newRotatingFileSink(filepath.Join(parent, "info.log"), RotationConfig{
		Enabled:    true,
		Mode:       "size",
		DateDir:    true,
		DateLayout: "20060102",
		// A zero DateDirMaxAgeDays falls back to the default of 7.
		// DateDirMaxAgeDays 为 0 时使用默认值 7。
		MaxSizeMB: 1,
		LocalTime: true,
	}, rotationModeSize)
	sink.now = func() time.Time {
		return time.Date(2026, 11, 6, 9, 0, 0, 0, time.Local)
	}
	if _, err := sink.Write([]byte("x\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired dir %s still exists (err=%v)", expired, err)
	}
	for _, p := range []string{boundary, kept, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("dir %s was removed: %v", p, err)
		}
	}
}

func readLogFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
