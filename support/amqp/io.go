package amqp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrUnknownInstance is returned when the requested name is not registered.
var ErrUnknownInstance = errors.New("amqp: unknown instance")

// Registry holds named configurations and opens their clients on first use.
//
// Configure installs a new generation and returns it; NewIO uses the most
// recently installed generation. Each generation owns the clients it opened,
// so a reload can build a new Registry, start using it, and close the previous
// one without disturbing in-flight users of the old clients.
type Registry struct {
	mu      sync.Mutex
	configs map[string]Config
	clients map[string]*Client
	closed  bool
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
		clients: make(map[string]*Client, len(configs)),
	}
	for name, cfg := range configs {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("amqp: instance name must not be empty")
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("amqp instance %q: %w", name, err)
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
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.closed {
		current.Store(r)
	}
}

// NewIO returns the client registered under name in the current generation,
// opening it on first use. It reports ErrNotConfigured before Configure and
// ErrUnknownInstance for an unregistered name.
func NewIO(ctx context.Context, name string) (*Client, error) {
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
func (r *Registry) NewIO(ctx context.Context, name string) (*Client, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	name = strings.TrimSpace(name)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, ErrNotConfigured
	}
	if client := r.clients[name]; client != nil {
		return client, nil
	}
	cfg, ok := r.configs[name]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownInstance, name)
	}
	client, err := New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("amqp instance %q: %w", name, err)
	}
	r.clients[name] = client
	return client, nil
}

// Close releases the clients opened by this generation and stops it from
// opening more. Closing an earlier generation does not affect the current one.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	clients := r.clients
	r.clients = nil
	r.mu.Unlock()

	current.CompareAndSwap(r, nil)

	var errs []error
	for name, client := range clients {
		if err := client.Close(); err != nil {
			errs = append(errs, fmt.Errorf("amqp instance %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
