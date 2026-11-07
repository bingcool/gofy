// Package kafka publishes and consumes Kafka records. Like support/redis,
// support/mysql and support/amqp it keeps no global state: the caller passes a
// context and a Config, owns the returned Client, and builds Producers and
// Consumers on top of it.
//
// 与 AMQP 不同，Kafka 的消费者组属于客户端级别，因此每个 Consumer 会用同一份
// Config 单独建一个客户端；Producer 共用 New 创建的客户端。多实例仍由应用装配
// 层管理：每个实例分别调用 New，或用 Wrap 注入已经建好的客户端。包本身不保存
// 任何连接句柄。
package kafka

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	defaultClientID      = "gofy"
	defaultDialTimeout   = 5 * time.Second
	defaultRecordRetries = 10
)

// ErrBrokerRequired is returned when Config.Brokers is empty.
var ErrBrokerRequired = errors.New("kafka: at least one broker is required")

// Config describes one Kafka cluster connection.
type Config struct {
	// Brokers is the seed broker list (host:port). The client discovers the
	// rest of the cluster from metadata.
	Brokers  []string `yaml:"brokers"`
	ClientID string   `yaml:"client_id" default:"gofy"`
	// DialTimeout bounds TCP dialing to each broker. Zero uses 5s.
	DialTimeout time.Duration `yaml:"dial_timeout" default:"5s"`
	// Idempotent enables broker-side deduplication of reissued produce
	// requests. It is off by default, matching the swoolefy library; enabling
	// it requires the cluster to allow idempotent writes.
	Idempotent bool `yaml:"idempotent"`
	// RecordRetries limits how often one record is produced. Zero uses 10;
	// -1 means unlimited (the franz-go default).
	RecordRetries int `yaml:"record_retries" default:"10"`
}

// Validate checks the config without connecting.
func (c Config) Validate() error {
	if len(c.Brokers) == 0 {
		return ErrBrokerRequired
	}
	for i, broker := range c.Brokers {
		if strings.TrimSpace(broker) == "" {
			return fmt.Errorf("kafka: broker %d is empty", i)
		}
	}
	if c.DialTimeout < 0 {
		return errors.New("kafka: dial_timeout must not be negative")
	}
	if c.RecordRetries < -1 {
		return errors.New("kafka: record_retries must be -1 or greater")
	}
	return nil
}

func (c Config) withDefaults() Config {
	out := c
	out.Brokers = make([]string, len(c.Brokers))
	for i, broker := range c.Brokers {
		out.Brokers[i] = strings.TrimSpace(broker)
	}
	out.ClientID = strings.TrimSpace(out.ClientID)
	if out.ClientID == "" {
		out.ClientID = defaultClientID
	}
	if out.DialTimeout == 0 {
		out.DialTimeout = defaultDialTimeout
	}
	if out.RecordRetries == 0 {
		out.RecordRetries = defaultRecordRetries
	}
	return out
}

// clientOptions renders the shared client options. Consumer groups add their
// own options on top and create a separate client, because franz-go stores the
// group configuration per client.
func (c Config) clientOptions() []kgo.Opt {
	opts := []kgo.Opt{
		kgo.SeedBrokers(c.Brokers...),
		kgo.ClientID(c.ClientID),
		kgo.DialTimeout(c.DialTimeout),
		kgo.RecordRetries(c.RecordRetries),
	}
	if !c.Idempotent {
		opts = append(opts, kgo.DisableIdempotentWrite())
	}
	return opts
}
