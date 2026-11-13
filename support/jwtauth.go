package support

import (
	"errors"
	"strings"

	"github.com/bingcool/gofy/support/jwtauth"
	"github.com/gin-gonic/gin"
)

// JWTClaimsKey is the gin.Context key holding the validated *jwtauth.Claims.
// JWTClaimsKey 是 gin.Context 中存放已校验 *jwtauth.Claims 的 key；用自定义
// ContextKey 时该常量不再适用，应改用 JWTClaims 或自行按配置读取。
const JWTClaimsKey = "gofy.jwt_claims"

// JWTAuthConfig enables Bearer token validation for module routes.
//
// Routes of modules registered on the application root require a valid token;
// health checks and unmatched paths are unaffected, so probes keep working and
// unknown URLs still return 404.
// JWTAuthConfig 为模块路由启用 Bearer Token 校验。
//
// 注册在应用根路由上的模块路由需要合法 token；健康检查和未匹配路径不受影响，
// 因此探针仍可用，未知 URL 仍返回 404。
type JWTAuthConfig struct {
	// Parser validates tokens. Required.
	// Parser 执行 token 校验，必填。
	Parser *jwtauth.Parser
	// Header is the request header carrying the token. Defaults to
	// "Authorization".
	// Header 是携带 token 的请求头，默认 "Authorization"。
	Header string
	// Scheme is the expected prefix. Defaults to "Bearer"; an empty token after
	// the prefix is rejected.
	// Scheme 是期望的前缀，默认 "Bearer"；前缀之后为空 token 会被拒绝。
	Scheme string
	// Optional lets requests without a valid token through. Routes that need
	// authentication must then check JWTClaims themselves and return 401.
	// Optional 为 true 时，没有合法 token 的请求也继续执行。
	// 需要认证的路由此时必须自行检查 JWTClaims 并返回 401。
	Optional bool
	// SkipPaths lists exact URL paths that bypass validation.
	// SkipPaths 列出跳过校验的精确 URL 路径，不做前缀匹配。
	SkipPaths []string
	// ContextKey overrides where claims are stored. Defaults to JWTClaimsKey.
	// ContextKey 覆盖 claims 的存放位置，默认 JWTClaimsKey。
	ContextKey string
}

// Validate returns configuration errors the middleware cannot work with.
// Validate 返回中间件无法处理的配置错误；应在启动期调用，避免运行期才暴露。
func (c JWTAuthConfig) Validate() error {
	if c.Parser == nil {
		return errors.New("support: jwt auth parser is required")
	}
	if strings.ContainsAny(c.Header, "\r\n") || strings.ContainsAny(c.Scheme, "\r\n") {
		return errors.New("support: jwt auth header and scheme must not contain newlines")
	}
	return nil
}

// JWTClaims returns the claims stored by the JWT middleware, if any.
// JWTClaims 返回 JWT 中间件存入的 claims；路由未启用中间件或未通过校验时
// ok 为 false，调用方必须处理这种情况而不是直接解引用。
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
