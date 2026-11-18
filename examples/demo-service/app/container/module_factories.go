package container

import (
	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support"
	"github.com/yourorg/demo-service/app/module/order"
	"github.com/yourorg/demo-service/app/module/product"
)

// A missing database no longer fails construction. `db` is an optional config
// section, so a deployment without it must still boot: the modules stay
// registered and their handlers fail at the point of use with
// db.ErrNotConfigured, which is a request error naming the missing instance
// rather than a process that refuses to start. The alternative — skipping the
// module — would turn a configuration mistake into a 404 that looks like a
// routing bug.
// 缺少数据库不再让构造失败。`db` 是可选配置段，没有它的部署仍然要能启动：模块照常注册，
// 由使用处返回 db.ErrNotConfigured——一个指名"实例未配置"的请求错误，而不是进程拒绝
// 启动。反过来"跳过模块"会把配置问题变成一个看起来像路由 bug 的 404。

// ProductModuleFactory 构建产品模块：记录本次服务的 logger 并取出产品 service，
// 最后交给模块根。产品模块现在也绑定契约路由，因此与订单工厂一样把
// BindOptions 传下去。
func (c *Container) ProductModuleFactory() gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		c.Init(rt.Logger)
		return product.NewModule(c.ProductService(), rt.Logger, c.BindOptions()), nil
	}
}

// OrderModuleFactory 构建订单模块。订单 service 的取用会按依赖链先构造产品
// service，因此这个工厂先于产品工厂执行也能正常工作。
//
// BindOptions 携带契约限流回调：模块根在 Register 时把它原样传给
// gofyadapter.BindGroup，具体哪条路由挂限流由 adapter 读取契约决定。
func (c *Container) OrderModuleFactory() gofy.ModuleFactory {
	return func(rt *gofy.Runtime) (support.Module, error) {
		c.Init(rt.Logger)
		return order.NewModule(c.OrderService(), c.OrderConfigService(), rt.Logger, c.BindOptions()), nil
	}
}

// ModuleFactories 返回本次服务的全部模块工厂。这里的顺序只决定 HTTP 路由的装配
// 顺序，不表示依赖顺序：依赖在首次取用 service 时按链解析完毕。
func (c *Container) ModuleFactories() []gofy.ModuleFactory {
	return []gofy.ModuleFactory{c.ProductModuleFactory(), c.OrderModuleFactory()}
}
