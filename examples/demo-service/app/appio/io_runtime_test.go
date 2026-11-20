package appio

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/core"
	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofylock "github.com/bingcool/gofy/support/lock"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	appkafka "github.com/yourorg/demo-service/app/appio/kafka"
	appredis "github.com/yourorg/demo-service/app/appio/redis"
	appredislock "github.com/yourorg/demo-service/app/appio/redislock"
	approcketmq "github.com/yourorg/demo-service/app/appio/rocketmq"
	appuuidint "github.com/yourorg/demo-service/app/appio/uuidint"
	"gorm.io/gorm"
)

func TestStartInstallsAndStopClearsHandles(t *testing.T) {
	mr := miniredis.RunT(t)
	rt := core.NewRuntime(core.RuntimeConfig{DB: &gorm.DB{}})
	m := NewModule(rt, map[string]gofy.DatabaseConfig{
		"default": {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
	}, appredis.Config{
		appredis.InstanceRateLimit: {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr()}},
	}, nil, nil, nil, appuuidint.Config{
		Enabled: true, RedisInstance: appredis.InstanceRateLimit, Key: "demo:uuid",
		PoolSize: 5, PoolInterval: time.Hour,
	}, gofylock.Config{
		Enabled: true, RedisInstance: appredis.InstanceRateLimit, Prefix: "demo:lock", Expiry: time.Minute,
	})

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(ctx)
		Publish(nil)
	})

	// The close list is opening order and Stop walks it backwards. Pinning it
	// here keeps the UUID pool ahead of the Redis clients its ticker reads from.
	// 关闭清单是打开顺序，Stop 反向遍历。在这里钉住它，UUID 池才会先于它读取的
	// Redis 客户端被停止。
	if got := closerNames(m.closers); strings.Join(got, ",") != "db,redis,amqp,kafka,rocketmq,uuidint,lock" {
		t.Fatalf("close order = %v", got)
	}
	if appdb.Default() != rt.DB {
		t.Fatal("db.Default does not point at the runtime connection")
	}
	if Current() == nil {
		t.Fatal("no snapshot was published")
	}
	if got, err := Current().NewDBIO(ctx, appdb.DefaultInstance); err != nil || got != rt.DB {
		t.Fatalf("snapshot NewDBIO(default) = %v, %v; want the runtime connection", got, err)
	}
	if appredis.RateLimit() == nil {
		t.Fatal("redis.RateLimit was not installed")
	}
	if err := appredis.RateLimit().Ping(ctx).Err(); err != nil {
		t.Fatalf("ping ratelimit: %v", err)
	}
	viaIO, err := appredis.NewIO(ctx, appredis.InstanceRateLimit)
	if err != nil {
		t.Fatalf("redis.NewIO: %v", err)
	}
	if viaIO != appredis.RateLimit() {
		t.Fatal("redis.NewIO does not return the installed instance")
	}
	pinned, err := Current().RedisIO(ctx, appredis.InstanceRateLimit)
	if err != nil {
		t.Fatalf("snapshot RedisIO: %v", err)
	}
	if pinned != appredis.RateLimit() {
		t.Fatal("snapshot RedisIO does not resolve this generation's instance")
	}
	if appuuidint.Default() == nil {
		t.Fatal("uuidint.Default was not installed")
	}
	raw, err := mr.Get("demo:uuid")
	if err != nil {
		t.Fatalf("read uuid counter: %v", err)
	}
	if raw != "5" {
		t.Fatalf("uuid pool warm fill = %q, want 5", raw)
	}
	id, err := appuuidint.Default().Get(ctx)
	if err != nil {
		t.Fatalf("uuidint.Get: %v", err)
	}
	if id <= 0 {
		t.Fatalf("uuidint id = %d, want > 0", id)
	}
	if raw, err := mr.Get("demo:uuid"); err != nil || raw != "5" {
		t.Fatalf("uuidint.Get did not consume the pool: value=%q err=%v", raw, err)
	}
	// The lock is bound to this generation's Redis client and writes the
	// configured prefix, which is what makes two applications able to share a db.
	// 锁绑定本代 Redis 客户端并写入配置的前缀，这正是两个应用能共用同一个库的原因。
	if appredislock.Default() == nil || Current().RedisLock() == nil {
		t.Fatal("redislock was not installed")
	}
	mu, err := Current().RedisLock().Lock(ctx, "io-test")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if !mr.Exists("demo:lock:io-test") {
		t.Fatal("the lock did not reach this generation's redis with the configured prefix")
	}
	if err := mu.Unlock(ctx); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if appuuidint.Default() != nil {
		t.Fatal("uuidint.Default was not cleared")
	}
	if appredislock.Default() != nil {
		t.Fatal("redislock.Default was not cleared")
	}
	if appredis.RateLimit() != nil {
		t.Fatal("redis.RateLimit was not cleared")
	}
	if Current() != nil {
		t.Fatal("Stop did not withdraw the published snapshot")
	}
	if _, err := appredis.NewIO(ctx, appredis.InstanceRateLimit); !errors.Is(err, gofyredis.ErrNotConfigured) {
		t.Fatalf("redis.NewIO after Stop = %v, want ErrNotConfigured", err)
	}
}

