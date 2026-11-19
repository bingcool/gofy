package consumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/bingcool/gofy/core"
)

// newSchemaRuntime returns a runtime on an empty in-memory database: nothing
// created the ledger table yet, so NewProcessor has to run the DDL itself —
// which is the step these tests are about.
// newSchemaRuntime 返回一个指向空内存库的 runtime：此时还没有任何东西建过台账表，
// 因此 NewProcessor 必须自己执行 DDL——这正是这组测试要观察的步骤。
func newSchemaRuntime(t *testing.T) *core.Runtime {
	t.Helper()
	dsn := fmt.Sprintf("file:schema_%d?mode=memory&cache=shared", time.Now().UnixNano())
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
	t.Cleanup(func() { _ = sqlDB.Close() })
	return core.NewRuntime(core.RuntimeConfig{Logger: zap.NewNop(), DB: db})
}

func schemaConfig() ProcessorConfig {
	return ProcessorConfig{
		Consumer:    testConsumerName,
		Handler:     func(context.Context, Message, Envelope) error { return nil },
		Idempotency: true,
		MaxAttempts: 3,
	}
}

// TestNewProcessorRequiresContext pins the constructor contract: the lifecycle
// context is not optional, because the schema initialisation that runs inside
// has to inherit it.
// TestNewProcessorRequiresContext 固定构造器契约：生命周期 context 不是可选项，
// 因为构造函数内部执行的建表必须继承它。
func TestNewProcessorRequiresContext(t *testing.T) {
	//nolint:staticcheck // passing nil on purpose: the constructor must reject it.
	_, err := NewProcessor(nil, newSchemaRuntime(t), schemaConfig())
	if err == nil {
		t.Fatal("NewProcessor accepted a nil context")
	}
	if !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("error = %v, want it to name the missing context", err)
	}
}

// TestSchemaInitInheritsCallerContext is the regression for the audit finding:
// the DDL used to run on context.Background(), so cancelling the consumer could
// not interrupt it. A cancelled parent must now fail construction.
// TestSchemaInitInheritsCallerContext 是审计结论的回归测试：DDL 原先跑在
// context.Background() 上，取消消费者无法打断它。现在父 context 被取消就必须让构造失败。
func TestSchemaInitInheritsCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := NewProcessor(ctx, newSchemaRuntime(t), schemaConfig())
	if err == nil {
		t.Fatal("schema initialisation ignored a cancelled parent context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "idempotency schema") {
		t.Fatalf("error = %v, want it to name the failed step", err)
	}
}

// TestSchemaInitHonoursTimeout covers the second half of the fix: even with a
// live parent, the DDL is bounded, so a database that never answers fails
// startup instead of blocking it.
// TestSchemaInitHonoursTimeout 覆盖修复的另一半：即使父 context 仍然有效，建表也有上限，
// 因此数据库没有应答时会启动失败，而不是一直卡住。
func TestSchemaInitHonoursTimeout(t *testing.T) {
	cfg := schemaConfig()
	cfg.SchemaTimeout = time.Nanosecond

	_, err := NewProcessor(context.Background(), newSchemaRuntime(t), cfg)
	if err == nil {
		t.Fatal("schema initialisation ignored SchemaTimeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

// TestSchemaInitDefaultsTimeout keeps the zero value usable: a caller that does
// not set SchemaTimeout must still get a bounded initialisation, not an
// unbounded one.
// TestSchemaInitDefaultsTimeout 保证零值可用：没有设置 SchemaTimeout 的调用方仍然得到一个
// 有上限的初始化，而不是无上限的。
func TestSchemaInitDefaultsTimeout(t *testing.T) {
	proc, err := NewProcessor(context.Background(), newSchemaRuntime(t), schemaConfig())
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	if proc.schemaTimeout != DefaultSchemaTimeout {
		t.Fatalf("schemaTimeout = %v, want %v", proc.schemaTimeout, DefaultSchemaTimeout)
	}
	if DefaultSchemaTimeout <= 0 {
		t.Fatalf("DefaultSchemaTimeout = %v, must be positive", DefaultSchemaTimeout)
	}
}
