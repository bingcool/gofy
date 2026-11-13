package otel

import (
	"context"
	"errors"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

const (
	gormSpanKey = "gofy:otel:gorm:span"
	// maxStatementLength bounds db.statement so one large query cannot blow up
	// an export batch.
	// maxStatementLength 限制 db.statement 的长度，避免单条大 SQL 撑爆一次
	// 导出批次。
	maxStatementLength = 4096
)

// GORMPlugin adds client spans around GORM create, query, update, delete, row
// and raw operations. The statement attribute holds the SQL template with
// placeholders, never the bound values, so secrets stay out of traces.
// GORMPlugin 为 GORM 的 create、query、update、delete、row、raw 操作加上
// client Span。statement 属性只保存带占位符的 SQL 模板，绝不写入绑定值，
// 敏感数据因此不会进入 Trace。
type GORMPlugin struct {
	tracer trace.Tracer
}

// NewGORMPlugin builds a plugin from a tracer provider. A no-op provider yields
// a disabled plugin.
// NewGORMPlugin 用 tracer provider 构建插件。传入 no-op provider 得到的是
// 不埋点的插件。
func NewGORMPlugin(tp trace.TracerProvider) *GORMPlugin {
	if tp == nil {
		return &GORMPlugin{}
	}
	return &GORMPlugin{tracer: tp.Tracer("github.com/bingcool/gofy/support/otel/gorm")}
}

// Name implements gorm.Plugin.
// Name 实现 gorm.Plugin 接口。
func (p *GORMPlugin) Name() string { return "gofy:otel" }

// Initialize registers the callbacks on db. Applying the same plugin twice to
// one *gorm.DB returns gorm's duplicate callback error.
// Initialize 在 db 上注册回调。同一个 *gorm.DB 重复应用同一插件会返回 gorm 的
// 回调重复注册错误，因此每个 db 只应安装一次。
func (p *GORMPlugin) Initialize(db *gorm.DB) error {
	if db == nil || p == nil || p.tracer == nil {
		return nil
	}
	// GORM returns unexported callback types, so each processor is registered
	// through the exported Register method only.
	// GORM 返回的是未导出的回调类型，因此只能通过导出的 Register 方法注册每个
	// processor。
	type registration struct {
		name   string
		op     string
		before callbackRegistrar
		after  callbackRegistrar
	}
	registrations := []registration{
		{"create", "INSERT", db.Callback().Create().Before("gorm:create"), db.Callback().Create().After("gorm:create")},
		{"query", "SELECT", db.Callback().Query().Before("gorm:query"), db.Callback().Query().After("gorm:query")},
		{"update", "UPDATE", db.Callback().Update().Before("gorm:update"), db.Callback().Update().After("gorm:update")},
		{"delete", "DELETE", db.Callback().Delete().Before("gorm:delete"), db.Callback().Delete().After("gorm:delete")},
		{"row", "SELECT", db.Callback().Row().Before("gorm:row"), db.Callback().Row().After("gorm:row")},
		{"raw", "RAW", db.Callback().Raw().Before("gorm:raw"), db.Callback().Raw().After("gorm:raw")},
	}
	for _, item := range registrations {
		op := item.op
		if err := item.before.Register("gofy:otel:before_"+item.name, p.before(op)); err != nil {
			return err
		}
		if err := item.after.Register("gofy:otel:after_"+item.name, p.after); err != nil {
			return err
		}
	}
	return nil
}

// callbackRegistrar is the exported part of GORM's unexported callback type.
// callbackRegistrar 是 GORM 未导出回调类型中已导出的部分。
type callbackRegistrar interface {
	Register(name string, fn func(*gorm.DB)) error
}

func (p *GORMPlugin) before(operation string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if db == nil || db.Statement == nil {
			return
		}
		table := strings.TrimSpace(db.Statement.Table)
		name := operation
		attrs := []attribute.KeyValue{
			attribute.String("db.operation", operation),
		}
		if dialect := dialectName(db); dialect != "" {
			attrs = append(attrs, attribute.String("db.system", dialect))
		}
		if table != "" {
			name = operation + " " + table
			attrs = append(attrs, attribute.String("db.sql.table", table))
		}
		ctx := db.Statement.Context
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, span := p.tracer.Start(ctx, name,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attrs...),
		)
		db.Statement.Context = ctx
		db.InstanceSet(gormSpanKey, span)
	}
}

func (p *GORMPlugin) after(db *gorm.DB) {
	if db == nil || db.Statement == nil {
		return
	}
	value, ok := db.InstanceGet(gormSpanKey)
	if !ok {
		return
	}
	span, ok := value.(trace.Span)
	if !ok {
		return
	}
	if sql := strings.TrimSpace(db.Statement.SQL.String()); sql != "" {
		if len(sql) > maxStatementLength {
			sql = sql[:maxStatementLength] + "..."
		}
		span.SetAttributes(attribute.String("db.statement", sql))
	}
	if db.Error != nil && !errors.Is(db.Error, gorm.ErrRecordNotFound) {
		span.RecordError(db.Error)
		span.SetStatus(codes.Error, db.Error.Error())
	}
	span.SetAttributes(attribute.Int64("db.rows_affected", db.RowsAffected))
	span.End()
}

func dialectName(db *gorm.DB) string {
	if db == nil || db.Dialector == nil {
		return ""
	}
	return strings.TrimSpace(db.Dialector.Name())
}

// InstrumentGORM installs the tracing plugin on db. It is a no-op when tracing
// is disabled or db is nil.
// InstrumentGORM 在 db 上安装追踪插件。追踪关闭或 db 为 nil 时为空操作。
func (p *Provider) InstrumentGORM(db *gorm.DB) error {
	if !p.Enabled() || db == nil {
		return nil
	}
	return db.Use(NewGORMPlugin(p.TracerProvider()))
}
