package worker_test

import (
	"strings"
	"testing"

	gofyamqp "github.com/bingcool/gofy/support/amqp"
	gofyconsumer "github.com/bingcool/gofy/support/consumer"
	gofykafka "github.com/bingcool/gofy/support/kafka"
	gofyredis "github.com/bingcool/gofy/support/redis"
	gofyrocketmq "github.com/bingcool/gofy/support/rocketmq"
	gofyworker "github.com/bingcool/gofy/support/worker"

	appworker "github.com/yourorg/demo-service/worker"
)

// testConnections is the broker table registration resolves against. Nothing
// connects, so unroutable addresses are enough and no service is required.
// testConnections 是注册阶段解析用的 broker 连接表。注册不建连，因此不可路由的地址
// 就够用，也不需要任何服务。
func testConnections() gofyconsumer.Connections {
	return gofyconsumer.Connections{
		Kafka:    map[string]gofykafka.Config{"default": {Brokers: []string{"127.0.0.1:9092"}}},
		AMQP:     map[string]gofyamqp.Config{"default": {Hosts: []gofyamqp.Host{{Host: "127.0.0.1", Port: 5672}}}},
		RocketMQ: map[string]gofyrocketmq.Config{"default": {Endpoint: "127.0.0.1:8081"}},
		Redis:    map[string]gofyredis.Config{"ratelimit": {Addr: "127.0.0.1:6379"}},
	}
}

// TestRegisterBuildsEveryGroup pins the registration list and the count each
// worker carries. A group's members and their instance counts are code now, so
// this test is where a drift from the documented list shows up.
// TestRegisterBuildsEveryGroup 固定注册清单与每个 worker 自带的实例数。分组的成员与
// 实例数现在是代码，因此与文档不一致时，会在这一条测试里暴露。
func TestRegisterBuildsEveryGroup(t *testing.T) {
	groups, err := appworker.Register(testConnections())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	got := make(map[string][]string, len(groups))
	for _, g := range groups {
		for _, w := range g.Workers() {
			got[g.Name()] = append(got[g.Name()], w.Name())
		}
	}
	// A subscription is a file, so every consumer is registered
	// unconditionally. Which ones run is decided by --name at startup.
	// 一个订阅一个文件，因此每个消费者都无条件注册。实际运行哪些由启动时的 --name 决定。
	want := map[string][]string{
		"group1": {
			"order-scan",
			"order-kafka-consumer",
			"order-amqp-consumer",
			"order-rocketmq-consumer",
			"order-queues-consumer",
			"order-heartbeat",
		},
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
		"group1/order-scan":              2,
		"group1/order-kafka-consumer":    1,
		"group1/order-amqp-consumer":     1,
		"group1/order-rocketmq-consumer": 1,
		"group1/order-queues-consumer":   2,
		"group1/order-heartbeat":         1,
		"group2/report-summary":          1,
	}
	for key, want := range wantDeclared {
		if declared[key] != want {
			t.Fatalf("declared concurrency for %s = %d, want %d (all: %v)", key, declared[key], want, declared)
		}
	}
}

// TestRegisterRejectsUnknownInstance keeps the failure at registration time: a
// consumer pointing at an instance the application never decoded must not reach
// the point where it silently runs nothing.
// TestRegisterRejectsUnknownInstance 把失败留在注册阶段：消费者指向应用从未解码的实例
// 时，不能走到"启动成功但什么都不消费"那一步。
func TestRegisterRejectsUnknownInstance(t *testing.T) {
	conns := testConnections()
	delete(conns.Kafka, "default")

	_, err := appworker.Register(conns)
	if err == nil {
		t.Fatal("Register succeeded without the kafka instance the consumer names")
	}
	if !strings.Contains(err.Error(), `kafka instance "default"`) {
		t.Fatalf("error = %v, want it to name the missing instance", err)
	}
}
