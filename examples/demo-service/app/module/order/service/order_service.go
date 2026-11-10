package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/bingcool/gofy/support/registry"
	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/demo-service/app/module/order/repository"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
	"go.uber.org/zap"
	"gofy/gofy-interface-service/appinterface/demoservice/orderdto"
	"gofy/gofy-interface-service/appinterface/demoservice/orderenum"
	"gofy/gofy-interface-service/contracts/common/types"
)

var (
	// ErrOrderNotFound is returned when an order does not exist.
	ErrOrderNotFound = repository.ErrNotFound
	// ErrProductNotFound is returned when a requested SKU does not exist.
	ErrProductNotFound = errors.New("product not found")
	// ErrProductUnavailable is returned when a requested SKU is not on sale.
	ErrProductUnavailable = errors.New("product unavailable")
)

// CreateItem is one SKU line of a create-order request. The controller maps
// the contract type to this plain value so the service never imports it.
type CreateItem struct {
	SkuID uint64
	Num   int32
}

// OrderService implements order use cases.
type OrderService struct {
	orders *repository.OrderRepository
	// repos 用于按需取用 product.products（模式 B）；实例由模块工厂在装配期登记。
	repos  *registry.Registry
	logger *zap.Logger
}

// NewOrderService builds an OrderService. Orders is required; repos is the
// assembly-scoped registry used to resolve product.products on demand. A nil
// logger falls back to zap.NewNop().
func NewOrderService(orders *repository.OrderRepository, repos *registry.Registry, logger *zap.Logger) *OrderService {
	if logger == nil {
		logger = zap.NewNop()
	}
	logger.Debug("order service initialized", zap.String("component", "order.service"))
	return &OrderService{
		orders: orders,
		repos:  repos,
		logger: logger.With(zap.String("component", "order.service")),
	}
}

// Get returns one order as a DTO.
func (s *OrderService) Get(ctx context.Context, id uint64) (*orderdto.OrderDTO, error) {
	o, err := s.orders.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	s.logger.Debug("get order", zap.Uint64("order_id", id))
	return toDTO(o), nil
}

// Create validates every SKU against the product module, then persists a
// pending order. Product lookup goes through the assembly registry (mode B):
// the dependency is resolved on demand rather than stored as a constructor
// parameter, and the module factory registers it before serving traffic.
func (s *OrderService) Create(ctx context.Context, userID uint64, amount int64, items []CreateItem) (*orderdto.OrderDTO, error) {
	names, err := s.validateItems(ctx, items)
	if err != nil {
		return nil, err
	}
	o, err := s.orders.Create(ctx, &model.Order{
		UserID: userID,
		Amount: amount,
		Status: string(orderenum.OrderStatusPending),
	})
	if err != nil {
		return nil, err
	}
	s.logger.Info("order created",
		zap.Uint64("order_id", o.ID),
		zap.Uint64("user_id", userID),
		zap.Int64("amount", amount),
		zap.Strings("products", names),
	)
	return toDTO(o), nil
}

// validateItems 按 SKU 批量查产品仓库，确认全部存在且上架。返回产品名称用于
// 日志；错误使用哨兵值，HTTP 层据此映射业务错误码。
func (s *OrderService) validateItems(ctx context.Context, items []CreateItem) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	products, err := registry.Get[*productrepo.ProductRepository](s.repos, productrepo.NameProducts)
	if err != nil {
		return nil, err
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
	views, err := products.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		view, ok := views[id]
		if !ok {
			return nil, fmt.Errorf("%w: sku %d", ErrProductNotFound, id)
		}
		if !view.OnSale() {
			return nil, fmt.Errorf("%w: sku %d", ErrProductUnavailable, id)
		}
		names = append(names, view.Name)
	}
	return names, nil
}

// List returns one page of DTOs plus the total count.
func (s *OrderService) List(ctx context.Context, userID uint64, page, size int) ([]orderdto.OrderDTO, int64, error) {
	items, total, err := s.orders.ListByUser(ctx, userID, (page-1)*size, size)
	if err != nil {
		return nil, 0, err
	}
	list := make([]orderdto.OrderDTO, 0, len(items))
	for i := range items {
		list = append(list, *toDTO(&items[i]))
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
func (s *OrderService) Cancel(ctx context.Context, id uint64) error {
	if err := s.orders.UpdateStatus(ctx, id, string(orderenum.OrderStatusCancelled)); err != nil {
		if errors.Is(err, ErrOrderNotFound) {
			// 预期内的业务结果，HTTP 层会映射为 404，不按错误级别记录。
			s.logger.Debug("cancel order: not found", zap.Uint64("order_id", id))
		}
		return err
	}
	s.logger.Info("order cancelled", zap.Uint64("order_id", id))
	return nil
}

// toDTO converts the persistence model at the service boundary.
func toDTO(o *model.Order) *orderdto.OrderDTO {
	return &orderdto.OrderDTO{
		ID:        types.Uint64String(o.ID),
		UserID:    types.Uint64String(o.UserID),
		Amount:    o.Amount,
		Status:    orderenum.OrderStatus(o.Status),
		CreatedAt: o.CreatedAt,
	}
}
