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
