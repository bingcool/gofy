// Package orderenum defines the order service enums.
// 包 orderenum 定义订单服务的枚举类型，取值同时作为 OpenAPI enum 的取值来源。
package orderenum

// OrderStatus is the lifecycle state of an order.
// OrderStatus 是订单的生命周期状态，取值只能是 pending、paid、cancelled。
type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "pending"
	OrderStatusPaid      OrderStatus = "paid"
	OrderStatusCancelled OrderStatus = "cancelled"
)

// AllOrderStatus lists every declared status.
// AllOrderStatus 列出全部已声明的状态，便于遍历与完整性校验。
var AllOrderStatus = []OrderStatus{
	OrderStatusPending,
	OrderStatusPaid,
	OrderStatusCancelled,
}

// OrderStatusLabels maps statuses to human labels.
// OrderStatusLabels 把状态映射为中文文案，同时充当合法状态的集合。
var OrderStatusLabels = map[OrderStatus]string{
	OrderStatusPending:   "待支付",
	OrderStatusPaid:      "已支付",
	OrderStatusCancelled: "已取消",
}

// Valid reports whether s is a declared status.
// Valid 报告 s 是否为已声明的状态。
func (s OrderStatus) Valid() bool {
	_, ok := OrderStatusLabels[s]
	return ok
}

// ParseOrderStatus parses and validates a status string.
// ParseOrderStatus 解析状态字符串，第二个返回值表示该取值是否合法。
func ParseOrderStatus(v string) (OrderStatus, bool) {
	s := OrderStatus(v)
	return s, s.Valid()
}

// String returns the label, falling back to the raw value.
// String 返回状态的中文文案，未声明时回退为原始值。
func (s OrderStatus) String() string {
	if label, ok := OrderStatusLabels[s]; ok {
		return label
	}
	return string(s)
}

// PayType is the payment channel.
// PayType 是支付渠道，0 表示未知，1 微信，2 支付宝。
type PayType int

const (
	PayTypeUnknown PayType = 0
	PayTypeWechat  PayType = 1
	PayTypeAlipay  PayType = 2
)

// PayTypeLabels maps pay types to human labels.
// PayTypeLabels 把支付渠道映射为中文文案；未登记的渠道按未知处理。
var PayTypeLabels = map[PayType]string{
	PayTypeWechat: "微信",
	PayTypeAlipay: "支付宝",
}
