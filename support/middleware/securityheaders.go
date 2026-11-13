package middleware

import (
	"strconv"
	"strings"

	"github.com/bingcool/gofy/support"
	"github.com/gin-gonic/gin"
)

// NameSecurityHeaders is the middleware alias SecurityHeaders uses on contract
// routes.
// NameSecurityHeaders 是 SecurityHeaders 在契约路由里使用的中间件别名。
const NameSecurityHeaders = "security-headers"

// SecurityHeaders writes the hardening headers on every response, including error
// responses produced further down the chain.
//
// Construction merges configuration and defaults into one static header table,
// so the request path writes once and has no branches: empty string fields fall
// back to support.DefaultSecurityHeadersConfig; HSTS 0 means no
// Strict-Transport-Security header; Custom is merged last and can override a
// built-in header of the same name.
//
// The headers are written before c.Next(), so they are still present when a
// handler returns 4xx/5xx.
// SecurityHeaders 给每个响应写安全加固头，包括后续链路产生的错误响应。
//
// 构造期把配置与默认值合并成一张静态头表，请求期只做一次写入、没有分支：
// 空字符串字段回退到 support.DefaultSecurityHeadersConfig；HSTS 为 0 表示
// 不写 Strict-Transport-Security；Custom 最后合并，可以覆盖同名内建头。
//
// 头在 c.Next() 之前写入，因此 handler 返回 4xx/5xx 时加固头依然存在。
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
	// HSTS only applies when a duration is configured explicitly: max-age is
	// mandatory while includeSubDomains and preload are appended on demand, so a
	// pure HTTP environment never gets a long-lived HSTS policy by accident.
	// HSTS 只在显式配置了时长时生效：max-age 必填，includeSubDomains 与
	// preload 按配置追加，避免给纯 HTTP 环境误加长期 HSTS。
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
