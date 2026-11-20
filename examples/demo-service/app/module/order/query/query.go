// Package query 是 order 模块的只读查询层。
//
// 每个模块可以有自己的 query 子包，只服务本模块的读取需求。它专门承载三类
// 读取：原生 SQL、跨表 JOIN、EXISTS / NOT EXISTS 判断，以及为列表页、报表、
// 导出定制的读模型。与同模块 repository 的分工是：
//
//   - repository 面向单个聚合，负责写与按主键/索引的简单读，返回 model；
//   - query 面向跨表读（包括 JOIN 其他模块的表），返回本包定义的行结构，不做
//     持久化映射，也不提供任何写方法。
//
// 层级边界：query 属于 order 模块，只能 import 本模块 model、app/appio 与
// support；读其他模块的表时在 SQL 里直接写表名，不 import 对方的 model 或
// repository——表结构是数据库契约，跨模块的 Go 依赖才需要 innerapi。
//
// 连接解析与 repository 完全一致：从 ctx 固定的 IO 快照取 db.default，ctx 中
// 已有事务时优先使用事务。因此 query 可以和写操作共享同一个事务（读到未提交
// 的数据），也可以作为纯只读查询独立执行；一次请求内的多条查询始终落在同一代
// 连接上，不会因为 Reload 而跨代。
//
// 约定：
//
//   - 只写 SELECT；需要写入时回到对应模块的 repository，写路径不在这里开口子。
//   - SQL 一律用 ? 占位符，禁止拼接用户输入；表名、列名只能是常量。
//   - 每个方法都接收 ctx，超时、取消与 trace 由调用方的 ctx 决定。
//   - 行结构只描述“这次查询要什么”，不是表结构，也不参与迁移；列名用
//     gorm:"column:..." 显式绑定，改表时编译不会报错，测试会先失败。
//   - 单行查询未命中返回 ErrNotFound，多行查询返回空切片和 nil。
package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/yourorg/demo-service/app/appio"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	"gorm.io/gorm"
)

// ErrNotFound 表示单行查询没有命中；多行查询用空切片表达同样的语义。
// ErrNotFound reports that a single-row query matched nothing.
var ErrNotFound = errors.New("query: row not found")

const (
	// defaultLimit 是调用方没给 limit 时的页大小；maxLimit 防止一次拖走整张表。
	defaultLimit = 20
	maxLimit     = 200
)

// Queries 是只读查询的入口。它不持有连接与日志：每次调用都从 ctx 解析本代
// 连接，因此可以在请求、脚本、后台任务之间共享同一个实例，也可以在装配根
// 构造一次后按需注入。
//
// Queries is the stateless entry point of the read-only query layer.
type Queries struct{}

// New returns a stateless query entry point.
// New 返回只读查询入口；它不建立连接，连接在每次调用时从 ctx 解析。
func New() *Queries { return &Queries{} }

// conn resolves the connection for one call: the transaction carried by ctx when
// present, otherwise the db.default of the request-pinned generation. DBConnect
// also binds ctx to the handle, so Raw/Scan cancellation and trace work without
// a second WithContext at each call site.
//
// conn 解析本次调用应使用的连接：ctx 中有事务时用事务，否则用请求固定的那一
// 代 db.default。DBConnect 同时把 ctx 绑到句柄上，Raw/Scan 的取消与 trace 因此
// 不需要在每个调用处再补一次 WithContext。和 repository 走同一条解析路径，
// 两者的读写才会落在同一个事务与同一代连接上。
func (q *Queries) conn(ctx context.Context) (*gorm.DB, error) {
	db, err := appio.WithContext(ctx).DBConnect(ctx, appdb.DefaultInstance)
	if err != nil {
		return nil, fmt.Errorf("query: resolve default database: %w", err)
	}
	return db, nil
}

// clampLimit keeps a caller-supplied page size inside [1, maxLimit].
// clampLimit 把调用方给的 limit 收敛到 [1, maxLimit]，零值或负数取默认页大小。
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}
