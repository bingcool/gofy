package middleware

import (
	"github.com/bingcool/gofy/support"
	gofymiddleware "github.com/bingcool/gofy/support/middleware"
	"go.uber.org/zap"
)

// Deps 是构造应用层中间件需要的依赖，由 App 启动/热更新时传入的
// gofy.MiddlewareRegistration 填充。
type Deps struct {
	// Logger 供 Audit 记业务流水；nil 时丢弃日志。
	Logger *zap.Logger
	// JWT 控制契约里显式声明的 "auth" 中间件的 Bearer Token 校验。
	JWT JWTConfig
	// Responder 用于认证失败时渲染统一错误响应。
	Responder support.Responder
}

// Register 把应用的中间件别名注册进 App 在本次装配中创建的共享注册表。
// gofy 在启动和每次 Reload 时都会用新的 Runtime、Responder 和空注册表调用
// 一次，因此 Nacos 更新 jwt 配置后会得到新的 auth 实例，新旧注册表互不共享。
//
// 业务模块只声明契约里的逻辑名（分组 "audit"、CreateOrder 的 "order-audit"、
// 认证 "auth"），不再自己创建或注入注册表。
func Register(reg *gofymiddleware.Registry, deps Deps) error {
	auth, err := Auth(deps.JWT, deps.Responder)
	if err != nil {
		return err
	}
	entries := []struct {
		alias string
		mw    support.MiddlewareFunc
	}{
		{NameAuth, auth},
		{NameAudit, Audit(deps.Logger, "group")},
		{NameOrderAudit, Audit(deps.Logger, "route")},
	}
	for _, e := range entries {
		if err := reg.Register(e.alias, e.mw); err != nil {
			return err
		}
	}
	return nil
}
