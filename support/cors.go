package support

import (
	"errors"
	"slices"
	"time"
)

// CORSConfig describes the cross-origin policy. When both AllowOrigins and
// AllowOriginPatterns are empty, CORS is disabled entirely.
// CORSConfig 描述跨域策略；AllowOrigins 与 AllowOriginPatterns 都为空时
// 完全不启用 CORS，此时不会写出任何 CORS 响应头。
type CORSConfig struct {
	// AllowOrigins lists allowed origins. Full origins (https://example.com)
	// and bare domains (example.com, expanded to http/https) are accepted; "*"
	// allows any origin.
	// AllowOrigins 是允许的来源。支持完整来源（https://example.com）或裸域名
	// （example.com，会自动展开为 http/https 两种）；"*" 表示任意来源。
	AllowOrigins []string
	// AllowOriginPatterns supports subdomain wildcards such as
	// "*.example.com" or "example.com", matching that domain and any subdomain
	// over http/https. Invalid patterns fail when the middleware is built.
	// AllowOriginPatterns 支持子域通配，例如 "*.example.com" 或 "example.com"，
	// 命中 http/https 的该域名及其任意子域。非法模式在构造中间件时报错。
	AllowOriginPatterns []string
	// AllowPaths is the path allowlist for cross-origin requests, with shell
	// wildcards (*, ?, [seq]); empty means ["*"]. It matches RequestURI,
	// including the query string.
	// AllowPaths 是跨域请求的路径白名单，支持 shell 通配符（*、?、[seq]）；
	// 为空时等价于 ["*"]。匹配对象是含 query 的 RequestURI。
	AllowPaths []string
	// AllowMethods is the value of Access-Control-Allow-Methods; empty defaults
	// to GET, POST, PUT, PATCH, DELETE, OPTIONS.
	// AllowMethods 是 Access-Control-Allow-Methods 的取值；为空时默认
	// GET, POST, PUT, PATCH, DELETE, OPTIONS。
	AllowMethods []string
	// AllowHeaders is the value of Access-Control-Allow-Headers; empty defaults
	// to "*".
	// AllowHeaders 是 Access-Control-Allow-Headers 的取值；为空时默认 "*"。
	AllowHeaders []string
	// ExposeHeaders is the value of Access-Control-Expose-Headers; the header is
	// omitted when empty.
	// ExposeHeaders 是 Access-Control-Expose-Headers 的取值；为空时不写该头。
	ExposeHeaders []string
	// AllowCredentials writes Access-Control-Allow-Credentials: true. It cannot
	// be combined with a "*" origin.
	// AllowCredentials 为 true 时写 Access-Control-Allow-Credentials: true，
	// 且不允许与 "*" 来源组合。
	AllowCredentials bool
	// MaxAge is the preflight cache lifetime; 0 uses the 24h default and a
	// negative value is invalid.
	// MaxAge 是预检结果缓存时间；0 使用默认 24h，负数非法。
	MaxAge time.Duration
}

// Validate rejects unsafe or meaningless combinations: a negative MaxAge, or
// credentialed mode combined with a "*" origin.
// Validate 拒绝不安全或无意义的组合：负的 MaxAge、凭证模式 + "*" 来源。它不做
// 通配符语法检查，非法模式要到构造中间件时才报错。
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
