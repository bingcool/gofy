package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gofyconfig "github.com/bingcool/gofy/config"

	appworker "github.com/yourorg/demo-service/worker"
)

// section mirrors how app/config embeds the worker section: the point of these
// tests is that the inline composition decodes and defaults correctly, because
// a broken inline tag silently drops either the shared settings or the
// per-worker block.
// section 复刻 app/config 里 worker 段的嵌入方式：这些用例要确认内联组合能正确解码
// 与补默认值——inline 标签写错时，公共设置或 worker 配置块会静默丢失。
type section struct {
	Worker appworker.Config `yaml:"worker"`
}

func loadSection(t *testing.T, body string) appworker.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := gofyconfig.Load[section](context.Background(), path, gofyconfig.WithStrict(true))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg.Worker
}

func TestConfigKeepsSharedAndScanSettingsApart(t *testing.T) {
	cfg := loadSection(t, `worker:
  health_addr: 127.0.0.1:9091
  scan:
    concurrency: 4
    interval: 2s
`)

	if cfg.HealthAddr != "127.0.0.1:9091" {
		t.Fatalf("health_addr = %q", cfg.HealthAddr)
	}
	if cfg.Scan.Concurrency != 4 {
		t.Fatalf("concurrency = %d, want 4", cfg.Scan.Concurrency)
	}
	if cfg.Scan.Interval != 2*time.Second {
		t.Fatalf("interval = %s, want 2s", cfg.Scan.Interval)
	}
}

func TestConfigAppliesScanDefaults(t *testing.T) {
	cfg := loadSection(t, `worker:
  health_addr: ""
`)

	if cfg.Scan.Concurrency != 2 {
		t.Fatalf("concurrency = %d, want the default 2", cfg.Scan.Concurrency)
	}
	if cfg.Scan.Interval != 5*time.Second {
		t.Fatalf("interval = %s, want the default 5s", cfg.Scan.Interval)
	}
}

func TestConfigRejectsUnknownWorkerKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`worker:
  scan:
    concurrency: 1
    unknown: true
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := gofyconfig.Load[section](context.Background(), path, gofyconfig.WithStrict(true)); err == nil {
		t.Fatal("strict load accepted an unknown key under worker.scan")
	}
}
