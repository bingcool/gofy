package serviceclients_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	nacosconfig "github.com/bingcool/gofy/support/nacos"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/serviceclients"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
)

// A configured base_url must win over Nacos: the local-development path cannot
// depend on the registry being reachable.
// 配置了 base_url 就必须优先于 Nacos：本地开发路径不能依赖注册中心可达。
func TestNewPinsBaseURL(t *testing.T) {
	clients, err := serviceclients.New(config.ClientsConfig{
		DemoService: config.DownstreamConfig{BaseURL: "10.0.0.5:8080", Timeout: time.Second},
	}, nacosconfig.Config{Enabled: true, Host: "127.0.0.1", Port: 8848})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = clients.Close() })

	if got := clients.Source(); !strings.Contains(got, "base_url http://10.0.0.5:8080") {
		t.Fatalf("Source = %q", got)
	}
}

// Without a base_url and without Nacos the client must still be usable: a call
// reports a configuration error instead of looking like a transport failure.
// 没有 base_url 也没有 Nacos 时客户端仍要可用：调用返回配置错误，而不是看起来像传输
// 失败。
func TestNewWithoutNacosIsNotConfigured(t *testing.T) {
	clients, err := serviceclients.New(config.ClientsConfig{}, nacosconfig.Config{Enabled: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = clients.Close() })

	if got := clients.Source(); got != "not configured" {
		t.Fatalf("Source = %q", got)
	}
	_, err = clients.DemoService().GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if !errors.Is(err, serviceclients.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

// With Nacos enabled the client discovers demoservice.ServiceName; the source
// string has to name the namespace and group, the two values whose typos show
// up only as "no instances".
// Nacos 启用时客户端按 demoservice.ServiceName 做发现；Source 必须带上 namespace 与
// group——这两个值写错时唯一症状就是"查不到实例"。
func TestNewUsesDiscoveryWhenNacosEnabled(t *testing.T) {
	clients, err := serviceclients.New(config.ClientsConfig{}, nacosconfig.Config{
		Enabled:   true,
		Host:      "127.0.0.1",
		Port:      8848,
		Discovery: nacosconfig.DiscoveryConfig{Namespace: "dev", Group: "GO_GROUP"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = clients.Close() })

	source := clients.Source()
	if !strings.Contains(source, "namespace=dev") || !strings.Contains(source, "group=GO_GROUP") {
		t.Fatalf("Source = %q, want the discovery scope", source)
	}
	if !strings.Contains(source, "127.0.0.1:8848") {
		t.Fatalf("Source = %q, want the connection address", source)
	}
}

// A nil Clients is a legal state for a process without downstream calls; every
// method has to stay callable.
// nil Clients 是没有下游调用的进程的合法状态；每个方法都必须仍可调用。
func TestNilClientsIsSafe(t *testing.T) {
	var clients *serviceclients.Clients
	if got := clients.Source(); got != "not configured" {
		t.Fatalf("Source = %q", got)
	}
	if err := clients.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := clients.DemoService().GetConfig(context.Background(), &orderrequest.GetConfigRequest{})
	if !errors.Is(err, serviceclients.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}
