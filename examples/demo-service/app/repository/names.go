// Package apprepository 是 demo-service 的组合层：集中构造各业务模块的
// repository，登记到一次装配独占的注册表里，供模块工厂和 service 按名字取用。
//
// 只有这个包允许同时 import 多个模块的 repository；模块自己的 repository 包
// 不依赖注册表，也不依赖其他模块。注册表本身由 gofy/support/registry 提供，
// 这里只维护业务名字、装配函数和类型化 accessor。
package apprepository

// 业务仓库名字是跨模块契约，格式为 "<module>.<aggregate>"。
// 重命名按破坏性变更处理：所有使用方的常量都要一起改。
const (
	// NameOrderOrders 是订单持久化仓库。
	NameOrderOrders = "order.orders"
	// NameProductProducts 是产品仓库。
	NameProductProducts = "product.products"
)
