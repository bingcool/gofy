// Package service implements product use cases on top of the product
// repository and publishes them through innerapi.ProductAPI. 其它模块只依赖
// innerapi 契约，不接触本包的实现、仓库或持久化模型。
package service

import (
	"context"
	"errors"
	"fmt"

	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
	"github.com/yourorg/demo-service/app/module/product/model"
	"github.com/yourorg/demo-service/app/module/product/repository"
	"go.uber.org/zap"
)

// 编译期保证 ProductService 满足产品模块对外发布的契约。
var _ productapi.ProductAPI = (*ProductService)(nil)

// ProductService implements product use cases.
type ProductService struct {
	products *repository.ProductRepository
	logger   *zap.Logger
}

// Get returns one product view or innerapi.ErrNotFound.
func (s *ProductService) Get(ctx context.Context, id uint64) (*productapi.Product, error) {
	p, err := s.products.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, productapi.ErrNotFound
		}
		return nil, err
	}
	s.logger.Debug("get product", zap.Uint64("product_id", id))
	view := toProduct(p)
	return &view, nil
}

// ListByIDs returns the products that exist, keyed by id. Missing ids are
// absent from the result.
func (s *ProductService) ListByIDs(ctx context.Context, ids []uint64) (map[uint64]productapi.Product, error) {
	items, err := s.products.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	views := make(map[uint64]productapi.Product, len(items))
	for id, p := range items {
		views[id] = toProduct(&p)
	}
	s.logger.Debug("list products",
		zap.Int("requested", len(ids)),
		zap.Int("found", len(views)),
	)
	return views, nil
}

// DecreaseStock 扣减库存。正常路径只跑一条条件更新 SQL；返回 0 行时在失败
// 路径补一次 FindByID，把「不存在 / 已下架 / 库存不足」区分成 innerapi 哨兵
// 错误。分类读取仅用于报错，不影响扣减的原子性。
func (s *ProductService) DecreaseStock(ctx context.Context, id uint64, num int) error {
	if num <= 0 {
		return fmt.Errorf("decrease product %d stock: num must be positive", id)
	}
	rows, err := s.products.DecreaseStock(ctx, id, num)
	if err != nil {
		return err
	}
	if rows > 0 {
		s.logger.Info("product stock decreased",
			zap.Uint64("product_id", id),
			zap.Int("num", num),
			zap.Int64("rows", rows),
		)
		return nil
	}
	p, err := s.products.FindByID(ctx, id)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return fmt.Errorf("%w: sku %d", productapi.ErrNotFound, id)
	case err != nil:
		return err
	case p.Status != repository.StatusOnSale:
		return fmt.Errorf("%w: sku %d", productapi.ErrUnavailable, id)
	default:
		return fmt.Errorf("%w: sku %d", productapi.ErrInsufficientStock, id)
	}
}

// toProduct 把持久化模型换算成跨模块只读视图，状态码在这里转成 bool OnSale。
func toProduct(p *model.Product) productapi.Product {
	return productapi.Product{
		ID:     p.ID,
		Name:   p.Name,
		Price:  p.Price,
		Stock:  p.Stock,
		OnSale: p.Status == repository.StatusOnSale,
	}
}
