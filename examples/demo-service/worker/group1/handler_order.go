package group1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/support/worker"

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
	// Reason is free text the payload may carry. In this demo the producer
	// fills it with the queue the event came in on, so one log line answers
	// "which transport delivered this".
	// Reason 是 payload 可携带的自由文本。本示例中生产者用它记录事件来自哪个
	// 队列，因此一行日志就能回答"这条是哪个传输投递的"。
	Reason string `json:"reason"`
}

// applyOrderEvent is the business branch every consumer in this group shares.
//
// It takes the decoded envelope, so each consumer file adapts its own message
// type to it — and still reaches the native record when it needs an offset, a
// delivery tag or a manual acknowledgement.
//
// The database comes from the runtime in ctx instead of being captured when the
// handler is built: a consumer config is a package value, and its handler must
// not hold a pool that outlives a reload. A worker process runs no HTTP module,
// so the runtime's handle is the only connection it can rely on.
// applyOrderEvent 是本分组内所有消费者共用的业务分支。
//
// 它接收已解码的信封，因此每个消费者文件把自己的消息类型适配到它——需要位点、投递
// 标签或手动确认时，仍然能拿到原生记录。
//
// 数据库来自 ctx 里的 Runtime，而不是在构造 handler 时捕获：消费者配置是包级值，它的
// handler 不应该持有一个比 reload 活得更久的连接池。worker 进程不运行 HTTP 模块，
// 能依赖的连接只有 Runtime 持有的那个。
func applyOrderEvent(ctx context.Context, event gofyconsumer.Envelope) error {
	rt, ok := gofyworker.RuntimeFrom(ctx)
	if !ok || rt.DB == nil {
		return errors.New("order handler requires a runtime database")
	}
	db := rt.DB

	target, ok := orderEventStatus[event.Type]
	if !ok {
		return gofyconsumer.Permanent(fmt.Errorf("unsupported event type %q", event.Type))
	}
	var payload orderEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return gofyconsumer.Permanent(fmt.Errorf("decode payload of %s: %w", event.Type, err))
	}
	if payload.OrderID == 0 {
		return gofyconsumer.Permanent(fmt.Errorf("%s payload: order_id is required", event.Type))
	}

	// The update is written as "set the target status where it differs", so a
	// redelivery touches zero rows instead of writing the same value twice.
	// Zero rows then splits into the two cases that matter: already in the
	// target state (idempotent success) and not in the table yet (retry — the
	// create may still be in flight).
	// 更新写成"把与目标状态不同的行改成目标状态"，因此重复投递影响 0 行，而不是把
	// 同一个值写两次。0 行随后分成两种要紧的情况：已是目标状态（幂等成功）与尚未
	// 落库（重试——创建请求可能还在路上）。
	res := db.WithContext(ctx).Model(&model.Order{}).
		Where("id = ? AND status <> ?", payload.OrderID, string(target)).
		Update("status", string(target))
	if res.Error != nil {
		return fmt.Errorf("update order %d to %s: %w", payload.OrderID, target, res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}

	var count int64
	if err := db.WithContext(ctx).Model(&model.Order{}).
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
