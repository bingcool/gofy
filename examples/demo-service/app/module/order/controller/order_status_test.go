package controller

import (
	"testing"

	"github.com/yourorg/demo-service/app/module/order/service"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// TestStatusMappingConsistency 保证 service 的领域状态常量与契约枚举一一对应，
// 且经 controller 的映射往返一致。契约新增状态而 service 漏加常量时，这里会
// 失败，防止两侧漂移。
func TestStatusMappingConsistency(t *testing.T) {
	pairs := map[orderenum.OrderStatus]string{
		orderenum.OrderStatusPending:   service.StatusPending,
		orderenum.OrderStatusPaid:      service.StatusPaid,
		orderenum.OrderStatusCancelled: service.StatusCancelled,
	}
	if len(pairs) != len(orderenum.AllOrderStatus) {
		t.Fatalf("domain constants = %d, contract statuses = %d; keep them in sync",
			len(pairs), len(orderenum.AllOrderStatus))
	}
	for _, status := range orderenum.AllOrderStatus {
		domain, ok := pairs[status]
		if !ok {
			t.Fatalf("no service domain constant for contract status %q", status)
		}
		if domain != string(status) {
			t.Fatalf("status %q maps to domain %q, want identical wire value", status, domain)
		}
		// controller 用 orderenum.OrderStatus(status) 映射，往返必须回到原值。
		if got := orderenum.OrderStatus(domain); got != status {
			t.Fatalf("round trip %q -> %q -> %q", status, domain, got)
		}
	}
}
