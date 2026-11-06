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
