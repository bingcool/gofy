package logger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// TestConcurrentSinksKeepEveryEntry pins the fact that a tee of a console and a
// file sink does not drop entries under concurrency, in any color mode. Two
// processes appending to one rotated log can make it look like the console sink
// lost lines; this test is the boundary that says the logger itself is not the
// one dropping them.
// TestConcurrentSinksKeepEveryEntry 固定「控制台 + 文件」双 sink 在并发下三种着色模式
// 都不丢条目。两个进程同时追加同一份滚动日志时，看起来会像控制台少写了行；这条测试
// 划清边界：丢行的不是 logger。
func TestConcurrentSinksKeepEveryEntry(t *testing.T) {
	for _, color := range []string{colorAuto, colorAlways, colorNever} {
		t.Run(color, func(t *testing.T) {
			stdout, err := os.CreateTemp(t.TempDir(), "stdout-*.log")
			if err != nil {
				t.Fatal(err)
			}
			old := os.Stdout
			os.Stdout = stdout
			defer func() { os.Stdout = old }()

			logPath := filepath.Join(t.TempDir(), "info.log")
			lg, err := New(Config{
				Level:    "info",
				Encoding: "json",
				Color:    color,
				Outputs:  []string{"stdout", logPath},
			})
			if err != nil {
				t.Fatal(err)
			}

			const (
				workers   = 4
				perWorker = 200
				want      = workers * perWorker
			)
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					l := lg.With(zap.Int("wid", w))
					for i := 0; i < perWorker; i++ {
						l.Info("probe", zap.Int("i", i))
					}
				}(w)
			}
			wg.Wait()
			if err := Sync(lg); err != nil {
				t.Logf("sync: %v", err)
			}
			if err := stdout.Close(); err != nil {
				t.Fatal(err)
			}

			for _, path := range []string{stdout.Name(), logPath} {
				got := countProbes(t, path)
				if got != want {
					t.Errorf("%s has %d probe entries, want %d", path, got, want)
				}
			}
		})
	}
}

// TestColorAlwaysDoesNotGlueEntriesTogether covers the real defect behind
// "missing lines" in a redirected stdout: the red error block used to end with
// \x1b[0m and no newline, so the next entry was appended to the same line.
// TestColorAlwaysDoesNotGlueEntriesTogether 覆盖重定向 stdout 里「少一行」的真实缺陷：
// 红色 error 块原先以不带换行的 \x1b[0m 结尾，下一条日志会粘在同一行上。
func TestColorAlwaysDoesNotGlueEntriesTogether(t *testing.T) {
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-*.log")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = stdout
	defer func() { os.Stdout = old }()

	lg, err := New(Config{
		Level: "info", Encoding: "json", Color: colorAlways,
		Outputs: []string{"stdout"},
	})
	if err != nil {
		t.Fatal(err)
	}
	lg.Error("first failed", zap.Error(errors.New("boom")))
	lg.Info("second arrived")
	if err := Sync(lg); err != nil {
		t.Logf("sync: %v", err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if strings.Contains(out, ansiReset+"{") {
		t.Fatalf("the entry after the error block was glued onto its reset code: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("output does not end with a newline: %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	last := lines[len(lines)-1]
	var entry map[string]any
	if err := json.Unmarshal([]byte(last), &entry); err != nil {
		t.Fatalf("last line is not a standalone JSON entry: %q (%v)", last, err)
	}
	if entry["msg"] != "second arrived" {
		t.Fatalf("last entry = %v, want the info entry", entry)
	}
}

func countProbes(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("%s has a broken line: %q (%v)", path, line, err)
		}
		if entry["msg"] == "probe" {
			count++
		}
	}
	return count
}
