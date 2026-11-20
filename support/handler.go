package support

import (
	"context"

	"github.com/gin-gonic/gin"
)

// Handler is a typed business handler. Request binding and validation happen
// before it is called; its result is rendered by the Router responder.
// Handler 是有类型的业务处理器：调用前已完成绑定与校验，返回值交给所属 Router 的
// responder 统一渲染；返回的 error 应为 *apperr.Error，否则按 500 处理。
type Handler[Req any, Res any] func(ctx context.Context, req *Req) (*Res, error)

// GET registers a typed GET handler.
// GET 注册有类型的 GET 路由；路径相对所属 Router 的前缀，mw 先于处理器执行。
func GET[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...MiddlewareFunc) {
	r.Handle("GET", path, adapt(r, h), mw...)
}

// POST registers a typed POST handler.
// POST 注册有类型的 POST 路由；路径相对所属 Router 的前缀，mw 先于处理器执行。
func POST[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...MiddlewareFunc) {
	r.Handle("POST", path, adapt(r, h), mw...)
}

// PUT registers a typed PUT handler.
// PUT 注册有类型的 PUT 路由；路径相对所属 Router 的前缀，mw 先于处理器执行。
func PUT[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...MiddlewareFunc) {
	r.Handle("PUT", path, adapt(r, h), mw...)
}

// PATCH registers a typed PATCH handler.
// PATCH 注册有类型的 PATCH 路由；路径相对所属 Router 的前缀，mw 先于处理器执行。
func PATCH[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...MiddlewareFunc) {
	r.Handle("PATCH", path, adapt(r, h), mw...)
}

// DELETE registers a typed DELETE handler.
// DELETE 注册有类型的 DELETE 路由；路径相对所属 Router 的前缀，mw 先于处理器执行。
func DELETE[Req any, Res any](r *Router, path string, h Handler[Req, Res], mw ...MiddlewareFunc) {
	r.Handle("DELETE", path, adapt(r, h), mw...)
}

// Any registers a typed handler for an arbitrary HTTP method.
// Any 为任意 HTTP 方法注册有类型处理器；method 原样传给 gin，不做规范化。
func Any[Req any, Res any](r *Router, method, path string, h Handler[Req, Res], mw ...MiddlewareFunc) {
	r.Handle(method, path, adapt(r, h), mw...)
}

// NoContent registers a typed handler without a response body.
// A successful call returns 204.
// NoContent 注册无响应体的有类型处理器，成功时返回 204；失败仍按统一 envelope
// 渲染并带响应体，因此成功与失败两条路径的响应形态并不一致。
func NoContent[Req any](r *Router, method, path string, h func(context.Context, *Req) error, mw ...MiddlewareFunc) {
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
