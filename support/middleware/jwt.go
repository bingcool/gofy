package middleware

import (
	"strings"

	"github.com/bingcool/gofy/apperr"
	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/bingcool/gofy/support/jwtauth"
	"github.com/gin-gonic/gin"
)

// NameJWTAuth 是 JWTAuth 在契约路由里使用的中间件别名。
const NameJWTAuth = "jwt-auth"

// JWTAuth 校验 bearer token，并把验证通过的 claims 放进 gin context。
//
// 流程：命中 SkipPaths 时直接放行；否则按配置的请求头与 scheme（默认
// Authorization: Bearer）取出 token，交给 cfg.Parser 校验。缺少 token 或
// 校验失败时：Optional 为 true 则匿名放行，否则写 WWW-Authenticate 头并返回
// 401 标准错误包络——响应里只有 "unauthorized"，不暴露具体解析错误。
//
// 校验成功后：
//   - claims 以 cfg.ContextKey（默认 support.JWTClaimsKey）存入 gin context，
//     handler 用 Claims 读取；
//   - 同一份身份经 frameworkcontext.WithUser 写入 request context，本中间件
//     解析出的身份优先于透传头，下游调用携带同一个用户与租户。
//
// 配置非法（cfg.Validate 失败）时返回错误，让装配根在启动期就失败。
func JWTAuth(cfg support.JWTAuthConfig, responder support.Responder) (gin.HandlerFunc, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if responder == nil {
		responder = support.NewResponder(support.ResponderConfig{})
	}
	headerName := cfg.Header
	if headerName == "" {
		headerName = "Authorization"
	}
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "Bearer"
	}
	contextKey := cfg.ContextKey
	if contextKey == "" {
		contextKey = support.JWTClaimsKey
	}
	skip := make(map[string]struct{}, len(cfg.SkipPaths))
	for _, p := range cfg.SkipPaths {
		skip[p] = struct{}{}
	}

	return func(c *gin.Context) {
		if _, ok := skip[c.Request.URL.Path]; ok {
			c.Next()
			return
		}
		token, found := bearerToken(c.GetHeader(headerName), scheme)
		if found {
			claims, err := cfg.Parser.Parse(token)
			if err == nil {
				c.Set(contextKey, claims)
				// 本地认证身份优先于透传头；WithUser 会把身份回写到 context，
				// 保证下游调用携带同一个用户和租户。
				c.Request = c.Request.WithContext(
					frameworkcontext.WithUser(c.Request.Context(), frameworkcontext.UserFromClaims(claims)),
				)
				c.Next()
				return
			}
		}
		if cfg.Optional {
			c.Next()
			return
		}
		c.Header("WWW-Authenticate", `Bearer realm="api"`)
		responder.Error(c, apperr.Unauthorized("unauthorized"))
		c.Abort()
	}, nil
}

// Claims 按 context key 取回 JWTAuth 写入的 claims。c 为 nil、key 为空时用
// 默认 key、key 不存在或类型不符都返回 (nil, false)，调用方据此判断是否已认证。
func Claims(c *gin.Context, contextKey string) (*jwtauth.Claims, bool) {
	if c == nil {
		return nil, false
	}
	if contextKey == "" {
		contextKey = support.JWTClaimsKey
	}
	v, ok := c.Get(contextKey)
	if !ok {
		return nil, false
	}
	claims, ok := v.(*jwtauth.Claims)
	return claims, ok
}

// bearerToken 从请求头里切出 token：scheme 为空时整个头就是 token；否则要求
// 前缀大小写不敏感匹配、后面紧跟空格或 Tab，再去掉首尾空白。空 token 视为
// 不存在，避免把 "Bearer" 这样的残缺头当成凭证去解析。
func bearerToken(header, scheme string) (string, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false
	}
	if scheme == "" {
		return header, true
	}
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	rest := header[len(scheme):]
	if rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	token := strings.TrimSpace(rest)
	return token, token != ""
}
