package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gofyconfig "github.com/bingcool/gofy/config"
	gofyworker "github.com/bingcool/gofy/support/worker"

	appworker "github.com/yourorg/demo-service/worker"
	"github.com/yourorg/demo-service/worker/group1"
	"github.com/yourorg/demo-service/worker/group2"
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
  group1:
    scan:
      concurrency: 4
      interval: 2s
  group2:
    summary:
      concurrency: 3
      interval: 30m
`)

	if cfg.HealthAddr != "127.0.0.1:9091" {
		t.Fatalf("health_addr = %q", cfg.HealthAddr)
	}
	if cfg.Group1.Scan.Concurrency != 4 {
		t.Fatalf("group1.scan.concurrency = %d, want 4", cfg.Group1.Scan.Concurrency)
	}
	if cfg.Group1.Scan.Interval != 2*time.Second {
		t.Fatalf("group1.scan.interval = %s, want 2s", cfg.Group1.Scan.Interval)
	}
	if cfg.Group2.Summary.Concurrency != 3 {
		t.Fatalf("group2.summary.concurrency = %d, want 3", cfg.Group2.Summary.Concurrency)
	}
	if cfg.Group2.Summary.Interval != 30*time.Minute {
		t.Fatalf("group2.summary.interval = %s, want 30m", cfg.Group2.Summary.Interval)
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
	if cfg.Group1.Scan.Concurrency != 2 {
		t.Fatalf("group1.scan.concurrency = %d, want the default 2", cfg.Group1.Scan.Concurrency)
	}
	if cfg.Group1.Scan.Interval != 5*time.Second {
		t.Fatalf("group1.scan.interval = %s, want the default 5s", cfg.Group1.Scan.Interval)
	}
	if cfg.Group2.Summary.Concurrency != 1 {
		t.Fatalf("group2.summary.concurrency = %d, want the default 1", cfg.Group2.Summary.Concurrency)
	}
	if cfg.Group2.Summary.Interval != time.Hour {
		t.Fatalf("group2.summary.interval = %s, want the default 1h", cfg.Group2.Summary.Interval)
	}
}

func TestConfigRejectsUnknownWorkerKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`worker:
  group1:
    scan:
      concurrency: 1
      unknown: true
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := gofyconfig.Load[section](context.Background(), path, gofyconfig.WithStrict(true)); err == nil {
		t.Fatal("strict load accepted an unknown key under worker.group1.scan")
	}
}

// TestRegisterBuildsEveryGroup pins the registration list and the count each
// worker carries: the count now lives in Func, so a group that forgets to pass
// the configured value would silently run one instance.
// TestRegisterBuildsEveryGroup 固定注册清单与每个 worker 自带的实例数：计数现在由
// Func 携带，某个分组忘了把配置值传进去时，就会静默只跑一个实例。
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
		"group1": {"order-scan", "order-heartbeat"},
		"group2": {"report-summary"},
	}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for name, wantWorkers := range want {
		if strings.Join(got[name], ",") != strings.Join(wantWorkers, ",") {
			t.Fatalf("group %s = %v, want %v", name, got[name], wantWorkers)
		}
	}

	declared := make(map[string]int, len(got))
	for _, g := range groups {
		for _, w := range g.Workers() {
			if c, ok := w.(gofyworker.Concurrent); ok {
				declared[g.Name()+"/"+w.Name()] = int(c.Concurrency())
			}
		}
	}
	wantDeclared := map[string]int{
		"group1/order-scan":      2,
		"group1/order-heartbeat": 1,
		"group2/report-summary":  1,
	}
	for key, want := range wantDeclared {
		if declared[key] != want {
			t.Fatalf("declared concurrency for %s = %d, want %d (all: %v)", key, declared[key], want, declared)
		}
	}
}

// TestGroupRejectsConcurrencyOverflow guards the conversion into Func's uint8:
// a value like 300 would otherwise truncate to 44 and start the wrong number of
// instances without a word.
// TestGroupRejectsConcurrencyOverflow 守住转成 Func 的 uint8 这一步：300 这样的值
// 会被截断成 44，悄悄启动错误数量的实例。
func TestGroupRejectsConcurrencyOverflow(t *testing.T) {
	if _, err := group1.NewGroup(group1.Config{Scan: group1.ScanWorkerConfig{Concurrency: 300}}); err == nil {
		t.Fatal("group1.NewGroup accepted concurrency 300")
	}
	if _, err := group2.NewGroup(group2.Config{Summary: group2.SummaryWorkerConfig{Concurrency: 0}}); err == nil {
		t.Fatal("group2.NewGroup accepted concurrency 0")
	}
}
