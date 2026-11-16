package service_test

import (
	"context"
	"testing"

	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/app/module/order/service"
)

// OrderConfigService 返回构造参数里那一段配置，供 Config 分组的控制器直接取用。
func TestOrderConfigServiceGetConfig(t *testing.T) {
	s := service.NewOrderConfigService(config.OrderConfig{}, nil)
	got := s.GetConfig(context.Background())
	if got == nil || got.AuthRequired {
		t.Fatalf("config = %+v, want AuthRequired=false", got)
	}
}

// 值来自构造参数，而不是写死的常量：容器把 config.yaml 的 order 段原样传进来，
// 因此这里换成 true 就该读到 true。
func TestOrderConfigServiceReturnsInjectedConfig(t *testing.T) {
	s := service.NewOrderConfigService(config.OrderConfig{AuthRequired: true}, nil)
	if got := s.GetConfig(context.Background()); got == nil || !got.AuthRequired {
		t.Fatalf("config = %+v, want AuthRequired=true", got)
	}
}
