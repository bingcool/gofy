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

// OrderConfigService 拥有订单模块的配置读取用例。它是 OrderService 的同包协作
// service：装配根 app/container 先构造它，再通过 Dependencies 注入 OrderService。
// 同包协作用具体类型，不走 innerapi——那是跨模块的缝。
type OrderConfigService struct {
	logger *zap.Logger
}

// NewOrderConfigService builds an OrderConfigService. A nil logger disables logs.
func NewOrderConfigService(logger *zap.Logger) *OrderConfigService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrderConfigService{
		logger: logger.With(zap.String("component", "order.config.service")),
	}
}

// GetConfig 返回订单模块配置。当前是固定样例（不读模块配置或 auth token），
// 不会失败；后续若接配置中心，再在这里扩展并改为返回 error。
func (s *OrderConfigService) GetConfig(ctx context.Context) *Config {
	s.logger.Debug("get order config")
	return &Config{AuthRequired: false}
}
