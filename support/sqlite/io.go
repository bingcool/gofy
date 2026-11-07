package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

var (
	// ErrNotConfigured is returned when Configure has not been called or the
	// registry has already been closed.
	ErrNotConfigured = errors.New("sqlite: no instance is configured")
	// ErrUnknownInstance is returned when the requested name is not registered.
	ErrUnknownInstance = errors.New("sqlite: unknown instance")
)

// Registry holds named configurations and opens their pools on first use.
//
// Configure installs a new generation and returns it; NewIO uses the most
// recently installed generation.
type Registry struct {
	mu      sync.Mutex
	configs map[string]Config
	dbs     map[string]*gorm.DB
	logger  *zap.Logger
	closed  bool
}

var current atomic.Pointer[Registry]

// Configure validates the named configurations, installs them as the current
// generation and returns it. An empty map is allowed: NewIO then reports
// ErrUnknownInstance, which lets a reload disable every instance.
func Configure(configs map[string]Config, logger *zap.Logger) (*Registry, error) {
	reg, err := NewRegistry(configs, logger)
	if err != nil {
		return nil, err
	}
	reg.Install()
	return reg, nil
}

// NewRegistry validates the named configurations without making them current.
// Do the eager opening through NewIO and call Install once every instance is
// ready, so a failed startup does not replace a working generation.
func NewRegistry(configs map[string]Config, logger *zap.Logger) (*Registry, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	reg := &Registry{
		configs: make(map[string]Config, len(configs)),
		dbs:     make(map[string]*gorm.DB, len(configs)),
		logger:  logger,
	}
	for name, cfg := range configs {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("sqlite: instance name must not be empty")
		}
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("sqlite instance %q: %w", name, err)
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

// NewIO returns the pool registered under name in the current generation,
// opening it on first use. It reports ErrNotConfigured before Configure and
// ErrUnknownInstance for an unregistered name.
func NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	reg := current.Load()
	if reg == nil {
		return nil, ErrNotConfigured
	}
	return reg.NewIO(ctx, name)
}

// CloseAllIO closes every pool opened by the current generation and clears it,
// so a later NewIO reports ErrNotConfigured.
func CloseAllIO() error {
	reg := current.Load()
	if reg == nil {
		return nil
	}
	return reg.Close()
}

// NewIO returns the pool registered under name in this generation.
func (r *Registry) NewIO(ctx context.Context, name string) (*gorm.DB, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	name = strings.TrimSpace(name)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, ErrNotConfigured
	}
	if db := r.dbs[name]; db != nil {
		return db, nil
	}
	cfg, ok := r.configs[name]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownInstance, name)
	}
	db, err := New(ctx, cfg, r.logger)
	if err != nil {
		return nil, fmt.Errorf("sqlite instance %q: %w", name, err)
	}
	r.dbs[name] = db
	return db, nil
}

// Close releases the pools opened by this generation and stops it from opening
// more. Closing an earlier generation does not affect the current one.
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
	dbs := r.dbs
	r.dbs = nil
	r.mu.Unlock()

	current.CompareAndSwap(r, nil)

	var errs []error
	for name, db := range dbs {
		if err := Close(db); err != nil {
			errs = append(errs, fmt.Errorf("sqlite instance %q: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
