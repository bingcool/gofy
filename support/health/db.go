package health

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// DefaultPoolTimeout is the timeout the built-in connection checkers declare: a
// pool that answers within a second is usable now, and one that does not is
// better reported as unavailable than waited on.
// DefaultPoolTimeout 是内置连接类检查器声明的超时：一秒内应答的连接池当前可用，超过
// 一秒的则宁可报告不可用，也不再继续等待。
const DefaultPoolTimeout = time.Second

// NewDB builds a checker over a gorm connection pool. It reuses PingContext, the
// same call the inline /readyz handler made before the registry existed, so the
// readiness answer and the response body stay compatible.
//
// The registry is rebuilt by a reload that replaces the pool, so the handle here
// never outlives the generation that created it.
// NewDB 在 gorm 连接池上构建检查器。它复用 PingContext——注册表出现之前内联 /readyz
// 处理器调用的就是它——因此 readiness 结论与响应体保持兼容。
//
// 替换连接池的 Reload 会重建注册表，因此这里的句柄不会活过创建它的那一代。
func NewDB(name string, db *gorm.DB) Checker { return &dbChecker{name: name, db: db} }

type dbChecker struct {
	name string
	db   *gorm.DB
}

func (c *dbChecker) Name() string { return c.name }

// CheckTimeout implements TimeoutProvider.
// CheckTimeout 实现 TimeoutProvider。
func (c *dbChecker) CheckTimeout() time.Duration { return DefaultPoolTimeout }

func (c *dbChecker) Check(ctx context.Context) error {
	if c.db == nil {
		return errors.New("database handle is nil")
	}
	sqlDB, err := c.db.DB()
	if err != nil {
		return fmt.Errorf("database pool: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("database ping: %w", err)
	}
	return nil
}
