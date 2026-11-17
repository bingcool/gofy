package nacos

import (
	"testing"
	"time"
)

func TestLoadParsesServiceRegisterAndDiscovery(t *testing.T) {
	clearNacosEnv(t)
	t.Setenv("POD_IP", "10.9.8.7")
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", `nacos:
  host: 10.0.0.2
  namespace: dev
`)
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  enable_nacos_register: true
  service_register:
    ip: ${POD_IP}
    service_name: demo-service
    group_name: gofy-group
    cluster_name: DEFAULT
    weight: 2
    heartbeat_interval: 3
    metadata:
      version: 1.0.0
  discovery_service_client:
    load_balancer: round_robin
    cache_ttl: 30
    stale_ttl: 90
    healthy_only: false
    namespace_id: dev
    group_name: pwa_group
`)

	cfg, err := Load(nacosPath, appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	reg := cfg.Register
	if !reg.Enabled {
		t.Fatal("Register.Enabled = false")
	}
	if reg.ServiceName != "demo-service" || reg.IP != "10.9.8.7" {
		t.Fatalf("register name/ip = %q/%q", reg.ServiceName, reg.IP)
	}
	if reg.Namespace != "dev" || reg.Group != "gofy-group" || reg.Cluster != "DEFAULT" {
		t.Fatalf("register namespace/group/cluster = %q/%q/%q", reg.Namespace, reg.Group, reg.Cluster)
	}
	if reg.Weight != 2 || reg.HeartbeatInterval != 3*time.Second {
		t.Fatalf("register weight/interval = %v/%s", reg.Weight, reg.HeartbeatInterval)
	}
	if reg.Metadata["version"] != "1.0.0" {
		t.Fatalf("register metadata = %v", reg.Metadata)
	}

	disc := cfg.Discovery
	if disc.Group != "pwa_group" || disc.Namespace != "dev" {
		t.Fatalf("discovery group/namespace = %q/%q", disc.Group, disc.Namespace)
	}
	if disc.LoadBalancer != "round_robin" {
		t.Fatalf("LoadBalancer = %q", disc.LoadBalancer)
	}
	if disc.CacheTTL != 30*time.Second || disc.StaleTTL != 90*time.Second {
		t.Fatalf("cache/stale = %s/%s", disc.CacheTTL, disc.StaleTTL)
	}
	if disc.HealthyOnly {
		t.Fatal("HealthyOnly = true, want false from healthy_only")
	}
}

func TestDiscoveryDefaultsFollowRegister(t *testing.T) {
	clearNacosEnv(t)
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 10.0.0.2\n  namespace: dev\n")
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  enable_nacos_register: true
  service_register:
    service_name: demo-service
    group_name: gofy-group
`)

	cfg, err := Load(nacosPath, appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	disc := cfg.Discovery
	if disc.Group != "gofy-group" || disc.Namespace != "dev" {
		t.Fatalf("discovery group/namespace = %q/%q, want register values", disc.Group, disc.Namespace)
	}
	if disc.LoadBalancer != "random" {
		t.Fatalf("LoadBalancer = %q, want random", disc.LoadBalancer)
	}
	if disc.CacheTTL != 60*time.Second || disc.StaleTTL != 180*time.Second {
		t.Fatalf("cache/stale = %s/%s", disc.CacheTTL, disc.StaleTTL)
	}
	if disc.Timeout != 2*time.Second {
		t.Fatalf("Timeout = %s, want the 2s discovery default", disc.Timeout)
	}
	if !disc.HealthyOnly {
		t.Fatal("HealthyOnly = false, want default true")
	}
}

func TestDiscoveryEnvOverrides(t *testing.T) {
	clearNacosEnv(t)
	t.Setenv("NACOS_DISCOVERY_LOAD_BALANCER", "weight")
	t.Setenv("NACOS_DISCOVERY_CACHE_TTL", "15")
	t.Setenv("NACOS_DISCOVERY_STALE_TTL", "45")
	t.Setenv("NACOS_DISCOVERY_HEALTHY_ONLY", "false")
	t.Setenv("NACOS_DISCOVERY_CLUSTERS", "DEFAULT, OTHER")

	cfg := Config{Host: "10.0.0.2"}.normalize()
	disc := cfg.Discovery
	if disc.LoadBalancer != "weight" {
		t.Fatalf("LoadBalancer = %q", disc.LoadBalancer)
	}
	if disc.CacheTTL != 15*time.Second || disc.StaleTTL != 45*time.Second {
		t.Fatalf("cache/stale = %s/%s", disc.CacheTTL, disc.StaleTTL)
	}
	if disc.HealthyOnly {
		t.Fatal("HealthyOnly = true, want env false")
	}
	if len(disc.Clusters) != 2 || disc.Clusters[1] != "OTHER" {
		t.Fatalf("Clusters = %v", disc.Clusters)
	}
}

func TestRegisterEnvOverrides(t *testing.T) {
	clearNacosEnv(t)
	t.Setenv("NACOS_SERVICE_NAMESPACE_ID", "test")
	t.Setenv("NACOS_SERVICE_GROUP_NAME", "ENV_GROUP")

	reg := RegisterConfig{ServiceName: "demo-service"}.normalize("dev")
	if reg.Namespace != "test" || reg.Group != "ENV_GROUP" {
		t.Fatalf("namespace/group = %q/%q", reg.Namespace, reg.Group)
	}
}

// A typo in load_balancer must fail at Load, not at the first lookup: a worker
// or script never builds a resolver, so the later error would never surface.
// load_balancer 拼错必须在 Load 就失败，而不是等到第一次寻址：worker 与 script 根本
// 不构造 resolver，后面那个错误永远不会出现。
func TestDiscoveryRejectsUnknownLoadBalancer(t *testing.T) {
	clearNacosEnv(t)
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 10.0.0.2\n")
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  discovery_service_client:
    load_balancer: round-robiin
`)

	if _, err := Load(nacosPath, appPath); err == nil {
		t.Fatal("Load accepted an unknown load_balancer")
	}
}

