package worker

import (
	"context"
	"strings"
	"testing"
)

// dbWorker is a Worker that optionally declares the db.default requirement.
// dbWorker 是可选声明 db.default 要求的 Worker。
type dbWorker struct {
	name     string
	requires bool
}

func (w dbWorker) Name() string              { return w.name }
func (w dbWorker) Run(context.Context) error { return nil }
func (w dbWorker) RequiresDefaultDB() bool   { return w.requires }

// plainWorker does not implement DefaultDBRequirer. A polling loop is such a
// worker, and it must never be rejected for a missing db.default.
// plainWorker 不实现 DefaultDBRequirer。轮询任务就是这种 worker，不能因为没有
// db.default 就把它们拦下来。
type plainWorker struct{ name string }

func (w plainWorker) Name() string              { return w.name }
func (w plainWorker) Run(context.Context) error { return nil }

// TestRequireDefaultDB pins the startup gate: the deployment shapes that must
// pass, and the ones that must fail with a message naming both the worker and
// the pool.
// TestRequireDefaultDB 固定启动闸门：哪些部署形状必须放行，哪些必须失败，且错误要点出
// worker 与连接池。
func TestRequireDefaultDB(t *testing.T) {
	group := func(t *testing.T, name string, workers ...Worker) *Group {
		t.Helper()
		g, err := NewGroup(name, workers...)
		if err != nil {
			t.Fatalf("NewGroup(%s): %v", name, err)
		}
		return g
	}

	cases := []struct {
		name      string
		groups    []*Group
		available bool
		wantErr   bool
		want      []string
	}{
		{
			// Nothing to check when the pool is there.
			// 池在时不需要检查。
			name:      "available pool satisfies a requiring worker",
			groups:    []*Group{group(t, "orders", dbWorker{name: "consume", requires: true})},
			available: true,
		},
		{
			// The case the check must not break: db.order alone is a valid
			// deployment for workers that do not declare the requirement.
			// 检查绝不能破坏的形状：对没有声明要求的 worker 来说，只有 db.order 也是
			// 合法部署。
			name:   "named instances are enough when nothing requires db.default",
			groups: []*Group{group(t, "orders", plainWorker{name: "scan"}, dbWorker{name: "consume", requires: false})},
		},
		{
			name:   "a nil group is skipped",
			groups: []*Group{nil, group(t, "orders", plainWorker{name: "scan"})},
		},
		{
			name:    "missing pool names the worker and db.default",
			groups:  []*Group{group(t, "orders", dbWorker{name: "consume", requires: true})},
			wantErr: true,
			want:    []string{"db.default", "orders/consume", "db.order"},
		},
		{
			// Every offending worker, not just the first: a group of ten
			// subscriptions would otherwise be fixed one startup at a time.
			// 要列出全部越界的 worker，而不只是第一个：十个订阅的分组否则要一次启动改一个。
			name: "every requiring worker is listed",
			groups: []*Group{
				group(t, "orders",
					dbWorker{name: "consume-kafka", requires: true},
					dbWorker{name: "consume-amqp", requires: true}),
				group(t, "reports", dbWorker{name: "consume-redis", requires: true}),
			},
			wantErr: true,
			want:    []string{"orders/consume-kafka", "orders/consume-amqp", "reports/consume-redis"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireDefaultDB(tc.groups, tc.available)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("RequireDefaultDB: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("RequireDefaultDB accepted a worker that needs a pool that is not there")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want it to mention %q", err, want)
				}
			}
		})
	}
}