// TestStartInstallsAMQPInstance 需要真实 RabbitMQ：
//
//	GOFY_TEST_AMQP_ADDR=127.0.0.1:5672 GOFY_TEST_AMQP_USERNAME=rabbitmq \
//	GOFY_TEST_AMQP_PASSWORD=... GOFY_TEST_AMQP_VHOST=my_vhost \
//	  go test ./app/appio -run AMQP -v
func TestStartInstallsAMQPInstance(t *testing.T) {
	addr := os.Getenv("GOFY_TEST_AMQP_ADDR")
	if addr == "" {
		t.Skip("set GOFY_TEST_AMQP_ADDR to run the AMQP wiring test")
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("GOFY_TEST_AMQP_ADDR: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("GOFY_TEST_AMQP_ADDR port: %v", err)
	}

	mr := miniredis.RunT(t)
	rt := core.NewRuntime(core.RuntimeConfig{DB: &gorm.DB{}})
	m := NewModule(rt, map[string]gofy.DatabaseConfig{
		"default": {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
	}, appredis.Config{
		appredis.InstanceRateLimit: {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr()}},
	}, appamqp.Config{
		appamqp.DefaultInstance: {Enabled: true, Config: gofyamqp.Config{
			Hosts: []gofyamqp.Host{{
				Host:     host,
				Port:     port,
				Username: os.Getenv("GOFY_TEST_AMQP_USERNAME"),
				Password: os.Getenv("GOFY_TEST_AMQP_PASSWORD"),
				Vhost:    os.Getenv("GOFY_TEST_AMQP_VHOST"),
			}},
		}},
	}, nil, nil, appuuidint.Config{}, gofylock.Config{})

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(ctx)
		Publish(nil)
	})

	if appamqp.Default() == nil {
		t.Fatal("amqp.Default was not installed")
	}
	if client := appamqp.Get(appamqp.DefaultInstance); client != appamqp.Default() {
		t.Fatal("Get(default) does not return the installed handle")
	}

	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if appamqp.Default() != nil {
		t.Fatal("amqp.Default was not cleared")
	}
}

