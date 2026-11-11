package service_test

import (
	"context"
	"testing"

	"github.com/yourorg/demo-service/app/module/order/service"
)

// OrderConfigService 拥有配置样例，供 Config 分组的控制器直接取用。
func TestOrderConfigServiceGetConfig(t *testing.T) {
	s := service.NewOrderConfigService(nil)
	got := s.GetConfig(context.Background())
	if got == nil || got.AuthRequired {
		t.Fatalf("config = %+v, want AuthRequired=false", got)
	}
}
