package appio

import (
	"context"
	"strings"

	"github.com/bingcool/gofy/support"
	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/generation"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofylock "github.com/bingcool/gofy/support/lock"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	gofyuuidint "github.com/bingcool/gofy/support/uuidint"
	goredis "github.com/redis/go-redis/v9"
	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	appkafka "github.com/yourorg/demo-service/app/appio/kafka"
	appredis "github.com/yourorg/demo-service/app/appio/redis"
	appredislock "github.com/yourorg/demo-service/app/appio/redislock"
	approcketmq "github.com/yourorg/demo-service/app/appio/rocketmq"
	appuuidint "github.com/yourorg/demo-service/app/appio/uuidint"
	"gorm.io/gorm"
)

// Snapshot 是一代 IO 句柄的只读视图：默认 DB、命名 DB、Redis、AMQP、Kafka、
// RocketMQ、UUID 生成器与分布式锁。它把一次 Reload 前后的资源隔离开——请求进入时
// 固定一个 Snapshot，后续查询即使横跨 Reload 也落在同一代连接上。
//
// Snapshot 本身不可变：字段只在 Start 组装时写入，发布后不再修改。
//
// 取句柄只有一条路：DBIO / RedisIO / AMQPIO / KafkaIO / RocketMQIO，按别名解析
// 本代 Registry，一律返回 (句柄, error)。没有"已经打开的句柄"快捷方法——那会让
// 同一件事有两种写法，也让"实例没配置"时而 nil、时而 error。UUID 与 RedisLock 是
// 例外：生成器和锁都不是连接，没有 Registry 可解析，仍由 UUID() / RedisLock() 直接
// 返回。
type Snapshot struct {
	db       *appdb.Set
	redis    *appredis.Set
	amqp     *appamqp.Set
	kafka    *appkafka.Set
	rocketmq *approcketmq.Set
	uuidint  *appuuidint.Set
	lock     *appredislock.Set
}

// currentSnapshot 指向当前一代的 Snapshot；发布、读取与请求级固定都由
// support/generation 提供，本包只负责组装这一代的句柄。
var currentSnapshot = generation.NewSet[Snapshot]("app/appio")

// Current returns the published Snapshot, or nil when none is installed.
// Current 返回已发布的 Snapshot；尚未安装时为 nil。
func Current() *Snapshot { return currentSnapshot.Current() }

// Publish installs s as the current generation; a nil s withdraws it at the end
// of a script or in test cleanup.
// Publish 把 s 发布为当前一代；s 为 nil 时撤回发布（脚本结束、测试清理）。
func Publish(s *Snapshot) { currentSnapshot.Publish(s) }

// PublishDB 发布一个只带默认数据库的快照。单元测试不执行 app/appio 模块生命周期，
// 用测试连接走这条路径；脚本与请求都跑 Registry/Start，不用它。
func PublishDB(db *gorm.DB) {
	Publish(&Snapshot{db: appdb.NewSet(db)})
}

func newSnapshot(db *appdb.Set, redis *appredis.Set, amqp *appamqp.Set, kafka *appkafka.Set, rocketmq *approcketmq.Set, uuidint *appuuidint.Set, lock *appredislock.Set) *Snapshot {
	return &Snapshot{db: db, redis: redis, amqp: amqp, kafka: kafka, rocketmq: rocketmq, uuidint: uuidint, lock: lock}
}

// UUID 返回这一代的 ID 生成器；未启用时为 nil。
//
// 它是唯一不带 IO 后缀的取用方法：生成器不是连接，没有 Registry 可按名字解析，
// 也就没有"解析失败"这种状态。启用与否在 Start 时已经决定。
func (s *Snapshot) UUID() *gofyuuidint.Generator {
	if s == nil {
		return nil
	}
	return s.uuidint.Generator()
}

// RedisLock 返回这一代的分布式锁；未启用时为 nil。
//
// It is named RedisLock rather than Lock because the caller immediately calls
// Lock on the returned locker: `Lock().Lock(...)` reads like a typo. Like the
// generator, the lock is not a connection and has no Registry to resolve, so it
// uses the same single exception to the handle rule.
//
// 它叫 RedisLock 而不是 Lock：调用方紧接着就会调 Locker 的 Lock，两个同名方法叠在
// 一起会写成 `Lock().Lock(...)`，读起来像笔误。锁同样不是连接，没有 Registry 可按
// 名字解析，因此也走"直接返回句柄"这条唯一例外。
func (s *Snapshot) RedisLock() *gofylock.Locker {
	if s == nil {
		return nil
	}
	return s.lock.Locker()
}

