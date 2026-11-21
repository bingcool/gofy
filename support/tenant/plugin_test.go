package tenant_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/tenant"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// tenantDBCounter names the in-memory databases. The name has to be unique per
// open, not per test: -count=2 re-runs the same test name in one process and
// cache=shared would hand the second run the first run's tables and rows,
// turning a clean test into a fake cross-tenant leak.
// tenantDBCounter 给内存库起名。名字必须每次打开都唯一，而不只是每个测试唯一：
// -count=2 会在同一进程里重跑同名测试，cache=shared 会把第一轮的表和数据交给第二轮，
// 让一条干净的测试伪装成跨租户泄漏。
var tenantDBCounter atomic.Int64

// order is tenant-aware: it declares tenant_id. cargo is not, so the plugin
// must leave it alone.
// order 是租户感知模型（声明了 tenant_id）；cargo 没有该列，插件不得改写它。
type order struct {
	ID       uint   `gorm:"primaryKey;autoIncrement"`
	TenantID string `gorm:"column:tenant_id;size:32"`
	Code     string `gorm:"size:32"`
	Amount   int
}

func (order) TableName() string { return "orders" }

type cargo struct {
	ID   uint `gorm:"primaryKey;autoIncrement"`
	Name string
}

func (cargo) TableName() string { return "cargos" }

// ctxFor builds a request-like context carrying a tenant. It uses the real
// framework identity path (JWT user first), so the tests also prove the plugin
// and the rest of the framework read the same source.
// ctxFor 构造带租户的请求上下文，走真实的框架身份路径（JWT 用户优先），
// 因此测试也证明插件与框架其余部分读取同一身份来源。
func ctxFor(id string) context.Context {
	return frameworkcontext.WithUser(context.Background(), frameworkcontext.User{
		ID: "u-" + id, TenantID: id,
	})
}

func upstreamCtx(id string) context.Context {
	return headerpropagation.ContextWithHeader(
		context.Background(), headerpropagation.HeaderTenantID, id)
}

func openDB(t *testing.T, opts tenant.Options) *gorm.DB {
	t.Helper()
	// Each call gets its own in-memory database, and the handle is closed when
	// the test ends: a shared DSN plus sqlite's pooling would otherwise leak
	// tables between cases, and between repeated runs of the same case.
	// 每次调用一个独立内存库，并在测试结束时关闭句柄：共享 DSN 加上 sqlite 的连接池
	// 会让表在用例之间、以及同一用例的重复运行之间泄漏。
	dsn := fmt.Sprintf("file:tenant_%d?mode=memory&cache=shared", tenantDBCounter.Add(1))
	db, err := gorm.Open(sqlite.Open(dsn),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&order{}, &cargo{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := tenant.Instrument(db, opts); err != nil {
		t.Fatalf("Instrument: %v", err)
	}
	return db
}

// seed inserts rows bypassing the plugin so tests control the exact tenant
// values in the table.
// seed 绕过插件插入，让测试精确控制表里的租户值。
func seed(t *testing.T, db *gorm.DB, rows ...order) {
	t.Helper()
	ctx := tenant.Without(context.Background())
	for i := range rows {
		if err := db.WithContext(ctx).Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestSelectFiltersByTenant(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db,
		order{TenantID: "a", Code: "a1"},
		order{TenantID: "a", Code: "a2"},
		order{TenantID: "b", Code: "b1"},
	)

	var got []order
	if err := db.WithContext(ctxFor("a")).Where("code LIKE ?", "a%").Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	for _, o := range got {
		if o.TenantID != "a" {
			t.Fatalf("leaked row from tenant %q", o.TenantID)
		}
	}

	// DryRun exposes the generated SQL and binds, so the test asserts the
	// condition is really a bound parameter, not string interpolation.
	stmt := db.WithContext(ctxFor("a")).Session(&gorm.Session{DryRun: true}).
		Where("code = ?", "a1").Find(&order{}).Statement
	if !containsAll(stmt.SQL.String(), "`tenant_id`", "?") {
		t.Fatalf("SQL missing tenant condition: %s", stmt.SQL.String())
	}
	assertTenantBind(t, stmt.Vars, "a")
}

func TestUpstreamHeaderIsIdentitySource(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db, order{TenantID: "up", Code: "x"}, order{TenantID: "b", Code: "y"})
	var got []order
	if err := db.WithContext(upstreamCtx("up")).Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "up" {
		t.Fatalf("propagated tenant ignored: %+v", got)
	}
}

func TestUpdateAndDeleteStayWithinTenant(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db,
		order{TenantID: "a", Code: "same", Amount: 1},
		order{TenantID: "b", Code: "same", Amount: 1},
	)

	res := db.WithContext(ctxFor("a")).Model(&order{}).
		Where("code = ?", "same").Update("amount", 99)
	if res.Error != nil {
		t.Fatalf("Update: %v", res.Error)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("RowsAffected = %d, want 1", res.RowsAffected)
	}

	var rows []order
	if err := db.WithContext(tenant.Without(context.Background())).Find(&rows).Error; err != nil {
		t.Fatalf("Find all: %v", err)
	}
	var updated, untouched int
	for _, r := range rows {
		if r.Amount == 99 {
			updated++
			if r.TenantID != "a" {
				t.Fatalf("updated wrong tenant: %+v", r)
			}
		} else {
			untouched++
		}
	}
	if updated != 1 || untouched != 1 {
		t.Fatalf("updated=%d untouched=%d, want 1/1", updated, untouched)
	}

	res = db.WithContext(ctxFor("a")).Where("code = ?", "same").Delete(&order{})
	if res.Error != nil {
		t.Fatalf("Delete: %v", res.Error)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("Delete RowsAffected = %d, want 1", res.RowsAffected)
	}
	var remaining []order
	_ = db.WithContext(tenant.Without(context.Background())).Find(&remaining).Error
	if len(remaining) != 1 || remaining[0].TenantID != "b" {
		t.Fatalf("delete escaped tenant: %+v", remaining)
	}
}

func TestInsertFillsTenantColumn(t *testing.T) {
	cases := []struct {
		name   string
		create func(*gorm.DB) error
	}{
		{"struct", func(db *gorm.DB) error {
			return db.Create(&order{Code: "s1"}).Error
		}},
		{"map", func(db *gorm.DB) error {
			return db.Model(&order{}).Create(map[string]any{"code": "s2"}).Error
		}},
		{"slice of structs", func(db *gorm.DB) error {
			return db.Create(&[]order{{Code: "s3"}, {Code: "s4"}}).Error
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t, tenant.Options{})
			if err := tc.create(db.WithContext(ctxFor("a"))); err != nil {
				t.Fatalf("Create: %v", err)
			}
			var rows []order
			if err := db.WithContext(tenant.Without(context.Background())).Find(&rows).Error; err != nil {
				t.Fatalf("Find: %v", err)
			}
			if len(rows) == 0 {
				t.Fatal("no rows inserted")
			}
			for _, r := range rows {
				if r.TenantID != "a" {
					t.Fatalf("row not filled with current tenant: %+v", r)
				}
			}
		})
	}
}

