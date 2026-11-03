// Package command defines custom CLI commands registered by an application.
package command

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bingcool/gofy/core"
	"github.com/spf13/pflag"
)

// Runtime is the shared runtime passed to custom commands.
type Runtime = core.Runtime

// Command is a custom subcommand.
type Command struct {
	Use             string // single word, e.g. "migrate"
	Short           string
	Flags           func(fs *pflag.FlagSet)
	Run             func(ctx context.Context, rt *Runtime, fs *pflag.FlagSet) error
	WithoutDatabase bool
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

var reservedNames = map[string]struct{}{
	"start":      {},
	"stop":       {},
	"script":     {},
	"version":    {},
	"help":       {},
	"completion": {},
}

// Validate checks command names, reserved words, duplicates and required
// fields. reserved allows the caller to reserve additional names.
func Validate(cmds []Command, reserved ...string) error {
	blocked := make(map[string]struct{}, len(reservedNames)+len(reserved))
	for name := range reservedNames {
		blocked[name] = struct{}{}
	}
	for _, name := range reserved {
		blocked[name] = struct{}{}
	}

	seen := make(map[string]struct{}, len(cmds))
	for i, c := range cmds {
		if c.Use == "" || strings.ContainsAny(c.Use, " \t") {
			return fmt.Errorf("command: invalid use %q at index %d (want a single word)", c.Use, i)
		}
		if !namePattern.MatchString(c.Use) {
			return fmt.Errorf("command: invalid name %q at index %d", c.Use, i)
		}
		if _, ok := blocked[c.Use]; ok {
			return fmt.Errorf("command: name %q is reserved", c.Use)
		}
		if _, ok := seen[c.Use]; ok {
			return fmt.Errorf("command: duplicate name %q", c.Use)
		}
		seen[c.Use] = struct{}{}
		if c.Run == nil {
			return fmt.Errorf("command: %q has nil Run", c.Use)
		}
	}
	return nil
}