// stale_ttl shorter than cache_ttl used to be widened silently. It is a real
// mistake — every cache hit after the stale window would fail closed — so it is
// reported instead.
// stale_ttl 短于 cache_ttl 过去会被悄悄放大。那是真错误——stale 窗口之后每次缓存命中
// 都会 fail-closed——因此改为报错。
func TestDiscoveryRejectsStaleShorterThanCache(t *testing.T) {
	clearNacosEnv(t)
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 10.0.0.2\n")
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  discovery_service_client:
    cache_ttl: 60
    stale_ttl: 30
`)

	if _, err := Load(nacosPath, appPath); err == nil {
		t.Fatal("Load accepted stale_ttl shorter than cache_ttl")
	}
}

func TestDiscoveryParsesTimeoutMS(t *testing.T) {
	clearNacosEnv(t)
	dir := t.TempDir()
	nacosPath := writeTestFile(t, dir, "nacos.yaml", "nacos:\n  host: 10.0.0.2\n")
	appPath := writeTestFile(t, dir, "application.yaml", `nacos:
  discovery_service_client:
    timeout_ms: 1500
`)

	cfg, err := Load(nacosPath, appPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Discovery.Timeout != 1500*time.Millisecond {
		t.Fatalf("Timeout = %s, want 1.5s", cfg.Discovery.Timeout)
	}
}

func TestDiscoveryTimeoutEnvOverride(t *testing.T) {
	clearNacosEnv(t)
	t.Setenv("NACOS_DISCOVERY_TIMEOUT_MS", "800")

	cfg := Config{Host: "10.0.0.2"}.normalize()
	if cfg.Discovery.Timeout != 800*time.Millisecond {
		t.Fatalf("Timeout = %s, want 800ms", cfg.Discovery.Timeout)
	}
}

// The inner base URL is the one value an assembly root sets after Load, so it
// has to normalize on the way in: a scheme-less host and a trailing slash both
// appear in real configuration files.
// 内网 base URL 是装配根在 Load 之后设置的唯一值，因此归一化必须发生在这里：真实配置
// 里既有不带 scheme 的主机，也有带末尾斜杠的写法。
func TestSetInnerExternalBaseURINormalizes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "bare host", in: "demo-service-dev.example.com:8080", want: "http://demo-service-dev.example.com:8080"},
		{name: "trailing slash", in: "https://demo.example.com/", want: "https://demo.example.com"},
		{name: "already normalized", in: "http://10.0.0.5:8080", want: "http://10.0.0.5:8080"},
		{name: "blank", in: "   ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := RegisterConfig{ServiceName: "demo-service"}
			reg.SetInnerExternalBaseURI(tc.in)
			if got := reg.Metadata[MetadataInnerExternalBaseURI]; got != tc.want {
				t.Fatalf("metadata = %q, want %q", got, tc.want)
			}
			if tc.want == "" && len(reg.Metadata) != 0 {
				t.Fatalf("metadata = %v, want no key for a blank value", reg.Metadata)
			}
		})
	}
}

// A nil RegisterConfig must not panic: the assembly root calls the setter on a
// config it may not have loaded (stop, version, script without LoadNacos).
// nil RegisterConfig 不能 panic：装配根可能在没加载配置的命令上调用它
// （stop、version、不读 nacos 的 script）。
func TestSetInnerExternalBaseURINilSafe(t *testing.T) {
	var reg *RegisterConfig
	reg.SetInnerExternalBaseURI("http://demo:8080")
}
