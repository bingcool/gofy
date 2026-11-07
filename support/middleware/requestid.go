// Package middleware contains the HTTP middleware installed by the gofy
// application. Every middleware receives its dependencies explicitly; there
// is no package-level logger or configuration.
package middleware

import (
	"regexp"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// RequestID reads a request id from header, generates a UUID when it is
// missing or invalid, and stores it in the gin context.
func RequestID(header string) gin.HandlerFunc {
	if header == "" {
		header = support.DefaultRequestIDHeader
	}
	return func(c *gin.Context) {
		id := c.GetHeader(header)
		if !requestIDPattern.MatchString(id) {
			id = uuid.NewString()
		}
		c.Set(support.RequestIDKey, id)
		c.Request.Header.Set(header, id)
		c.Writer.Header().Set(header, id)
		c.Next()
	}
}
