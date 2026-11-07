package queues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Message 是队列中的消息信封。__id、__retry_num、__timestamp 为保留字段，
// 业务数据放在 payload 内，用 Decode 还原。
type Message struct {
	ID        string          `json:"__id"`
	RetryNum  int             `json:"__retry_num"`
	Timestamp int64           `json:"__timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

// Decode 把消息 payload 反序列化到 dest。
func (m *Message) Decode(dest any) error {
	if m == nil {
		return errors.New("queues: message is nil")
	}
	if err := json.Unmarshal(m.Payload, dest); err != nil {
		return fmt.Errorf("queues: decode payload: %w", err)
	}
	return nil
}

func newMessage(payload any) (*Message, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("queues: marshal payload: %w", err)
	}
	return &Message{
		ID:        uuid.NewString(),
		Timestamp: time.Now().Unix(),
		Payload:   raw,
	}, nil
}

func encodeMessage(msg *Message) (string, error) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("queues: marshal message: %w", err)
	}
	return string(raw), nil
}

func decodeMessage(raw string) (*Message, error) {
	var msg Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return nil, fmt.Errorf("queues: decode message: %w", err)
	}
	return &msg, nil
}

// scheduleRetry 把失败消息写入重试 ZSET。返回 false 表示已达重试上限。
// 调用会修改 msg.RetryNum，避免同一消息被重复安排。
func scheduleRetry(ctx context.Context, client goredis.Cmdable, key string, maxRetries int, msg *Message, delay time.Duration) (bool, error) {
	if msg == nil {
		return false, errors.New("queues: message is nil")
	}
	if msg.RetryNum >= maxRetries {
		return false, nil
	}
	if msg.ID == "" {
		msg.ID = uuid.NewString()
	}
	msg.RetryNum++
	raw, err := encodeMessage(msg)
	if err != nil {
		return false, err
	}
	at := time.Now().Add(normalizeDelay(delay))
	if err := client.ZAdd(contextOrBackground(ctx), key, goredis.Z{Score: score(at), Member: raw}).Err(); err != nil {
		return false, fmt.Errorf("queues: retry %q: %w", key, err)
	}
	return true, nil
}

// score 用 Unix 秒（含小数）作为 ZSET 分值，与 swoolefy 的秒级 score 兼容。
func score(at time.Time) float64 {
	return float64(at.UnixNano()) / float64(time.Second)
}

func normalizeDelay(delay time.Duration) time.Duration {
	if delay < 0 {
		return 0
	}
	return delay
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
