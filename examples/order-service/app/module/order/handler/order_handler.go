package handler

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/apperr"
	"github.com/yourorg/order-service/app/module/order/dto"
	"github.com/yourorg/order-service/app/module/order/model"
	"github.com/yourorg/order-service/app/module/order/service"
)

type OrderHandler struct {
	svc *service.OrderService
}

func New(svc *service.OrderService) *OrderHandler {
	return &OrderHandler{svc: svc}
}

func (h *OrderHandler) Get(ctx context.Context, req *dto.GetOrderRequest) (*dto.GetOrderResponse, error) {
	o, err := h.svc.Get(ctx, req.ID)
	if err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			return nil, apperr.NotFound("order not found")
		}
		return nil, apperr.Internal("get order failed").WithCause(err)
	}
	return toResponse(o), nil
}

func (h *OrderHandler) Create(ctx context.Context, req *dto.CreateOrderRequest) (*dto.GetOrderResponse, error) {
	o, err := h.svc.Create(ctx, req.UserID, req.Amount)
	if err != nil {
		return nil, apperr.Internal("create order failed").WithCause(err)
	}
	return toResponse(o), nil
}

func (h *OrderHandler) List(ctx context.Context, req *dto.ListOrderRequest) (*dto.ListOrderResponse, error) {
	items, total, err := h.svc.List(ctx, req.UserID, req.Page, req.Size)
	if err != nil {
		return nil, apperr.Internal("list orders failed").WithCause(err)
	}
	res := make([]dto.GetOrderResponse, 0, len(items))
	for i := range items {
		res = append(res, *toResponse(&items[i]))
	}
	return &dto.ListOrderResponse{Items: res, Total: total}, nil
}

func (h *OrderHandler) Cancel(ctx context.Context, req *dto.GetOrderRequest) error {
	if err := h.svc.Cancel(ctx, req.ID); err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			return apperr.NotFound("order not found")
		}
		return apperr.Internal("cancel order failed").WithCause(err)
	}
	return nil
}

func toResponse(o *model.Order) *dto.GetOrderResponse {
	return &dto.GetOrderResponse{
		ID:     o.ID,
		UserID: o.UserID,
		Amount: o.Amount,
		Status: o.Status,
	}
}
