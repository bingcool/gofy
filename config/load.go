// Package config is an optional YAML loader. Applications may ignore it and
// load configuration however they prefer.
package config

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

type options struct {
	expandEnv     bool
	allowMissing  bool
	strict        bool
	applyDefaults bool
	validate      bool
}

// Option customizes Load.
type Option func(*options) error

// WithExpandEnv enables ${VAR} expansion (enabled by default).
func WithExpandEnv(enabled bool) Option {
	return func(o *options) error { o.expandEnv = enabled; return nil }
}

// WithAllowMissingEnv replaces missing ${VAR} with an empty string instead of
// failing (disabled by default).
func WithAllowMissingEnv(enabled bool) Option {
	return func(o *options) error { o.allowMissing = enabled; return nil }
}

// WithStrict rejects YAML keys that do not map to a struct field.
func WithStrict(enabled bool) Option {
	return func(o *options) error { o.strict = enabled; return nil }
}

// WithApplyDefaults applies `default:"..."` tags before decoding.
func WithApplyDefaults(enabled bool) Option {
	return func(o *options) error { o.applyDefaults = enabled; return nil }
}

// WithValidate runs the config's Validate method and validate tags.
func WithValidate(enabled bool) Option {
	return func(o *options) error { o.validate = enabled; return nil }
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads path into T, applying defaults, environment expansion and
// validation. It never mutates process-level state.
func Load[T any](ctx context.Context, path string, opts ...Option) (*T, error) {
	o := options{expandEnv: true, applyDefaults: true, validate: true}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&o); err != nil {
			return nil, fmt.Errorf("config: option: %w", err)
		}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("config: path is required")
	}

	var cfg T
	if o.applyDefaults {
		if err := defaults.Set(&cfg); err != nil {
			return nil, fmt.Errorf("config: apply defaults: %w", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	if o.expandEnv {
		data, err = expandEnvVars(data, o.allowMissing)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", path, err)
		}
	}
	if o.strict {
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && err != io.EOF {
			return nil, fmt.Errorf("config: decode %s: %w", path, err)
		}
	} else if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: decode %s: %w", path, err)
	}

	if o.validate {
		if v, ok := any(&cfg).(interface{ Validate() error }); ok {
			if err := v.Validate(); err != nil {
				return nil, fmt.Errorf("config: validate %s: %w", path, err)
			}
		}
		if err := validator.New().Struct(&cfg); err != nil {
			return nil, fmt.Errorf("config: validate %s: %w", path, err)
		}
	}
	return &cfg, nil
}

func expandEnvVars(data []byte, allowMissing bool) ([]byte, error) {
	const dollarSentinel = "\x00gofy-dollar\x00"
	s := strings.ReplaceAll(string(data), "$$", dollarSentinel)

	var missing []string
	out := envPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1]
		value, ok := os.LookupEnv(name)
		if !ok {
			if allowMissing {
				return ""
			}
			missing = append(missing, name)
			return match
		}
		return value
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return []byte(strings.ReplaceAll(out, dollarSentinel, "$")), nil
}
