package commandline_test

import (
	"context"
	"testing"
	"time"

	"github.com/bingcool/gofy/internal/commandline"
)

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
