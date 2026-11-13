package db

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var (
	// ErrTxFuncRequired reports that Transaction was called without a callback.
	// ErrTxFuncRequired 表示 Transaction 没有收到回调。
	ErrTxFuncRequired = errors.New("db: transaction callback is required")
	// ErrTxDBRequired reports that ctx carries no transaction and the caller
	// supplied no connection to start one on.
	// ErrTxDBRequired 表示 ctx 中没有事务，且调用方没有提供可开启事务的连接。
	ErrTxDBRequired = errors.New("db: transaction requires a connection")
)

// txKey is the unique context key for the transaction, so it cannot collide
// with context values of other packages.
// txKey 是事务在 context 中的唯一键，避免与其他包的 context 值冲突。
type txKey struct{}

// Transaction starts a transaction on conn and runs fn with a ctx carrying it.
//
// The transaction travels down through ctx and repositories pick it up with
// WithContext, so repository calls of several modules in one request land in
// the same transaction. Nested calls reuse the outer transaction and create no
// savepoint: an inner error bubbles up to the outer caller, which rolls back.
// Transaction 在 conn 上开启事务，并在携带事务的 ctx 上执行 fn。
//
// 事务通过 ctx 向下传递，repository 用 WithContext 取用，因此同一次请求里
// 多个模块的 repository 调用会落在同一个事务中。嵌套调用直接复用外层事务，
// 不创建 savepoint：内层返回错误会冒泡到外层，由最外层统一回滚。
func Transaction(ctx context.Context, conn *gorm.DB, fn func(ctx context.Context) error) error {
	if fn == nil {
		return ErrTxFuncRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if tx := txFromContext(ctx); tx != nil {
		return fn(ctx)
	}
	if conn == nil {
		return ErrTxDBRequired
	}
	return conn.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// WithContext returns the connection this call should use: the transaction in
// ctx when present, otherwise fallback.
//
// fallback is usually the repository's explicit connection or the package-level
// handle installed by app/io; it is also returned when ctx is nil. An explicit
// connection never overrides an existing transaction: the outermost caller
// opens it and inner repositories only consume it, never switch connections.
// WithContext 返回本次调用应使用的连接：ctx 中带有事务时返回该事务，否则返回
// fallback。
//
// fallback 通常是 repository 的显式连接或 app/io 安装的包级句柄，ctx 为 nil
// 时同样返回它。显式连接不会覆盖 ctx 中已有的事务：事务由最外层调用方开启，
// 内层 repository 只消费它，不能自行切换连接。
func WithContext(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx := txFromContext(ctx); tx != nil {
		return tx
	}
	return fallback
}

func txFromContext(ctx context.Context) *gorm.DB {
	if ctx == nil {
		return nil
	}
	tx, _ := ctx.Value(txKey{}).(*gorm.DB)
	return tx
}
