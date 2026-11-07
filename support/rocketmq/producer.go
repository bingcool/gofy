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
	ErrTopicRequired = errors.New("rocketmq: topic is required")
	// ErrInvalidDelay is returned when DeliverAt is not in the future.
	ErrInvalidDelay = errors.New("rocketmq: deliver_at must be in the future")
	// ErrTooManyKeys is returned when a message has more than 32 keys.
	ErrTooManyKeys = errors.New("rocketmq: too many keys")
	// ErrInvalidKey is returned for an empty, oversized or whitespace key.
	ErrInvalidKey = errors.New("rocketmq: invalid key")
	// ErrTooManyProperties is returned when a message has more than 32
	// properties.
	ErrTooManyProperties = errors.New("rocketmq: too many properties")
	// ErrInvalidPropertyKey is returned for a key that is empty, starts with
	// "__" or contains characters outside letters, digits, "_", "." and "-".
	ErrInvalidPropertyKey = errors.New("rocketmq: invalid property key")
	// ErrInvalidPropertyValue is returned for an empty or oversized property
	// value.
	ErrInvalidPropertyValue = errors.New("rocketmq: invalid property value")
	// ErrPropertiesTooLarge is returned when the properties exceed 16 KiB.
	ErrPropertiesTooLarge = errors.New("rocketmq: properties exceed 16 KiB")
)

// PublishOptions describes one message. Topic is required for every call
// because a Client is not bound to one topic.
type PublishOptions struct {
	Topic string
	// Tag classifies the message; consumers subscribe by tag or SQL92.
	Tag string
	// Keys are business keys indexed by the broker. Duplicates are removed;
	// at most 32 keys, each up to 128 bytes without whitespace.
	Keys []string
	// Properties are custom message attributes. At most 32; keys may contain
	// letters, digits, "_", "." and "-" and must not start with "__"; values
	// are up to 1024 bytes and the total is limited to 16 KiB.
	Properties map[string]string
	// DeliverAt delays delivery until the given time. v5 uses an absolute
	// timestamp rather than the v2 delay levels. Zero sends immediately.
	DeliverAt time.Time
	// MessageGroup selects the queue for FIFO messages; messages with the
	// same group are sent in order. The topic must accept FIFO messages.
	MessageGroup string
}

// SendResult reports where the broker stored the message.
type SendResult struct {
	MessageID string
	Topic     string
	Offset    int64
}

// Publish sends body synchronously and waits for the broker acknowledgement.
// The wait is bounded by Config.SendTimeout and by ctx, whichever ends first.
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
	if !opts.DeliverAt.IsZero() && !opts.DeliverAt.After(time.Now()) {
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
	if !opts.DeliverAt.IsZero() {
		message.SetDelayTimestamp(opts.DeliverAt)
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