// TestStartInstallsKafkaInstance 需要真实 Kafka：
//
//	GOFY_TEST_KAFKA_BROKERS=127.0.0.1:9092 go test ./app/appio -run Kafka -v
func TestStartInstallsKafkaInstance(t *testing.T) {
	value := os.Getenv("GOFY_TEST_KAFKA_BROKERS")
	if strings.TrimSpace(value) == "" {
		t.Skip("set GOFY_TEST_KAFKA_BROKERS to run the Kafka wiring test")
	}
	brokers := make([]string, 0, 2)
	for _, broker := range strings.Split(value, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			brokers = append(brokers, broker)
		}
	}

	mr := miniredis.RunT(t)
	rt := core.NewRuntime(core.RuntimeConfig{DB: &gorm.DB{}})
	m := NewModule(rt, map[string]gofy.DatabaseConfig{
		"default": {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
	}, appredis.Config{
		appredis.InstanceRateLimit: {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr()}},
	}, nil, appkafka.Config{
		appkafka.DefaultInstance: {Enabled: true, Config: gofykafka.Config{
			Brokers:     brokers,
			ClientID:    "demo-service-test",
			DialTimeout: 5 * time.Second,
		}},
	}, nil, appuuidint.Config{}, gofylock.Config{})

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(ctx)
		Publish(nil)
	})

	if appkafka.Default() == nil {
		t.Fatal("kafka.Default was not installed")
	}
	if client := appkafka.Get(appkafka.DefaultInstance); client != appkafka.Default() {
		t.Fatal("Get(default) does not return the installed handle")
	}

	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if appkafka.Default() != nil {
		t.Fatal("kafka.Default was not cleared")
	}
}

// TestStartInstallsRocketMQInstance 需要真实 RocketMQ gRPC proxy：
//
//	GOFY_TEST_ROCKETMQ_ENDPOINT=127.0.0.1:8081 go test ./app/appio -run RocketMQ -v
func TestStartInstallsRocketMQInstance(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("GOFY_TEST_ROCKETMQ_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set GOFY_TEST_ROCKETMQ_ENDPOINT to run the RocketMQ wiring test")
	}

	mr := miniredis.RunT(t)
	rt := core.NewRuntime(core.RuntimeConfig{DB: &gorm.DB{}})
	m := NewModule(rt, map[string]gofy.DatabaseConfig{
		"default": {DSN: "user:pass@tcp(127.0.0.1:3306)/main"},
	}, appredis.Config{
		appredis.InstanceRateLimit: {Enabled: true, Config: gofyredis.Config{Addr: mr.Addr()}},
	}, nil, nil, approcketmq.Config{
		approcketmq.DefaultInstance: {Enabled: true, Config: gofyrocketmq.Config{
			Endpoint:    endpoint,
			SendTimeout: 5 * time.Second,
		}},
	}, appuuidint.Config{}, gofylock.Config{})

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(ctx)
		Publish(nil)
	})

	if approcketmq.Default() == nil {
		t.Fatal("rocketmq.Default was not installed")
	}
	if client := approcketmq.Get(approcketmq.DefaultInstance); client != approcketmq.Default() {
		t.Fatal("Get(default) does not return the installed handle")
	}

	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if approcketmq.Default() != nil {
		t.Fatal("rocketmq.Default was not cleared")
	}
}

// closerNames is the ordering assertion helper: the list itself stays the
// source of truth, this only makes it comparable.
// closerNames 是顺序断言用的辅助函数：清单本身仍是唯一事实来源，这里只把它变成
// 可比较的形式。
func closerNames(closers []namedCloser) []string {
	names := make([]string, 0, len(closers))
	for _, c := range closers {
		names = append(names, c.name)
	}
	return names
}

// TestCloseAllRunsInReverseOrder fixes the rule Stop depends on: a resource is
// closed only after everything opened after it, which is what puts UUID (and
// its Redis-touching ticker) before the clients it uses. It also pins that the
// caller's budget reaches every closer: only the pool uses it, but a closer
// that never sees it could not honor it either.
// TestCloseAllRunsInReverseOrder 固定 Stop 依赖的规则：资源在它之后打开的全部关闭
// 完之后才关闭，UUID（及其访问 Redis 的 ticker）因此排在它使用的客户端之前。同时
// 钉住调用方的预算会到达每个 closer：只有池会用它，但拿不到它的 closer 也无从遵守。
func TestCloseAllRunsInReverseOrder(t *testing.T) {
	var order []string
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	mk := func(name string) namedCloser {
		return namedCloser{name: name, close: func(ctx context.Context) error {
			if got, ok := ctx.Deadline(); !ok || !got.Equal(deadline) {
				t.Errorf("%s got no release budget", name)
			}
			order = append(order, name)
			return nil
		}}
	}
	if err := closeAll(ctx, []namedCloser{mk("db"), mk("redis"), mk("uuidint"), mk("lock")}); err != nil {
		t.Fatalf("closeAll: %v", err)
	}
	want := "lock,uuidint,redis,db"
	if got := strings.Join(order, ","); got != want {
		t.Fatalf("close order = %q, want %q", got, want)
	}
}

