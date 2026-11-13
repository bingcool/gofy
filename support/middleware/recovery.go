package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// NameRecovery is the middleware alias Recovery uses on contract routes.
// NameRecovery 是 Recovery 在契约路由里使用的中间件别名。
const NameRecovery = "recovery"

// Recovery turns a panic into a 500 response; the panic value and stack trace go
// to the log only and are never returned to the client.
//
// It must sit outside the business middleware to catch panics from the handler
// and the middleware after it; usually it goes inside AccessLog, so a panicking
// request is still recorded as 500 in the access log.
//
// Two details:
//   - http.ErrAbortHandler is re-panicked as is: it signals a deliberate abort
//     (for example the client went away) and net/http depends on that semantic,
//     so swallowing it would change connection handling;
//   - when the response is already written it only aborts and skips the error
//     envelope, so a second write cannot corrupt the response.
//
// Recovery 把 panic 转成 500 响应，panic 值与堆栈只进日志、不返回给客户端。
//
// 必须装在业务中间件外层，才能兜住 handler 及其后中间件的 panic；通常再装在
// AccessLog 内层，这样 panic 请求在访问日志里仍记为 500。
//
// 两个细节：
//   - http.ErrAbortHandler 原样重新 panic：它表示调用方主动中断（例如客户端
//     断开），net/http 依赖这个语义，吞掉会改变连接处理方式；
//   - 响应已经写出时只 Abort、不再写错误包络，避免二次写头把响应写坏。
func Recovery(logger *zap.Logger, responder support.Responder) gin.HandlerFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	if responder == nil {
		responder = support.NewResponder(support.ResponderConfig{})
	}
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			// The log carries the panic value, method, path and stack trace, plus
			// req_id / trace_id when available, so a panic can be correlated with
			// the access log and the trace.
			// 日志带上 panic 值、方法、路径和堆栈；可用时再补 req_id /
			// trace_id，便于把 panic 与访问日志、链路追踪对上。
			fields := []zap.Field{
				zap.Any("panic", rec),
				zap.String("method", c.Request.Method),
				zap.String("path", c.Request.URL.Path),
				zap.ByteString("stacktrace", debug.Stack()),
			}
			if id := c.GetString(support.RequestIDKey); id != "" {
				fields = append(fields, zap.String("req_id", id))
			}
			if sc := trace.SpanContextFromContext(c.Request.Context()); sc.IsValid() {
				fields = append(fields,
					zap.String("trace_id", sc.TraceID().String()),
					zap.String("span_id", sc.SpanID().String()),
				)
			}
			logger.Error("panic recovered", fields...)

			if c.Writer.Written() {
				c.Abort()
				return
			}
			responder.Error(c, apperr.Internal("internal server error"))
			c.Abort()
		}()
		c.Next()
	}
}
