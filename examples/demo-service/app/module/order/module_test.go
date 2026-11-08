package order_test

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/gofytest"
	"github.com/yourorg/demo-service/app/module/order"
	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"github.com/yourorg/demo-service/app/module/order/service"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gofy/gofy-interface-service/appinterface/demoservice/orderdto"
	"gofy/gofy-interface-service/appinterface/demoservice/orderenum"
	"gofy/gofy-interface-service/appinterface/demoservice/orderresponse"
	"gofy/gofy-interface-service/contracts/common/paging"
)

type fakeRepository struct {
	mu     sync.Mutex
	nextID uint64
	orders map[uint64]*model.Order
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{nextID: 1, orders: map[uint64]*model.Order{
		1: {ID: 1, UserID: 100, Amount: 9900, Status: "paid"},
	}}
}

func (f *fakeRepository) FindByID(_ context.Context, id uint64) (*model.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == 9999 {
		panic("boom: database password is hunter2")
	}
	o, ok := f.orders[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	cp := *o
	return &cp, nil
}

func (f *fakeRepository) Create(_ context.Context, o *model.Order) (*model.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o.ID = f.nextID
	f.nextID++
	cp := *o
	f.orders[o.ID] = &cp
	return o, nil
}

func (f *fakeRepository) ListByUser(_ context.Context, userID uint64, offset, limit int) ([]model.Order, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []model.Order
	for _, o := range f.orders {
		if o.UserID == userID {
			all = append(all, *o)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	total := int64(len(all))
	if offset >= len(all) {
		return []model.Order{}, total, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}

func (f *fakeRepository) UpdateStatus(_ context.Context, id uint64, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[id]
	if !ok {
		return repository.ErrNotFound
	}
	o.Status = status
	return nil
}

func newTestApp(t *testing.T) *gofytest.App {
	t.Helper()
	return newTestAppWithConfig(t, order.Config{})
}

func newTestAppWithConfig(t *testing.T, cfg order.Config) *gofytest.App {
	t.Helper()
	svc := service.NewOrderService(service.Deps{
		Orders: newFakeRepository(),
		Logger: zap.NewNop(),
	})
	module := order.NewModule(cfg, svc, zap.NewNop())
	return gofytest.New(t,
		gofy.WithName("demo-service-test"),
		gofy.WithModules(module),
	)
}

func TestGetOrderContract(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/order/orders/1")
	gofytest.Status(t, rr, http.StatusOK)

	body := gofytest.RawJSON(t, rr)
	if body["code"] != float64(0) || body["msg"] != "success" {
		t.Fatalf("envelope = %v", body)
	}
	reqID, _ := body["req_id"].(string)
	if reqID == "" {
		t.Fatal("response envelope has no req_id")
	}
	if got := rr.Header().Get("X-Trace-ID"); got != reqID {
		t.Fatalf("header req_id = %q, body req_id = %q", got, reqID)
	}

	resp := gofytest.JSON[orderresponse.GetOrderResponse](t, rr)
	if resp.Order == nil {
		t.Fatal("order is nil")
	}
	if resp.Order.ID.Uint64() != 1 || resp.Order.UserID.Uint64() != 100 ||
		resp.Order.Amount != 9900 || resp.Order.Status != orderenum.OrderStatusPaid {
		t.Fatalf("order = %+v", resp.Order)
	}
}

func TestGetConfigContract(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/order/config")
	gofytest.Status(t, rr, http.StatusOK)

	resp := gofytest.JSON[orderresponse.GetConfigResponse](t, rr)
	if resp.AuthRequired {
		t.Fatalf("config = %+v", resp)
	}
}

func TestGetOrderRequestIDIsPropagated(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/order/orders/1", "X-Trace-ID", "client-request-id")
	gofytest.Status(t, rr, http.StatusOK)
	if got := gofytest.RawJSON(t, rr)["req_id"]; got != "client-request-id" {
		t.Fatalf("req_id = %v, want client-request-id", got)
	}
}

func TestGetOrderValidationContract(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/order/orders/0")
	gofytest.Status(t, rr, http.StatusBadRequest)

	body := gofytest.RawJSON(t, rr)
	if body["code"] != float64(40001) || body["msg"] != "invalid argument" {
		t.Fatalf("envelope = %v", body)
	}
	details, _ := body["details"].(map[string]any)
	if msg, _ := details["id"].(string); msg == "" {
		t.Fatalf("details = %v", details)
	}
}

func TestGetOrderNotFoundContract(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/order/orders/999")
	gofytest.Status(t, rr, http.StatusNotFound)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40401) {
		t.Fatalf("code = %v, want 40401", code)
	}
}

func TestPanicContract(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/order/orders/9999")
	gofytest.Status(t, rr, http.StatusInternalServerError)

	body := gofytest.RawJSON(t, rr)
	if body["code"] != float64(50000) || body["msg"] != "internal server error" {
		t.Fatalf("envelope = %v", body)
	}
	raw := rr.Body.String()
	if strings.Contains(raw, "hunter2") || strings.Contains(raw, "goroutine") {
		t.Fatalf("panic response leaked internals: %s", raw)
	}
}

func TestCreateOrderContract(t *testing.T) {
	app := newTestApp(t)

	rr := app.POSTJSON("/api/v1/order/orders", map[string]any{})
	gofytest.Status(t, rr, http.StatusBadRequest)
	details, _ := gofytest.RawJSON(t, rr)["details"].(map[string]any)
	for _, field := range []string{"user_id", "amount", "items"} {
		if msg, _ := details[field].(string); msg == "" {
			t.Fatalf("details missing %q: %v", field, details)
		}
	}

	rr = app.POSTJSON("/api/v1/order/orders", map[string]any{
		"user_id": 100,
		"amount":  500,
		"items":   []map[string]any{{"sku_id": 90001, "num": 1}},
	})
	gofytest.Status(t, rr, http.StatusOK)
	created := gofytest.JSON[orderresponse.CreateOrderResponse](t, rr)
	if created.Order == nil {
		t.Fatal("created order is nil")
	}
	if created.Order.ID.Uint64() == 0 || created.Order.UserID.Uint64() != 100 ||
		created.Order.Amount != 500 || created.Order.Status != orderenum.OrderStatusPending {
		t.Fatalf("created = %+v", created.Order)
	}
}

func TestListOrderDefaultsAndPagination(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/order/orders?user_id=100")
	gofytest.Status(t, rr, http.StatusOK)

	list := gofytest.JSON[paging.ListData[orderdto.OrderDTO]](t, rr)
	if list.Total != 1 || len(list.List) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if list.List[0].ID.Uint64() != 1 || list.List[0].Status != orderenum.OrderStatusPaid {
		t.Fatalf("first order = %+v", list.List[0])
	}

	rr = app.GET("/api/v1/order/orders?user_id=100&page_size=101")
	gofytest.Status(t, rr, http.StatusBadRequest)
	details, _ := gofytest.RawJSON(t, rr)["details"].(map[string]any)
	if msg, _ := details["page_size"].(string); msg == "" {
		t.Fatalf("details = %v", details)
	}
}

func TestCancelWithoutTokenConfigured(t *testing.T) {
	app := newTestApp(t)

	rr := app.DELETE("/api/v1/order/orders/1")
	gofytest.Status(t, rr, http.StatusNoContent)
	if rr.Body.Len() != 0 {
		t.Fatalf("204 body = %q", rr.Body.String())
	}

	rr = app.DELETE("/api/v1/order/orders/999")
	gofytest.Status(t, rr, http.StatusNotFound)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40401) {
		t.Fatalf("code = %v, want 40401", code)
	}
}

func TestCancelWithTokenConfigured(t *testing.T) {
	app := newTestAppWithConfig(t, order.Config{AuthToken: "test-token"})

	rr := app.DELETE("/api/v1/order/orders/1")
	gofytest.Status(t, rr, http.StatusUnauthorized)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40100) {
		t.Fatalf("code = %v, want 40100", code)
	}

	rr = app.DELETE("/api/v1/order/orders/1", "Authorization", "Bearer test-token")
	gofytest.Status(t, rr, http.StatusNoContent)

	rr = app.DELETE("/api/v1/order/orders/999", "Authorization", "Bearer test-token")
	gofytest.Status(t, rr, http.StatusNotFound)

	rr = app.GET("/api/v1/order/orders/1")
	gofytest.Status(t, rr, http.StatusUnauthorized)
}

func TestContractMiddlewareNamesAreResolved(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	svc := service.NewOrderService(service.Deps{
		Orders: newFakeRepository(),
		Logger: zap.NewNop(),
	})
	module := order.NewModule(order.Config{}, svc, zap.New(core))
	app := gofytest.New(t,
		gofy.WithName("demo-service-test"),
		gofy.WithModules(module),
	)

	// 分组名 "audit" 来自 orderroute.Orders 的 group.Use。
	gofytest.Status(t, app.GET("/api/v1/order/orders/1"), http.StatusOK)
	entries := logs.FilterMessage("order audit").All()
	if len(entries) != 1 || entries[0].ContextMap()["scope"] != "group" {
		t.Fatalf("group audit entries = %+v", entries)
	}

	// CreateOrder 另外声明了路由级名字 "order-audit"，它在分组中间件内层，
	// 因此先于分组审计落日志。
	gofytest.Status(t, app.POSTJSON("/api/v1/order/orders", map[string]any{
		"user_id": 100,
		"amount":  500,
		"items":   []map[string]any{{"sku_id": 90001, "num": 1}},
	}), http.StatusOK)
	entries = logs.FilterMessage("order audit").All()
	if len(entries) != 3 {
		t.Fatalf("audit entries = %d, want 3", len(entries))
	}
	if got := entries[1].ContextMap()["scope"]; got != "route" {
		t.Fatalf("second audit scope = %v, want route", got)
	}
	if got := entries[2].ContextMap()["scope"]; got != "group" {
		t.Fatalf("third audit scope = %v, want group", got)
	}
}

// 同一个 module 实例被装配两次（等价于 Nacos 热更新重建路由）时，每次
// Register 都新建一份中间件注册表，因此不会撞上「别名已注册」错误。
func TestModuleRegisterBuildsFreshRegistry(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	svc := service.NewOrderService(service.Deps{
		Orders: newFakeRepository(),
		Logger: zap.NewNop(),
	})
	module := order.NewModule(order.Config{}, svc, zap.New(core))

	for i := 0; i < 2; i++ {
		app := gofytest.New(t,
			gofy.WithName("demo-service-test"),
			gofy.WithModules(module),
		)
		gofytest.Status(t, app.GET("/api/v1/order/orders/1"), http.StatusOK)
	}
	if got := len(logs.FilterMessage("order audit").All()); got != 2 {
		t.Fatalf("audit entries = %d, want one per assembly", got)
	}
}

func TestVersionAndStopDoNotConnectToDatabase(t *testing.T) {
	app, err := gofy.New(
		gofy.WithName("demo-service-test"),
		gofy.WithDatabase(gofy.DatabaseConfig{
			DSN:         "invalid:invalid@tcp(127.0.0.1:1)/missing?timeout=50ms",
			PingTimeout: time.Millisecond,
		}),
		gofy.WithPIDFile(t.TempDir()+"/missing.pid"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.ExecuteArgs(context.Background(), []string{"version"}); err != nil {
		t.Fatalf("version: %v", err)
	}
	if err := app.ExecuteArgs(context.Background(), []string{"version", "--json"}); err != nil {
		t.Fatalf("version --json: %v", err)
	}
	err = app.ExecuteArgs(context.Background(), []string{"stop", "--force=1"})
	if !errors.Is(err, gofy.ErrNotRunning) {
		t.Fatalf("stop error = %v, want ErrNotRunning", err)
	}
}
