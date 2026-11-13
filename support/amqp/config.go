// Package amqp opens AMQP 0-9-1 connections (RabbitMQ) and publishes or
// consumes messages. Like support/redis and support/db it keeps no global
// state: the caller passes a context and a Config, owns the returned Client,
// and builds Queue values on top of it.
//
// 多实例属于应用装配层：为每个实例分别调用 New，或者用 Wrap 注入已经建好的
// 连接，再把对应的 Client 传给业务代码。包本身不保存任何连接句柄。
package amqp

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPort              = 5672
	defaultHeartbeat         = 10 * time.Second
	defaultConnectionTimeout = 5 * time.Second
	defaultLocale            = "en_US"
)

// ErrHostRequired is returned when Config.Hosts is empty.
// ErrHostRequired 在 Config.Hosts 为空时返回。
var ErrHostRequired = errors.New("amqp: at least one host is required")

// Host is one broker endpoint. Config tries hosts in order, so a cluster can
// list its nodes and still connect when the first node is down.
// Host 是一个 broker 端点。Config 按顺序尝试各 host，集群列出多个节点时，
// 首个节点宕机仍能连上。
type Host struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port" default:"5672"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Vhost    string `yaml:"vhost" default:"/"`
	// Scheme is amqp or amqps. amqps enables TLS with the system roots.
	// Scheme 取 amqp 或 amqps。amqps 使用系统根证书启用 TLS。
	Scheme string `yaml:"scheme" default:"amqp"`
}

// Config describes one AMQP connection. Every field applies to all hosts.
// Config 描述一个 AMQP 连接，所有字段对所有 host 生效。
type Config struct {
	Hosts []Host `yaml:"hosts"`
	// Heartbeat is the keep-alive interval negotiated with the broker. Zero
	// uses 10s; the broker may lower it.
	// Heartbeat 是与 broker 协商的保活间隔。0 表示使用 10s，broker 可能下调。
	Heartbeat time.Duration `yaml:"heartbeat" default:"10s"`
	// ConnectionTimeout bounds TCP dialing and the AMQP handshake for each
	// host. Zero uses 5s.
	// ConnectionTimeout 限制对每个 host 的 TCP 拨号和 AMQP 握手耗时，
	// 0 表示使用 5s。
	ConnectionTimeout time.Duration `yaml:"connection_timeout" default:"5s"`
	Locale            string        `yaml:"locale" default:"en_US"`
}

// Validate checks the config without connecting.
// Validate 只校验配置，不建立连接。
func (c Config) Validate() error {
	if len(c.Hosts) == 0 {
		return ErrHostRequired
	}
	for i, host := range c.Hosts {
		if err := host.validate(); err != nil {
			return fmt.Errorf("amqp: host %d: %w", i, err)
		}
	}
	if c.Heartbeat < 0 || c.ConnectionTimeout < 0 {
		return errors.New("amqp: durations must not be negative")
	}
	return nil
}

func (h Host) validate() error {
	if strings.TrimSpace(h.Host) == "" {
		return errors.New("host is required")
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("invalid port %d", h.Port)
	}
	if strings.TrimSpace(h.Username) == "" {
		return errors.New("username is required")
	}
	switch strings.ToLower(strings.TrimSpace(h.Scheme)) {
	case "", "amqp", "amqps":
	default:
		return fmt.Errorf("invalid scheme %q", h.Scheme)
	}
	return nil
}

func (c Config) withDefaults() Config {
	out := c
	out.Heartbeat = positiveDurationOr(out.Heartbeat, defaultHeartbeat)
	out.ConnectionTimeout = positiveDurationOr(out.ConnectionTimeout, defaultConnectionTimeout)
	out.Locale = strings.TrimSpace(out.Locale)
	if out.Locale == "" {
		out.Locale = defaultLocale
	}
	out.Hosts = make([]Host, len(c.Hosts))
	for i, host := range c.Hosts {
		out.Hosts[i] = host.withDefaults()
	}
	return out
}

func (h Host) withDefaults() Host {
	out := h
	out.Host = strings.TrimSpace(out.Host)
	out.Username = strings.TrimSpace(out.Username)
	out.Scheme = strings.ToLower(strings.TrimSpace(out.Scheme))
	if out.Scheme == "" {
		out.Scheme = "amqp"
	}
	if out.Port == 0 {
		out.Port = defaultPort
	}
	out.Vhost = strings.TrimSpace(out.Vhost)
	if out.Vhost == "" {
		out.Vhost = "/"
	}
	return out
}

// uri renders the host as an AMQP URI. Credentials are percent-encoded by
// net/url, so reserved characters in a password are safe.
// uri 把 host 渲染成 AMQP URI。net/url 会对凭据做百分号编码，密码中的保留
// 字符因此可以安全使用。
func (h Host) uri() string {
	u := url.URL{
		Scheme: h.Scheme,
		User:   url.UserPassword(h.Username, h.Password),
		Host:   net.JoinHostPort(h.Host, strconv.Itoa(h.Port)),
		Path:   "/",
	}
	if vhost := strings.TrimPrefix(h.Vhost, "/"); vhost != "" {
		u.Path = "/" + vhost
	}
	return u.String()
}

// address is the endpoint used in logs and errors; it never includes
// credentials.
// address 是日志与错误中使用的端点，永不包含凭据。
func (h Host) address() string {
	return net.JoinHostPort(h.Host, strconv.Itoa(h.Port))
}

func positiveDurationOr(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
