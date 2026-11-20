// Package service implements product use cases on top of the product
// repository and publishes them through innerapi.ProductAPI. 其它模块只依赖
// innerapi 契约，不接触本包的实现、仓库或持久化模型。
package service

import (
	"context"
	"errors"
	"fmt"

	productapi "github.com/yourorg/demo-service/app/module/product/innerapi"
	"github.com/yourorg/demo-service/app/module/product/model/productmodel"
	"github.com/yourorg/demo-service/app/module/product/repository"
	"go.uber.org/zap"
)

// 编译期保证 ProductService 满足产品模块对外发布的契约。
var _ productapi.ProductAPI = (*ProductService)(nil)

// ErrNotFound 表示产品不存在。controller 用它把错误映射成契约的
// ErrProductNotFound，不必 import innerapi。
var ErrNotFound = productapi.ErrNotFound

// ProductService implements product use cases.
// ProductService 是产品用例实现，也是 innerapi.ProductAPI 的唯一实现：把持久化
// 模型换算成跨模块只读视图，并把状态码换算成 bool OnSale。
type ProductService struct {
	productRepository *repository.ProductRepository
	logger            *zap.Logger
}

// Get returns one product view or innerapi.ErrNotFound.
// Get 返回单个产品视图；仓库 ErrNotFound 直接翻译成 innerapi.ErrNotFound，
// 消费方无需认识本模块仓库的错误类型。
func (s *ProductService) Get(ctx context.Context, id uint64) (*productapi.Product, error) {
	p, err := s.productRepository.FindByID(ctx, id)
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
// ListByIDs 是订单模块下单校验的入口：只返回存在的产品视图，缺失 id 不出现，
// 由调用方判定哪个 SKU 不存在。
func (s *ProductService) ListByIDs(ctx context.Context, ids []uint64) (map[uint64]productapi.Product, error) {
	items, err := s.productRepository.ListByIDs(ctx, ids)
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

// List 分页返回产品视图和总数，包含已下架产品。方法不依赖 HTTP 分页类型，
// 只收页码与页大小，偏移量在这里换算。
func (s *ProductService) List(ctx context.Context, page, size int) ([]productapi.Product, int64, error) {
	rows, total, err := s.productRepository.List(ctx, (page-1)*size, size)
	if err != nil {
		return nil, 0, err
	}
	views := make([]productapi.Product, len(rows))
	for i := range rows {
		views[i] = toProduct(&rows[i])
	}
	s.logger.Debug("list products",
		zap.Int("page", page),
		zap.Int("page_size", size),
		zap.Int("rows", len(views)),
		zap.Int64("total", total),
	)
	return views, total, nil
}

// DecreaseStock 扣减库存。正常路径只跑一条条件更新 SQL；返回 0 行时在失败
// 路径补一次 FindByID，把「不存在 / 已下架 / 库存不足」区分成 innerapi 哨兵
// 错误。分类读取仅用于报错，不影响扣减的原子性。
func (s *ProductService) DecreaseStock(ctx context.Context, id uint64, num int) error {
	if num <= 0 {
		return fmt.Errorf("decrease product %d stock: num must be positive", id)
	}
	rows, err := s.productRepository.DecreaseStock(ctx, id, num)
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
	p, err := s.productRepository.FindByID(ctx, id)
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
func toProduct(p *productmodel.Product) productapi.Product {
	return productapi.Product{
		ID:     p.ID,
		Name:   p.Name,
		Price:  p.Price,
		Stock:  p.Stock,
		OnSale: p.Status == repository.StatusOnSale,
	}
}