// 命名实例解析（按别名查本代 Registry，首次访问才建连）。
//
// 这组方法是取句柄的唯一入口：名字不存在返回各包的 ErrUnknownInstance，快照或
// Registry 缺失返回 ErrNotConfigured，调用方据此决定降级还是失败。请求内应始终经
// WithContext(ctx) 拿到固定的 Snapshot 再解析，避免解析发生在 Reload 边界、读到
// 下一代 Registry。

// DBIO 在本代 Registry 中按别名解析数据库连接（首次访问才建连）。
//
// "default" 是特例：它由框架打开、刻意不在 registry 里，因此这里直接返回本代的
// defaultDB，而不是交给 Set.NewIO。没有它，调用方就得记住"默认库走另一条路"，
// 而这正是统一入口要消除的东西。
// DBIO resolves a database connection by alias in this generation's Registry,
// opening it on first use.
//
// "default" is the one special case: the framework opens it and deliberately
// keeps it out of the registry, so it resolves to this generation's defaultDB
// instead of going through Set.NewIO.
func (s *Snapshot) DBIO(ctx context.Context, name string) (*gorm.DB, error) {
	if s == nil {
		return nil, gofydb.ErrNotConfigured
	}
	if strings.TrimSpace(name) == appdb.DefaultInstance {
		if db := s.db.Default(); db != nil {
			return db, nil
		}
		return nil, gofydb.ErrNotConfigured
	}
	return s.db.NewIO(ctx, name)
}

// RedisIO 在本代 Registry 中按别名解析 Redis 客户端（首次访问才建连）。
func (s *Snapshot) RedisIO(ctx context.Context, name string) (*goredis.Client, error) {
	if s == nil {
		return nil, gofyredis.ErrNotConfigured
	}
	return s.redis.NewIO(ctx, name)
}

// AMQPIO 在本代 Registry 中按别名解析 AMQP 连接（首次访问才建连）。
func (s *Snapshot) AMQPIO(ctx context.Context, name string) (*gofyamqp.Client, error) {
	if s == nil {
		return nil, gofyamqp.ErrNotConfigured
	}
	return s.amqp.NewIO(ctx, name)
}

// KafkaIO 在本代 Registry 中按别名解析 Kafka 连接（首次访问才建连）。
func (s *Snapshot) KafkaIO(ctx context.Context, name string) (*gofykafka.Client, error) {
	if s == nil {
		return nil, gofykafka.ErrNotConfigured
	}
	return s.kafka.NewIO(ctx, name)
}

// RocketMQIO 在本代 Registry 中按别名解析 RocketMQ 生产者（首次访问才建连）。
func (s *Snapshot) RocketMQIO(ctx context.Context, name string) (*gofyrocketmq.Client, error) {
	if s == nil {
		return nil, gofyrocketmq.ErrNotConfigured
	}
	return s.rocketmq.NewIO(ctx, name)
}

// WithSnapshot pins s to ctx for this request, script or background task.
// WithSnapshot 把 s 固定到 ctx，供请求内的 Repository、Service 与后台任务取用。
func WithSnapshot(ctx context.Context, s *Snapshot) context.Context {
	return currentSnapshot.WithSnapshot(ctx, s)
}

// WithContext returns the Snapshot pinned in ctx (written by WithSnapshot at the
// entry), falling back to Current() when the context carries no pin.
//
// If neither exists, the entry point failed to publish a generation, so this
// panics instead of handing a nil handle to shared code that would dereference
// it far from the cause. The request middleware, the script withIO wrapper and
// the io module Start all publish before business code runs.
//
// WithContext 返回 ctx 中固定的 Snapshot（由 WithSnapshot 在请求入口写入）；
// 没有固定值时回退到 Current()。两者都没有，说明入口没有发布本代：请求中间件、
// 脚本 withIO、io 模块 Start 都应该在业务代码之前发布，因此这里直接 panic，
// 让缺失在第一次取用时暴露，而不是把 nil 句柄留给下游解引用。
func WithContext(ctx context.Context) *Snapshot {
	return currentSnapshot.WithContext(ctx)
}

// Middleware pins the generation that is current when a request enters, unless
// the request already carries one. HTTP requests are normally pinned by the
// framework's generation binder (see Module.RequestBinder), which fixes the
// generation that owns the handler; this middleware is the fallback for an
// engine built outside the framework and never overwrites that pin.
//
// Middleware 把进入请求时的当前一代固定到上下文；ctx 已经固定过则不覆盖。HTTP
// 请求通常由框架的代际 binder（见 Module.RequestBinder）固定，它固定的是 handler
// 所属的那一代；这里的中间件只服务框架之外自建的 engine，且绝不覆盖那个固定值。
func Middleware() support.Middleware {
	return currentSnapshot.Middleware()
}
