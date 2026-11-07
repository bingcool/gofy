package support

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// Router is a thin wrapper around a gin.RouterGroup. It keeps the router
// prefix, the per-application responder and the shared validator.
// A Router is only used while modules register routes; it is not safe for
// concurrent use.
type Router struct {
	group  *gin.RouterGroup
	engine *engine
	prefix string
}

type engine struct {
	responder Responder
	validator *validator.Validate
}

// RouterConfig configures the root router of an application.
type RouterConfig struct {
	Prefix    string
	Responder Responder
	Validator *validator.Validate
}

// NewRouter creates the root router on top of e. It is called by the gofy
// root package while building the HTTP layer.
func NewRouter(e *gin.Engine, cfg RouterConfig) *Router {
	if cfg.Responder == nil {
		cfg.Responder = NewResponder(ResponderConfig{})
	}
	if cfg.Validator == nil {
		cfg.Validator = NewValidator()
	}
	eng := &engine{responder: cfg.Responder, validator: cfg.Validator}
	prefix := NormalizePrefix(cfg.Prefix)
	return &Router{
		group:  e.Group(prefix),
		engine: eng,
		prefix: prefix,
	}
}

// Group creates a sub-router. The prefix is normalized to start with "/" and
// to have no trailing "/". An empty prefix keeps the parent path.
func (r *Router) Group(prefix string, mw ...Middleware) *Router {
	prefix = NormalizePrefix(prefix)
	group := r.group.Group(prefix)
	if len(mw) > 0 {
		group.Use(mw...)
	}
	return &Router{
		group:  group,
		engine: r.engine,
		prefix: joinPrefix(r.prefix, prefix),
	}
}

// Use appends middleware to this router.
func (r *Router) Use(mw ...Middleware) {
	r.group.Use(mw...)
}

// GET registers a GET route.
func (r *Router) GET(path string, h gin.HandlerFunc, mw ...Middleware) {
	r.Handle("GET", path, h, mw...)
}

// POST registers a POST route.
func (r *Router) POST(path string, h gin.HandlerFunc, mw ...Middleware) {
	r.Handle("POST", path, h, mw...)
}

// PUT registers a PUT route.
func (r *Router) PUT(path string, h gin.HandlerFunc, mw ...Middleware) {
	r.Handle("PUT", path, h, mw...)
}

// PATCH registers a PATCH route.
func (r *Router) PATCH(path string, h gin.HandlerFunc, mw ...Middleware) {
	r.Handle("PATCH", path, h, mw...)
}

// DELETE registers a DELETE route.
func (r *Router) DELETE(path string, h gin.HandlerFunc, mw ...Middleware) {
	r.Handle("DELETE", path, h, mw...)
}

// HEAD registers a HEAD route.
func (r *Router) HEAD(path string, h gin.HandlerFunc, mw ...Middleware) {
	r.Handle("HEAD", path, h, mw...)
}

// OPTIONS registers an OPTIONS route.
func (r *Router) OPTIONS(path string, h gin.HandlerFunc, mw ...Middleware) {
	r.Handle("OPTIONS", path, h, mw...)
}

// Handle registers a route for an arbitrary HTTP method.
func (r *Router) Handle(method, path string, h gin.HandlerFunc, mw ...Middleware) {
	handlers := make([]gin.HandlerFunc, 0, len(mw)+1)
	handlers = append(handlers, mw...)
	handlers = append(handlers, h)
	r.group.Handle(method, path, handlers...)
}

// Static serves files from root under relativePath.
func (r *Router) Static(relativePath, root string) {
	r.group.Static(relativePath, root)
}

// Prefix returns the full prefix of this router.
func (r *Router) Prefix() string { return r.prefix }

// Gin exposes the underlying router group. Its API is not covered by the
// gofy compatibility promise.
func (r *Router) Gin() *gin.RouterGroup { return r.group }

// Responder returns the responder configured for this application so adapters
// can render results with the same envelope.
func (r *Router) Responder() Responder { return r.engine.responder }

// Validator returns the validator shared by this application.
func (r *Router) Validator() *validator.Validate { return r.engine.validator }

// BindAndValidate applies defaults, binds path/query/body values and validates
// req. It is exported for typed adapters that invoke handlers outside the
// generic helpers.
func (r *Router) BindAndValidate(c *gin.Context, req any) error {
	return r.engine.bindAndValidate(c, req)
}

// NormalizePrefix makes prefix start with "/" and removes a trailing "/".
// An empty prefix stays empty.
func NormalizePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimRight(prefix, "/")
}

func joinPrefix(base, sub string) string {
	if sub == "" {
		return base
	}
	if base == "" {
		return sub
	}
	return base + sub
}
