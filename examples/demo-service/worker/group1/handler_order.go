package group1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/bingcool/gofy/core"
	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// orderEventStatus maps an event type to the order status it drives. Adding a
// new lifecycle event is one line here plus the producer that emits it; the
// consumer does not need a new worker.
// orderEventStatus 把事件类型映射到它驱动的订单状态。新增一个生命周期事件就是
// 这里加一行、生产者发出它；消费端不需要新增 worker。
var orderEventStatus = map[string]orderenum.OrderStatus{
	"order.paid":      orderenum.OrderStatusPaid,
	"order.cancelled": orderenum.OrderStatusCancelled,
}

// orderEventPayload is the body carried by every order lifecycle event.
// orderEventPayload 是每个订单生命周期事件携带的消息体。
type orderEventPayload struct {
	// OrderID is the order the event applies to.
	// OrderID 是事件作用的订单。
	OrderID uint64 `json:"order_id"`
	// UserID is kept for audit logs; it is not required for the update.
	// UserID 用于审计日志，更新本身并不需要它。
	UserID uint64 `json:"user_id"`
	// Reason explains a cancellation; optional.
	// Reason 说明取消原因，可选。
	Reason string `json:"reason"`
}

// orderEventHandler applies order events to the orders table.
//
// It writes through the runtime's database handle instead of a module-scoped
// repository: a worker process runs no HTTP module, so the connection it can
// rely on is the one the host opened for it.
// orderEventHandler 把订单事件落到 orders 表。
//
// 它通过 Runtime 的数据库句柄写入，而不是模块内的 repository：worker 进程不运行
// HTTP 模块，能依赖的连接只有宿主为它打开的那个。
type orderEventHandler struct {
	db *gorm.DB
}

// newOrderHandler builds the demo's business handler. The database is required:
// an event sink that cannot persist anything would only acknowledge messages
// it never applied.
// newOrderHandler 构造示例的业务 handler。数据库是必需的：一个什么都存不下的
// 事件汇点只会确认它从未应用过的消息。
func newOrderHandler(rt *core.Runtime) (gofyconsumer.HandlerFunc, error) {
	if rt == nil || rt.DB == nil {
		return nil, errors.New("order handler requires a runtime database")
	}
	h := &orderEventHandler{db: rt.DB}
	return h.handle, nil
}

// handle applies one order event.
//
// The update is written as "set the target status where it differs", so a
// redelivery touches zero rows instead of writing the same value twice. Zero
// rows then splits into the two cases that matter: already in the target state
// (idempotent success) and not in the table yet (retry — the create may still
// be in flight).
// handle 应用一条订单事件。
//
// 更新写成"把与目标状态不同的行改成目标状态"，因此重复投递影响 0 行，而不是把
// 同一个值写两次。0 行随后分成两种要紧的情况：已是目标状态（幂等成功）与尚未
// 落库（重试——创建请求可能还在路上）。
func (h *orderEventHandler) handle(ctx context.Context, env gofyconsumer.Envelope) error {
	target, ok := orderEventStatus[env.Type]
	if !ok {
		return gofyconsumer.Permanent(fmt.Errorf("unsupported event type %q", env.Type))
	}
	var payload orderEventPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return gofyconsumer.Permanent(fmt.Errorf("decode payload of %s: %w", env.Type, err))
	}
	if payload.OrderID == 0 {
		return gofyconsumer.Permanent(fmt.Errorf("%s payload: order_id is required", env.Type))
	}

	res := h.db.WithContext(ctx).Model(&model.Order{}).
		Where("id = ? AND status <> ?", payload.OrderID, string(target)).
		Update("status", string(target))
	if res.Error != nil {
		return fmt.Errorf("update order %d to %s: %w", payload.OrderID, target, res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}

	// Nothing changed: either the order already had the target status, or it
	// does not exist yet.
	// 没有行被改动：要么订单已经是目标状态，要么订单还不存在。
	var count int64
	if err := h.db.WithContext(ctx).Model(&model.Order{}).
		Where("id = ?", payload.OrderID).
		Count(&count).Error; err != nil {
		return fmt.Errorf("lookup order %d: %w", payload.OrderID, err)
	}
	if count == 0 {
		// Out-of-order delivery is normal on several partitions: retry until
		// the order row appears or the attempt budget runs out.
		// 多分区上乱序投递是常态：重试到订单行出现，或尝试次数用完为止。
		return fmt.Errorf("order %d does not exist yet", payload.OrderID)
	}
	return nil
}
