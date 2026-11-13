package commandline_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bingcool/gofy/internal/commandline"
)

func TestWorkerListPrintsGroups(t *testing.T) {
	var out bytes.Buffer
	root := commandline.Build(commandline.Deps{
		Name:   "demo",
		Out:    &out,
		ErrOut: &out,
		WorkerGroups: []commandline.WorkerGroup{
			{Name: "order", Workers: []commandline.WorkerInfo{
				{Name: "scan", Concurrency: 3},
				{Name: "notify", Concurrency: 1},
			}},
		},
		RunWorker: func(context.Context, commandline.WorkerOptions) error {
			t.Fatalf("RunWorker must not be called for --list")
			return nil
		},
	})
	root.SetArgs([]string{"worker", "--list"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext: %v", err)
	}
	text := out.String()
	for _, want := range []string{"GROUP", "WORKER", "CONCURRENCY", "order", "scan", "3"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q does not contain %q", text, want)
		}
	}
}

func TestWorkerListWithoutGroups(t *testing.T) {
	var out bytes.Buffer
	root := commandline.Build(commandline.Deps{Name: "demo", Out: &out, ErrOut: &out})
	root.SetArgs([]string{"worker", "--list"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext: %v", err)
	}
	if !strings.Contains(out.String(), "no worker groups registered") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestWorkerRequiresGroup(t *testing.T) {
	root := commandline.Build(commandline.Deps{
		Name: "demo",
		RunWorker: func(context.Context, commandline.WorkerOptions) error {
			t.Fatalf("RunWorker must not be called without --group")
			return nil
		},
	})
	root.SetArgs([]string{"worker"})
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--group is required") {
		t.Fatalf("error = %v, want --group required", err)
	}
}

func TestWorkerPassesFlags(t *testing.T) {
	var got commandline.WorkerOptions
	root := commandline.Build(commandline.Deps{
		Name: "demo",
		RunWorker: func(_ context.Context, opts commandline.WorkerOptions) error {
			got = opts
			return nil
		},
	})
	root.SetArgs([]string{"worker", "--group=order", "--name=scan", "--name=notify", "--concurrency=2"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext: %v", err)
	}
	if got.Group != "order" || got.Concurrency != 2 {
		t.Fatalf("options = %+v", got)
	}
	if len(got.Names) != 2 || got.Names[0] != "scan" || got.Names[1] != "notify" {
		t.Fatalf("names = %v", got.Names)
	}
}
