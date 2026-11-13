package middleware

import (
	"fmt"

	"github.com/bingcool/gofy/support"
	"github.com/bingcool/gofy/support/jwtauth"
	gofymiddleware "github.com/bingcool/gofy/support/middleware"
	"github.com/gin-gonic/gin"
)

// NameAuth 是认证中间件的显式别名。契约在分组或路由上声明 "auth"
// （AuthRequired 本身只是 OpenAPI 标记），adapter 从 App 注册表取实例，
// 模块不需要再传任何认证选项。
const NameAuth = "auth"

// NameOptionalAuth 是“可选认证”中间件的逻辑名：有凭证则校验、无凭证放行。
// demo 目前没有路由声明 AuthOptional，也没有注册该别名；容器的启动校验会用
// 这个名字判定 Optional 路由，新增 Optional 路由前需要先注册实现，
// 不能直接复用 NameAuth（Required 与 Optional 的放行语义不同）。
const NameOptionalAuth = "optional-auth"

// JWTConfig 描述契约里显式声明的 "auth" 中间件使用的 Bearer Token 校验参数，
// 映射到 support/jwtauth.Config。
type JWTConfig struct {
	// Secret 是 HMAC 密钥；为空时示例放行所有请求，方便本地直接调用。
	Secret string `yaml:"secret"`
	// Algorithms 是允许的 alg，空表示只接受 HS256。
	Algorithms []string `yaml:"algorithms"`
	// Issuer 非空时要求 token 的 iss 与之一致。
	Issuer string `yaml:"issuer"`
	// Audience 非空时要求 token 的 aud 包含该值。
	Audience string `yaml:"audience"`
	// RequireExpiry 为 true 时拒绝没有 exp 的 token。
	RequireExpiry bool `yaml:"require_expiry"`
}

// Auth 用框架的 JWTAuth（support/middleware/jwt.go）实现契约里的显式
// "auth" 别名。secret 为空时返回放行中间件：示例默认不要求凭证，但别名
// 始终指向一个非 nil 实例，注册表不会因为缺省而漏掉认证。
func Auth(cfg JWTConfig, responder support.Responder) (support.Middleware, error) {
	if cfg.Secret == "" {
		return func(c *gin.Context) { c.Next() }, nil
	}
	parser, err := jwtauth.NewParser(jwtauth.Config{
		Secret:        []byte(cfg.Secret),
		Algorithms:    cfg.Algorithms,
		Issuer:        cfg.Issuer,
		Audience:      cfg.Audience,
		RequireExpiry: cfg.RequireExpiry,
	})
	if err != nil {
		return nil, fmt.Errorf("middleware: jwt: %w", err)
	}
	return gofymiddleware.JWTAuth(support.JWTAuthConfig{Parser: parser}, responder)
}