func TestInsertSliceOfMapsFillsEveryRow(t *testing.T) {
	db := openDB(t, tenant.Options{})
	// sqlite cannot scan RETURNING ids back into maps, so this asserts the
	// generated statement and its binds rather than executing it.
	// sqlite 无法把 RETURNING 的 id 扫回 map，因此只断言生成的语句与绑定值。
	stmt := db.WithContext(ctxFor("a")).Session(&gorm.Session{DryRun: true}).
		Model(&order{}).Create([]map[string]any{
		{"code": "s1"}, {"code": "s2"},
	}).Statement

	if !contains(stmt.SQL.String(), "`tenant_id`") {
		t.Fatalf("tenant column missing from INSERT: %s", stmt.SQL.String())
	}
	// One tenant bind per row, proving SetColumn(fromCallbacks=true) covered
	// the whole batch rather than only the first row.
	// 每行一个租户绑定值，证明 SetColumn(fromCallbacks=true) 覆盖整批而非只第一行。
	count := 0
	for _, v := range stmt.Vars {
		if s, ok := v.(string); ok && s == "a" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("tenant binds = %d, want 2; vars = %v; sql = %s", count, stmt.Vars, stmt.SQL.String())
	}
}

// emptyColumnHandler is the misconfiguration P2-01 describes: a custom Handler
// whose column name is blank. FuncHandler cannot express it because it falls
// back to the default column, so the test needs a real custom implementation.
// emptyColumnHandler 是 P2-01 描述的误配置：自定义 Handler 返回空列名。FuncHandler 会回退到
// 默认列名，表达不了这种情形，因此测试需要一个真正的自定义实现。
type emptyColumnHandler struct{}

func (emptyColumnHandler) TenantID(context.Context) string { return "a" }
func (emptyColumnHandler) Column() string                  { return "   " }
func (emptyColumnHandler) IgnoreTable(string) bool         { return false }

