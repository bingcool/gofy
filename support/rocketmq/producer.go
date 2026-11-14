package rocketmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
)

const (
	maxKeys               = 32
	maxKeyBytes           = 128
	maxProperties         = 32
	maxPropertyValueBytes = 1024
	maxPropertiesBytes    = 16 << 10
)

var (
	// ErrTopicRequired is returned when a message has no topic.
	// ErrTopicRequired 在消息没有 topic 时返回。
	ErrTopicRequired = errors.New("rocketmq: topic is required")
	// ErrInvalidDelay is returned when DeliverAt is not in the future.
	// ErrInvalidDelay 在 DeliverAt 不是未来时间时返回。
	ErrInvalidDelay = errors.New("rocketmq: deliver_at must be in the future")
	// ErrTooManyKeys is returned when a message has more than 32 keys.
	// ErrTooManyKeys 在消息的 key 超过 32 个时返回。
	ErrTooManyKeys = errors.New("rocketmq: too many keys")
	// ErrInvalidKey is returned for an empty, oversized or whitespace key.
	// ErrInvalidKey 在 key 为空、超长或含空白字符时返回。
	ErrInvalidKey = errors.New("rocketmq: invalid key")
	// ErrTooManyProperties is returned when a message has more than 32
	// properties.
	// ErrTooManyProperties 在消息属性超过 32 个时返回。
	ErrTooManyProperties = errors.New("rocketmq: too many properties")
	// ErrInvalidPropertyKey is returned for a key that is empty, starts with
	// "__" or contains characters outside letters, digits, "_", "." and "-".
	// ErrInvalidPropertyKey 在属性 key 为空、以 "__" 开头或含有字母、数字、
	// "_"、"."、"-" 之外的字符时返回。
	ErrInvalidPropertyKey = errors.New("rocketmq: invalid property key")
	// ErrInvalidPropertyValue is returned for an empty or oversized property
	// value.
	// ErrInvalidPropertyValue 在属性值为空或超长时返回。
	ErrInvalidPropertyValue = errors.New("rocketmq: invalid property value")
	// ErrPropertiesTooLarge is returned when the properties exceed 16 KiB.
	// ErrPropertiesTooLarge 在属性总量超过 16 KiB 时返回。
	ErrPropertiesTooLarge = errors.New("rocketmq: properties exceed 16 KiB")
)

// PublishOptions describes one message. Topic is required for every call
// because a Client is not bound to one topic.
// PublishOptions 描述一条消息。Client 不绑定 topic，因此每次调用都必须给出
// Topic。
type PublishOptions struct {
	Topic string
	// Tag classifies the message; consumers subscribe by tag or SQL92.
	// Tag 用于给消息分类，消费者按 tag 或 SQL92 过滤订阅。
	Tag string
	// Keys are business keys indexed by the broker. Duplicates are removed;
	// at most 32 keys, each up to 128 bytes without whitespace.
	// Keys 是 broker 建立索引的业务键。重复项会被去掉；最多 32 个，每个不超过
	// 128 字节且不能含空白字符。
	Keys []string
	// Properties are custom message attributes. At most 32; keys may contain
	// letters, digits, "_", "." and "-" and must not start with "__"; values
	// are up to 1024 bytes and the total is limited to 16 KiB.
	// Properties 是自定义消息属性。最多 32 个；key 只能包含字母、数字、"_"、
	// "."、"-" 且不能以 "__" 开头；单个值不超过 1024 字节，总量不超过 16 KiB。
	Properties map[string]string
	// DeliverAt delays delivery until the given time. v5 uses an absolute
	// timestamp rather than the v2 delay levels. Zero sends immediately.
	// DeliverAt 把投递推迟到指定时刻。v5 使用绝对时间戳，不再是 v2 的延迟等级。
	// 零值表示立即发送。
	DeliverAt time.Time
	// Delay is the relative form of DeliverAt: the message becomes visible
	// after this duration. It is a convenience for producers that schedule
	// "in 10s" rather than "at 01:30"; setting both is an error.
	// Delay 是 DeliverAt 的相对形式：消息在这段时长之后才可见。适合"10 秒后"
	// 这类调度，而不必自己算"01:30 投递"；两者同时设置会报错。
	Delay time.Duration
	// MessageGroup selects the queue for FIFO messages; messages with the
	// same group are sent in order. The topic must accept FIFO messages.
	// MessageGroup 为 FIFO 消息选择队列，相同 group 的消息按顺序发送。该 topic
	// 必须支持 FIFO 消息。
	MessageGroup string
}

// SendResult reports where the broker stored the message.
// SendResult 报告 broker 存下该消息的位置。
type SendResult struct {
	MessageID string
	Topic     string
	Offset    int64
}

