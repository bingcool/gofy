package script_test

import (
	"context"
	"testing"

	"github.com/bingcool/gofy/script"
	"github.com/spf13/pflag"
)

func TestValidateAcceptsValidScripts(t *testing.T) {
	err := script.Validate([]script.Script{
		{Name: "fix-order", Run: func(context.Context, *script.Runtime, *pflag.FlagSet) error { return nil }},
		{Name: "rebuild-index", Run: func(context.Context, *script.Runtime, *pflag.FlagSet) error { return nil }},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsInvalidScripts(t *testing.T) {
	valid := func() script.Script {
		return script.Script{Name: "valid", Run: func(context.Context, *script.Runtime, *pflag.FlagSet) error { return nil }}
	}
	cases := map[string][]script.Script{
		"invalid name":   {{Name: "Fix_Order", Run: valid().Run}},
		"reserved name":  {{Name: "list", Run: valid().Run}},
		"duplicate name": {valid(), valid()},
		"nil run":        {{Name: "no-run"}},
	}
	for name, scripts := range cases {
		t.Run(name, func(t *testing.T) {
			if err := script.Validate(scripts); err == nil {
				t.Fatal("Validate accepted an invalid script")
			}
		})
	}
}

func TestFind(t *testing.T) {
	scripts := []script.Script{
		{Name: "a", Run: func(context.Context, *script.Runtime, *pflag.FlagSet) error { return nil }},
		{Name: "b", Run: func(context.Context, *script.Runtime, *pflag.FlagSet) error { return nil }},
	}
	if got, ok := script.Find(scripts, "b"); !ok || got.Name != "b" {
		t.Fatalf("Find(b) = %+v, %v", got, ok)
	}
	if _, ok := script.Find(scripts, "missing"); ok {
		t.Fatal("Find returned a script for an unknown name")
	}
}
