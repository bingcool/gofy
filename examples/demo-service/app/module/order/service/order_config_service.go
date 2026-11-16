package service

import (
	"context"

	"go.uber.org/zap"
)

// Config 是订单模块配置的领域视图。service 不 import HTTP 契约，契约响应
// （orderresponse.GetConfigResponse）的映射留在 controller。配置只在本模块内
// 使用，没有跨模块消费方，因此不发布到 innerapi。
type Config struct {
	AuthRequired bool
}

// Settings 是订单配置在 service 层的值对象。
//
// 它只包含 service 真正使用的字段：装配根从 config.yaml 的 order 段翻译出它，
// service 不 import app/config、也不读文件。这样新增一个无关配置段不会牵动订单
// 模块，测试也可以直接构造值，不必伪造整份应用配置。
type Settings struct {
	AuthRequired bool
}

// OrderConfigService 拥有订单模块的配置读取用例。装配根 app/container 构造它，
// 模块根把它注入 OrderConfigController；消费方直接依赖具体类型，不走 innerapi
// ——那是跨模块的缝。
type OrderConfigService struct {
	settings Settings
	logger   *zap.Logger
}

// NewOrderConfigService builds an OrderConfigService from the injected settings.
// A nil logger disables logs.
// NewOrderConfigService 用装配根注入的设置构造配置 service，logger 为 nil 时回退
// zap.NewNop()，让测试与脚本不必引入日志依赖。
func NewOrderConfigService(settings Settings, logger *zap.Logger) *OrderConfigService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrderConfigService{
		settings: settings,
		logger:   logger.With(zap.String("component", "order.config.service")),
	}
}

// GetConfig 返回订单模块配置。值来自装配期注入的 Settings——它是本次装配代捕获的
// 配置，热更新后新代读到新值，在途请求继续用旧代的值。
func (s *OrderConfigService) GetConfig(ctx context.Context) *Config {
	s.logger.Debug("get order config", zap.Bool("auth_required", s.settings.AuthRequired))
	return &Config{AuthRequired: s.settings.AuthRequired}
}