// Publish sends body synchronously and waits for the broker acknowledgement.
// The wait is bounded by Config.SendTimeout and by ctx, whichever ends first.
// Publish 同步发送 body 并等待 broker 确认，等待时间由 Config.SendTimeout 与
// ctx 中先到期者决定。
func (c *Client) Publish(ctx context.Context, body []byte, opts PublishOptions) (*SendResult, error) {
	raw, err := c.producer()
	if err != nil {
		return nil, err
	}
	message, err := buildMessage(body, opts)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	receipts, err := raw.Send(ctx, message)
	if err != nil {
		return nil, fmt.Errorf("rocketmq: publish to %q: %w", message.Topic, err)
	}
	if len(receipts) == 0 || receipts[0] == nil {
		return nil, fmt.Errorf("rocketmq: publish to %q: empty receipt", message.Topic)
	}
	return &SendResult{
		MessageID: receipts[0].MessageID,
		Topic:     message.Topic,
		Offset:    receipts[0].Offset,
	}, nil
}

// PublishJSON marshals value as JSON and publishes it. A content-type
// property is added when the caller did not set one.
// PublishJSON 把 value 序列化为 JSON 后发布。调用方未设置 content-type 属性时
// 会自动补上 application/json。
func (c *Client) PublishJSON(ctx context.Context, value any, opts PublishOptions) (*SendResult, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("rocketmq: marshal message: %w", err)
	}
	opts.Properties = withContentType(opts.Properties, "application/json")
	return c.Publish(ctx, body, opts)
}

func buildMessage(body []byte, opts PublishOptions) (*rmq.Message, error) {
	topic := strings.TrimSpace(opts.Topic)
	if topic == "" {
		return nil, ErrTopicRequired
	}
	deliverAt := opts.DeliverAt
	if opts.Delay != 0 {
		if opts.Delay < 0 {
			return nil, fmt.Errorf("%w: delay must not be negative", ErrInvalidDelay)
		}
		if !deliverAt.IsZero() {
			return nil, fmt.Errorf("%w: set either Delay or DeliverAt, not both", ErrInvalidDelay)
		}
		deliverAt = time.Now().Add(opts.Delay)
	}
	if !deliverAt.IsZero() && !deliverAt.After(time.Now()) {
		return nil, ErrInvalidDelay
	}
	keys, err := normalizeKeys(opts.Keys)
	if err != nil {
		return nil, err
	}
	if err := validateProperties(opts.Properties); err != nil {
		return nil, err
	}

	message := &rmq.Message{Topic: topic, Body: body}
	if tag := strings.TrimSpace(opts.Tag); tag != "" {
		message.SetTag(tag)
	}
	if len(keys) > 0 {
		message.SetKeys(keys...)
	}
	if group := strings.TrimSpace(opts.MessageGroup); group != "" {
		message.SetMessageGroup(group)
	}
	if !deliverAt.IsZero() {
		message.SetDelayTimestamp(deliverAt)
	}
	for key, value := range opts.Properties {
		message.AddProperty(key, value)
	}
	return message, nil
}

func normalizeKeys(keys []string) ([]string, error) {
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%w: key must not be empty", ErrInvalidKey)
		}
		if len(key) > maxKeyBytes {
			return nil, fmt.Errorf("%w: %q exceeds %d bytes", ErrInvalidKey, key, maxKeyBytes)
		}
		if strings.ContainsAny(key, " \t\r\n") {
			return nil, fmt.Errorf("%w: %q contains whitespace", ErrInvalidKey, key)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		if len(out) == maxKeys {
			return nil, fmt.Errorf("%w: at most %d", ErrTooManyKeys, maxKeys)
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out, nil
}

func validateProperties(properties map[string]string) error {
	if len(properties) > maxProperties {
		return fmt.Errorf("%w: %d exceeds %d", ErrTooManyProperties, len(properties), maxProperties)
	}
	total := 0
	for key, value := range properties {
		if !validPropertyKey(key) {
			return fmt.Errorf("%w: %q", ErrInvalidPropertyKey, key)
		}
		if value == "" {
			return fmt.Errorf("%w: %q is empty", ErrInvalidPropertyValue, key)
		}
		if len(value) > maxPropertyValueBytes {
			return fmt.Errorf("%w: %q exceeds %d bytes", ErrInvalidPropertyValue, key, maxPropertyValueBytes)
		}
		total += len(key) + len(value)
	}
	if total > maxPropertiesBytes {
		return fmt.Errorf("%w: %d bytes", ErrPropertiesTooLarge, total)
	}
	return nil
}

func validPropertyKey(key string) bool {
	if key == "" || strings.HasPrefix(key, "__") {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == '-':
		default:
			return false
		}
	}
	return true
}

func withContentType(properties map[string]string, contentType string) map[string]string {
	out := make(map[string]string, len(properties)+1)
	for key, value := range properties {
		out[key] = value
	}
	if _, ok := out["content-type"]; !ok {
		out["content-type"] = contentType
	}
	return out
}
