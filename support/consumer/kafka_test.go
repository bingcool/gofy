package consumer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	gofykafka "github.com/bingcool/gofy/support/kafka"
)

// kafkaConns is the connection table these tests resolve against. Registration
// never connects, so a broker address that is not listening is enough.
// kafkaConns 是这些测试解析的连接表。注册不建连，因此一个没人监听的 broker 地址
// 就够用。
func kafkaConns() Connections {
	return Connections{
		Kafka: map[string]gofykafka.Config{
			"default": {Brokers: []string{"127.0.0.1:9092"}},
		},
	}
}

func noopKafkaHandler(context.Context, KafkaMessage) error { return nil }

// TestNewKafkaAppliesDefaults pins the zero-value contract: a consumer file that
// only names its topic and group still gets at-least-once delivery, a manual
// commit and a bounded attempt budget.
// TestNewKafkaAppliesDefaults 固定零值契约：只写了 topic 与 group 的消费者文件，仍然
// 得到至少一次投递、手动提交与有上限的尝试次数。
func TestNewKafkaAppliesDefaults(t *testing.T) {
	consumer, err := NewKafka(kafkaConns(), KafkaConfig{
		Name:    "order-consumer",
		Topic:   "demo.order.events",
		Group:   "order-group",
		Handler: noopKafkaHandler,
	})
	if err != nil {
		t.Fatalf("NewKafka: %v", err)
	}
	cfg := consumer.cfg
	if cfg.Instance != "default" {
		t.Fatalf("instance = %q, want default", cfg.Instance)
	}
	if cfg.Concurrency != 1 || consumer.Concurrency() != 1 {
		t.Fatalf("concurrency = %d/%d, want 1", cfg.Concurrency, consumer.Concurrency())
	}
	if cfg.CommitMode != string(gofykafka.CommitModeManual) {
		t.Fatalf("commit mode = %q, want manual", cfg.CommitMode)
	}
	if cfg.SessionTimeout != 45*time.Second {
		t.Fatalf("session timeout = %v", cfg.SessionTimeout)
	}
	if cfg.MaxAttempts != 5 {
		t.Fatalf("max attempts = %d, want 5", cfg.MaxAttempts)
	}
	if consumer.Name() != "order-consumer" {
		t.Fatalf("name = %q", consumer.Name())
	}
}

// TestNewKafkaRejects covers the mistakes worth catching before a goroutine
// exists: a typo in the name, a missing topic or group, a handler nobody
// supplied and an instance the application never configured.
// TestNewKafkaRejects 覆盖值得在任何 goroutine 存在之前拦下的错误：名字拼错、缺
// topic 或 group、没给 handler，以及应用根本没配置的实例。
func TestNewKafkaRejects(t *testing.T) {
	valid := KafkaConfig{
		Name:    "order-consumer",
		Topic:   "demo.order.events",
		Group:   "order-group",
		Handler: noopKafkaHandler,
	}
	tests := []struct {
		name   string
		mutate func(*KafkaConfig)
		want   string
	}{
		{name: "no name", mutate: func(c *KafkaConfig) { c.Name = " " }, want: "name is required"},
		{name: "bad name", mutate: func(c *KafkaConfig) { c.Name = "order consumer" }, want: "invalid worker name"},
		{name: "no topic", mutate: func(c *KafkaConfig) { c.Topic = "" }, want: "topic is required"},
		{name: "no group", mutate: func(c *KafkaConfig) { c.Group = "" }, want: "group is required"},
		{name: "no handler", mutate: func(c *KafkaConfig) { c.Handler = nil }, want: "handler is required"},
		{
			name:   "too many instances",
			mutate: func(c *KafkaConfig) { c.Concurrency = 256 },
			want:   "concurrency must be between 1 and 255",
		},
		{name: "bad commit mode", mutate: func(c *KafkaConfig) { c.CommitMode = "batch" }, want: "commit_mode"},
		{name: "bad offset reset", mutate: func(c *KafkaConfig) { c.AutoOffsetReset = "middle" }, want: "auto_offset_reset"},
		{name: "unknown instance", mutate: func(c *KafkaConfig) { c.Instance = "order" }, want: `kafka instance "order"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			_, err := NewKafka(kafkaConns(), cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewKafka = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

// TestKafkaMessageID pins the deduplication key: the producer header wins, and
// the partition offset is the fallback because it is stable across
// redeliveries of the same record.
// TestKafkaMessageID 固定去重键：生产者头优先，分区位点兜底，因为它在同一条记录的
// 重投之间保持稳定。
func TestKafkaMessageID(t *testing.T) {
	record := &kgo.Record{Topic: "orders", Partition: 2, Offset: 42}
	if got := kafkaMessageID(record, nil); got != "orders/2@42" {
		t.Fatalf("id = %q, want orders/2@42", got)
	}
	if got := kafkaMessageID(record, map[string]string{"message_id": "evt-9"}); got != "evt-9" {
		t.Fatalf("id = %q, want evt-9", got)
	}
}

// TestKafkaMessageKeepsHeadersAndNative checks both halves of the conversion:
// traceparent survives so the consumer joins the producer's trace, and the
// native record is carried so the handler can reach the offset.
// TestKafkaMessageKeepsHeadersAndNative 校验转换的两半：traceparent 保留下来，消费者
// 因此接上生产者的链路；原生记录被带上，handler 因此能读到位点。
func TestKafkaMessageKeepsHeadersAndNative(t *testing.T) {
	record := &kgo.Record{
		Topic:     "orders",
		Partition: 1,
		Offset:    9,
		Key:       []byte("user-7"),
		Value:     []byte(`{"type":"order.paid"}`),
		Headers: []kgo.RecordHeader{
			{Key: "traceparent", Value: []byte("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")},
		},
	}
	msg := kafkaMessage(record)
	if msg.ID != "orders/1@9" {
		t.Fatalf("ID = %q", msg.ID)
	}
	if msg.Headers["traceparent"] == "" {
		t.Fatal("traceparent header was dropped")
	}
	if msg.Key != "user-7" {
		t.Fatalf("Key = %q, want user-7", msg.Key)
	}
	if msg.Native != record {
		t.Fatal("the native record was not carried through")
	}
}
