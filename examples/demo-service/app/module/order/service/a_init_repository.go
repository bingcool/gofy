package service

import (
	"github.com/yourorg/demo-service/app/module/order/repository"
)

// RepositoryDeps 汇总订单模块自己的 repository：字段即本模块声明的全部仓库，
// 新增仓库时在这里加字段并由模块工厂传给 service。跨模块依赖不放在这里，
// 由 service 持有注册表按需取用（模式 B）。
type RepositoryDeps struct {
	// Orders 是订单持久化仓库，必填。
	Orders *repository.OrderRepository
}

// Deps 是模块自带的默认仓库集合，模块工厂把它传给 service；测试可以另建
// RepositoryDeps 填自己的实例，不改这个变量。
//
// 仓库是无状态的：不持有连接和日志，每次查询才从 app/io 句柄解析 db.Db，
// 因此包初始化早于 app/io.Start 也安全，Reload 换连接后自动生效；仓库日志
// 由 service 用 rt.Logger 记录。
var Deps = RepositoryDeps{
	Orders: &repository.OrderRepository{},
}