// TestEmptyColumnFailsAtInstrument pins the fail-closed boundary: a blank column
// would make LookUpField("") miss every model, so no statement would be scoped
// and nothing would report why. Both Instrument and a direct db.Use must reject
// it before the first query runs.
// TestEmptyColumnFailsAtInstrument 固定失败关闭的边界：空列名会让 LookUpField("") 匹配不到
// 任何模型，所有语句都不被隔离且不报错。Instrument 与直接 db.Use 都必须在第一条查询之前拒绝。
func TestEmptyColumnFailsAtInstrument(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:empty_column?mode=memory&cache=shared"),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = tenant.Instrument(db, tenant.Options{Handler: emptyColumnHandler{}})
	if !errors.Is(err, tenant.ErrTenantColumnRequired) {
		t.Fatalf("Instrument err = %v, want ErrTenantColumnRequired", err)
	}
	// The rejected plugin must not have left callbacks installed.
	// 被拒绝的插件不能留下已注册的回调。
	if err := db.AutoMigrate(&order{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	var got []order
	if err := db.WithContext(context.Background()).Find(&got).Error; err != nil {
		t.Fatalf("Find after rejected install: %v", err)
	}
}

// boolPtr makes the tri-state explicit: nil means overwrite (the safe default),
// &false means preserve a caller's value on a privileged connection.
// boolPtr 表达三态：nil 表示覆盖（安全默认值），&false 表示在特权连接上保留调用方的值。
func boolPtr(v bool) *bool { return &v }

// TestInsertOverwritesExplicitTenantByDefault pins the secure default: a DTO
// carrying tenant_id from the request must not write into that tenant. The
// plugin cannot tell an administrator from an untrusted field, so the context
// tenant wins unless the deployment opts out explicitly.
// TestInsertOverwritesExplicitTenantByDefault 固定安全默认值：请求结构体里带进来的
// tenant_id 不能据此写入别的租户。插件无法分辨管理员与不可信字段，因此除非显式关闭，
// 一律用上下文租户。
func TestInsertOverwritesExplicitTenantByDefault(t *testing.T) {
	db := openDB(t, tenant.Options{})
	if err := db.WithContext(ctxFor("a")).Create(&order{TenantID: "b", Code: "x"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got order
	_ = db.WithContext(tenant.Without(context.Background())).First(&got).Error
	if got.TenantID != "a" {
		t.Fatalf("explicit tenant b survived: %+v", got)
	}
}

// TestInsertPreservesExplicitTenantWhenOptedOut covers the same insert on a
// deliberately privileged connection: an explicit false keeps the caller's
// value. The zero value must not accidentally take this path.
// TestInsertPreservesExplicitTenantWhenOptedOut 覆盖刻意提权的连接：显式 false 才保留
// 调用方的值。零值绝不能不小心走到这条路上。
func TestInsertPreservesExplicitTenantWhenOptedOut(t *testing.T) {
	db := openDB(t, tenant.Options{OverwriteExplicit: boolPtr(false)})
	if err := db.WithContext(ctxFor("a")).Create(&order{TenantID: "b", Code: "x"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got order
	_ = db.WithContext(tenant.Without(context.Background())).First(&got).Error
	if got.TenantID != "b" {
		t.Fatalf("explicit tenant b was overwritten: %+v", got)
	}
}

// TestWithoutStillWritesAnotherTenant proves the privileged path survives the
// secure default: Without skips the plugin entirely, so an explicit tenant on
// INSERT is kept without weakening ordinary request connections.
// TestWithoutStillWritesAnotherTenant 证明安全默认值下提权路径仍然可用：Without 整条跳过
// 插件，INSERT 上的显式租户因此被保留，不必削弱普通请求连接。
func TestWithoutStillWritesAnotherTenant(t *testing.T) {
	db := openDB(t, tenant.Options{})
	if err := db.WithContext(tenant.Without(context.Background())).
		Create(&order{TenantID: "b", Code: "x"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got order
	_ = db.WithContext(tenant.Without(context.Background())).First(&got).Error
	if got.TenantID != "b" {
		t.Fatalf("privileged insert did not keep tenant b: %+v", got)
	}
}

func TestMissingTenantFailsClosed(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db, order{TenantID: "a", Code: "x"})

	bare := context.Background()
	for name, run := range map[string]func() error{
		"select": func() error { return db.WithContext(bare).Find(&[]order{}).Error },
		"update": func() error {
			return db.WithContext(bare).Model(&order{}).Update("amount", 1).Error
		},
		"delete": func() error { return db.WithContext(bare).Delete(&order{}).Error },
		"insert": func() error { return db.WithContext(bare).Create(&order{Code: "z"}).Error },
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if !errors.Is(err, tenant.ErrTenantRequired) {
				t.Fatalf("err = %v, want ErrTenantRequired", err)
			}
		})
	}
}

func TestModelWithoutTenantColumnIsUntouched(t *testing.T) {
	db := openDB(t, tenant.Options{})
	// No tenant in context: a model without the column must still work.
	if err := db.WithContext(context.Background()).Create(&cargo{Name: "c1"}).Error; err != nil {
		t.Fatalf("Create cargo: %v", err)
	}
	var got []cargo
	if err := db.WithContext(context.Background()).Find(&got).Error; err != nil {
		t.Fatalf("Find cargo: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d cargos, want 1", len(got))
	}
}

func TestWithoutAndWithTenant(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db, order{TenantID: "a", Code: "x"}, order{TenantID: "b", Code: "y"})

	var all []order
	if err := db.WithContext(tenant.Without(ctxFor("a"))).Find(&all).Error; err != nil {
		t.Fatalf("Without: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Without returned %d rows, want 2", len(all))
	}

	var bs []order
	if err := db.WithContext(tenant.WithTenant(ctxFor("a"), "b")).Find(&bs).Error; err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	if len(bs) != 1 || bs[0].TenantID != "b" {
		t.Fatalf("WithTenant override ignored: %+v", bs)
	}
}

func TestNoDuplicateCondition(t *testing.T) {
	db := openDB(t, tenant.Options{})
	// The caller already constrained the tenant column. The plugin must not
	// append a second condition: count the "a" binds, one from the caller and
	// no more. A hand-written Where is a clause.Expr, so the plugin's
	// dedup walk must recognise it too.
	// 调用方已经限制了租户列，插件不得再追加一次：数 "a" 的绑定值，调用方一个、
	// 不该有第二个。手写的 Where 是 clause.Expr，去重遍历也要能识别。
	stmt := db.WithContext(ctxFor("a")).Session(&gorm.Session{DryRun: true}).
		Where("tenant_id = ?", "a").Find(&[]order{}).Statement
	count := 0
	for _, v := range stmt.Vars {
		if s, ok := v.(string); ok && s == "a" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("tenant binds = %d, want 1; vars = %v; sql = %s", count, stmt.Vars, stmt.SQL.String())
	}
}

// TestExistingConditionMustMatchTheContext is the regression test for the one
// shape the plugin used to trust blindly: a tenant equality already in the
// caller's WHERE. Recognising `tenant_id = ?` says nothing about the value bound
// to it, and a repository that still takes tenantID as a parameter — the usual
// residue when a codebase moves onto the plugin — binds somebody else's ID. The
// statement must then return nothing, not that tenant's rows.
//
// The bind count proves the mechanism: a mismatched condition keeps the caller's
// bind and gains the plugin's, so nothing can match either tenant.
// TestExistingConditionMustMatchTheContext 固定插件原先唯一盲信的形状：调用方 WHERE
// 里已有的租户等值条件。认出 `tenant_id = ?` 并不说明绑定的是哪个值，而把 tenantID 当参数
// 的仓储——迁到插件上时最常见的残留——绑的是别的租户。语句必须返回空，而不是那个租户的数据。
//
// 绑定个数证明机制：值不匹配时调用方的绑定保留、插件再追加一个，两个租户都不可能命中。
func TestExistingConditionMustMatchTheContext(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db,
		order{TenantID: "a", Code: "a1", Amount: 1},
		order{TenantID: "b", Code: "b1", Amount: 2},
	)

	cases := []struct {
		name  string
		apply func(*gorm.DB) *gorm.DB
		rows  int // rows a tenant-a request may see
		binds int // tenant ids bound as query args, plugin included
	}{
		{
			name:  "raw sql, same tenant",
			apply: func(db *gorm.DB) *gorm.DB { return db.Where("tenant_id = ?", "a") },
			rows:  1,
			binds: 1,
		},
		{
			name:  "raw sql, other tenant",
			apply: func(db *gorm.DB) *gorm.DB { return db.Where("tenant_id = ?", "b") },
			rows:  0,
			binds: 2,
		},
		{
			name:  "map condition, other tenant",
			apply: func(db *gorm.DB) *gorm.DB { return db.Where(map[string]any{"tenant_id": "b"}) },
			rows:  0,
			binds: 2,
		},
		{
			name:  "struct condition, other tenant",
			apply: func(db *gorm.DB) *gorm.DB { return db.Where(order{TenantID: "b"}) },
			rows:  0,
			binds: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []order
			if err := tc.apply(db.WithContext(ctxFor("a"))).Find(&got).Error; err != nil {
				t.Fatalf("find: %v", err)
			}
			if len(got) != tc.rows {
				t.Fatalf("rows = %d, want %d: %+v", len(got), tc.rows, got)
			}
			for _, row := range got {
				if row.TenantID != "a" {
					t.Fatalf("tenant-a context read tenant %q", row.TenantID)
				}
			}

			stmt := tc.apply(db.WithContext(ctxFor("a")).Session(&gorm.Session{DryRun: true})).
				Find(&[]order{}).Statement
			binds := 0
			for _, v := range stmt.Vars {
				if s, ok := v.(string); ok && (s == "a" || s == "b") {
					binds++
				}
			}
			if binds != tc.binds {
				t.Fatalf("tenant binds = %d, want %d; sql = %s; vars = %v",
					binds, tc.binds, stmt.SQL.String(), stmt.Vars)
			}
		})
	}
}

// spoofedTenant renders tenant a while binding tenant b. The plugin used to
// compare the display string from fmt.Sprint, so this value was accepted as
// "pinned to tenant a" and the automatic condition was skipped — the row it
// really bound belonged to tenant b.
// spoofedTenant 渲染出租户 a，绑定的却是租户 b。插件过去比较的是 fmt.Sprint 的展示
// 字符串，于是这个值被当成"已钉死租户 a"而跳过自动条件——它真正绑定的是租户 b 的行。
type spoofedTenant string

func (spoofedTenant) String() string { return "a" }

// TestDisplayStringCannotSpoofTenantScope is the end-to-end regression for the
// audit finding: an explicit tenant condition is only trusted when its bound
// value equals the effective tenant, and the bound value is what database/sql
// will send, never a display string. A custom Stringer that shows tenant a
// while carrying tenant b must therefore leave the statement scoped to tenant
// a: SELECT returns nothing and DELETE removes nothing.
// TestDisplayStringCannotSpoofTenantScope 是审计结论的端到端回归：显式租户条件只有在
// 绑定值等于生效租户时才被信任，而绑定值是 database/sql 真正会发送的值，绝不是展示字符串。
// 自定义 Stringer 展示租户 a、携带租户 b 时，语句必须仍然被限定在租户 a：SELECT 查不到
// 数据，DELETE 也删不掉任何行。
func TestDisplayStringCannotSpoofTenantScope(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db,
		order{TenantID: "a", Code: "mine"},
		order{TenantID: "b", Code: "theirs"},
	)

	var got []order
	err := db.WithContext(ctxFor("a")).
		Where("tenant_id = ?", spoofedTenant("b")).Find(&got).Error
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("display string spoofed the scope, read %+v", got)
	}

	res := db.WithContext(ctxFor("a")).
		Where("tenant_id = ?", spoofedTenant("b")).Delete(&order{})
	if res.Error != nil {
		t.Fatalf("Delete: %v", res.Error)
	}
	if res.RowsAffected != 0 {
		t.Fatalf("Delete RowsAffected = %d, want 0", res.RowsAffected)
	}

	var raw []order
	if err := db.WithContext(tenant.Without(context.Background())).Find(&raw).Error; err != nil {
		t.Fatalf("Find all: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("rows = %d, want 2 intact: %+v", len(raw), raw)
	}
}

// TestWithTenantKeepsAnotherTenantReachable guards the escape hatch: WithTenant
// sets the effective tenant, so a matching explicit condition is still deduped
// and the query returns that tenant's rows. Without this, the comparison above
// would make a legitimate cross-tenant read impossible.
// TestWithTenantKeepsAnotherTenantReachable 守住逃生口：WithTenant 设定了生效租户，
// 因此与之相等的显式条件仍然被去重，查询照常返回那个租户的数据。没有它，上面的比较会让
// 合法的跨租户读取变得不可能。
func TestWithTenantKeepsAnotherTenantReachable(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db,
		order{TenantID: "a", Code: "a1"},
		order{TenantID: "b", Code: "b1"},
	)

	ctx := tenant.WithTenant(ctxFor("a"), "b")
	var got []order
	if err := db.WithContext(ctx).Where("tenant_id = ?", "b").Find(&got).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "b" {
		t.Fatalf("WithTenant override returned %+v, want tenant b only", got)
	}

	stmt := db.WithContext(ctx).Session(&gorm.Session{DryRun: true}).
		Where("tenant_id = ?", "b").Find(&[]order{}).Statement
	binds := 0
	for _, v := range stmt.Vars {
		if s, ok := v.(string); ok && s == "b" {
			binds++
		}
	}
	if binds != 1 {
		t.Fatalf("tenant binds = %d, want 1; sql = %s; vars = %v",
			binds, stmt.SQL.String(), stmt.Vars)
	}
}

