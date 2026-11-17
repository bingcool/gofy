package command_test

import (
	"context"
	"testing"

	"github.com/bingcool/gofy/command"
	"github.com/spf13/pflag"
)

func run(context.Context, *command.Runtime, *pflag.FlagSet) error { return nil }

func TestValidateAcceptsValidCommands(t *testing.T) {
	err := command.Validate([]command.Command{
		{Use: "migrate", Run: run},
		{Use: "seed", Run: run},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsInvalidCommands(t *testing.T) {
	cases := map[string][]command.Command{
		"two words":        {{Use: "db migrate", Run: run}},
		"reserved":         {{Use: "start", Run: run}},
		"reserved restart": {{Use: "restart", Run: run}},
		"reserved status":  {{Use: "status", Run: run}},
		"invalid name":     {{Use: "Migrate", Run: run}},
		"duplicate":        {{Use: "migrate", Run: run}, {Use: "migrate", Run: run}},
		"nil run":          {{Use: "migrate"}},
		"extra reserved":   {{Use: "custom", Run: run}},
	}
	for name, cmds := range cases {
		t.Run(name, func(t *testing.T) {
			var err error
			if name == "extra reserved" {
				err = command.Validate(cmds, "custom")
			} else {
				err = command.Validate(cmds)
			}
			if err == nil {
				t.Fatal("Validate accepted an invalid command")
			}
		})
	}
}
