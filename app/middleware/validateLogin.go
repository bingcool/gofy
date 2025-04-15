package middleware

import (
	"fmt"

	"github.com/gin-gonic/gin"
)

func ValidateLogin() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		fmt.Println("validate login middleware")
	}
}
