package dto

type GetOrderRequest struct {
	ID uint64 `uri:"id" json:"id" validate:"required,min=1" message:"required:订单ID不能为空,min:订单ID非法"`
}

type GetOrderResponse struct {
	ID     uint64 `json:"id"`
	UserID uint64 `json:"user_id"`
	Amount int64  `json:"amount"`
	Status string `json:"status"`
}

type CreateOrderRequest struct {
	UserID uint64 `json:"user_id" validate:"required,min=1" message:"required:用户ID不能为空,min:用户ID非法"`
	Amount int64  `json:"amount" validate:"required,min=1" message:"required:金额不能为空,min:金额必须大于0"`
}

type ListOrderRequest struct {
	UserID uint64 `form:"user_id" json:"user_id" validate:"required,min=1" message:"required:用户ID不能为空,min:用户ID非法"`
	Page   int    `form:"page" json:"page" default:"1" validate:"min=1" message:"min:页码必须大于0"`
	Size   int    `form:"size" json:"size" default:"20" validate:"min=1,max=100" message:"min:每页至少1条,max:每页最多100条"`
}

type ListOrderResponse struct {
	Items []GetOrderResponse `json:"items"`
	Total int64              `json:"total"`
}
