package order_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy"
	gofyadapter "github.com/bingcool/gofy-adapter/adapter/gofy"
	"github.com/bingcool/gofy-interface-api/contracts/common/paging"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/support/sqlite"
	goredis "github.com/redis/go-redis/v9"
	"github.com/yourorg/demo-service/app/appio"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/app/container"
	"github.com/yourorg/demo-service/app/middleware"
	"github.com/yourorg/demo-service/app/module/order"
	"github.com/yourorg/demo-service/app/module/order/model/ordermodel"
	"github.com/yourorg/demo-service/app/module/order/service"
	"github.com/yourorg/demo-service/app/module/product/model/productmodel"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderdto"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderenum"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

// newTestDB 用纯 Go SQLite 建一个含订单表和产品表的内存库，并塞入 HTTP 用例
// 需要的固定数据：订单 1（用户 100、已支付）、产品 90001 上架 / 90002 下架。
// repository 不再声明接口，测试直接跑真实 SQL，不连外部 MySQL。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	conn, err := sqlite.New(context.Background(), sqlite.Config{
		DSN:      ":memory:",
		LogLevel: "silent",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 先取出 *sql.DB 再包连接池：包装后的 ConnPool 不再是 *sql.DB，
	// sqlite.Close 会取不到底层连接。
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	if err := conn.AutoMigrate(&ordermodel.Order{}, &productmodel.Product{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seeds := []any{
		&ordermodel.Order{ID: 1, UserID: 100, Amount: 9900, Status: string(orderenum.OrderStatusPaid)},
		// 用户 102 的两笔订单固定创建时间，供列表筛选用例断言时间窗。
		&ordermodel.Order{ID: 2, UserID: 102, Amount: 1000, Status: string(orderenum.OrderStatusPaid), CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		&ordermodel.Order{ID: 3, UserID: 102, Amount: 2000, Status: string(orderenum.OrderStatusPending), CreatedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},
		&productmodel.Product{ID: 90001, Name: "Gofy Demo SKU", Price: 1000, Stock: 999, Status: productrepo.StatusOnSale},
		&productmodel.Product{ID: 90002, Name: "Retired SKU", Price: 500, Stock: 0, Status: 0},
	}
	for _, seed := range seeds {
		if err := conn.Create(seed).Error; err != nil {
			t.Fatalf("seed %T: %v", seed, err)
		}
	}
	return conn
}

// panicPool 在驱动层对 id=9999 的查询 panic，用来验证 Recovery 中间件返回 500
// 且不泄漏内部信息。repository 是具体类型，panic 只能从连接池注入。
type panicPool struct {
	gorm.ConnPool
}

func (p panicPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	for _, arg := range args {
		if fmt.Sprint(arg) == "9999" {
			panic("boom: database password is hunter2")
		}
	}
	return p.ConnPool.QueryContext(ctx, query, args...)
}

// BeginTx 把开事务委托给底层连接池。内嵌的 gorm.ConnPool 接口不含 BeginTx，
// 不显式补上会让 GORM 的 Begin 落到 default 分支报 ErrInvalidTransaction；
// Create 走事务，因此测试池必须能开事务。事务内查询直接用返回的 *sql.Tx，
// 不再经过本包装，panic 注入只作用于非事务查询（如 GET /orders/9999）。
func (p panicPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.ConnPool.(gorm.TxBeginner).BeginTx(ctx, opts)
}

// newTestServices 通过装配根在 SQLite 上构建订单与配置 service：容器同时构造
// 产品与订单 service，并把产品能力以 innerapi 接口注入订单 service。仓库从
// app/appio 句柄取连接，测试把内存库发布成当前句柄，结束后恢复。
func newTestServices(t *testing.T) (*service.OrderService, *service.OrderConfigService) {
	t.Helper()
	return newTestServicesWithConfig(t, nil)
}

// newTestServicesWithConfig 与 newTestServices 相同，但把业务配置交给装配根，
// 用来验证 config → container → service 这条链。
func newTestServicesWithConfig(t *testing.T, cfg *config.Config) (*service.OrderService, *service.OrderConfigService) {
	t.Helper()
	conn := newTestDB(t)
	// GORM 执行 SQL 用的是 Statement.ConnPool（Open 时从 Config 复制一份），
	// 只改 Config.ConnPool 不会生效，两处都要替换。
	pool := panicPool{ConnPool: conn.ConnPool}
	conn.ConnPool = pool
	conn.Statement.ConnPool = pool
	prev := appio.Current()
	appio.PublishDB(conn)
	t.Cleanup(func() { appio.Publish(prev) })
	ctr := container.NewContainer(cfg)
	ctr.Init(zap.NewNop())
	return ctr.OrderService(), ctr.OrderConfigService()
}

func newTestApp(t *testing.T) *gofytest.App {
	t.Helper()
	return newTestAppWithConfig(t, middleware.JWTConfig{})
}

// testRedisClient 返回连到本测试 miniredis 的客户端，供限流计数使用。
func testRedisClient(t *testing.T) *goredis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// newTestAppWithConfig 用与 demo-service 相同的装配方式启动测试 App：
// 应用层中间件通过 WithMiddlewareRegistrar 注册进框架创建的共享注册表。
func newTestAppWithConfig(t *testing.T, jwtCfg middleware.JWTConfig) *gofytest.App {
	t.Helper()
	return newTestAppWith(t, jwtCfg, gofyadapter.BindOptions{})
}

// newTestAppWithBindOptions 与 newTestAppWithConfig 相同，但把装配根提供的
// 绑定选项（例如按契约生成的限流回调）交给模块。
func newTestAppWithBindOptions(t *testing.T, bindOptions gofyadapter.BindOptions) *gofytest.App {
	t.Helper()
	return newTestAppWith(t, middleware.JWTConfig{}, bindOptions)
}

// newTestAppWithOrderConfig 用注入的订单配置启动测试 App，供配置链路的
// 端到端用例使用；其余装配与 newTestAppWith 相同。
func newTestAppWithOrderConfig(t *testing.T, orderCfg config.OrderConfig) *gofytest.App {
	t.Helper()
	orderService, orderConfigService := newTestServicesWithConfig(t, &config.Config{Order: orderCfg})
	module := order.NewModule(orderService, orderConfigService, zap.NewNop(), gofyadapter.BindOptions{})
	return gofytest.New(t,
		gofy.WithName("demo-service-test"),
		gofy.WithMiddlewareRegistrar(testRegistrar(zap.NewNop(), middleware.JWTConfig{})),
		gofy.WithModules(module),
	)
}

func newTestAppWith(t *testing.T, jwtCfg middleware.JWTConfig, bindOptions gofyadapter.BindOptions) *gofytest.App {
	t.Helper()
	orderService, orderConfigService := newTestServices(t)
	module := order.NewModule(orderService, orderConfigService, zap.NewNop(), bindOptions)
	return gofytest.New(t,
		gofy.WithName("demo-service-test"),
		gofy.WithMiddlewareRegistrar(testRegistrar(zap.NewNop(), jwtCfg)),
		gofy.WithModules(module),
	)
}

// testRegistrar 注册测试用的应用层中间件，与 main 里的 middlewareRegistrar
// 走同一条框架注册路径。
func testRegistrar(logger *zap.Logger, jwtCfg middleware.JWTConfig) gofy.MiddlewareRegistrar {
	return func(reg gofy.MiddlewareRegistration) error {
		return middleware.Register(reg.Registry, middleware.Deps{
			Logger:    logger,
			JWT:       jwtCfg,
			Responder: reg.Responder,
		})
	}
}

// signJWT 签一个 HS256 token：模块用框架 jwtauth 校验，测试只需合法签名。
func signJWT(t *testing.T, secret string, expires time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"sub":"tester","exp":%d}`, expires.Unix())),
	)
	unsigned := header + "." + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
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

// 配置从装配根一路走到 HTTP：order.auth_required 为 true 时，契约响应的
// auth_required 也必须是 true。中间没有任何 service 读文件或 import app/config。
func TestGetConfigUsesInjectedConfig(t *testing.T) {
	app := newTestAppWithOrderConfig(t, config.OrderConfig{AuthRequired: true})
	rr := app.GET("/api/v1/order/config")
	gofytest.Status(t, rr, http.StatusOK)

	resp := gofytest.JSON[orderresponse.GetConfigResponse](t, rr)
	if !resp.AuthRequired {
		t.Fatalf("config = %+v, want AuthRequired=true", resp)
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
	if body["code"] != float64(40001) || body["msg"] != "订单ID不能为空" {
		t.Fatalf("envelope = %v", body)
	}
	details, _ := body["details"].(map[string]any)
	if msg, _ := details["id"].(string); msg != "订单ID不能为空" {
		t.Fatalf("details = %v", details)
	}
}

func TestListOrderValidationMessageContract(t *testing.T) {
	app := newTestApp(t)
	rr := app.GET("/api/v1/order/orders")
	gofytest.Status(t, rr, http.StatusBadRequest)

	body := gofytest.RawJSON(t, rr)
	if body["code"] != float64(40001) || body["msg"] != "用户ID不能为空" {
		t.Fatalf("envelope = %v", body)
	}
	details, _ := body["details"].(map[string]any)
	if msg, _ := details["user_id"].(string); msg != "用户ID不能为空" {
		t.Fatalf("details = %v", details)
	}
}

// 限流完全由契约驱动：ListOrders 声明 100 次/分钟、按用户计数。测试 App 的
// JWT 未启用，取不到身份时退回客户端 IP，所以同一测试客户端的第 101 次请求
// 返回 429；GetOrder 有独立额度，不受影响。
func TestRateLimitFollowsContract(t *testing.T) {
	client := testRedisClient(t)
	rateLimiter := middleware.RateLimiter{RedisClient: func(context.Context, string) (*goredis.Client, error) {
		return client, nil
	}}.Callback()
	app := newTestAppWithBindOptions(t, gofyadapter.BindOptions{RateLimit: rateLimiter})

	for i := 0; i < 100; i++ {
		gofytest.Status(t, app.GET("/api/v1/order/orders?user_id=100"), http.StatusOK)
	}
	rr := app.GET("/api/v1/order/orders?user_id=100")
	gofytest.Status(t, rr, http.StatusTooManyRequests)
	if body := gofytest.RawJSON(t, rr); body["code"] != float64(42900) {
		t.Fatalf("envelope = %v", body)
	}
	if got := rr.Header().Get("X-RateLimit-Limit"); got != "100" {
		t.Fatalf("X-RateLimit-Limit = %q, want 100", got)
	}

	gofytest.Status(t, app.GET("/api/v1/order/orders/1"), http.StatusOK)
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

func TestCreateOrderRejectsUnknownOrOffSaleProduct(t *testing.T) {
	app := newTestApp(t)

	rr := app.POSTJSON("/api/v1/order/orders", map[string]any{
		"user_id": 100,
		"amount":  500,
		"items":   []map[string]any{{"sku_id": 404, "num": 1}},
	})
	gofytest.Status(t, rr, http.StatusBadRequest)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40000) {
		t.Fatalf("unknown sku code = %v, want 40000", code)
	}

	rr = app.POSTJSON("/api/v1/order/orders", map[string]any{
		"user_id": 100,
		"amount":  500,
		"items":   []map[string]any{{"sku_id": 90002, "num": 1}},
	})
	gofytest.Status(t, rr, http.StatusBadRequest)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40000) {
		t.Fatalf("off-sale sku code = %v, want 40000", code)
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

	rr = app.GET("/api/v1/order/orders?user_id=100&page_size=10001")
	gofytest.Status(t, rr, http.StatusBadRequest)
	details, _ := gofytest.RawJSON(t, rr)["details"].(map[string]any)
	if msg, _ := details["page_size"].(string); msg == "" {
		t.Fatalf("details = %v", details)
	}
}

func TestListOrderFilters(t *testing.T) {
	app := newTestApp(t)

	cases := []struct {
		name  string
		query string
		total int
		id    uint64
	}{
		{"status", "user_id=102&status=pending", 1, 3},
		{"created from", "user_id=102&created_from=2026-01-15T00:00:00Z", 1, 3},
		{"created to", "user_id=102&created_to=2026-01-15T00:00:00Z", 1, 2},
		{"both bounds", "user_id=102&created_from=2026-01-01T00:00:00Z&created_to=2026-01-31T00:00:00Z", 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := app.GET("/api/v1/order/orders?" + tc.query)
			gofytest.Status(t, rr, http.StatusOK)
			list := gofytest.JSON[paging.ListData[orderdto.OrderDTO]](t, rr)
			if list.Total != int64(tc.total) || len(list.List) != tc.total {
				t.Fatalf("list = %+v, want total %d", list, tc.total)
			}
			if list.List[0].ID.Uint64() != tc.id {
				t.Fatalf("first id = %d, want %d", list.List[0].ID.Uint64(), tc.id)
			}
		})
	}

	rr := app.GET("/api/v1/order/orders?user_id=102&created_from=2026-02-01T00:00:00Z&created_to=2026-01-01T00:00:00Z")
	gofytest.Status(t, rr, http.StatusBadRequest)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40000) {
		t.Fatalf("inverted window code = %v, want 40000", code)
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

func TestCancelWithJWT(t *testing.T) {
	const secret = "test-jwt-secret"
	app := newTestAppWithConfig(t, middleware.JWTConfig{Secret: secret, RequireExpiry: true})

	rr := app.DELETE("/api/v1/order/orders/1")
	gofytest.Status(t, rr, http.StatusUnauthorized)
	if code := gofytest.RawJSON(t, rr)["code"]; code != float64(40100) {
		t.Fatalf("code = %v, want 40100", code)
	}

	token := signJWT(t, secret, time.Now().Add(time.Hour))
	rr = app.DELETE("/api/v1/order/orders/1", "Authorization", "Bearer "+token)
	gofytest.Status(t, rr, http.StatusNoContent)

	rr = app.DELETE("/api/v1/order/orders/999", "Authorization", "Bearer "+token)
	gofytest.Status(t, rr, http.StatusNotFound)

	rr = app.GET("/api/v1/order/orders/1", "Authorization", "Bearer "+signJWT(t, "wrong-secret", time.Now().Add(time.Hour)))
	gofytest.Status(t, rr, http.StatusUnauthorized)
}

func TestContractMiddlewareNamesAreResolved(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	orderService, orderConfigService := newTestServices(t)
	module := order.NewModule(orderService, orderConfigService, zap.New(core), gofyadapter.BindOptions{})
	app := gofytest.New(t,
		gofy.WithName("demo-service-test"),
		gofy.WithMiddlewareRegistrar(testRegistrar(zap.New(core), middleware.JWTConfig{})),
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

// 同一个 module 实例被装配两次（等价于 Nacos 热更新重建路由）时，每次装配
// App 都新建一份注册表并重新执行 registrar，因此不会撞上「别名已注册」错误。
func TestModuleRegisterBuildsFreshRegistry(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	orderService, orderConfigService := newTestServices(t)
	module := order.NewModule(orderService, orderConfigService, zap.New(core), gofyadapter.BindOptions{})

	for i := 0; i < 2; i++ {
		app := gofytest.New(t,
			gofy.WithName("demo-service-test"),
			gofy.WithMiddlewareRegistrar(testRegistrar(zap.New(core), middleware.JWTConfig{})),
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
