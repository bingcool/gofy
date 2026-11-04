package handler

import (
	"context"
	"errors"

	"github.com/bingcool/gofy/apperr"
	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/demo-service/app/module/order/service"
	gofyadapter "gofy/gofy-interface-service/adapter/gofy"
	"gofy/gofy-interface-service/appclient/demoservice"
	"gofy/gofy-interface-service/appclient/demoservice/orderdto"
	"gofy/gofy-interface-service/appclient/demoservice/orderenum"
	"gofy/gofy-interface-service/appclient/demoservice/orderrequest"
	"gofy/gofy-interface-service/appclient/demoservice/orderresponse"
	"gofy/gofy-interface-service/contracts/common/paging"
	"gofy/gofy-interface-service/contracts/common/types"
)

var _ demoservice.Service = (*OrderHandler)(nil)

type OrderHandler struct {
	svc *service.OrderService
}

func New(svc *service.OrderService) *OrderHandler {
	return &OrderHandler{svc: svc}
}

func (h *OrderHandler) GetOrder(ctx context.Context, req *orderrequest.GetOrderRequest) (*orderresponse.GetOrderResponse, error) {
	o, err := h.svc.Get(ctx, req.ID.Uint64())
	if err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			return nil, gofyadapter.Error(demoservice.ErrOrderNotFound)
		}
		return nil, apperr.Internal("get order failed").WithCause(err)
	}
	return &orderresponse.GetOrderResponse{Order: toDTO(o)}, nil
}

func (h *OrderHandler) ListOrders(ctx context.Context, req *orderrequest.ListOrdersRequest) (*paging.ListData[orderdto.OrderDTO], error) {
	items, total, err := h.svc.List(ctx, req.UserID.Uint64(), req.Page, req.PageSize)
	if err != nil {
		return nil, apperr.Internal("list orders failed").WithCause(err)
	}
	list := make([]orderdto.OrderDTO, 0, len(items))
	for i := range items {
		list = append(list, *toDTO(&items[i]))
	}
	return &paging.ListData[orderdto.OrderDTO]{Total: total, List: list}, nil
}

func (h *OrderHandler) CreateOrder(ctx context.Context, req *orderrequest.CreateOrderRequest) (*orderresponse.CreateOrderResponse, error) {
	o, err := h.svc.Create(ctx, req.UserID.Uint64(), req.Amount)
	if err != nil {
		return nil, apperr.Internal("create order failed").WithCause(err)
	}
	return &orderresponse.CreateOrderResponse{Order: toDTO(o)}, nil
}

func (h *OrderHandler) CancelOrder(ctx context.Context, req *orderrequest.CancelOrderRequest) error {
	if err := h.svc.Cancel(ctx, req.ID.Uint64()); err != nil {
		if errors.Is(err, service.ErrOrderNotFound) {
			return gofyadapter.Error(demoservice.ErrOrderNotFound)
		}
		return apperr.Internal("cancel order failed").WithCause(err)
	}
	return nil
}

func toDTO(o *model.Order) *orderdto.OrderDTO {
	return &orderdto.OrderDTO{
		ID:        types.Uint64String(o.ID),
		UserID:    types.Uint64String(o.UserID),
		Amount:    o.Amount,
		Status:    orderenum.OrderStatus(o.Status),
		CreatedAt: o.CreatedAt,
	}
}
