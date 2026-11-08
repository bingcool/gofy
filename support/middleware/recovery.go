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

// NameRecovery 是 Recovery 在契约路由里使用的中间件别名。
const NameRecovery = "recovery"

// Recovery turns panics into a 500 response without leaking the panic value or
// stack trace to the client. It must be installed outside business middleware.
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
