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

// Without Nacos the base URL is the only address there is, so it must be used
// directly rather than treated as a fallback that never gets a primary.
// 没有 Nacos 时这个地址就是唯一地址，因此必须直接使用，而不是当成一个永远等不到
// primary 的兜底。
func TestNewUsesBaseURIWithoutNacos(t *testing.T) {
	clients, err := serviceclients.New(config.ClientConfig{
		InnerExternalBaseURI: "10.0.0.5:8080",
		Timeout:              time.Second,
	}, nacosconfig.Config{Enabled: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = clients.Close() })

	// The scheme is added by ClientConfig.Normalize, so the source line doubles
	// as proof that the constructor normalized its input.
	// scheme 由 ClientConfig.Normalize 补上，因此这行 source 同时证明构造函数对入参
	// 做了归一化。
	if got := clients.Source(); !strings.Contains(got, "inner_external_base_uri http://10.0.0.5:8080") {
		t.Fatalf("Source = %q", got)
	}
}

// Without Nacos and without a base URL the client must still be usable: a call
// reports a configuration error instead of looking like a transport failure.
// 既没有 Nacos 也没有 base URL 时客户端仍要可用：调用返回配置错误，而不是看起来像
// 传输失败。
func TestNewWithoutNacosIsNotConfigured(t *testing.T) {
	clients, err := serviceclients.New(config.ClientConfig{}, nacosconfig.Config{Enabled: false})
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
	clients, err := serviceclients.New(config.ClientConfig{}, nacosconfig.Config{
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
	if strings.Contains(source, "fallback") {
		t.Fatalf("Source = %q, want no fallback when the base URI is empty", source)
	}
}

// When both addresses exist the source line has to name them both: the point of
// the fallback is that a request may reach a different environment than the
// resolver selected, and that must be visible without reading code.
// 两个地址都在时 source 必须都点名：兜底的意义就是请求可能落到 resolver 选的之外
// 那套环境，这必须不读代码就能看见。
func TestNewReportsDiscoveryAndFallback(t *testing.T) {
	clients, err := serviceclients.New(config.ClientConfig{
		InnerExternalBaseURI: "http://demo-service-dev.example.com:8080/",
	}, nacosconfig.Config{
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
	if !strings.Contains(source, "namespace=dev") {
		t.Fatalf("Source = %q, want the discovery scope", source)
	}
	if !strings.Contains(source, "fallback http://demo-service-dev.example.com:8080") {
		t.Fatalf("Source = %q, want the normalized fallback", source)
	}
}

// A lookup answered by the deployment group is a different environment, so the
// startup line has to name it — but only when that path is actually reachable.
// 由部署分组作答意味着打到了另一个环境，启动日志必须点名；但也只在那条路径真的可达时
// 才写出来。
func TestNewReportsDeploymentGroupFallback(t *testing.T) {
	newClients := func(t *testing.T, switchOn bool) string {
		t.Helper()
		clients, err := serviceclients.New(config.ClientConfig{}, nacosconfig.Config{
			Enabled:  true,
			Host:     "127.0.0.1",
			Port:     8848,
			Register: nacosconfig.RegisterConfig{Group: "GO_GROUP"},
			Discovery: nacosconfig.DiscoveryConfig{
				Namespace:       "dev",
				Group:           "local-bingcool",
				LocalAutoSwitch: switchOn,
			},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		t.Cleanup(func() { _ = clients.Close() })
		return clients.Source()
	}

	if source := newClients(t, true); !strings.Contains(source, "fallback_group=GO_GROUP") {
		t.Fatalf("Source = %q, want the deployment group when the switch is on", source)
	}
	if source := newClients(t, false); strings.Contains(source, "fallback_group") {
		t.Fatalf("Source = %q, want no fallback group when the switch is off", source)
	}
}

// The budget has to leave room for both attempts: one per-attempt timeout only
// reaches the primary, so the fallback would never run.
// 预算必须容得下两次尝试：只给一次尝试的超时只够主调用，兜底永远不会执行。
func TestCallBudgetCoversTwoAttempts(t *testing.T) {
	clients, err := serviceclients.New(config.ClientConfig{Timeout: 2 * time.Second}, nacosconfig.Config{Enabled: false})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = clients.Close() })
	if got, want := clients.CallBudget(), 5*time.Second; got != want {
		t.Fatalf("CallBudget = %s, want %s", got, want)
	}

	var nilClients *serviceclients.Clients
	if got, want := nilClients.CallBudget(), 2*config.DefaultClientTimeout+time.Second; got != want {
		t.Fatalf("nil CallBudget = %s, want %s", got, want)
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
