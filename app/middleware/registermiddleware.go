package middleware

import (
	"github.com/gin-gonic/gin"
)

func SetGlobalMiddleware(router *gin.Engine) {
	SetGlobalRequestId(router)
	SetGlobalRecovery(router)
	SetGlobalCors(router)
}
