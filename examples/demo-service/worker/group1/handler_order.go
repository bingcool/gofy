package group1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/support/worker"

	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// orderEventRule says which status an event drives and which states it may move
// an order from. Target and sources live in one value on purpose: keeping them
// apart lets a new event map to a target nobody can legally reach.
//
// Idempotency and ordering are different problems. The ledger makes a repeated
// event harmless; it cannot stop an older event from arriving after a newer one,
// so the source set is what keeps a cancelled order cancelled.
// orderEventRule 说明一个事件驱动的目标状态，以及它允许从哪些状态迁移过来。目标与前置
// 状态放在同一个值里是有意的：分开维护会让新增事件映射到一个谁也到不了的目标。
//
// 幂等与顺序是两个问题。台账让重复事件无害，但挡不住旧事件在新事件之后到达，因此"已取消"
// 要靠前置状态集合来保证。
type orderEventRule struct {
	Target  orderenum.OrderStatus
	Sources []orderenum.OrderStatus
}

// orderEventRules maps an event type to its transition rule. Adding a lifecycle
// event is one line here plus the producer that emits it; the consumer does not
// need a new worker.
// orderEventRules 把事件类型映射到它的迁移规则。新增一个生命周期事件就是这里加一行、
// 生产者发出它；消费端不需要新增 worker。
var orderEventRules = map[string]orderEventRule{
	// Pending -> Paid. Nothing else may become paid: cancelled is final, and a
	// paid order receiving another order.paid touches zero rows.
	// Pending -> Paid。其他状态都不能变成 paid：cancelled 是终态，已支付的订单再收到
	// order.paid 也不会改任何行。
	"order.paid": {Target: orderenum.OrderStatusPaid, Sources: []orderenum.OrderStatus{orderenum.OrderStatusPending}},
	// Pending/Paid -> Cancelled. A paid order may still be cancelled; cancelling
	// twice is a no-op.
	// Pending/Paid -> Cancelled。已支付的订单仍可取消；重复取消是无操作。
	"order.cancelled": {
		Target:  orderenum.OrderStatusCancelled,
		Sources: []orderenum.OrderStatus{orderenum.OrderStatusPending, orderenum.OrderStatusPaid},
	},
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

	rule, ok := orderEventRules[event.Type]
	if !ok {
		return gofyconsumer.Permanent(fmt.Errorf("unsupported event type %q", event.Type))
	}
	target := rule.Target
	var payload orderEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return gofyconsumer.Permanent(fmt.Errorf("decode payload of %s: %w", event.Type, err))
	}
	if payload.OrderID == 0 {
		return gofyconsumer.Permanent(fmt.Errorf("%s payload: order_id is required", event.Type))
	}

	// The transition condition lives in the UPDATE, not in a read-modify-write:
	// two concurrent handlers would both pass a SELECT and both write. Only rows
	// whose current status is a legal source move, and a redelivery to a status
	// the order already has touches nothing.
	// 迁移条件写在 UPDATE 里，而不是先读再写：两个并发 handler 会同时通过 SELECT 然后都
	// 写入。只有当前状态属于合法前置集合的行会变，重复投递到已处于目标状态的行也不会改任何东西。
	sources := make([]string, 0, len(rule.Sources))
	for _, source := range rule.Sources {
		sources = append(sources, string(source))
	}
	res := db.WithContext(ctx).Model(&model.Order{}).
		Where("id = ? AND status IN ?", payload.OrderID, sources).
		Update("status", string(target))
	if res.Error != nil {
		return fmt.Errorf("update order %d to %s: %w", payload.OrderID, target, res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}

	// Zero rows has three causes, and they must not be confused: the order is not
	// there yet (retry), it is already in the target state (idempotent success),
	// or the event is stale and the transition is not allowed (ignore — retrying
	// cannot change the outcome). Only a read can tell them apart.
	// 0 行有三种原因，必须分清：订单还没落库（重试）、已经是目标状态（幂等成功）、事件过期
	// 且迁移不被允许（忽略——重试改变不了结果）。只有读一次才能区分。
	var current model.Order
	err := db.WithContext(ctx).Model(&model.Order{}).
		Select("status").
		Where("id = ?", payload.OrderID).
		Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Out-of-order delivery is normal on several partitions: retry until
		// the order row appears or the attempt budget runs out.
		// 多分区上乱序投递是常态：重试到订单行出现，或尝试次数用完为止。
		return fmt.Errorf("order %d does not exist yet", payload.OrderID)
	}
	if err != nil {
		return fmt.Errorf("lookup order %d: %w", payload.OrderID, err)
	}
	if current.Status == string(target) {
		return nil
	}
	// The event is older than the state it would overwrite: ack it, because a
	// retry would arrive at the same answer, and leave a line that says why the
	// order did not move.
	// 这条事件比它要覆盖的状态更旧：确认它——重试会得到同一个答案——并留一行日志说明订单
	// 为什么没有变化。
	if rt.Logger != nil {
		rt.Logger.Warn("order event ignored: transition not allowed",
			zap.String("event_type", event.Type),
			zap.String("event_id", event.ID),
			zap.Uint64("order_id", payload.OrderID),
			zap.String("from", current.Status),
			zap.String("to", string(target)),
		)
	}
	return nil
}
