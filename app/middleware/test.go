package middleware

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

func TestEcho() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		fmt.Println("test middleware")
		panic("panic panic ")
	}
}