// TestCloseAllKeepsGoingAndNamesFailures: one failing Close must not skip the
// rest, and the joined error has to say which resource failed — otherwise the
// operator sees a driver error with no owner.
// TestCloseAllKeepsGoingAndNamesFailures：一个 Close 失败不能跳过其余，聚合错误
// 必须指出是哪个资源失败，否则运维只看到一个没有归属的驱动错误。
func TestCloseAllKeepsGoingAndNamesFailures(t *testing.T) {
	var order []string
	closers := []namedCloser{
		{name: "db", close: func(context.Context) error { order = append(order, "db"); return nil }},
		{name: "redis", close: func(context.Context) error { order = append(order, "redis"); return errors.New("boom") }},
		{name: "uuidint", close: func(context.Context) error { order = append(order, "uuidint"); return errors.New("ticker stuck") }},
	}

	err := closeAll(context.Background(), closers)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"redis: boom", "uuidint: ticker stuck"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	if len(order) != len(closers) {
		t.Fatalf("closed %v, want all %d resources", order, len(closers))
	}
}

// TestRollbackJoinsTheCleanupFailure is the audit finding made testable: a
// failed rollback used to be dropped, so a leaked connection was invisible
// behind the startup error. Both errors must now survive, and the original must
// stay identifiable through errors.Is.
// TestRollbackJoinsTheCleanupFailure 把审计结论变成可测的：回滚失败原先被丢弃，连接
// 泄漏就藏在启动错误后面。现在两者都必须保留，且原始错误仍能通过 errors.Is 识别。
func TestRollbackJoinsTheCleanupFailure(t *testing.T) {
	cause := errors.New("redis: dial tcp 127.0.0.1:6379: connect: connection refused")
	opened := []namedCloser{
		{name: "db", close: func(context.Context) error { return nil }},
		{name: "amqp", close: func(context.Context) error { return errors.New("broker already gone") }},
	}

	err := rollback(context.Background(), opened, cause)

	if !errors.Is(err, cause) {
		t.Fatalf("rollback error %v does not wrap the startup error", err)
	}
	for _, want := range []string{cause.Error(), "amqp: broker already gone"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
	// Two failures, two lines: the closer that succeeded must not add a third.
	// 两个失败两行：关闭成功的资源不该多出第三行。
	if lines := strings.Split(strings.TrimSpace(err.Error()), "\n"); len(lines) != 2 {
		t.Fatalf("error has %d lines, want 2: %q", len(lines), err)
	}
}

// TestRollbackWithoutCleanupFailureKeepsTheOriginalMessage: when the rollback is
// clean, the returned error must read exactly like the startup error — an
// errors.Join with a nil cleanup still prints the same text, and this pins it.
// TestRollbackWithoutCleanupFailureKeepsTheOriginalMessage：回滚干净时，返回的错误
// 必须与启动错误逐字相同——errors.Join 聚合一个 nil 时打印的就是原文本，这里钉住它。
func TestRollbackWithoutCleanupFailureKeepsTheOriginalMessage(t *testing.T) {
	cause := errors.New("amqp: connect: connection refused")
	err := rollback(context.Background(), []namedCloser{
		{name: "db", close: func(context.Context) error { return nil }},
	}, cause)
	if err == nil {
		t.Fatal("rollback dropped the startup error")
	}
	if err.Error() != cause.Error() {
		t.Fatalf("error = %q, want %q", err, cause.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error %v does not wrap the startup error", err)
	}
}
