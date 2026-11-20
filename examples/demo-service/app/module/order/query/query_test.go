package query_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/sqlite"
	"github.com/bingcool/gofy/support/sqlmodel"
	"github.com/yourorg/demo-service/app/appio"
	"github.com/yourorg/demo-service/app/module/order/query"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 下面的 fixture 只用于测试建表：query 包不导出 model，也不依赖各模块的 model，
// 所以测试用本地结构体复刻三张表的列，schema 与 migrations/0004_order_items.sql
// 保持一致。列名故意用 gorm 的默认命名，验证读模型的 column tag 与真实表对得上。
type orderFixture struct {
	ID        uint64 `gorm:"primaryKey"`
	UserID    uint64
	Amount    int64
	Status    string `gorm:"size:32"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (orderFixture) TableName() string { return "orders" }

type orderItemFixture struct {
	ID        uint64 `gorm:"primaryKey"`
	OrderID   uint64
	ProductID uint64
	Quantity  int
	UnitPrice int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (orderItemFixture) TableName() string { return "order_items" }

type productFixture struct {
	ID        uint64 `gorm:"primaryKey"`
	Name      string `gorm:"size:128"`
	Price     int64
	Stock     int
	Status    int8
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (productFixture) TableName() string { return "products" }

// newQueries builds an in-memory SQLite database with the three tables, seeds it
// and publishes it as the app/appio generation, so the query layer resolves the
// connection through the same path as production code.
func newQueries(t *testing.T) (*query.Queries, *gorm.DB) {
	t.Helper()
	conn, err := sqlite.New(context.Background(), sqlite.Config{
		DSN:      ":memory:",
		LogLevel: "silent",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlite.Close(conn); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	if err := conn.AutoMigrate(&productFixture{}, &orderFixture{}, &orderItemFixture{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	products := []productFixture{
		{ID: 1, Name: "Gofy T-Shirt", Price: 9900, Stock: 120, Status: 1},
		{ID: 2, Name: "Gofy Mug", Price: 3900, Stock: 80, Status: 1},
		{ID: 3, Name: "Gofy Sticker Pack", Price: 1500, Stock: 500, Status: 1},
		{ID: 4, Name: "Retired Mug", Price: 3900, Stock: 0, Status: 0},
	}
	orders := []orderFixture{
		{ID: 900001, UserID: 90001, Amount: 23700, Status: "paid"},
		{ID: 900002, UserID: 90001, Amount: 1500, Status: "pending"},
		{ID: 900003, UserID: 90002, Amount: 5000, Status: "cancelled"},
	}
	items := []orderItemFixture{
		{ID: 900001, OrderID: 900001, ProductID: 1, Quantity: 2, UnitPrice: 9900},
		{ID: 900002, OrderID: 900001, ProductID: 2, Quantity: 1, UnitPrice: 3900},
		{ID: 900003, OrderID: 900002, ProductID: 3, Quantity: 1, UnitPrice: 1500},
		// 悬空明细：产品 99 不存在，内连接必须把它排除。
		{ID: 900004, OrderID: 900001, ProductID: 99, Quantity: 1, UnitPrice: 100},
	}
	for _, seed := range []any{&products, &orders, &items} {
		if err := conn.Create(seed).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	prev := appio.Current()
	appio.PublishDB(conn)
	t.Cleanup(func() { appio.Publish(prev) })

	// 与 config.yaml 的 tenant.enabled=false 一致：测试里的连接没有装租户插件，
	// 解析器也必须关着，否则构造器路径会比它查询的连接多一层过滤。
	return query.New(sqlmodel.New(sqlmodel.Options{Disabled: true})), conn
}

func TestListOrderLines(t *testing.T) {
	q, _ := newQueries(t)

	rows, err := q.ListOrderLines(context.Background(), 900001)
	if err != nil {
		t.Fatalf("ListOrderLines: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (the dangling product must be excluded): %+v", len(rows), rows)
	}
	first := rows[0]
	if first.OrderID != 900001 || first.UserID != 90001 || first.Status != "paid" {
		t.Fatalf("first row header = %+v", first)
	}
	if first.ProductID != 1 || first.ProductName != "Gofy T-Shirt" || first.Quantity != 2 ||
		first.UnitPrice != 9900 || first.LineTotal != 19800 {
		t.Fatalf("first row = %+v", first)
	}
	if rows[1].ProductName != "Gofy Mug" || rows[1].LineTotal != 3900 {
		t.Fatalf("second row = %+v", rows[1])
	}
}

func TestListOrderLinesEmpty(t *testing.T) {
	q, _ := newQueries(t)

	rows, err := q.ListOrderLines(context.Background(), 404)
	if err != nil {
		t.Fatalf("ListOrderLines: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want empty", rows)
	}
}

func TestListUnsoldProducts(t *testing.T) {
	q, _ := newQueries(t)

	rows, err := q.ListUnsoldProducts(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListUnsoldProducts: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 4 || rows[0].Name != "Retired Mug" {
		t.Fatalf("rows = %+v, want only product 4", rows)
	}

	limited, err := q.ListUnsoldProducts(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListUnsoldProducts limit: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("limited rows = %d, want 1", len(limited))
	}
}

// TestListUnsoldProductsBuilderMatchesRaw runs the two implementations against one
// fixture. Comparing them only proves they agree, so it also asserts the known
// answer: if both were wrong the same way, equality alone would not notice.
//
// TestListUnsoldProductsBuilderMatchesRaw 在同一份 fixture 上跑两个实现。互相比对只能
// 证明两者一致，所以同时断言已知答案：万一两边错得一样，只比相等是看不出来的。
func TestListUnsoldProductsBuilderMatchesRaw(t *testing.T) {
	q, _ := newQueries(t)
	ctx := context.Background()

	// 0 走 clampLimit 的默认页大小，1 与 500 分别是下界和上界，两条路径必须同时收敛。
	for _, limit := range []int{0, 1, 10, 500} {
		raw, err := q.ListUnsoldProducts(ctx, limit)
		if err != nil {
			t.Fatalf("ListUnsoldProducts(limit=%d): %v", limit, err)
		}
		built, err := q.ListUnsoldProductsWithBuilder(ctx, limit)
		if err != nil {
			t.Fatalf("ListUnsoldProductsWithBuilder(limit=%d): %v", limit, err)
		}
		if !reflect.DeepEqual(raw, built) {
			t.Fatalf("limit=%d: raw = %+v, builder = %+v", limit, raw, built)
		}
	}

	rows, err := q.ListUnsoldProductsWithBuilder(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnsoldProductsWithBuilder: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want only product 4", rows)
	}
	want := query.UnsoldProduct{ID: 4, Name: "Retired Mug", Price: 3900, Stock: 0}
	if rows[0] != want {
		t.Fatalf("row = %+v, want %+v", rows[0], want)
	}
}

func TestUserHasOrder(t *testing.T) {
	q, _ := newQueries(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		userID uint64
		status string
		want   bool
	}{
		{name: "paid order exists", userID: 90001, status: "paid", want: true},
		{name: "pending only, ask paid", userID: 90001, status: "cancelled", want: false},
		{name: "cancelled order exists", userID: 90002, status: "cancelled", want: true},
		{name: "unknown user", userID: 404, status: "paid", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := q.UserHasOrder(ctx, tc.userID, tc.status)
			if err != nil {
				t.Fatalf("UserHasOrder: %v", err)
			}
			if got != tc.want {
				t.Fatalf("UserHasOrder(%d, %q) = %v, want %v", tc.userID, tc.status, got, tc.want)
			}
		})
	}
}

func TestListUserOrderStats(t *testing.T) {
	q, _ := newQueries(t)

	rows, err := q.ListUserOrderStats(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("ListUserOrderStats: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2 users", rows)
	}
	// 累计金额降序：90001（23700 + 1500）排在 90002（5000）前面。
	if rows[0].UserID != 90001 || rows[0].OrderCount != 2 || rows[0].TotalAmount != 25200 || rows[0].MaxOrderID != 900002 {
		t.Fatalf("first stat = %+v", rows[0])
	}
	if rows[1].UserID != 90002 || rows[1].OrderCount != 1 || rows[1].TotalAmount != 5000 {
		t.Fatalf("second stat = %+v", rows[1])
	}

	filtered, err := q.ListUserOrderStats(context.Background(), 2, 10)
	if err != nil {
		t.Fatalf("ListUserOrderStats having: %v", err)
	}
	if len(filtered) != 1 || filtered[0].UserID != 90001 {
		t.Fatalf("filtered = %+v, want only user 90001", filtered)
	}
}

func TestGetUserOrderStat(t *testing.T) {
	q, _ := newQueries(t)

	stat, err := q.GetUserOrderStat(context.Background(), 90001)
	if err != nil {
		t.Fatalf("GetUserOrderStat: %v", err)
	}
	if stat.OrderCount != 2 || stat.TotalAmount != 25200 || stat.MaxOrderID != 900002 {
		t.Fatalf("stat = %+v", stat)
	}

	if _, err := q.GetUserOrderStat(context.Background(), 404); !errors.Is(err, query.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestQueriesShareAmbientTransaction 验证 query 与写操作共享 ctx 事务：事务内
// 插入的订单对查询立即可见，回滚后消失。若 conn 解析没有优先使用 ctx 事务，
// SQLite 单连接池会在这里直接死锁，测试不会静默通过。
func TestQueriesShareAmbientTransaction(t *testing.T) {
	q, conn := newQueries(t)
	rollback := errors.New("rollback")

	err := gofydb.Transaction(context.Background(), conn, func(ctx context.Context) error {
		// 写操作也必须经 gofydb.WithContext 取事务句柄，直接 conn 会拿池而不是 tx。
		if err := gofydb.WithContext(ctx, conn).WithContext(ctx).Create(&orderFixture{
			ID: 900100, UserID: 90099, Amount: 7000, Status: "paid",
		}).Error; err != nil {
			return err
		}
		has, err := q.UserHasOrder(ctx, 90099, "paid")
		if err != nil {
			return err
		}
		if !has {
			return errors.New("query did not see the uncommitted row")
		}
		stat, err := q.GetUserOrderStat(ctx, 90099)
		if err != nil {
			return err
		}
		if stat.TotalAmount != 7000 {
			return errors.New("query saw a different amount inside the transaction")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction err = %v, want rollback", err)
	}

	has, err := q.UserHasOrder(context.Background(), 90099, "paid")
	if err != nil {
		t.Fatalf("UserHasOrder after rollback: %v", err)
	}
	if has {
		t.Fatal("rolled-back order is still visible")
	}
}
