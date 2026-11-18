// Package tenantguard carries the tenant plugin options for worker processes
// and installs them on a runtime pool.
//
// A worker group is built before the framework creates the runtime, and it
// receives the framework's db.default instead of opening an io generation, so
// there is no single assembly point equivalent to appio/db.Open. The options
// are therefore configured once when the group is built and installed lazily by
// every worker entry that touches the database.
//
// Installation cannot be left to the consumer alone: the plugin lives on the
// pool, not on a context, so a scan loop that starts a millisecond earlier — or
// a process whose consumer never receives a message — would query unfiltered.
// Ensure is idempotent for that reason; calling it from every entry is the
// intended shape.
// 包 tenantguard 为 worker 进程持有租户插件选项，并把它安装到 Runtime 的连接池上。
//
// worker 分组在框架创建 Runtime 之前构造，拿到的是框架的 db.default 而不是自己打开
// 的 io 装配代，因此不存在与 appio/db.Open 对等的单一装配点。选项因此在构造分组时
// 配置一次，由每个访问数据库的 worker 入口惰性安装。
//
// 安装不能只交给消费者：插件装在池上而不是 context 上，早一毫秒启动的扫描循环，
// 或消费者一直收不到消息的进程，都会裸查。Ensure 因此是幂等的；每个入口各调一次
// 才是预期的用法。
package tenantguard

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/bingcool/gofy/core"
	"github.com/bingcool/gofy/support/tenant"

	appdb "github.com/yourorg/demo-service/app/appio/db"
)

// options holds the resolved tenant section. Nil means the feature is off and
// every Ensure is a no-op, which is what keeps a project without multi-tenancy
// on ordinary GORM behaviour.
// options 持有解析后的租户段。nil 表示功能关闭，所有 Ensure 都是空操作——这正是
// 没有多租户的项目保持普通 GORM 行为的原因。
var options atomic.Pointer[tenant.Options]

// Configure records the tenant section for this process. It runs at assembly
// time, before any worker goroutine starts; workers only read afterwards.
// Configure 记录本进程的租户段。它在装配期、任何 worker goroutine 启动之前执行；
// 之后 worker 只读取。
func Configure(enabled bool, opts tenant.Options) {
	if !enabled {
		options.Store(nil)
		return
	}
	stored := opts
	options.Store(&stored)
}

// Enabled reports whether isolation was configured. Diagnostics and tests use
// it; worker entries only need Ensure.
// Enabled 报告是否配置了隔离。诊断与测试使用；worker 入口只需要 Ensure。
func Enabled() bool { return options.Load() != nil }

// Ensure installs the plugin on rt's pool when isolation is configured. It is
// idempotent: the install checks for the plugin's callback before adding it, so
// every entry can call it without coordinating.
// Ensure 在配置了隔离时把插件装到 rt 的连接池上。它是幂等的：安装前会检查插件的
// 回调是否已注册，因此每个入口都可以直接调用，无需彼此协调。
func Ensure(rt *core.Runtime) error {
	opts := options.Load()
	if opts == nil {
		return nil
	}
	if rt == nil || rt.DB == nil {
		return errors.New("tenantguard: runtime database is required")
	}
	if err := appdb.EnsureTenant(rt.DB, *opts); err != nil {
		return fmt.Errorf("tenantguard: install plugin: %w", err)
	}
	return nil
}
