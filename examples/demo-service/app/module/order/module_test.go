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

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/gofytest"
	"github.com/bingcool/gofy/support/registry"
	"github.com/bingcool/gofy/support/sqlite"
	appdb "github.com/yourorg/demo-service/app/io/db"
	"github.com/yourorg/demo-service/app/middleware"
	"github.com/yourorg/demo-service/app/module/order"
	"github.com/yourorg/demo-service/app/module/order/model"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"github.com/yourorg/demo-service/app/module/order/service"
	product "github.com/yourorg/demo-service/app/module/product"
	productmodel "github.com/yourorg/demo-service/app/module/product/model"
	productrepo "github.com/yourorg/demo-service/app/module/product/repository"
	productservice "github.com/yourorg/demo-service/app/module/product/service"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gofy/gofy-interface-service/appinterface/demoservice/orderdto"
	"gofy/gofy-interface-service/appinterface/demoservice/orderenum"
	"gofy/gofy-interface-service/appinterface/demoservice/orderresponse"
	"gofy/gofy-interface-service/contracts/common/paging"
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
	if err := conn.AutoMigrate(&model.Order{}, &productmodel.Product{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seeds := []any{
		&model.Order{ID: 1, UserID: 100, Amount: 9900, Status: string(orderenum.OrderStatusPaid)},
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

// newTestService 装配一个跑在 SQLite 上的订单服务，跨模块依赖注入真实产品
// service（它实现 innerapi.ProductAPI）。仓库从 app/io 句柄取连接，测试把内存库
// 发布成当前句柄，结束后恢复。
func newTestService(t *testing.T) *service.OrderService {
	t.Helper()
	conn := newTestDB(t)
	// GORM 执行 SQL 用的是 Statement.ConnPool（Open 时从 Config 复制一份），
	// 只改 Config.ConnPool 不会生效，两处都要替换。
	pool := panicPool{ConnPool: conn.ConnPool}
	conn.ConnPool = pool
	conn.Statement.ConnPool = pool
	prev := appdb.Db
	appdb.Db = conn
	t.Cleanup(func() { appdb.Db = prev })
	products := productservice.NewProductService(productservice.Dependencies{
		Products: &productrepo.ProductRepository{},
		Logger:   zap.NewNop(),
	})
	return service.NewOrderService(service.Dependencies{
		Orders:   &repository.OrderRepository{},
		Products: products,
		Config:   service.NewOrderConfigService(zap.NewNop()),
		Logger:   zap.NewNop(),
	})
}

func newTestApp(t *testing.T) *gofytest.App {
	t.Helper()
	return newTestAppWithConfig(t, middleware.JWTConfig{})
}

// newTestAppWithConfig 用与 demo-service 相同的装配方式启动测试 App：
// 应用层中间件通过 WithMiddlewareRegistrar 注册进框架创建的共享注册表。
func newTestAppWithConfig(t *testing.T, jwtCfg middleware.JWTConfig) *gofytest.App {
	t.Helper()
	svc := newTestService(t)
	module := order.NewModule(svc, zap.NewNop())
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
	svc := newTestService(t)
	module := order.NewModule(svc, zap.New(core))
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
	svc := newTestService(t)
	module := order.NewModule(svc, zap.New(core))

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

// 组合根每次装配新建共享注册表，产品工厂先登记 innerapi、订单工厂构造期取用：
// 连续装配两次不撞名字，缺登记或缺数据库时启动失败。
func TestModuleFactoryBuildsFreshRegistryPerAssembly(t *testing.T) {
	rt := &gofy.Runtime{DB: &gorm.DB{}, Logger: zap.NewNop()}
	for i := 0; i < 2; i++ {
		reg := registry.NewRegistry()
		if _, err := product.NewModuleFactory(reg)(rt); err != nil {
			t.Fatalf("assembly %d product: %v", i, err)
		}
		module, err := order.NewModuleFactory(reg)(rt)
		if err != nil {
			t.Fatalf("assembly %d order: %v", i, err)
		}
		if module.Name() != "order" {
			t.Fatalf("module name = %q, want order", module.Name())
		}
	}
	// 缺产品 innerapi 登记时，订单工厂构造期取用失败。
	if _, err := order.NewModuleFactory(registry.NewRegistry())(rt); err == nil {
		t.Fatal("order factory should fail when product innerapi is not registered")
	}
	// 缺数据库时直接失败。
	if _, err := order.NewModuleFactory(registry.NewRegistry())(&gofy.Runtime{Logger: zap.NewNop()}); err == nil {
		t.Fatal("a runtime without database should fail")
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
