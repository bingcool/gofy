package commandline_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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

// TestWorkerStartArgs covers the argv the daemon child receives. The property
// that matters is negative: no `restart`, no `--force`, no `--timeout` — the
// child must start the group, not repeat the control command that spawned it.
// TestWorkerStartArgs 覆盖 daemon 子进程收到的 argv。真正要紧的是"不含什么"：
// 没有 restart、--force、--timeout——子进程只启动分组，不重复拉起它的控制命令。
func TestWorkerStartArgs(t *testing.T) {
	cases := []struct {
		name string
		opts commandline.WorkerOptions
		want []string
	}{
		{
			name: "group only",
			opts: commandline.WorkerOptions{Group: "order"},
			want: []string{"worker", "--group=order", "--daemon=false"},
		},
		{
			name: "names and concurrency",
			opts: commandline.WorkerOptions{Group: "order", Names: []string{"scan", "notify"}, Concurrency: 3},
			want: []string{"worker", "--group=order", "--concurrency=3", "--name=scan", "--name=notify", "--daemon=false"},
		},
		{
			name: "wildcard group",
			opts: commandline.WorkerOptions{Group: commandline.WorkerGroupAllAlias},
			want: []string{"worker", "--group=all", "--daemon=false"},
		},
		{
			name: "stop-only flags are dropped",
			opts: commandline.WorkerOptions{Group: "order", Force: true, Timeout: 30 * time.Second, Daemon: true},
			want: []string{"worker", "--group=order", "--daemon=false"},
		},
		{
			name: "blank name is skipped",
			opts: commandline.WorkerOptions{Group: "order", Names: []string{" ", ""}},
			want: []string{"worker", "--group=order", "--daemon=false"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commandline.WorkerStartArgs(tc.opts)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("WorkerStartArgs = %v, want %v", got, tc.want)
			}
			for _, arg := range got {
				if strings.Contains(arg, "restart") || strings.HasPrefix(arg, "--force") || strings.HasPrefix(arg, "--timeout") {
					t.Fatalf("argv carries %q; the child must not repeat the control command", arg)
				}
			}
		})
	}
}
