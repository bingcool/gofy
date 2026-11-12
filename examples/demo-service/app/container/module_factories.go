package container

import (
	"errors"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	"github.com/yourorg/demo-service/app/module/order"
	"github.com/yourorg/demo-service/app/module/product"
)

// ProductModuleFactory 构建产品模块：先确认数据库就绪，再记录本代 logger 并
// 取出产品 service，最后交给模块根。产品模块没有路由，service 只通过
// innerapi 注入订单侧，所以工厂不传绑定选项。
func (c *Container) ProductModuleFactory() gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		if rt.DB == nil {
			return nil, errors.New("product module requires database")
		}
		c.Init(rt.Logger)
		return product.NewModule(c.ProductService(), rt.Logger), nil
	}
}

// OrderModuleFactory 构建订单模块。订单 service 的取用会按依赖链先构造产品
// service，因此这个工厂先于产品工厂执行也能正常工作。
//
// BindOptions 携带契约限流回调：模块根在 Register 时把它原样传给
// gofyadapter.BindGroup，具体哪条路由挂限流由 adapter 读取契约决定。
func (c *Container) OrderModuleFactory() gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		if rt.DB == nil {
			return nil, errors.New("order module requires database")
		}
		c.Init(rt.Logger)
		return order.NewModule(c.OrderService(), c.OrderConfigService(), rt.Logger, c.BindOptions()), nil
	}
}

// ModuleFactories 返回本代全部模块工厂。这里的顺序只决定 HTTP 路由的装配
// 顺序，不表示依赖顺序：依赖在首次取用 service 时按链解析完毕。
func (c *Container) ModuleFactories() []gofy.ModuleFactory {
	return []gofy.ModuleFactory{c.ProductModuleFactory(), c.OrderModuleFactory()}
}
