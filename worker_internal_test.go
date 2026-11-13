package gofy

import (
	"context"
	"testing"

	"github.com/bingcool/gofy/support/worker"
)

func TestWorkerPIDPath(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		group string
		want  string
	}{
		{"empty", "", "order", ""},
		{"relative", "app/demo-service.pid", "order", "app/demo-service-worker-order.pid"},
		{"absolute", "/tmp/demo.pid", "settle", "/tmp/demo-worker-settle.pid"},
		{"no extension", "/tmp/demo", "order", "/tmp/demo-worker-order"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerPIDPath(tc.path, tc.group); got != tc.want {
				t.Fatalf("workerPIDPath(%q, %q) = %q, want %q", tc.path, tc.group, got, tc.want)
			}
		})
	}
}

func TestWorkerDescriptorsResolveConcurrency(t *testing.T) {
	order, err := worker.NewGroup("order",
		worker.Func("scan", func(context.Context) error { return nil }),
		worker.Func("notify", func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}
	settle, err := worker.NewGroup("settle",
		worker.Func("reconcile", func(context.Context) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewGroup: %v", err)
	}

	app, err := New(
		WithName("worker-test"),
		WithWorkerGroups(order, settle),
		WithWorkerConcurrency(map[string]int{"order/scan": 4}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got := app.workerDescriptors()
	if len(got) != 2 {
		t.Fatalf("groups = %d, want 2", len(got))
	}
	concurrency := map[string]int{}
	for _, g := range got {
		for _, w := range g.Workers {
			concurrency[g.Name+"/"+w.Name] = w.Concurrency
		}
	}
	want := map[string]int{
		"order/scan":       4,
		"order/notify":     1,
		"settle/reconcile": 1,
	}
	for key, n := range want {
		if concurrency[key] != n {
			t.Fatalf("concurrency[%s] = %d, want %d (all: %v)", key, concurrency[key], n, concurrency)
		}
	}
}

func TestWithWorkerConcurrencyRejectsBadInput(t *testing.T) {
	cases := []map[string]int{
		{"order": 1},
		{"order/scan/extra": 1},
		{"/scan": 1},
		{"order/": 1},
		{"order/scan": 0},
	}
	for _, c := range cases {
		if _, err := New(WithName("worker-test"), WithWorkerConcurrency(c)); err == nil {
			t.Fatalf("New(%v) = nil error, want validation failure", c)
		}
	}
}

func TestWithWorkerGroupsRejectsDuplicate(t *testing.T) {
	build := func() *worker.Group {
		g, err := worker.NewGroup("order", worker.Func("scan", func(context.Context) error { return nil }))
		if err != nil {
			t.Fatalf("NewGroup: %v", err)
		}
		return g
	}
	if _, err := New(WithName("worker-test"), WithWorkerGroups(build(), build())); err == nil {
		t.Fatalf("New with two identical groups = nil error, want failure")
	}
}
