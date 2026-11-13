package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bingcool/gofy/support/internal/instanceinit"
)

var (
	// ErrNotConfigured is returned when Configure has not been called or the
	// registry has already been closed.
	ErrNotConfigured = errors.New("redis: no instance is configured")
	// ErrUnknownInstance is returned when the requested name is not registered.
	ErrUnknownInstance = errors.New("redis: unknown instance")
)

// Registry holds named configurations and opens their clients on first use.
//
// Configure installs a new generation and returns it; NewIO uses the most
// recently installed generation. Each generation owns the clients it opened,
// so a reload can build a new Registry, start using it, and close the previous
// one without disturbing in-flight users of the old clients.
//
// Clients open outside the registry lock: different names initialize in
// parallel, concurrent callers of one name share a single attempt, and a failed
// attempt is retried by the next call. Prefer the instance Registry for
// per-generation isolation.
type Registry struct {
	configs map[string]Config
	slots   *instanceinit.Slots[*goredis.Client]
}

var current atomic.Pointer[Registry]

// Configure validates the named configurations, installs them as the current
// generation and returns it. An empty map is allowed: NewIO then reports
// ErrUnknownInstance, which lets a reload disable every instance.
func Configure(configs map[string]Config) (*Registry, error) {
	reg, err := NewRegistry(configs)
	if err != nil {
		return nil, err
	}
	reg.Install()
	return reg, nil
}

// NewRegistry validates the named configurations without making them current.
// Do the eager opening through NewIO and call Install once every instance is
// ready, so a failed startup does not replace a working generation.
func NewRegistry(configs map[string]Config) (*Registry, error) {
	reg := &Registry{
		configs: make(map[string]Config, len(configs)),
		slots: instanceinit.New(func(name string, client *goredis.Client) error {
			if err := Close(client); err != nil {
				return fmt.Errorf("redis instance %q: %w", name, err)
			}
			return nil
		}),
	}
	for name, cfg := range configs {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("redis: instance name must not be empty")
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("redis instance %q: %w", name, err)
		}
		reg.configs[name] = cfg
	}
	return reg, nil
}

// Install makes this generation current for package-level NewIO. Installing a
// closed generation is a no-op.
func (r *Registry) Install() {
	if r == nil {
		return
	}
	r.slots.Publish(func() { current.Store(r) })
}

// NewIO returns the client registered under name in the current generation,
// opening it on first use. It reports ErrNotConfigured before Configure and
// ErrUnknownInstance for an unregistered name.
func NewIO(ctx context.Context, name string) (*goredis.Client, error) {
	reg := current.Load()
	if reg == nil {
		return nil, ErrNotConfigured
	}
	return reg.NewIO(ctx, name)
}

// CloseAllIO closes every client opened by the current generation and clears
// it, so a later NewIO reports ErrNotConfigured.
func CloseAllIO() error {
	reg := current.Load()
	if reg == nil {
		return nil
	}
	return reg.Close()
}

// NewIO returns the client registered under name in this generation.
func (r *Registry) NewIO(ctx context.Context, name string) (*goredis.Client, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	name = strings.TrimSpace(name)

	client, err := r.slots.Get(ctx, name, func(ctx context.Context) (*goredis.Client, error) {
		cfg, ok := r.configs[name]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrUnknownInstance, name)
		}
		client, err := New(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("redis instance %q: %w", name, err)
		}
		return client, nil
	})
	if errors.Is(err, instanceinit.ErrClosed) {
		return nil, ErrNotConfigured
	}
	return client, err
}

// Close releases the clients opened by this generation and stops it from
// opening more. Closing an earlier generation does not affect the current one.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	current.CompareAndSwap(r, nil)
	return r.slots.Close()
}
