package group1

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofyworker "github.com/bingcool/gofy/support/worker"

	"github.com/bingcool/gofy/core"
	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
)

// newOrderHandlerContext builds the runtime the handler reads its database from.
// One in-memory SQLite is enough: the transition rules are single UPDATE
// statements, and their order is what these tests are about.
// newOrderHandlerContext 构造 handler 读取数据库所需的 runtime。一个内存 SQLite 就够：
// 迁移规则都是单条 UPDATE，测试关心的是它们的约束。
func newOrderHandlerContext(t *testing.T) (context.Context, *gorm.DB) {
	t.Helper()
	dsn := "file:order_handler?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.Order{}); err != nil {
		t.Fatalf("migrate orders: %v", err)
	}
	// Every test case gets a clean table; the shared cache keeps one database
	// per DSN, so DELETE is the reset.
	// 每个用例都要干净的表；共享缓存让同一个 DSN 只有一个库，因此用 DELETE 重置。
	if err := db.Exec("DELETE FROM orders").Error; err != nil {
		t.Fatalf("reset orders: %v", err)
	}

	rt := core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop(), DB: db})
	return gofyworker.WithRuntime(context.Background(), rt), db
}

func seedOrder(t *testing.T, db *gorm.DB, id uint64, status orderenum.OrderStatus) {
	t.Helper()
	if err := db.Create(&model.Order{ID: id, UserID: 7, Amount: 100, Status: string(status)}).Error; err != nil {
		t.Fatalf("seed order %d: %v", id, err)
	}
}

func orderStatusOf(t *testing.T, db *gorm.DB, id uint64) string {
	t.Helper()
	var order model.Order
	if err := db.First(&order, id).Error; err != nil {
		t.Fatalf("read order %d: %v", id, err)
	}
	return order.Status
}

func orderEvent(t *testing.T, eventType string, id uint64) gofyconsumer.Envelope {
	t.Helper()
	payload, err := json.Marshal(orderEventPayload{OrderID: id, UserID: 7, Reason: "test"})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return gofyconsumer.Envelope{ID: eventType + "-event", Type: eventType, Payload: payload}
}

// TestApplyOrderEventTransitions pins the state machine P2-07 asked for:
// pending may become paid, pending and paid may become cancelled, and nothing
// may become paid again after cancellation. The last row is the one the old
// "update where status differs" condition got wrong.
// TestApplyOrderEventTransitions 固定 P2-07 要求的状态机：pending 可以变 paid，pending 与
// paid 可以变 cancelled，而取消之后不能再变回 paid。最后一行正是旧的"状态不同就更新"写错的地方。
func TestApplyOrderEventTransitions(t *testing.T) {
	cases := []struct {
		name   string
		from   orderenum.OrderStatus
		event  string
		want   orderenum.OrderStatus
		reason string
	}{
		{name: "pending to paid", from: orderenum.OrderStatusPending, event: "order.paid", want: orderenum.OrderStatusPaid},
		{name: "paid is final for paid", from: orderenum.OrderStatusPaid, event: "order.paid", want: orderenum.OrderStatusPaid},
		{name: "pending to cancelled", from: orderenum.OrderStatusPending, event: "order.cancelled", want: orderenum.OrderStatusCancelled},
		{name: "paid to cancelled", from: orderenum.OrderStatusPaid, event: "order.cancelled", want: orderenum.OrderStatusCancelled},
		{
			name:   "cancelled never returns to paid",
			from:   orderenum.OrderStatusCancelled,
			event:  "order.paid",
			want:   orderenum.OrderStatusCancelled,
			reason: "a stale order.paid event must not reopen a cancelled order",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db := newOrderHandlerContext(t)
			seedOrder(t, db, 1, tc.from)

			if err := applyOrderEvent(ctx, orderEvent(t, tc.event, 1)); err != nil {
				t.Fatalf("applyOrderEvent: %v", err)
			}
			if got := orderStatusOf(t, db, 1); got != string(tc.want) {
				t.Fatalf("status = %s, want %s%s", got, tc.want, prefixReason(tc.reason))
			}
		})
	}
}

func prefixReason(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

// TestApplyOrderEventRetriesUnknownOrder keeps the one case that must stay
// retryable: an event that arrives before its order row exists is not stale, it
// is early, so the handler returns a normal error and the broker redelivers.
// TestApplyOrderEventRetriesUnknownOrder 保住必须保持可重试的那种情况：订单行还不存在时
// 到达的事件不是过期，而是来早了，因此 handler 返回普通错误，由 broker 重投。
func TestApplyOrderEventRetriesUnknownOrder(t *testing.T) {
	ctx, _ := newOrderHandlerContext(t)

	err := applyOrderEvent(ctx, orderEvent(t, "order.paid", 404))
	if err == nil {
		t.Fatal("applyOrderEvent succeeded for an order that does not exist")
	}
	if gofyconsumer.IsPermanent(err) {
		t.Fatalf("error = %v, want a retryable one: the create may still be in flight", err)
	}
}

// TestApplyOrderEventRejectsUnknownEventType keeps the permanent branch pinned:
// an event this build does not know is not going to become known on a retry.
// TestApplyOrderEventRejectsUnknownEventType 固定永久失败分支：本构建不认识的事件不会因为
// 重试而变得认识。
func TestApplyOrderEventRejectsUnknownEventType(t *testing.T) {
	ctx, _ := newOrderHandlerContext(t)

	err := applyOrderEvent(ctx, orderEvent(t, "order.refunded", 1))
	if !gofyconsumer.IsPermanent(err) {
		t.Fatalf("error = %v, want Permanent for an unsupported event type", err)
	}
}
