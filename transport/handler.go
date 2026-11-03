package transport

import (
	"context"

	"github.com/gin-gonic/gin"
)

// Handler is a typed business handler. Request binding and validation happen
// before it is called; its result is rendered by the Router responder.
type Handler[Req any, Res any] func(ctx context.Context, req *Req) (*Res, error)

// GET registers a typed GET handler.
func GET[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware) {
	r.Handle("GET", path, adapt(r, h), mw...)
}

// POST registers a typed POST handler.
func POST[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware) {
	r.Handle("POST", path, adapt(r, h), mw...)
}

// PUT registers a typed PUT handler.
func PUT[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware) {
	r.Handle("PUT", path, adapt(r, h), mw...)
}

// PATCH registers a typed PATCH handler.
func PATCH[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware) {
	r.Handle("PATCH", path, adapt(r, h), mw...)
}

// DELETE registers a typed DELETE handler.
func DELETE[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...Middleware) {
	r.Handle("DELETE", path, adapt(r, h), mw...)
}

// Any registers a typed handler for an arbitrary HTTP method.
func Any[Req any, Res any](r *Router, method, path string, h Handler[Req, Res], mw ...Middleware) {
	r.Handle(method, path, adapt(r, h), mw...)
}

// NoContent registers a typed handler without a response body.
// A successful call returns 204.
func NoContent[Req any](r *Router, method, path string, h func(context.Context, *Req) error, mw ...Middleware) {
	r.Handle(method, path, func(c *gin.Context) {
		var req Req
		if err := r.engine.bindAndValidate(c, &req); err != nil {
			r.engine.responder.Error(c, err)
			return
		}
		if err := h(c.Request.Context(), &req); err != nil {
			r.engine.responder.Error(c, err)
			return
		}
		c.Status(204)
	}, mw...)
}

func adapt[Req any, Res any](r *Router, h Handler[Req, Res]) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req Req
		if err := r.engine.bindAndValidate(c, &req); err != nil {
			r.engine.responder.Error(c, err)
			return
		}
		res, err := h(c.Request.Context(), &req)
		if err != nil {
			r.engine.responder.Error(c, err)
			return
		}
		r.engine.responder.Success(c, res)
	}
}
