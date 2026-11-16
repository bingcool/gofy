package service_test

import (
	"context"
	"testing"

	"github.com/yourorg/demo-service/app/module/order/service"
)

// OrderConfigService 返回装配根注入的设置，供 Config 分组的控制器直接取用。
func TestOrderConfigServiceGetConfig(t *testing.T) {
	s := service.NewOrderConfigService(service.Settings{}, nil)
	got := s.GetConfig(context.Background())
	if got == nil || got.AuthRequired {
		t.Fatalf("config = %+v, want AuthRequired=false", got)
	}
}

// 值来自装配期注入的 Settings，而不是写死的常量：容器把 config.yaml 的 order 段
// 翻译成它，因此这里换成 true 就该读到 true。
func TestOrderConfigServiceReturnsInjectedSettings(t *testing.T) {
	s := service.NewOrderConfigService(service.Settings{AuthRequired: true}, nil)
	if got := s.GetConfig(context.Background()); got == nil || !got.AuthRequired {
		t.Fatalf("config = %+v, want AuthRequired=true", got)
	}
}
