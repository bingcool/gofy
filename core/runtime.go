// Package core holds the runtime dependencies shared by the gofy root
// package, scripts and custom commands. It is a leaf package: it must not
// import any other gofy package.
package core

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// VersionInfo describes the build an application is running.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// Runtime is the set of dependencies built by the framework during startup.
// Business code reads it; it is not a dependency-injection container.
type Runtime struct {
	Name      string
	Env       string
	Logger    *zap.Logger
	DB        *gorm.DB
	Version   VersionInfo
	StartedAt time.Time

	ctx  context.Context
	fail func(error)
}

// RuntimeConfig contains the fields used to construct a Runtime.
// Applications normally do not call this directly.
type RuntimeConfig struct {
	Name      string
	Env       string
	Logger    *zap.Logger
	DB        *gorm.DB
	Version   VersionInfo
	StartedAt time.Time
	Ctx       context.Context
	Fail      func(error)
}

// NewRuntime builds a Runtime from cfg, filling safe defaults for nil fields.
func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	if cfg.Fail == nil {
		cfg.Fail = func(error) {}
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = time.Now()
	}
	return &Runtime{
		Name:      cfg.Name,
		Env:       cfg.Env,
		Logger:    cfg.Logger,
		DB:        cfg.DB,
		Version:   cfg.Version,
		StartedAt: cfg.StartedAt,
		ctx:       cfg.Ctx,
		fail:      cfg.Fail,
	}
}

// Context is cancelled when the application starts shutting down.
// Background goroutines should watch it instead of registering signals.
func (r *Runtime) Context() context.Context {
	if r == nil || r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

// Fail reports a fatal error and triggers application shutdown.
// The first call wins; later calls are ignored. Fail never panics.
func (r *Runtime) Fail(err error) {
	if r == nil || r.fail == nil || err == nil {
		return
	}
	r.fail(err)
}
