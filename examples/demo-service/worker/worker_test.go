package worker_test

import (
	"strings"
	"testing"

	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyworker "github.com/bingcool/gofy/support/worker"

	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	appworker "github.com/yourorg/demo-service/worker"
)

// TestRegisterBuildsEveryGroup pins the registration list and the count each
// worker carries. The count now lives in the group package next to the worker,
// so a group that drifts from its documented number would change how many
// instances start without anything else noticing.
// TestRegisterBuildsEveryGroup 固定注册清单与每个 worker 自带的实例数。计数现在就在
// 分组包里、紧挨它配置的 worker，某处与文档里的数字脱节时，启动的实例数会跟着变而
// 没有人察觉。
func TestRegisterBuildsEveryGroup(t *testing.T) {
	groups, err := appworker.Register(appworker.Deps{})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	got := make(map[string][]string, len(groups))
	for _, g := range groups {
		for _, w := range g.Workers() {
			got[g.Name()] = append(got[g.Name()], w.Name())
		}
	}
	// Consumers stay out of the list until they are enabled, so an
	// out-of-the-box config registers only the two loops group1 shipped with.
	// 消费者在启用之前不会进入清单，因此默认配置只注册 group1 原本的两个循环。
	want := map[string][]string{
		"group1": {"order-scan", "order-heartbeat"},
		"group2": {"report-summary"},
	}
	if len(got) != len(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	for name, wantWorkers := range want {
		if strings.Join(got[name], ",") != strings.Join(wantWorkers, ",") {
			t.Fatalf("group %s = %v, want %v", name, got[name], wantWorkers)
		}
	}

	declared := make(map[string]int, len(got))
	for _, g := range groups {
		for _, w := range g.Workers() {
			if c, ok := w.(gofyworker.Concurrent); ok {
				declared[g.Name()+"/"+w.Name()] = int(c.Concurrency())
			}
		}
	}
	wantDeclared := map[string]int{
		"group1/order-scan":      2,
		"group1/order-heartbeat": 1,
		"group2/report-summary":  1,
	}
	for key, want := range wantDeclared {
		if declared[key] != want {
			t.Fatalf("declared concurrency for %s = %d, want %d (all: %v)", key, declared[key], want, declared)
		}
	}
}

// TestRegisterAddsEnabledConsumers proves that a consumer only becomes a worker
// when its block is enabled, and that the declared concurrency follows the
// configuration rather than a hard-coded number.
// TestRegisterAddsEnabledConsumers 验证消费者只有在其配置块启用时才变成 worker，
// 且声明的并发数跟随配置，而不是写死的数字。
func TestRegisterAddsEnabledConsumers(t *testing.T) {
	groups, err := appworker.Register(appworker.Deps{
		Group1: gofyconsumer.Config{Consumers: gofyconsumer.Consumers{
			Kafka: gofyconsumer.KafkaConsumer{
				Enabled:     true,
				Instance:    "default",
				Concurrency: 3,
				Topic:       "demo.order.events",
				Group:       "demo-service-order-consumer",
				MaxAttempts: 5,
			},
			Queues: gofyconsumer.QueuesConsumer{
				Enabled:       true,
				RedisInstance: "ratelimit",
				Concurrency:   4,
				Key:           "demo:order:events",
				MaxAttempts:   5,
			},
		}},
		Connections: gofyconsumer.Connections{
			Kafka: map[string]gofykafka.Config{
				"default": {Brokers: []string{"127.0.0.1:9092"}},
			},
			Redis: map[string]gofyredis.Config{
				"ratelimit": {Addr: "127.0.0.1:6379"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	var names []string
	declared := make(map[string]int)
	for _, g := range groups {
		if g.Name() != "group1" {
			continue
		}
		for _, w := range g.Workers() {
			names = append(names, w.Name())
			if c, ok := w.(gofyworker.Concurrent); ok {
				declared[w.Name()] = int(c.Concurrency())
			}
		}
	}
	want := "order-scan,order-kafka-consumer,order-queues-consumer,order-heartbeat"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("group1 workers = %s, want %s", got, want)
	}
	if declared["order-kafka-consumer"] != 3 {
		t.Fatalf("kafka concurrency = %d, want 3", declared["order-kafka-consumer"])
	}
	if declared["order-queues-consumer"] != 4 {
		t.Fatalf("queues concurrency = %d, want 4", declared["order-queues-consumer"])
	}
}

// TestRegisterRejectsUnknownInstance keeps the failure at registration time: a
// consumer pointing at an instance that is missing or disabled must not reach
// the point where it silently runs nothing.
// TestRegisterRejectsUnknownInstance 把失败留在注册阶段：消费者指向缺失或未启用的
// 实例时，不能走到"启动成功但什么都不消费"那一步。
func TestRegisterRejectsUnknownInstance(t *testing.T) {
	_, err := appworker.Register(appworker.Deps{
		Group1: gofyconsumer.Config{Consumers: gofyconsumer.Consumers{
			Kafka: gofyconsumer.KafkaConsumer{
				Enabled:     true,
				Instance:    "order",
				Concurrency: 1,
				Topic:       "demo.order.events",
				Group:       "demo-service-order-consumer",
				MaxAttempts: 5,
			},
		}},
		Connections: gofyconsumer.Connections{
			Kafka: map[string]gofykafka.Config{
				"default": {Brokers: []string{"127.0.0.1:9092"}},
			},
		},
	})
	if err == nil {
		t.Fatal("Register succeeded with a kafka instance that is not configured")
	}
	if !strings.Contains(err.Error(), `kafka instance "order"`) {
		t.Fatalf("error = %v, want it to name the missing instance", err)
	}
}
