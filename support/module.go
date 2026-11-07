// Package support contains the HTTP-facing building blocks: modules,
// routers, generic handlers, binding, validation and response rendering.
package support

import (
	"context"

	"github.com/bingcool/gofy/core"
	"github.com/gin-gonic/gin"
)

// Middleware is a gin middleware function.
type Middleware = gin.HandlerFunc

// Module is how a business package attaches itself to the framework.
type Module interface {
	// Name is the module name. It must be unique and match
	// ^[a-z][a-z0-9-]{0,62}$.
	Name() string
	// Register registers routes. It must not start goroutines or open
	// connections; use Start for that.
	Register(r *Router) error
}

// ModuleFactory builds a Module during startup, when the Runtime is ready.
type ModuleFactory func(rt *core.Runtime) (Module, error)

// Prefixer lets a module override its mount prefix.
// The default prefix is "/" + Module.Name().
type Prefixer interface {
	Prefix() string
}

// Starter is called after the HTTP listener is ready.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper is called after the HTTP server stops accepting new requests.
type Stopper interface {
	Stop(ctx context.Context) error
}
