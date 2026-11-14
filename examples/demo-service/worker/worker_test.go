package worker_test

import (
	"strings"
	"testing"

	gofyworker "github.com/bingcool/gofy/support/worker"

	appworker "github.com/yourorg/demo-service/worker"
)

// TestRegisterBuildsEveryGroup pins the registration list and the count each
// worker carries. The count now lives in the group package next to the worker,
// so a group that drifts from its documented number would change how many
// instances start without anything else noticing.
// TestRegisterBuildsEveryGroup 固定注册清单与每个 worker 自带的实例数。计数现在就在
// 分组包里、紧挨它配置的 worker，某处与文档里的数字脱节时，启动的实例数会跟着变而
// 没有人察觉。
func TestRegisterBuildsEveryGroup(t *testing.T) {
	groups, err := appworker.Register()
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
