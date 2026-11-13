package script

import (
	"context"
	"testing"

	"github.com/bingcool/gofy/core"
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/config"
)

func TestWithIORequiresDatabase(t *testing.T) {
	ran := false
	s := withIO(&config.Config{}, gofyscript.Script{
		Name: "needs-db",
		Run: func(context.Context, *gofyscript.Runtime, *pflag.FlagSet) error {
			ran = true
			return nil
		},
	})
	rt := core.NewRuntime(core.RuntimeConfig{})
	err := s.Run(context.Background(), rt, pflag.NewFlagSet("needs-db", pflag.ContinueOnError))
	if err == nil {
		t.Fatal("expected an error when db.default is missing")
	}
	if ran {
		t.Fatal("script ran without the io generation")
	}
}

func TestWithIOSkipsScriptsWithoutDatabase(t *testing.T) {
	ran := false
	s := withIO(&config.Config{}, gofyscript.Script{
		Name:            "redis-only",
		WithoutDatabase: true,
		Run: func(context.Context, *gofyscript.Runtime, *pflag.FlagSet) error {
			ran = true
			return nil
		},
	})
	rt := core.NewRuntime(core.RuntimeConfig{})
	if err := s.Run(context.Background(), rt, pflag.NewFlagSet("redis-only", pflag.ContinueOnError)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatal("script did not run")
	}
}
