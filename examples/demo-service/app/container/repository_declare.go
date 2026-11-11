package container

import (
	orderrepo "github.com/yourorg/demo-service/app/module/order/repository"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
)

// RepositoryDeclare 集中声明一次装配用到的全部仓库。仓库无状态、每次查询
// 自行解析连接，零值构造后可在本代容器内复用；新增仓库时在这里补字段，
// 再到对应模块的访问器文件（product.go / order.go）注入。
type RepositoryDeclare struct {
	OrdersRepo   *orderrepo.OrderRepository
	ProductsRepo *productrepo.ProductRepository
}

// NewRepositoryDeclare 零值构造本代全部仓库。
func NewRepositoryDeclare() RepositoryDeclare {
	return RepositoryDeclare{
		OrdersRepo:   &orderrepo.OrderRepository{},
		ProductsRepo: &productrepo.ProductRepository{},
	}
}
