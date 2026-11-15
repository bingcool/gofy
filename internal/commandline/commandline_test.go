package commandline_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/internal/commandline"
)

func TestStopRequiresYesConfirmation(t *testing.T) {
	var out bytes.Buffer
	called := false
	d := commandline.Deps{
		Name: "app",
		In:   strings.NewReader("no\n"),
		Out:  &out,
		Stop: func(context.Context, commandline.StopOptions) error {
			called = true
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"stop"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if called {
		t.Fatal("stop ran without confirmation")
	}
	if !strings.Contains(out.String(), "你已放弃停止应用【app】") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestStopConfirmYesRuns(t *testing.T) {
	called := false
	d := commandline.Deps{
		Name: "app",
		In:   strings.NewReader("YES\n"),
		Stop: func(context.Context, commandline.StopOptions) error {
			called = true
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"stop"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !called {
		t.Fatal("stop skipped after yes")
	}
}

func TestStopForceSkipsConfirmation(t *testing.T) {
	var got commandline.StopOptions
	d := commandline.Deps{
		Name: "app",
		In:   strings.NewReader(""),
		Stop: func(_ context.Context, opts commandline.StopOptions) error {
			got = opts
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"stop", "--force=1"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !got.Force {
		t.Fatalf("force = %+v", got)
	}
}

func TestRestartRequiresConfirmation(t *testing.T) {
	called := false
	d := commandline.Deps{
		Name: "app",
		In:   strings.NewReader("no\n"),
		Out:  &bytes.Buffer{},
		Restart: func(context.Context, commandline.StopOptions) error {
			called = true
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"restart"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if called {
		t.Fatal("restart ran without confirmation")
	}
}

func TestRestartForceSkipsConfirmation(t *testing.T) {
	var got commandline.StopOptions
	d := commandline.Deps{
		Name: "app",
		In:   strings.NewReader(""),
		Restart: func(_ context.Context, opts commandline.StopOptions) error {
			got = opts
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"restart", "--force=1"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !got.Force {
		t.Fatalf("force = %+v", got)
	}
}

func TestStartDaemonFlag(t *testing.T) {
	var got commandline.StartOptions
	d := commandline.Deps{
		Name: "app",
		Start: func(_ context.Context, opts commandline.StartOptions) error {
			got = opts
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"start", "--daemon=1"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !got.Daemon {
		t.Fatalf("daemon = %+v", got)
	}
}

func TestRestartUsesStopFlags(t *testing.T) {
	var got commandline.StopOptions
	d := commandline.Deps{
		Name: "app",
		Restart: func(_ context.Context, opts commandline.StopOptions) error {
			got = opts
			return nil
		},
		DefaultPIDFile:     "app/app.pid",
		DefaultStopTimeout: 7 * time.Second,
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"restart", "--pid-file", "custom.pid", "--timeout", "3s", "--force"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got.PIDFile != "custom.pid" || got.Timeout != 3*time.Second || !got.Force {
		t.Fatalf("restart options = %+v", got)
	}
}

func TestRestartDefaults(t *testing.T) {
	var got commandline.StopOptions
	d := commandline.Deps{
		Name: "app",
		In:   strings.NewReader("yes\n"),
		Restart: func(_ context.Context, opts commandline.StopOptions) error {
			got = opts
			return nil
		},
		DefaultPIDFile:     "app/app.pid",
		DefaultStopTimeout: 7 * time.Second,
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"restart"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got.PIDFile != "app/app.pid" || got.Timeout != 7*time.Second || got.Force {
		t.Fatalf("restart defaults = %+v", got)
	}
}

func TestScriptListPrintsHeader(t *testing.T) {
	var out bytes.Buffer
	d := commandline.Deps{
		Name: "app",
		Out:  &out,
		Scripts: []commandline.Script{
			{Name: "fix-order", Short: "fix inconsistent order data"},
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"script", "--list"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("script --list: %v", err)
	}
	got := out.String()
	for _, want := range []string{"NAME", "DESCRIPTION", "fix-order", "fix inconsistent order data"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRestartRejectsArguments(t *testing.T) {
	d := commandline.Deps{
		Name:    "app",
		Restart: func(context.Context, commandline.StopOptions) error { return nil },
		Usage: func(err error) error {
			return err
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"restart", "extra"})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("restart accepted positional arguments")
	}
}

func TestWorkerGroupSelectorSpellings(t *testing.T) {
	for _, group := range []string{"*", "all", " all "} {
		if !commandline.IsAllWorkerGroup(group) {
			t.Fatalf("IsAllWorkerGroup(%q) = false, want true", group)
		}
	}
	for _, group := range []string{"", "order", "all-orders", "*x"} {
		if commandline.IsAllWorkerGroup(group) {
			t.Fatalf("IsAllWorkerGroup(%q) = true, want false", group)
		}
	}
}

func TestWorkerDaemonFlag(t *testing.T) {
	var got commandline.WorkerOptions
	d := commandline.Deps{
		Name: "app",
		RunWorker: func(_ context.Context, opts commandline.WorkerOptions) error {
			got = opts
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"worker", "--group=order", "--daemon=1"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("worker: %v", err)
	}
	if !got.Daemon || got.Group != "order" {
		t.Fatalf("worker options = %+v", got)
	}
}

func TestWorkerRequiresGroupButSuggestsTheAlias(t *testing.T) {
	d := commandline.Deps{
		Name:      "app",
		RunWorker: func(context.Context, commandline.WorkerOptions) error { return nil },
		Usage:     func(err error) error { return err },
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"worker"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--group=all") {
		t.Fatalf("worker without --group = %v, want an error naming --group=all", err)
	}
}

func TestWorkerStopDispatchesEveryGroupByDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no group", args: []string{"worker", "stop", "--force"}, want: ""},
		{name: "wildcard", args: []string{"worker", "stop", "--group=*", "--force"}, want: "*"},
		{name: "alias", args: []string{"worker", "stop", "--group=all", "--force"}, want: "all"},
		{name: "named group before the subcommand", args: []string{"worker", "--group=order", "stop", "--force"}, want: "order"},
		{name: "named group after the subcommand", args: []string{"worker", "stop", "--group=order", "--force"}, want: "order"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got commandline.WorkerOptions
			called := false
			d := commandline.Deps{
				Name: "app",
				StopWorker: func(_ context.Context, opts commandline.WorkerOptions) error {
					called = true
					got = opts
					return nil
				},
			}
			cmd := commandline.Build(d)
			cmd.SetArgs(tc.args)
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("worker stop: %v", err)
			}
			if !called {
				t.Fatal("worker stop did not reach the app")
			}
			if got.Group != tc.want || !got.Force {
				t.Fatalf("worker stop options = %+v, want group %q and force", got, tc.want)
			}
		})
	}
}

func TestWorkerStopAsksBeforeSignalling(t *testing.T) {
	var out bytes.Buffer
	called := false
	d := commandline.Deps{
		Name: "app",
		In:   strings.NewReader("no\n"),
		Out:  &out,
		StopWorker: func(context.Context, commandline.WorkerOptions) error {
			called = true
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"worker", "stop", "--group=order"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("worker stop: %v", err)
	}
	if called {
		t.Fatal("worker stop ran without confirmation")
	}
	if !strings.Contains(out.String(), "你已放弃停止worker 分组【order】") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestWorkerRestartDefaultsToTheBackground(t *testing.T) {
	var got commandline.WorkerOptions
	d := commandline.Deps{
		Name: "app",
		RestartWorker: func(_ context.Context, opts commandline.WorkerOptions) error {
			got = opts
			return nil
		},
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"worker", "restart", "--group=order", "--force=1"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("worker restart: %v", err)
	}
	if !got.Daemon || got.Group != "order" || !got.Force {
		t.Fatalf("worker restart options = %+v, want daemon with group order", got)
	}

	cmd = commandline.Build(d)
	cmd.SetArgs([]string{"worker", "restart", "--group=order", "--force=1", "--daemon=false"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("worker restart --daemon=false: %v", err)
	}
	if got.Daemon {
		t.Fatalf("worker restart --daemon=false options = %+v, want foreground", got)
	}
}

func TestWorkerStopUsesTheDefaultTimeout(t *testing.T) {
	var got commandline.WorkerOptions
	d := commandline.Deps{
		Name:               "app",
		StopWorker:         func(_ context.Context, opts commandline.WorkerOptions) error { got = opts; return nil },
		DefaultStopTimeout: 7 * time.Second,
	}
	cmd := commandline.Build(d)
	cmd.SetArgs([]string{"worker", "stop", "--force=1", "--group=order", "--timeout=3s"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("worker stop: %v", err)
	}
	if got.Timeout != 3*time.Second {
		t.Fatalf("timeout = %v, want 3s", got.Timeout)
	}
}
