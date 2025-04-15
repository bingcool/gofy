package controller

import (
	"fmt"

	"github.com/bingcool/gofy/src/system"
	"github.com/gin-gonic/gin"
)

type Order struct {
}

func NewOrder() *Order {
	return &Order{}
}

type GetOrderListRequest struct {
	OrderId     uint32   `json:"order_id" form:"order_id"`
	UserId      uint32   `json:"user_id" form:"user_id"`
	CategoryIds []uint32 `json:"category_ids" form:"category_ids" default:"[1,2]"`
	Page        uint32   `json:"page" form:"page" default:"1"`
	Size        uint32   `json:"size" form:"size" default:"10"`
}

type GetOrderListRequest1 struct {
	OrderId     uint32   `json:"order_id" form:"order_id"`
	UserId      uint32   `json:"user_id" form:"user_id" validate:"required,min=1" message:"required:姓名不能为空,min:姓名长度至少3个字符"`
	CategoryIds []uint32 `json:"category_ids" form:"category_ids" default:"[1,2]"`
	Page        uint32   `json:"page" form:"page" default:"1"`
	Size        uint32   `json:"size" form:"size" default:"10"`
}

type GetOrderListResponse struct {
	OrderList []OrderItem `json:"order_list"`
}

type OrderItem struct {
	OrderId  uint32 `json:"order_id"`
	UserId   uint32 `json:"user_id"`
	Env      string `json:"env"`
	RunModel bool   `json:"run_model"`
}

func (order *Order) GetOrderList(_ *gin.Context, req *GetOrderListRequest) (res *GetOrderListResponse, err error) {
	orderItem := &OrderItem{
		OrderId:  1,
		UserId:   1,
		Env:      system.GetEnv(),
		RunModel: system.IsCliService(),
	}

	fmt.Println("aaaaaaaaaaaaaa")

	orderList := make([]OrderItem, 0)
	orderList = append(orderList, *orderItem)
	res = &GetOrderListResponse{
		OrderList: orderList,
	}
	return res, nil
}
