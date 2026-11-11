package service_test

import (
	"context"
	"testing"

	"github.com/yourorg/demo-service/app/module/order/service"
)

// OrderConfigService 拥有配置样例；OrderService 经 Dependencies 注入它并委托，
// 验证模块内 service→service 组合（具体类型，不走 innerapi / 注册表）。
func TestOrderConfigServiceGetConfig(t *testing.T) {
	s := service.NewOrderConfigService(nil)
	got := s.GetConfig(context.Background())
	if got == nil || got.AuthRequired {
		t.Fatalf("config = %+v, want AuthRequired=false", got)
	}
}

func TestOrderServiceGetConfigDelegates(t *testing.T) {
	svc := service.NewOrderService(service.Dependencies{
		Config: service.NewOrderConfigService(nil),
	})
	got := svc.GetConfig(context.Background())
	if got == nil || got.AuthRequired {
		t.Fatalf("config = %+v, want delegated sample", got)
	}
}