func TestIgnoreTableForUnresolvedSchema(t *testing.T) {
	// Table() without a model leaves Schema nil. A handler that ignores the
	// table keeps the statement runnable without a tenant.
	ignored := ""
	db := openDB(t, tenant.Options{
		Handler: tenant.FuncHandler{
			IgnoreTableFn: func(table string) bool { ignored = table; return true },
		},
		OnUnresolved: func(_ context.Context, _ string) {
			t.Error("OnUnresolved must not fire when IgnoreTable returns true")
		},
	})
	seed(t, db, order{TenantID: "a", Code: "x"})
	// A map Dest leaves GORM without a Schema — that is the only shape where
	// IgnoreTable is consulted. A model slice still parses a Schema.
	// map 作为 Dest 时 GORM 解析不出 Schema，这才是 IgnoreTable 被咨询的场景；
	// 模型切片仍然能解析出 Schema。
	var got []map[string]any
	if err := db.WithContext(context.Background()).Table("orders").Find(&got).Error; err != nil {
		t.Fatalf("Table().Find: %v", err)
	}
	if ignored != "orders" {
		t.Fatalf("IgnoreTable got %q, want orders", ignored)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
}

func TestUnresolvedSchemaFailsClosedByDefault(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db, order{TenantID: "a", Code: "x"})
	var got []map[string]any
	err := db.WithContext(context.Background()).Table("orders").Find(&got).Error
	if !errors.Is(err, tenant.ErrTenantRequired) {
		t.Fatalf("err = %v, want ErrTenantRequired (fail closed without schema)", err)
	}
}

