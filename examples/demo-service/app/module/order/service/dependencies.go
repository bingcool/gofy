package service

import (
	"github.com/yourorg/demo-service/app/module/order/repository"
	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
	"go.uber.org/zap"
)

// Dependencies 显式声明订单 service 的全部依赖：本模块仓库与跨模块产品契约
// 必填，Logger 允许 nil。产品能力以 innerapi.ProductAPI 接口注入，实现由装配
// 根提供；service 不 import 其它模块的 service/repository/model。
type Dependencies struct {
	OrderRepository *repository.OrderRepository
	ProductAPI      productapi.ProductAPI
	Logger          *zap.Logger
}
