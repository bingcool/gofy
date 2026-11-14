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
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	appkafka "github.com/yourorg/demo-service/app/appio/kafka"
	appredis "github.com/yourorg/demo-service/app/appio/redis"
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
	})

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = m.Stop(ctx)
		Publish(nil)
	})

	if appdb.Default() != rt.DB {
		t.Fatal("db.Default does not point at the runtime connection")
	}
	if Current() == nil {
		t.Fatal("no snapshot was published")
	}
	if got, err := Current().DBIO(ctx, appdb.DefaultInstance); err != nil || got != rt.DB {
		t.Fatalf("snapshot DBIO(default) = %v, %v; want the runtime connection", got, err)
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

	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if appuuidint.Default() != nil {
		t.Fatal("uuidint.Default was not cleared")
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
	}, nil, nil, appuuidint.Config{})

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
	}, nil, appuuidint.Config{})

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
	}, appuuidint.Config{})

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
