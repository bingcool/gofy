package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
)

// CORS builds a CORS middleware. An empty AllowOrigins returns (nil, nil),
// meaning CORS is disabled.
func CORS(cfg support.CORSConfig) (gin.HandlerFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.AllowOrigins) == 0 {
		return nil, nil
	}
	methods := cfg.AllowMethods
	if len(methods) == 0 {
		methods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	}
	headers := cfg.AllowHeaders
	if len(headers) == 0 {
		headers = []string{"Origin", "Content-Type", "Authorization", "X-Request-ID"}
	}
	maxAge := cfg.MaxAge
	if maxAge == 0 {
		maxAge = 12 * time.Hour
	}

	allowAll := false
	origins := make(map[string]struct{}, len(cfg.AllowOrigins))
	for _, o := range cfg.AllowOrigins {
		if o == "*" {
			allowAll = true
		}
		origins[o] = struct{}{}
	}
	allowMethods := strings.Join(methods, ", ")
	allowHeaders := strings.Join(headers, ", ")
	exposeHeaders := strings.Join(cfg.ExposeHeaders, ", ")

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		allowed := allowAll
		if _, ok := origins[origin]; ok {
			allowed = true
		}
		if !allowed {
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusNoContent)
				return
			}
			c.Next()
			return
		}

		h := c.Writer.Header()
		h.Add("Vary", "Origin")
		if allowAll && !cfg.AllowCredentials {
			h.Set("Access-Control-Allow-Origin", "*")
		} else {
			h.Set("Access-Control-Allow-Origin", origin)
		}
		if cfg.AllowCredentials {
			h.Set("Access-Control-Allow-Credentials", "true")
		}
		if exposeHeaders != "" {
			h.Set("Access-Control-Expose-Headers", exposeHeaders)
		}

		if c.Request.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", allowMethods)
			if requested := c.GetHeader("Access-Control-Request-Headers"); requested != "" {
				h.Set("Access-Control-Allow-Headers", requested)
			} else {
				h.Set("Access-Control-Allow-Headers", allowHeaders)
			}
			h.Set("Access-Control-Max-Age", strconv.Itoa(int(maxAge.Seconds())))
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}, nil
}
