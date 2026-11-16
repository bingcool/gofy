package service

import (
	"context"

	"go.uber.org/zap"

	"github.com/yourorg/demo-service/app/config"
)

// Config 是订单模块配置的领域视图。service 不 import HTTP 契约，契约响应
// （orderresponse.GetConfigResponse）的映射留在 controller。配置只在本模块内
// 使用，没有跨模块消费方，因此不发布到 innerapi。
type Config struct {
	AuthRequired bool
}

// OrderConfigService 拥有订单模块的配置读取用例。装配根 app/container 构造它，
// 模块根把它注入 OrderConfigController；消费方直接依赖具体类型，不走 innerapi
// ——那是跨模块的缝。
type OrderConfigService struct {
	cfg    config.OrderConfig
	logger *zap.Logger
}

// NewOrderConfigService builds an OrderConfigService from the order section of
// the application config. A nil logger disables logs.
// NewOrderConfigService 用应用配置的 order 段构造配置 service，logger 为 nil 时回退
// zap.NewNop()，让测试与脚本不必引入日志依赖。
//
// The parameter is the section this module owns, not *config.Config: accepting
// the whole file would let this package read db, kafka and nacos as well, and
// no reviewer would see it in the call site.
// 参数是本模块自己那一段，而不是 *config.Config：一旦收下整份文件，这个包就能顺手读到
// db、kafka 与 nacos，而调用点上看不出这件事。
func NewOrderConfigService(cfg config.OrderConfig, logger *zap.Logger) *OrderConfigService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrderConfigService{
		cfg:    cfg,
		logger: logger.With(zap.String("component", "order.config.service")),
	}
}

// GetConfig returns the order module's configuration. It is the copy captured
// when this generation was assembled, so a reload hands the next generation the
// new value while in-flight requests keep the old one.
// GetConfig 返回订单模块配置。它是本代装配时捕获的副本，因此热更新后新代读到新值，
// 在途请求继续用旧代的值。
func (s *OrderConfigService) GetConfig(ctx context.Context) *Config {
	s.logger.Debug("get order config", zap.Bool("auth_required", s.cfg.AuthRequired))
	return &Config{AuthRequired: s.cfg.AuthRequired}
}
