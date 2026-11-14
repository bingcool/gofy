// Package service implements order use cases. 它实现本模块 innerapi.OrderAPI，
// 跨模块只依赖产品 innerapi 契约，出入参用领域视图与普通值，不 import HTTP
// 契约；契约 DTO/枚举的映射留在 controller。
package service

import (
	"context"
	"errors"
	"fmt"

	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/yourorg/demo-service/app/appio"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	orderapi "github.com/yourorg/demo-service/app/module/order/innerapi"
	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/demo-service/app/module/order/repository"
	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
	"go.uber.org/zap"
)

// 编译期保证 OrderService 满足订单模块对外发布的契约。
var _ orderapi.OrderAPI = (*OrderService)(nil)

// OrderService implements order use cases.
// OrderService 是订单用例的实现，也是 innerapi.OrderAPI 的唯一实现：对外只暴露
// 领域视图，模块内持有订单仓库与产品 innerapi 契约。
type OrderService struct {
	orderRepository *repository.OrderRepository
	productAPI      productapi.ProductAPI
	logger          *zap.Logger
}

// NewOrderService builds an OrderService from explicit dependencies.
// OrderRepository and ProductAPI are required; a nil logger falls back to
// zap.NewNop().
// NewOrderService 按显式依赖构造 service：仓库与产品 innerapi 必填，缺失会在
// 调用时 panic；logger 为 nil 时回退 zap.NewNop()。依赖由装配根在构造期注入，
// service 不自行查容器。
func NewOrderService(deps Dependencies) *OrderService {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OrderService{
		orderRepository: deps.OrderRepository,
		productAPI:      deps.ProductAPI,
		logger:          logger.With(zap.String("component", "order.service")),
	}
}

// Get returns one order view or ErrOrderNotFound.
// Get 返回单个订单视图；仓库 ErrNotFound 翻译成订单域 ErrOrderNotFound，model 在
// 这里换算成 innerapi 视图，不出 service 边界。
func (s *OrderService) Get(ctx context.Context, id uint64) (*orderapi.Order, error) {
	o, err := s.orderRepository.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	s.logger.Debug("get order", zap.Uint64("order_id", id))
	return toOrderView(o), nil
}

// Create 先按 SKU 批量校验产品（存在且上架），再在同一事务里扣库存 + 落单。
// 跨模块调用传同一 ctx，产品仓库从 ctx 取到同一个事务，扣库存与建订单同提交
// 同回滚。产品 innerapi 错误在这里翻译成订单域哨兵错误。
func (s *OrderService) Create(ctx context.Context, userID uint64, amount int64, items []CreateItem) (*orderapi.Order, error) {
	names, err := s.validateItems(ctx, items)
	if err != nil {
		return nil, err
	}
	var created *model.Order
	db, err := appio.WithContext(ctx).DBIO(ctx, appdb.DefaultInstance)
	if err != nil {
		return nil, fmt.Errorf("resolve default database: %w", err)
	}
	err = gofydb.Transaction(ctx, db, func(ctx context.Context) error {
		for _, it := range items {
			if err := s.productAPI.DecreaseStock(ctx, it.SkuID, int(it.Num)); err != nil {
				return translateProductErr(it.SkuID, err)
			}
		}
		o, err := s.orderRepository.Create(ctx, &model.Order{
			UserID: userID,
			Amount: amount,
			Status: StatusPending,
		})
		if err != nil {
			return err
		}
		created = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.logger.Info("order created",
		zap.Uint64("order_id", created.ID),
		zap.Uint64("user_id", userID),
		zap.Int64("amount", amount),
		zap.Strings("products", names),
	)
	return toOrderView(created), nil
}

// validateItems 按 SKU 批量查产品 innerapi，确认全部存在且上架。返回产品名称
// 用于日志；错误使用订单域哨兵值，HTTP 层据此映射业务错误码。
func (s *OrderService) validateItems(ctx context.Context, items []CreateItem) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	ids := make([]uint64, 0, len(items))
	seen := make(map[uint64]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item.SkuID]; ok {
			continue
		}
		seen[item.SkuID] = struct{}{}
		ids = append(ids, item.SkuID)
	}
	views, err := s.productAPI.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		view, ok := views[id]
		if !ok {
			return nil, fmt.Errorf("%w: sku %d", ErrProductNotFound, id)
		}
		if !view.OnSale {
			return nil, fmt.Errorf("%w: sku %d", ErrProductUnavailable, id)
		}
		names = append(names, view.Name)
	}
	return names, nil
}

// List returns one page of order views plus the total count.
// List 按用户分页返回订单视图与总数，偏移量由页码在这里换算；空页返回空切片
// 而不是 nil，controller 可直接构造响应列表。
func (s *OrderService) List(ctx context.Context, userID uint64, page, size int) ([]orderapi.Order, int64, error) {
	items, total, err := s.orderRepository.ListByUser(ctx, userID, (page-1)*size, size)
	if err != nil {
		return nil, 0, err
	}
	list := make([]orderapi.Order, 0, len(items))
	for i := range items {
		list = append(list, *toOrderView(&items[i]))
	}
	s.logger.Debug("list orders",
		zap.Uint64("user_id", userID),
		zap.Int("page", page),
		zap.Int("page_size", size),
		zap.Int64("total", total),
	)
	return list, total, nil
}

// Cancel marks an order as cancelled.
// Cancel 把订单状态置为 cancelled；订单不存在返回 ErrOrderNotFound，且只按 Debug
// 记录——404 是预期业务结果，不该当成故障告警。
func (s *OrderService) Cancel(ctx context.Context, id uint64) error {
	if err := s.orderRepository.UpdateStatus(ctx, id, StatusCancelled); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// 预期内的业务结果，HTTP 层会映射为 404，不按错误级别记录。
			s.logger.Debug("cancel order: not found", zap.Uint64("order_id", id))
			return ErrOrderNotFound
		}
		return err
	}
	s.logger.Info("order cancelled", zap.Uint64("order_id", id))
	return nil
}

// toOrderView 把持久化模型换算成跨模块只读视图，model 不出 service 边界。
func toOrderView(o *model.Order) *orderapi.Order {
	return &orderapi.Order{
		ID:        o.ID,
		UserID:    o.UserID,
		Amount:    o.Amount,
		Status:    o.Status,
		CreatedAt: o.CreatedAt,
	}
}

// translateProductErr 把产品 innerapi 哨兵错误翻译成订单域哨兵错误，保留 SKU
// 上下文。controller 只认订单域错误，产品错误语义的变化被 service 吸收。
func translateProductErr(sku uint64, err error) error {
	switch {
	case errors.Is(err, productapi.ErrNotFound):
		return fmt.Errorf("%w: sku %d", ErrProductNotFound, sku)
	case errors.Is(err, productapi.ErrUnavailable):
		return fmt.Errorf("%w: sku %d", ErrProductUnavailable, sku)
	case errors.Is(err, productapi.ErrInsufficientStock):
		return fmt.Errorf("%w: sku %d", ErrInsufficientStock, sku)
	default:
		return err
	}
}
