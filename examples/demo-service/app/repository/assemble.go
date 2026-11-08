package apprepository

import (
	"fmt"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support/registry"
	orderrepo "github.com/yourorg/demo-service/app/module/order/repository"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
)

// Assemble 构造本次装配的所有 repository 并登记到新的注册表。
//
// repository 必须用 NewFromHandle 延迟解析连接：模块工厂在 app/io.Start 之前
// 执行，此时 appdb.* 句柄仍为 nil；每次查询再解析也能让 Reload 自动用上新连接。
func Assemble(rt *gofy.Runtime) (*registry.Registry, error) {
	reg := registry.NewRegistry()
	entries := []struct {
		name string
		repo any
	}{
		{NameOrderOrders, orderrepo.NewFromHandle(rt.Logger)},
		{NameProductProducts, productrepo.NewFromHandle(rt.Logger)},
	}
	for _, entry := range entries {
		if err := reg.Register(entry.name, entry.repo); err != nil {
			return nil, fmt.Errorf("app/repository: %s: %w", entry.name, err)
		}
	}
	return reg, nil
}
