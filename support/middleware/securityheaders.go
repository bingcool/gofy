package middleware

import (
	"strconv"
	"strings"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
)

// SecurityHeaders sets hardening headers on every response, including error
// responses produced later in the chain.
func SecurityHeaders(cfg support.SecurityHeadersConfig) (gin.HandlerFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	defaults := support.DefaultSecurityHeadersConfig()
	pick := func(value, fallback string) string {
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return value
	}

	headers := make(map[string]string, len(cfg.Custom)+6)
	if v := pick(cfg.ContentTypeOptions, defaults.ContentTypeOptions); v != "" {
		headers["X-Content-Type-Options"] = v
	}
	if v := pick(cfg.FrameOptions, defaults.FrameOptions); v != "" {
		headers["X-Frame-Options"] = v
	}
	if v := pick(cfg.ReferrerPolicy, defaults.ReferrerPolicy); v != "" {
		headers["Referrer-Policy"] = v
	}
	if cfg.HSTS > 0 {
		value := "max-age=" + strconv.Itoa(int(cfg.HSTS.Seconds()))
		if cfg.HSTSIncludeSubdomains {
			value += "; includeSubDomains"
		}
		if cfg.HSTSPreload {
			value += "; preload"
		}
		headers["Strict-Transport-Security"] = value
	}
	if cfg.ContentSecurityPolicy != "" {
		headers["Content-Security-Policy"] = cfg.ContentSecurityPolicy
	}
	if cfg.PermissionsPolicy != "" {
		headers["Permissions-Policy"] = cfg.PermissionsPolicy
	}
	for name, value := range cfg.Custom {
		headers[name] = value
	}

	return func(c *gin.Context) {
		out := c.Writer.Header()
		for name, value := range headers {
			out.Set(name, value)
		}
		c.Next()
	}, nil
}
