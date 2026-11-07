package support

import (
	"errors"
	"strings"

	"github.com/bingcool/gofy/support/jwtauth"
	"github.com/gin-gonic/gin"
)

// JWTClaimsKey 是 gin.Context 中存放已校验 *jwtauth.Claims 的 key。
const JWTClaimsKey = "gofy.jwt_claims"

// JWTAuthConfig 为模块路由启用 Bearer Token 校验。
//
// 注册在应用根路由上的模块路由需要合法 token；健康检查和未匹配路径不受影响，
// 因此探针仍可用，未知 URL 仍返回 404。
type JWTAuthConfig struct {
	// Parser 执行 token 校验，必填。
	Parser *jwtauth.Parser
	// Header 是携带 token 的请求头，默认 "Authorization"。
	Header string
	// Scheme 是期望的前缀，默认 "Bearer"；前缀之后为空 token 会被拒绝。
	Scheme string
	// Optional 为 true 时，没有合法 token 的请求也继续执行。
	// 需要认证的路由此时必须自行检查 JWTClaims 并返回 401。
	Optional bool
	// SkipPaths 列出跳过校验的精确 URL 路径。
	SkipPaths []string
	// ContextKey 覆盖 claims 的存放位置，默认 JWTClaimsKey。
	ContextKey string
}

// Validate 返回中间件无法处理的配置错误。
func (c JWTAuthConfig) Validate() error {
	if c.Parser == nil {
		return errors.New("support: jwt auth parser is required")
	}
	if strings.ContainsAny(c.Header, "\r\n") || strings.ContainsAny(c.Scheme, "\r\n") {
		return errors.New("support: jwt auth header and scheme must not contain newlines")
	}
	return nil
}

// JWTClaims 返回 JWT 中间件存入的 claims（如果存在）。
func JWTClaims(c *gin.Context) (*jwtauth.Claims, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.Get(JWTClaimsKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*jwtauth.Claims)
	return claims, ok
}
