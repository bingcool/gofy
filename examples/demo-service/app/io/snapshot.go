package io

import (
	"context"

	"github.com/bingcool/gofy/support"
	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofydb "github.com/bingcool/gofy/support/db"
	"github.com/bingcool/gofy/support/generation"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	gofyuuidint "github.com/bingcool/gofy/support/uuidint"
	goredis "github.com/redis/go-redis/v9"
	appamqp "github.com/yourorg/demo-service/app/io/amqp"
	appdb "github.com/yourorg/demo-service/app/io/db"
	appkafka "github.com/yourorg/demo-service/app/io/kafka"
	appredis "github.com/yourorg/demo-service/app/io/redis"
	approcketmq "github.com/yourorg/demo-service/app/io/rocketmq"
	appuuidint "github.com/yourorg/demo-service/app/io/uuidint"
	"gorm.io/gorm"
)

// Snapshot 是一代 IO 句柄的只读视图：默认 DB、命名 DB、Redis、AMQP、Kafka、
// RocketMQ 与 UUID 生成器。它把一次 Reload 前后的资源隔离开——请求进入时
// 固定一个 Snapshot，后续查询即使横跨 Reload 也落在同一代连接上。
//
// Snapshot 本身不可变：字段只在 Start 组装时写入，发布后不再修改。
type Snapshot struct {
	db       *appdb.Set
	redis    *appredis.Set
	amqp     *appamqp.Set
	kafka    *appkafka.Set
	rocketmq *approcketmq.Set
	uuidint  *appuuidint.Set
}

// currentSnapshot 指向当前一代的 Snapshot；发布、读取与请求级固定都由
// support/generation 提供，本包只负责组装这一代的句柄。
var currentSnapshot = generation.NewSet[Snapshot]("app/io")

// Current returns the published Snapshot, or nil when none is installed.
// Current 返回已发布的 Snapshot；尚未安装时为 nil。
func Current() *Snapshot { return currentSnapshot.Current() }

// Publish installs s as the current generation; a nil s withdraws it at the end
// of a script or in test cleanup.
// Publish 把 s 发布为当前一代；s 为 nil 时撤回发布（脚本结束、测试清理）。
func Publish(s *Snapshot) { currentSnapshot.Publish(s) }

// PublishDB 发布一个只带默认数据库的快照。单元测试不执行 app/io 模块生命周期，
// 用测试连接走这条路径；脚本与请求都跑 Registry/Start，不用它。
func PublishDB(db *gorm.DB) {
	Publish(&Snapshot{db: appdb.NewSet(db)})
}

func newSnapshot(db *appdb.Set, redis *appredis.Set, amqp *appamqp.Set, kafka *appkafka.Set, rocketmq *approcketmq.Set, uuidint *appuuidint.Set) *Snapshot {
	return &Snapshot{db: db, redis: redis, amqp: amqp, kafka: kafka, rocketmq: rocketmq, uuidint: uuidint}
}

// DB 返回这一代的默认数据库（db.default）；没有安装时为 nil。
func (s *Snapshot) DB() *gorm.DB {
	if s == nil {
		return nil
	}
	return s.db.Default()
}

// DBInstance 返回这一代的命名数据库（order、product）；未配置时为 nil。
func (s *Snapshot) DBInstance(name string) *gorm.DB {
	if s == nil {
		return nil
	}
	return s.db.Instance(name)
}

// Redis 返回这一代的命名 Redis 实例；未启用时为 nil。
func (s *Snapshot) Redis(name string) *goredis.Client {
	if s == nil {
		return nil
	}
	return s.redis.Client(name)
}

// AMQP 返回这一代的命名 AMQP 连接；未启用时为 nil。
func (s *Snapshot) AMQP(name string) *gofyamqp.Client {
	if s == nil {
		return nil
	}
	return s.amqp.Client(name)
}

// Kafka 返回这一代的命名 Kafka 连接；未启用时为 nil。
func (s *Snapshot) Kafka(name string) *gofykafka.Client {
	if s == nil {
		return nil
	}
	return s.kafka.Client(name)
}

// RocketMQ 返回这一代的命名 RocketMQ 生产者；未启用时为 nil。
func (s *Snapshot) RocketMQ(name string) *gofyrocketmq.Client {
	if s == nil {
		return nil
	}
	return s.rocketmq.Client(name)
}

// UUID 返回这一代的 ID 生成器；未启用时为 nil。
func (s *Snapshot) UUID() *gofyuuidint.Generator {
	if s == nil {
		return nil
	}
	return s.uuidint.Generator()
}

// 命名实例懒取用（NewIO 语义）。
//
// DB / DBInstance 与 Redis / AMQP / Kafka / RocketMQ 只返回本代已经打开的句柄；
// 下面这组方法按别名解析本代 Registry，实例不存在时返回各包的 ErrUnknownInstance。
// 请求内应始终经 WithContext(ctx) 拿到固定的 Snapshot 再解析，避免解析发生在
// Reload 边界、读到下一代 Registry。

// DBNewIO 在本代 Registry 中按别名解析数据库连接（首次访问才建连）。
func (s *Snapshot) DBNewIO(ctx context.Context, name string) (*gorm.DB, error) {
	if s == nil {
		return nil, gofydb.ErrNotConfigured
	}
	return s.db.NewIO(ctx, name)
}

// RedisNewIO 在本代 Registry 中按别名解析 Redis 客户端（首次访问才建连）。
func (s *Snapshot) RedisNewIO(ctx context.Context, name string) (*goredis.Client, error) {
	if s == nil {
		return nil, gofyredis.ErrNotConfigured
	}
	return s.redis.NewIO(ctx, name)
}

// AMQPNewIO 在本代 Registry 中按别名解析 AMQP 连接（首次访问才建连）。
func (s *Snapshot) AMQPNewIO(ctx context.Context, name string) (*gofyamqp.Client, error) {
	if s == nil {
		return nil, gofyamqp.ErrNotConfigured
	}
	return s.amqp.NewIO(ctx, name)
}

// KafkaNewIO 在本代 Registry 中按别名解析 Kafka 连接（首次访问才建连）。
func (s *Snapshot) KafkaNewIO(ctx context.Context, name string) (*gofykafka.Client, error) {
	if s == nil {
		return nil, gofykafka.ErrNotConfigured
	}
	return s.kafka.NewIO(ctx, name)
}

// RocketMQNewIO 在本代 Registry 中按别名解析 RocketMQ 生产者（首次访问才建连）。
func (s *Snapshot) RocketMQNewIO(ctx context.Context, name string) (*gofyrocketmq.Client, error) {
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

// Middleware pins the generation that is current when a request enters. A
// module that reloads starts the successor before serving its handler; without
// this step, in-flight requests would read the successor's handles halfway
// through.
//
// Middleware 把进入请求时的当前一代固定到上下文。模块在 Reload 时先启动下一代
// 再发布 Handler，没有这一步，旧代请求会在中途读到下一代句柄。
func Middleware() support.Middleware {
	return currentSnapshot.Middleware()
}
