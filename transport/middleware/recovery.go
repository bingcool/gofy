package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/transport"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Recovery turns panics into a 500 response without leaking the panic value or
// stack trace to the client. It must be installed outside business middleware.
func Recovery(logger *zap.Logger, responder transport.Responder) gin.HandlerFunc {
	if logger == nil {
		logger = zap.NewNop()
	}
	if responder == nil {
		responder = transport.NewResponder(transport.ResponderConfig{})
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
			if id := c.GetString(transport.RequestIDKey); id != "" {
				fields = append(fields, zap.String("req_id", id))
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