func TestWithoutWinsWithoutSchema(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db, order{TenantID: "a", Code: "x"})
	// Regression: the bypass check used to sit after the schema branch, so a
	// Table("x") + map Dest statement (no Schema) still failed closed even
	// though the caller explicitly asked to skip isolation.
	// 回归：绕过检查原先放在 schema 分支之后，导致无 Schema 的 Table("x") + map
	// 语句即使显式要求跳过隔离，仍会失败关闭。
	var got []map[string]any
	if err := db.WithContext(tenant.Without(context.Background())).
		Table("orders").Find(&got).Error; err != nil {
		t.Fatalf("Without failed on a statement without schema: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
}

func TestBypassHookFires(t *testing.T) {
	var tables []string
	db := openDB(t, tenant.Options{
		OnBypass: func(_ context.Context, table string) { tables = append(tables, table) },
	})
	seed(t, db, order{TenantID: "a", Code: "x"})
	if err := db.WithContext(tenant.Without(context.Background())).Find(&[]order{}).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(tables) == 0 || tables[0] != "orders" {
		t.Fatalf("OnBypass tables = %v, want [orders]", tables)
	}
}

func TestCustomColumn(t *testing.T) {
	type scoped struct {
		ID      uint `gorm:"primaryKey;autoIncrement"`
		OwnerID string
		Code    string
	}
	// The column option applies to the model field named owner_id only if the
	// plugin is configured with that exact DB name.
	db := openDB(t, tenant.Options{
		Handler: tenant.FuncHandler{ColumnName: "owner_id", TenantIDFunc: func(context.Context) string { return "t9" }},
	})
	if err := db.AutoMigrate(&scoped{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.WithContext(context.Background()).Create(&scoped{Code: "x"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got scoped
	_ = db.WithContext(tenant.Without(context.Background())).First(&got).Error
	if got.OwnerID != "t9" {
		t.Fatalf("custom column not filled: %+v", got)
	}
}

// TestConcurrentTenantsDoNotLeak is the guard for the whole architecture: the
// plugin is a shared instance, so a tenant stored on it would cross requests.
// TestConcurrentTenantsDoNotLeak 是整条架构约束的守门员：插件是共享实例，
// 若租户值存在插件字段上就会跨请求串扰。
func TestConcurrentTenantsDoNotLeak(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db, order{TenantID: "a", Code: "x"}, order{TenantID: "b", Code: "x"})

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		want := "a"
		if i%2 == 1 {
			want = "b"
		}
		go func(want string) {
			defer wg.Done()
			var got []order
			if err := db.WithContext(ctxFor(want)).Where("code = ?", "x").Find(&got).Error; err != nil {
				errs <- err
				return
			}
			if len(got) != 1 || got[0].TenantID != want {
				errs <- fmt.Errorf("tenant %q saw %+v", want, got)
			}
		}(want)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestCoexistsWithAContextReplacingPlugin pins the ordering contract from the
// design: the tracing plugin replaces Statement.Context in its before callback,
// so the tenant plugin must be installed afterwards and read the final ctx.
// A stand-in callback replaces the context with one carrying a different
// tenant; the tenant query must use that value, proving the tenant plugin ran
// after the replacement rather than caching an earlier context.
// TestCoexistsWithAContextReplacingPlugin 钉住设计里的顺序契约：链路插件在自己的
// before 回调里替换 Statement.Context，因此租户插件必须后安装并读取最终 ctx。
// 这里用一个替身回调把 ctx 换成带另一个租户的 ctx；查询必须使用那个值，
// 才能证明租户插件在替换之后运行、而不是缓存了更早的 context。
func TestCoexistsWithAContextReplacingPlugin(t *testing.T) {
	db := openDB(t, tenant.Options{})
	if err := db.Callback().Query().Before("gofy:tenant:before_query").
		Register("test:replace_ctx", func(tx *gorm.DB) {
			tx.Statement.Context = ctxFor("replaced")
		}); err != nil {
		t.Fatalf("register stand-in: %v", err)
	}
	seed(t, db, order{TenantID: "original", Code: "x"}, order{TenantID: "replaced", Code: "x"})

	// The caller's context says "original"; the stand-in rewrites it to
	// "replaced". Reading the final ctx is what the design requires.
	var got []order
	if err := db.WithContext(ctxFor("original")).Find(&got).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 1 || got[0].TenantID != "replaced" {
		t.Fatalf("tenant plugin read a stale context: %+v", got)
	}
}

func TestDuplicateInstrumentIsRejected(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := tenant.Instrument(db, tenant.Options{}); err != nil {
		t.Fatalf("first Instrument: %v", err)
	}
	if err := tenant.Instrument(db, tenant.Options{}); err == nil {
		t.Fatal("second Instrument succeeded; duplicate callbacks must be rejected")
	}
}

func TestInstrumentRejectsNilDB(t *testing.T) {
	if err := tenant.Instrument(nil, tenant.Options{}); !errors.Is(err, tenant.ErrPluginNil) {
		t.Fatalf("err = %v, want ErrPluginNil", err)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func assertTenantBind(t *testing.T, vars []any, want string) {
	t.Helper()
	for _, v := range vars {
		if s, ok := v.(string); ok && s == want {
			return
		}
	}
	t.Fatalf("bind vars %v do not contain tenant %q", vars, want)
}

// TestTenantFilterSurvivesConditionsThatMentionTheColumn is the regression for
// the audit finding: "the WHERE clause mentions tenant_id" used to be enough to
// switch the automatic filter off. An inequality, a disjunction, a negation or
// even a value that happens to spell the column name therefore read other
// tenants' rows with no error and no log. Every case must come back scoped.
// TestTenantFilterSurvivesConditionsThatMentionTheColumn 是审计结论的回归测试：
// 过去只要 WHERE "提到 tenant_id" 就会关掉自动过滤。不等式、析取、取反，甚至只是一个
// 恰好拼出列名的字面量，都能读到别的租户的行，且不报错、不留日志。每个用例都必须
// 仍然被限定在本租户内。
func TestTenantFilterSurvivesConditionsThatMentionTheColumn(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*gorm.DB) *gorm.DB
		want  int
	}{
		{
			// The exact probe from the audit: tenant a's context, both rows match.
			// 审计里的原始探针：租户 a 的上下文，两行都满足条件。
			name:  "inequality",
			apply: func(db *gorm.DB) *gorm.DB { return db.Where("tenant_id <> ?", "zzz") },
			want:  1,
		},
		{
			name: "disjunction",
			apply: func(db *gorm.DB) *gorm.DB {
				return db.Where("tenant_id = ? OR code = ?", "a", "theirs")
			},
			want: 1,
		},
		{
			name:  "negation",
			apply: func(db *gorm.DB) *gorm.DB { return db.Where("NOT (tenant_id = ?)", "a") },
			want:  0,
		},
		{
			// The column name appears only as a value, so the substring check
			// used to disable the filter without any tenant condition existing.
			// 列名只作为字面量出现，子串检查因此在实际没有任何租户条件时关掉了过滤。
			name:  "value mentions the column",
			apply: func(db *gorm.DB) *gorm.DB { return db.Where("code <> ?", "tenant_id") },
			want:  1,
		},
		{
			name: "or branch on the tenant column",
			apply: func(db *gorm.DB) *gorm.DB {
				return db.Where("tenant_id = ?", "b").Or("code = ?", "theirs")
			},
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t, tenant.Options{})
			seed(t, db, order{TenantID: "a", Code: "mine"}, order{TenantID: "b", Code: "theirs"})

			var rows []order
			if err := tc.apply(db.WithContext(ctxFor("a"))).Find(&rows).Error; err != nil {
				t.Fatalf("Find: %v", err)
			}
			for _, row := range rows {
				if row.TenantID != "a" {
					t.Fatalf("leaked tenant %q (code %q) into tenant a's query", row.TenantID, row.Code)
				}
			}
			if len(rows) != tc.want {
				t.Fatalf("rows = %d, want %d (sql: %s)", len(rows), tc.want, tc.apply(db.Session(&gorm.Session{DryRun: true}).WithContext(ctxFor("a"))).Find(&[]order{}).Statement.SQL.String())
			}
		})
	}
}

// TestSaveDoesNotClearTenant is the regression for the audit's (b): Save and
// Select("*").Updates write every field of the destination, so the ordinary
// "primary key plus the fields being changed" struct also sent an empty
// tenant_id. The row stayed in the table but left every tenant — invisible to
// every scoped query, with no error.
// TestSaveDoesNotClearTenant 是审计 (b) 的回归测试：Save 与 Select("*").Updates 会写
// 目标的全部字段，于是"主键 + 要改的字段"这种常规写法也把空的 tenant_id 发了出去。行还在
// 表里，却离开了所有租户——任何带租户过滤的查询都看不到它，而且没有报错。
func TestSaveDoesNotClearTenant(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*gorm.DB, *order) *gorm.DB
	}{
		{"save", func(db *gorm.DB, row *order) *gorm.DB { return db.Save(row) }},
		{"select all", func(db *gorm.DB, row *order) *gorm.DB { return db.Select("*").Updates(row) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t, tenant.Options{})
			seed(t, db, order{TenantID: "a", Code: "keep"})

			var row order
			if err := db.WithContext(ctxFor("a")).Where("code = ?", "keep").First(&row).Error; err != nil {
				t.Fatalf("First: %v", err)
			}
			partial := order{ID: row.ID, Code: "renamed"}
			if err := tc.apply(db.WithContext(ctxFor("a")), &partial).Error; err != nil {
				t.Fatalf("update: %v", err)
			}

			var raw []order
			if err := db.WithContext(tenant.Without(context.Background())).Find(&raw).Error; err != nil {
				t.Fatalf("Find: %v", err)
			}
			if len(raw) != 1 {
				t.Fatalf("rows = %d, want 1", len(raw))
			}
			if raw[0].TenantID != "a" || raw[0].Code != "renamed" {
				t.Fatalf("row = %+v, want tenant a / renamed", raw[0])
			}
		})
	}
}

