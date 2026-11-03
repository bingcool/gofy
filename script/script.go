// Package script defines one-off maintenance tasks executed through the
// `script` subcommand.
package script

import (
	"context"
	"fmt"
	"regexp"

	"github.com/bingcool/gofy/core"
	"github.com/spf13/pflag"
)

// Runtime is the shared runtime passed to scripts.
type Runtime = core.Runtime

// Script is a registered maintenance task.
type Script struct {
	Name            string
	Short           string
	Flags           func(fs *pflag.FlagSet)
	Run             func(ctx context.Context, rt *Runtime, fs *pflag.FlagSet) error
	WithoutDatabase bool
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

var reservedNames = map[string]struct{}{
	"list":       {},
	"help":       {},
	"completion": {},
}

// Validate checks names, reserved words, duplicates and required fields.
func Validate(scripts []Script) error {
	seen := make(map[string]struct{}, len(scripts))
	for i, s := range scripts {
		if !namePattern.MatchString(s.Name) {
			return fmt.Errorf("script: invalid name %q at index %d", s.Name, i)
		}
		if _, ok := reservedNames[s.Name]; ok {
			return fmt.Errorf("script: name %q is reserved", s.Name)
		}
		if _, ok := seen[s.Name]; ok {
			return fmt.Errorf("script: duplicate name %q", s.Name)
		}
		seen[s.Name] = struct{}{}
		if s.Run == nil {
			return fmt.Errorf("script: %q has nil Run", s.Name)
		}
	}
	return nil
}

// Find returns the script with the given name.
func Find(scripts []Script, name string) (Script, bool) {
	for _, s := range scripts {
		if s.Name == name {
			return s, true
		}
	}
	return Script{}, false
}
