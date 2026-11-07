package support

import (
	"errors"
	"slices"
	"time"
)

// CORSConfig 描述跨域策略；AllowOrigins 与 AllowOriginPatterns 都为空时
// 完全不启用 CORS。
type CORSConfig struct {
	// AllowOrigins 是允许的来源。支持完整来源（https://example.com）或裸域名
	// （example.com，会自动展开为 http/https 两种）；"*" 表示任意来源。
	AllowOrigins []string
	// AllowOriginPatterns 支持子域通配，例如 "*.example.com" 或 "example.com"，
	// 命中 http/https 的该域名及其任意子域。非法模式在构造中间件时报错。
	AllowOriginPatterns []string
	// AllowPaths 是跨域请求的路径白名单，支持 shell 通配符（*、?、[seq]）；
	// 为空时等价于 ["*"]。匹配对象是含 query 的 RequestURI。
	AllowPaths []string
	// AllowMethods 是 Access-Control-Allow-Methods 的取值；为空时默认
	// GET, POST, PUT, PATCH, DELETE, OPTIONS。
	AllowMethods []string
	// AllowHeaders 是 Access-Control-Allow-Headers 的取值；为空时默认 "*"。
	AllowHeaders []string
	// ExposeHeaders 是 Access-Control-Expose-Headers 的取值；为空时不写该头。
	ExposeHeaders []string
	// AllowCredentials 为 true 时写 Access-Control-Allow-Credentials: true，
	// 且不允许与 "*" 来源组合。
	AllowCredentials bool
	// MaxAge 是预检结果缓存时间；0 使用默认 24h，负数非法。
	MaxAge time.Duration
}

// Validate 拒绝不安全或无意义的组合：负的 MaxAge、凭证模式 + "*" 来源。
func (c CORSConfig) Validate() error {
	if c.MaxAge < 0 {
		return errors.New("support: cors max age must not be negative")
	}
	if c.AllowCredentials {
		if slices.Contains(c.AllowOrigins, "*") {
			return errors.New(`support: cors cannot combine AllowCredentials with wildcard origin "*"`)
		}
	}
	return nil
}