// TestWithoutCanStillMoveARowAcrossTenants: the write-side guard exists only
// for tenant-scoped statements. An administrator using tenant.Without must
// keep the ability to change tenant_id, including through Save.
// TestWithoutCanStillMoveARowAcrossTenants：写入侧守卫只针对带租户的语句。用
// tenant.Without 的管理端仍必须能改 tenant_id，包括通过 Save。
func TestWithoutCanStillMoveARowAcrossTenants(t *testing.T) {
	db := openDB(t, tenant.Options{})
	seed(t, db, order{TenantID: "a", Code: "keep"})

	var row order
	if err := db.WithContext(ctxFor("a")).Where("code = ?", "keep").First(&row).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	moving := order{ID: row.ID, TenantID: "b", Code: "keep"}
	if err := db.WithContext(tenant.Without(context.Background())).Save(&moving).Error; err != nil {
		t.Fatalf("Save: %v", err)
	}

	var raw []order
	if err := db.WithContext(tenant.Without(context.Background())).Find(&raw).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(raw) != 1 || raw[0].TenantID != "b" {
		t.Fatalf("rows = %+v, want the row moved to tenant b", raw)
	}
}

// TestBatchInsertFillsOnlyZeroTenantRows is the regression for (c): one row
// with an explicit tenant used to make the plugin skip the entire batch, so
// the other rows landed with an empty tenant id. The explicit row must keep
// its value (even when it names another tenant) and only the empty ones get
// the context tenant.
// TestBatchInsertFillsOnlyZeroTenantRows 是审计 (c) 的回归测试：只要有一行显式赋了租户，
// 插件就跳过整批，其它行因此以空租户落库。显式值必须保留（哪怕它写的是另一个租户），
// 只有空值行才取上下文租户。
// TestBatchInsertFillsPerRow covers the two policy values across both batch
// shapes. Assignment is per row either way: the default overwrites every row
// (a request DTO cannot smuggle in another tenant), while an explicit opt-out
// keeps rows that already carry a value and fills only the empty ones.
// TestBatchInsertFillsPerRow 覆盖两种策略 × 两种批量形态。两种策略都按行赋值：默认覆盖每行
// （请求 DTO 塞不进别的租户），显式关闭时保留已有值的行、只填空值行。
func TestBatchInsertFillsPerRow(t *testing.T) {
	cases := []struct {
		name     string
		opts     tenant.Options
		mapShape bool
		want     []string
	}{
		{name: "structs overwrite by default", opts: tenant.Options{},
			want: []string{"a/explicit", "a/implicit"}},
		{name: "maps overwrite by default", opts: tenant.Options{}, mapShape: true,
			want: []string{"a/explicit", "a/implicit"}},
		{name: "structs preserve when opted out", opts: tenant.Options{OverwriteExplicit: boolPtr(false)},
			want: []string{"b/explicit", "a/implicit"}},
		{name: "maps preserve when opted out", opts: tenant.Options{OverwriteExplicit: boolPtr(false)}, mapShape: true,
			want: []string{"b/explicit", "a/implicit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t, tc.opts)
			if tc.mapShape {
				rows := []map[string]any{
					{"tenant_id": "b", "code": "explicit"},
					{"code": "implicit"},
				}
				if err := db.WithContext(ctxFor("a")).Table("orders").Create(&rows).Error; err != nil {
					t.Fatalf("Create: %v", err)
				}
			} else {
				rows := []order{
					{TenantID: "b", Code: "explicit"},
					{Code: "implicit"},
				}
				if err := db.WithContext(ctxFor("a")).Create(&rows).Error; err != nil {
					t.Fatalf("Create: %v", err)
				}
			}

			var raw []struct {
				TenantID string `gorm:"column:tenant_id"`
				Code     string
			}
			if err := db.WithContext(tenant.Without(context.Background())).
				Table("orders").Order("id").Find(&raw).Error; err != nil {
				t.Fatalf("Find: %v", err)
			}
			if len(raw) != len(tc.want) {
				t.Fatalf("rows = %+v, want %d rows", raw, len(tc.want))
			}
			for i, row := range raw {
				if got := row.TenantID + "/" + row.Code; got != tc.want[i] {
					t.Fatalf("row %d = %q, want %q (all: %+v)", i, got, tc.want[i], raw)
				}
			}
		})
	}
}
