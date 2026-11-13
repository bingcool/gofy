package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// ErrTopicRequired is returned when a producer or consumer has no topic.
// ErrTopicRequired 在生产或消费未指定 topic 时返回。
var ErrTopicRequired = errors.New("kafka: topic is required")

// ProducerConfig describes one producer. Durability and retry settings live on
// Config because they apply to the shared client.
// ProducerConfig 描述一个生产者。持久性与重试设置放在 Config 上，因为它们
// 作用于共享客户端。
type ProducerConfig struct {
	Topic string `yaml:"topic"`
	// FlushTimeout bounds one Publish call, including waiting for the broker
	// acknowledgement. Zero uses 5s.
	// FlushTimeout 限制单次 Publish 的耗时，包含等待 broker 确认，0 表示 5s。
	FlushTimeout time.Duration `yaml:"flush_timeout" default:"5s"`
}

func (c ProducerConfig) validate() error {
	if strings.TrimSpace(c.Topic) == "" {
		return ErrTopicRequired
	}
	if c.FlushTimeout < 0 {
		return errors.New("kafka: flush_timeout must not be negative")
	}
	return nil
}

func (c ProducerConfig) withDefaults() ProducerConfig {
	out := c
	out.Topic = strings.TrimSpace(out.Topic)
	if out.FlushTimeout == 0 {
		out.FlushTimeout = 5 * time.Second
	}
	return out
}

// Producer publishes records to one topic. It shares the client's connection
// and is safe for concurrent use.
// Producer 向一个 topic 发布记录。它共用客户端的连接，可以并发使用。
type Producer struct {
	raw *kgo.Client
	cfg ProducerConfig
}

// NewProducer validates cfg and binds it to client.
// NewProducer 校验 cfg 并把它绑定到 client。
func NewProducer(client *Client, cfg ProducerConfig) (*Producer, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	raw, _, _, err := client.rawClient()
	if err != nil {
		return nil, err
	}
	return &Producer{raw: raw, cfg: cfg}, nil
}

// Config returns the effective producer configuration.
// Config 返回实际生效的生产者配置。
func (p *Producer) Config() ProducerConfig {
	return p.cfg
}

// PublishOptions overrides the defaults for one record.
// PublishOptions 覆盖单条记录的默认值。
type PublishOptions struct {
	// Topic overrides ProducerConfig.Topic.
	// Topic 覆盖 ProducerConfig.Topic。
	Topic string
	// Key chooses the partition through Kafka's default partitioner. Records
	// with the same key keep their order, so use a stable business id such as
	// an order id when ordering matters.
	// Key 经 Kafka 默认分区器决定分区。相同 key 的记录保持顺序，因此对顺序
	// 敏感的场景应使用订单号这类稳定的业务 ID。
	Key       string
	Headers   map[string]string
	Timestamp time.Time
}

// Publish sends body and waits for the broker acknowledgement. The wait is
// bounded by ProducerConfig.FlushTimeout and by ctx, whichever ends first.
// Publish 发送 body 并等待 broker 确认，等待时间由 ProducerConfig.FlushTimeout
// 与 ctx 中先到期者决定。
func (p *Producer) Publish(ctx context.Context, body []byte, opts PublishOptions) error {
	if p == nil || p.raw == nil {
		return ErrClientRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	topic := strings.TrimSpace(opts.Topic)
	if topic == "" {
		topic = p.cfg.Topic
	}
	if topic == "" {
		return ErrTopicRequired
	}

	record := &kgo.Record{
		Topic:     topic,
		Key:       []byte(opts.Key),
		Value:     body,
		Headers:   recordHeaders(opts.Headers),
		Timestamp: opts.Timestamp,
	}
	flushCtx, cancel := context.WithTimeout(ctx, p.cfg.FlushTimeout)
	defer cancel()

	if _, err := p.raw.ProduceSync(flushCtx, record).First(); err != nil {
		if errors.Is(err, kgo.ErrClientClosed) {
			return ErrClientClosed
		}
		return fmt.Errorf("kafka: publish to %q: %w", topic, err)
	}
	return nil
}

// PublishJSON marshals value as JSON and publishes it. The record gets a
// content-type header when the caller did not set one.
// PublishJSON 把 value 序列化为 JSON 后发布。调用方未设置 content-type 头时
// 会自动补上 application/json。
func (p *Producer) PublishJSON(ctx context.Context, value any, opts PublishOptions) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("kafka: marshal message: %w", err)
	}
	opts.Headers = withContentType(opts.Headers, "application/json")
	return p.Publish(ctx, body, opts)
}

func recordHeaders(headers map[string]string) []kgo.RecordHeader {
	if len(headers) == 0 {
		return nil
	}
	out := make([]kgo.RecordHeader, 0, len(headers))
	for key, value := range headers {
		out = append(out, kgo.RecordHeader{Key: key, Value: []byte(value)})
	}
	return out
}

func withContentType(headers map[string]string, contentType string) map[string]string {
	out := make(map[string]string, len(headers)+1)
	for key, value := range headers {
		out[key] = value
	}
	if _, ok := out["content-type"]; !ok {
		out["content-type"] = contentType
	}
	return out
}
