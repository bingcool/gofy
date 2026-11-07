// Package rocketmq publishes and consumes RocketMQ messages through the
// official apache/rocketmq-clients/golang gRPC client.
//
// 与 v2 remoting 客户端不同，v5 通过 Proxy 的 gRPC 端点（通常是 8081）访问
// 集群，不再直连 NameServer（9876）。包保留与 support/kafka 一致的使用方式：
// 调用方传入 context 和 Config，持有返回的 Client，并从同一份 Config 构建
// 消费者。
package rocketmq

import (
	"errors"
	"fmt"
	"strings"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
)

const (
	defaultSendTimeout = 3 * time.Second
	defaultMaxAttempts = 3
)

var (
	// ErrEndpointRequired is returned when Config.Endpoint is empty.
	ErrEndpointRequired = errors.New("rocketmq: endpoint is required")
	// ErrCredentialsIncomplete is returned when only one of AccessKey and
	// AccessSecret is set, or SecurityToken is set without them.
	ErrCredentialsIncomplete = errors.New("rocketmq: access_key and access_secret must be set together")
)

// Config describes one RocketMQ cluster connection.
type Config struct {
	// Endpoint is the gRPC proxy endpoint (host:port). Multiple endpoints may
	// be joined with ";".
	Endpoint string `yaml:"endpoint"`
	// Namespace prefixes topics and groups for multi-tenant clusters. Leave
	// empty when the cluster does not use namespaces.
	Namespace string `yaml:"namespace"`
	// AccessKey and AccessSecret enable ACL authentication. Set both or
	// neither.
	AccessKey string `yaml:"access_key"`
	// AccessSecret is the secret paired with AccessKey.
	AccessSecret string `yaml:"access_secret"`
	// SecurityToken is an optional STS token. It requires AccessKey and
	// AccessSecret as well.
	SecurityToken string `yaml:"security_token"`
	// SendTimeout bounds one RPC call and the route lookup. v5.1.2 always
	// dials with its own 5s timeout, so an unreachable endpoint can take that
	// long to fail. Zero uses 3s.
	SendTimeout time.Duration `yaml:"send_timeout" default:"3s"`
	// MaxAttempts is how often the producer retries one message. Zero uses 3.
	MaxAttempts int32 `yaml:"max_attempts" default:"3"`
	// StartupTopics are optional route lookups performed by New: when set,
	// an unreachable endpoint or an unknown topic fails startup. Topics belong
	// to the business code, so leaving this empty is the normal case and the
	// first publish establishes the connection.
	StartupTopics []string `yaml:"startup_topics"`
	// SkipStartupCheck disables the route lookups above even when
	// StartupTopics is set.
	SkipStartupCheck bool `yaml:"skip_startup_check"`
}

// Validate checks the configuration without connecting.
func (c Config) Validate() error {
	if err := c.validateConnection(); err != nil {
		return err
	}
	for i, topic := range c.StartupTopics {
		if strings.TrimSpace(topic) == "" {
			return fmt.Errorf("rocketmq: startup topic %d is empty", i)
		}
	}
	return nil
}

// validateConnection checks the settings shared by producers and consumers.
func (c Config) validateConnection() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return ErrEndpointRequired
	}
	if c.SendTimeout < 0 {
		return errors.New("rocketmq: send_timeout must not be negative")
	}
	if c.MaxAttempts < 0 {
		return errors.New("rocketmq: max_attempts must not be negative")
	}
	accessKey := strings.TrimSpace(c.AccessKey)
	accessSecret := strings.TrimSpace(c.AccessSecret)
	if (accessKey == "") != (accessSecret == "") {
		return ErrCredentialsIncomplete
	}
	if strings.TrimSpace(c.SecurityToken) != "" && accessKey == "" {
		return ErrCredentialsIncomplete
	}
	return nil
}

func (c Config) withDefaults() Config {
	out := c
	out.Endpoint = strings.TrimSpace(c.Endpoint)
	out.Namespace = strings.TrimSpace(c.Namespace)
	out.AccessKey = strings.TrimSpace(c.AccessKey)
	out.AccessSecret = strings.TrimSpace(c.AccessSecret)
	out.SecurityToken = strings.TrimSpace(c.SecurityToken)
	out.StartupTopics = make([]string, len(c.StartupTopics))
	for i, topic := range c.StartupTopics {
		out.StartupTopics[i] = strings.TrimSpace(topic)
	}
	if out.SendTimeout == 0 {
		out.SendTimeout = defaultSendTimeout
	}
	if out.MaxAttempts == 0 {
		out.MaxAttempts = defaultMaxAttempts
	}
	return out
}

// rmqConfig maps Config to the client configuration. Credentials is always
// non-nil because the SDK dereferences it in Sign even when ACL is disabled.
func (c Config) rmqConfig(consumerGroup string) *rmq.Config {
	return &rmq.Config{
		Endpoint:      c.Endpoint,
		NameSpace:     c.Namespace,
		ConsumerGroup: consumerGroup,
		Credentials: &credentials.SessionCredentials{
			AccessKey:     c.AccessKey,
			AccessSecret:  c.AccessSecret,
			SecurityToken: c.SecurityToken,
		},
	}
}

// clientOptions sets the request timeout. v5.1.2 freezes the client-level
// WithConnOptions / WithRpcClientOptions before creating RPC connections, so
// only the query-route timeout reaches the settings; it also becomes the
// request timeout of send and receive calls.
func (c Config) clientOptions() []rmq.ClientOption {
	return []rmq.ClientOption{rmq.WithQueryRouteTimeout(c.SendTimeout)}
}

func (c Config) clientFunc() rmq.NewClientFunc {
	return func(cfg *rmq.Config, opts ...rmq.ClientOption) (rmq.Client, error) {
		return rmq.NewClient(cfg, append(c.clientOptions(), opts...)...)
	}
}

func (c Config) producerOptions() []rmq.ProducerOption {
	opts := []rmq.ProducerOption{
		rmq.WithMaxAttempts(c.MaxAttempts),
		rmq.WithClientFunc(c.clientFunc()),
	}
	if !c.SkipStartupCheck && len(c.StartupTopics) > 0 {
		// v5 只有在 WithTopics 非空时才会在 Start 中查询路由，这是生产端
		// 唯一能在启动阶段发现端点不可达的办法；topic 属于业务代码，
		// 不配置时不探测，等第一次发送再建连。
		opts = append(opts, rmq.WithTopics(c.StartupTopics...))
	}
	return opts
}

func (c Config) simpleConsumerOptions(consumerCfg ConsumerConfig) []rmq.SimpleConsumerOption {
	return []rmq.SimpleConsumerOption{
		rmq.WithAwaitDuration(consumerCfg.AwaitDuration),
		rmq.WithSubscriptionExpressions(map[string]*rmq.FilterExpression{
			consumerCfg.Topic: consumerCfg.selector(),
		}),
		rmq.WithClientFuncForSimpleConsumer(c.clientFunc()),
	}
}
