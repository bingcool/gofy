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
	// ErrEndpointRequired 在 Config.Endpoint 为空时返回。
	ErrEndpointRequired = errors.New("rocketmq: endpoint is required")
	// ErrCredentialsIncomplete is returned when only one of AccessKey and
	// AccessSecret is set, or SecurityToken is set without them.
	// ErrCredentialsIncomplete 在 AccessKey 与 AccessSecret 只设置了一个，或
	// 只设置 SecurityToken 时返回。
	ErrCredentialsIncomplete = errors.New("rocketmq: access_key and access_secret must be set together")
)

// Config describes one RocketMQ cluster connection.
// Config 描述一个 RocketMQ 集群连接。
type Config struct {
	// Endpoint is the gRPC proxy endpoint (host:port). Multiple endpoints may
	// be joined with ";".
	// Endpoint 是 gRPC proxy 地址（host:port），多个地址用 ";" 连接。
	Endpoint string `yaml:"endpoint"`
	// Namespace prefixes topics and groups for multi-tenant clusters. Leave
	// empty when the cluster does not use namespaces.
	// Namespace 为多租户集群给 topic 和 group 加前缀，集群未启用命名空间时
	// 留空。
	Namespace string `yaml:"namespace"`
	// AccessKey and AccessSecret enable ACL authentication. Set both or
	// neither.
	// AccessKey 与 AccessSecret 启用 ACL 认证，两者必须同时设置或同时留空。
	AccessKey string `yaml:"access_key"`
	// AccessSecret is the secret paired with AccessKey.
	// AccessSecret 是与 AccessKey 配对的密钥。
	AccessSecret string `yaml:"access_secret"`
	// SecurityToken is an optional STS token. It requires AccessKey and
	// AccessSecret as well.
	// SecurityToken 是可选的 STS 令牌，同时仍需要 AccessKey 与 AccessSecret。
	SecurityToken string `yaml:"security_token"`
	// SendTimeout bounds one RPC call and the route lookup. v5.1.2 always
	// dials with its own 5s timeout, so an unreachable endpoint can take that
	// long to fail. Zero uses 3s.
	// SendTimeout 限制单次 RPC 调用与路由查询。v5.1.2 拨号固定使用 5s 超时，
	// 因此端点不可达时最长要等 5s 才失败。0 表示 3s。
	SendTimeout time.Duration `yaml:"send_timeout" default:"3s"`
	// MaxAttempts is how often the producer retries one message. Zero uses 3.
	// MaxAttempts 是 Producer 对单条消息的重试次数，0 表示 3。
	MaxAttempts int32 `yaml:"max_attempts" default:"3"`
	// StartupTopics are optional route lookups performed by New: when set,
	// an unreachable endpoint or an unknown topic fails startup. Topics belong
	// to the business code, so leaving this empty is the normal case and the
	// first publish establishes the connection.
	// StartupTopics 是 New 期间可选执行的路由查询：设置后，端点不可达或 topic
	// 不存在都会让启动失败。topic 归属业务代码，通常留空，由第一次发送建立连接。
	StartupTopics []string `yaml:"startup_topics"`
	// SkipStartupCheck disables the route lookups above even when
	// StartupTopics is set.
	// SkipStartupCheck 即使设置了 StartupTopics 也跳过上述路由查询。
	SkipStartupCheck bool `yaml:"skip_startup_check"`
}

// Validate checks the configuration without connecting.
// Validate 只校验配置，不建立连接。
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
// validateConnection 校验 Producer 与 Consumer 共用的连接设置。
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
// rmqConfig 把 Config 映射成 SDK 的客户端配置。Credentials 始终非 nil，因为
// 即使关闭 ACL，SDK 在 Sign 中也会解引用它。
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
// clientOptions 设置请求超时。v5.1.2 在建立 RPC 连接前就冻结了客户端级的
// WithConnOptions / WithRpcClientOptions，只有查询路由超时能生效，它同时成为
// 发送与接收调用的请求超时。
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
		// v5 only queries routes in Start when WithTopics is non-empty, which is
		// the producer's only way to notice an unreachable endpoint at startup.
		// Topics belong to business code, so without this option the connection is
		// established on the first send.
		// v5 只有在 WithTopics 非空时才会在 Start 中查询路由，这是生产端唯一
		// 能在启动阶段发现端点不可达的办法；topic 属于业务代码，不配置时
		// 不探测，等第一次发送再建连。
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
