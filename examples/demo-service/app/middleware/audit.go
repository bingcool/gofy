package middleware

import (
	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Audit 记录业务操作流水，用于实现契约里声明的中间件逻辑名：订单分组的
// "audit" 与 CreateOrder 路由的 "order-audit"。scope 区分名字来自分组还是路由。
// 与框架的 AccessLog 不同，这里只关心业务维度，因此在 handler 之后记录结果。
func Audit(logger *zap.Logger, scope string) support.Middleware {
	if logger == nil {
		logger = zap.NewNop()
	}
	return func(c *gin.Context) {
		c.Next()
		logger.Info("order audit",
			zap.String("scope", scope),
			zap.String("method", c.Request.Method),
			zap.String("route", c.FullPath()),
			zap.Int("status", c.Writer.Status()),
			zap.String("req_id", c.GetString(support.RequestIDKey)),
		)
	}
}
