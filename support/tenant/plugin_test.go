package tenant_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/bingcool/gofy/support/frameworkcontext"
	"github.com/bingcool/gofy/support/headerpropagation"
	"github.com/bingcool/gofy/support/tenant"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

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
	// Each test gets its own in-memory database; a shared DSN plus sqlite's
	// pooling would otherwise leak tables between cases.
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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

func TestInsertRespectsExplicitTenantByDefault(t *testing.T) {
	db := openDB(t, tenant.Options{})
	if err := db.WithContext(ctxFor("a")).Create(&order{TenantID: "b", Code: "x"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got order
	_ = db.WithContext(tenant.Without(context.Background())).First(&got).Error
	if got.TenantID != "b" {
		t.Fatalf("explicit tenant overwritten: %+v", got)
	}
}

func TestInsertOverwriteExplicitOption(t *testing.T) {
	db := openDB(t, tenant.Options{OverwriteExplicit: true})
	if err := db.WithContext(ctxFor("a")).Create(&order{TenantID: "b", Code: "x"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}
	var got order
	_ = db.WithContext(tenant.Without(context.Background())).First(&got).Error
	if got.TenantID != "a" {
		t.Fatalf("OverwriteExplicit did not force context tenant: %+v", got)
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
