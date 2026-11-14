package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gofyconfig "github.com/bingcool/gofy/config"

	appworker "github.com/yourorg/demo-service/worker"
)

// section mirrors how app/config embeds the worker section: the point of these
// tests is that the inline composition and the per-group blocks decode and
// default correctly, because a broken tag silently drops either the shared
// settings or a group's block.
// section 复刻 app/config 里 worker 段的嵌入方式：这些用例要确认内联组合与分组块能
// 正确解码与补默认值——标签写错时，公共设置或某个分组的配置块会静默丢失。
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

func TestConfigKeepsSharedAndGroupSettingsApart(t *testing.T) {
	cfg := loadSection(t, `worker:
  health_addr: 127.0.0.1:9091
  order:
    scan:
      concurrency: 4
      interval: 2s
  report:
    summary:
      concurrency: 3
      interval: 30m
`)

	if cfg.HealthAddr != "127.0.0.1:9091" {
		t.Fatalf("health_addr = %q", cfg.HealthAddr)
	}
	if cfg.Order.Scan.Concurrency != 4 {
		t.Fatalf("order.scan.concurrency = %d, want 4", cfg.Order.Scan.Concurrency)
	}
	if cfg.Order.Scan.Interval != 2*time.Second {
		t.Fatalf("order.scan.interval = %s, want 2s", cfg.Order.Scan.Interval)
	}
	if cfg.Report.Summary.Concurrency != 3 {
		t.Fatalf("report.summary.concurrency = %d, want 3", cfg.Report.Summary.Concurrency)
	}
	if cfg.Report.Summary.Interval != 30*time.Minute {
		t.Fatalf("report.summary.interval = %s, want 30m", cfg.Report.Summary.Interval)
	}
}

func TestConfigAppliesGroupDefaults(t *testing.T) {
	cfg := loadSection(t, `worker:
  health_addr: ""
`)

	// Defaults have to reach through the group blocks, not just the inlined
	// shared struct: a config that omits a group entirely must still start.
	// 默认值必须穿过分组块，而不只作用在内联的公共结构上：完全省略某个分组的配置
	// 仍然要能启动。
	if cfg.Order.Scan.Concurrency != 2 {
		t.Fatalf("order.scan.concurrency = %d, want the default 2", cfg.Order.Scan.Concurrency)
	}
	if cfg.Order.Scan.Interval != 5*time.Second {
		t.Fatalf("order.scan.interval = %s, want the default 5s", cfg.Order.Scan.Interval)
	}
	if cfg.Report.Summary.Concurrency != 1 {
		t.Fatalf("report.summary.concurrency = %d, want the default 1", cfg.Report.Summary.Concurrency)
	}
	if cfg.Report.Summary.Interval != time.Hour {
		t.Fatalf("report.summary.interval = %s, want the default 1h", cfg.Report.Summary.Interval)
	}
}

func TestConfigRejectsUnknownWorkerKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`worker:
  order:
    scan:
      concurrency: 1
      unknown: true
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := gofyconfig.Load[section](context.Background(), path, gofyconfig.WithStrict(true)); err == nil {
		t.Fatal("strict load accepted an unknown key under worker.order.scan")
	}
}

// TestRegisterBuildsEveryGroup pins the registration list and the invariant that
// every concurrency key addresses a worker that actually exists: a renamed
// worker would otherwise keep the binary building while running one instance.
// TestRegisterBuildsEveryGroup 固定注册清单，并守住"每个并发键都对应真实存在的
// worker"这条不变量：worker 改名后二进制仍能编译，但会静默按 1 个实例运行。
func TestRegisterBuildsEveryGroup(t *testing.T) {
	cfg := loadSection(t, `worker:
  health_addr: ""
`)

	groups, err := appworker.Register(cfg)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	got := make(map[string][]string, len(groups))
	for _, g := range groups {
		for _, w := range g.Workers() {
			got[g.Name()] = append(got[g.Name()], w.Name())
		}
	}
	want := map[string][]string{
		"order":  {"order-scan", "order-heartbeat"},
		"report": {"report-summary"},
	}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for name, wantWorkers := range want {
		if strings.Join(got[name], ",") != strings.Join(wantWorkers, ",") {
			t.Fatalf("group %s = %v, want %v", name, got[name], wantWorkers)
		}
	}

	for key := range appworker.Concurrency(cfg) {
		group, name, ok := strings.Cut(key, "/")
		if !ok {
			t.Fatalf("concurrency key %q is not group/worker", key)
		}
		found := false
		for _, workerName := range got[group] {
			if workerName == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("concurrency key %q addresses no registered worker", key)
		}
	}
}
