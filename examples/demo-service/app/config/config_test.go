package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
)

func TestLoadDatabasesByInstanceName(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
  report:
    dsn: user:pass@tcp(127.0.0.1:3306)/report
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Database(DefaultDatabase).DSN; got != "user:pass@tcp(127.0.0.1:3306)/main" {
		t.Fatalf("default dsn = %q", got)
	}
	if got := cfg.Database("report").DSN; got != "user:pass@tcp(127.0.0.1:3306)/report" {
		t.Fatalf("report dsn = %q", got)
	}
}

func TestLoadRequiresDefaultDatabase(t *testing.T) {
	path := writeConfig(t, `
db:
  report:
    dsn: user:pass@tcp(127.0.0.1:3306)/report
`)

	if _, err := Load(context.Background(), path); err == nil {
		t.Fatal("Load accepted config without db.default")
	}
}

// 订单模块的业务配置来自 order 段。Load 走 strict 模式，所以这个用例同时证明
// 该段已经映射到结构体字段，而不是被静默忽略。
func TestLoadOrderSection(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
order:
  auth_required: true
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Order.AuthRequired {
		t.Fatal("order.auth_required = false, want true")
	}
}

// 没有 order 段时按 default tag 处理：业务配置可选，不会因为缺段而启动失败。
func TestLoadOrderSectionDefaultsToFalse(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Order.AuthRequired {
		t.Fatal("missing order section should default to false")
	}
}

func TestLoadRedisInstancesByInstanceName(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
redis:
  cache:
    enabled: true
    addr: 127.0.0.1:6379
    db: 2
  session:
    enabled: false
    addr: 127.0.0.1:6379
  ratelimit:
    enabled: true
    addr: 127.0.0.1:6379
    password: test-pass
    db: 3
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cache := cfg.Redis["cache"]
	if !cache.Enabled || cache.Addr != "127.0.0.1:6379" || cache.DB != 2 {
		t.Fatalf("cache redis = %+v", cache)
	}
	if cfg.Redis["session"].Enabled {
		t.Fatal("session redis must stay disabled")
	}
	if limit := cfg.Redis["ratelimit"]; !limit.Enabled || limit.Addr != "127.0.0.1:6379" ||
		limit.Password != "test-pass" || limit.DB != 3 {
		t.Fatalf("ratelimit redis = %+v", limit)
	}
}

func TestLoadRejectsEnabledRedisWithoutAddr(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
redis:
  cache:
    enabled: true
`)

	if _, err := Load(context.Background(), path); err == nil {
		t.Fatal("Load accepted enabled redis without addr")
	}
}

func TestLoadKafkaInstancesByInstanceName(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
kafka:
  default:
    enabled: true
    brokers:
      - 127.0.0.1:9092
      - 127.0.0.2:9092
    client_id: demo-service
    dial_timeout: 3s
    idempotent: true
    record_retries: 5
  order:
    enabled: false
    brokers:
      - 127.0.0.1:9092
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := cfg.Kafka[DefaultDatabase]
	if !def.Enabled || len(def.Brokers) != 2 || def.Brokers[0] != "127.0.0.1:9092" {
		t.Fatalf("default kafka = %+v", def)
	}
	if def.ClientID != "demo-service" || def.DialTimeout != 3*time.Second ||
		!def.Idempotent || def.RecordRetries != 5 {
		t.Fatalf("default kafka settings = %+v", def.Config)
	}
	if cfg.Kafka["order"].Enabled {
		t.Fatal("order kafka must stay disabled")
	}
}

func TestLoadRejectsEnabledKafkaWithoutBrokers(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
kafka:
  default:
    enabled: true
`)

	if _, err := Load(context.Background(), path); err == nil {
		t.Fatal("Load accepted enabled kafka without brokers")
	}
}

func TestLoadRocketMQInstancesByInstanceName(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
rocketmq:
  default:
    enabled: true
    endpoint: 127.0.0.1:8081
    send_timeout: 3s
    max_attempts: 5
  order:
    enabled: false
    endpoint: 127.0.0.1:8081
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := cfg.RocketMQ[DefaultDatabase]
	if !def.Enabled || def.Endpoint != "127.0.0.1:8081" {
		t.Fatalf("default rocketmq = %+v", def)
	}
	if def.SendTimeout != 3*time.Second || def.MaxAttempts != 5 {
		t.Fatalf("default rocketmq settings = %+v", def.Config)
	}
	if cfg.RocketMQ["order"].Enabled {
		t.Fatal("order rocketmq must stay disabled")
	}
}

func TestLoadRejectsInvalidRocketMQInstances(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "no endpoint",
			yaml: `
rocketmq:
  default:
    enabled: true
`,
		},
		{
			name: "access key without secret",
			yaml: `
rocketmq:
  default:
    enabled: true
    endpoint: 127.0.0.1:8081
    startup_topics:
      - demo-topic
    access_key: ak
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
`+test.yaml)
			if _, err := Load(context.Background(), path); err == nil {
				t.Fatal("Load accepted an invalid rocketmq instance")
			}
		})
	}
}

func TestLoadAMQPInstancesByInstanceName(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
amqp:
  default:
    enabled: true
    hosts:
      - host: 127.0.0.1
        port: 5672
        username: rabbitmq
        password: test-pass
        vhost: my_vhost
    heartbeat: 15s
  order:
    enabled: false
`)

	cfg, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := cfg.AMQP[appamqp.DefaultInstance]
	if !def.Enabled || len(def.Hosts) != 1 {
		t.Fatalf("default amqp = %+v", def)
	}
	host := def.Hosts[0]
	if host.Host != "127.0.0.1" || host.Port != 5672 || host.Username != "rabbitmq" ||
		host.Password != "test-pass" || host.Vhost != "my_vhost" {
		t.Fatalf("amqp host = %+v", host)
	}
	if def.Heartbeat != 15*time.Second {
		t.Fatalf("heartbeat = %s", def.Heartbeat)
	}
	if cfg.AMQP["order"].Enabled {
		t.Fatal("order amqp must stay disabled")
	}
}

func TestLoadRejectsEnabledAMQPWithoutHosts(t *testing.T) {
	path := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
amqp:
  default:
    enabled: true
`)

	if _, err := Load(context.Background(), path); err == nil {
		t.Fatal("Load accepted enabled amqp without hosts")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestApplyLogPathsFollowsServiceMode(t *testing.T) {
	original := os.Args
	defer func() { os.Args = original }()

	cases := []struct {
		name   string
		args   []string
		dir    string
		info   string
		error  string
		system string
		access string
	}{
		{"http start", []string{"start"}, filepath.Join("storage", "log", "http"), "info.log", "error.log", "system_error.log", filepath.Join("storage", "log", "access", "access.log")},
		{"http stop", []string{"stop"}, filepath.Join("storage", "log", "http"), "info.log", "error.log", "system_error.log", filepath.Join("storage", "log", "access", "access.log")},
		{"script", []string{"script", "fix-order", "--order-id=1"}, filepath.Join("storage", "log", "script", "{date}"), filepath.Join("info", "fix-order.log"), filepath.Join("error", "fix-order.log"), filepath.Join("error", "fix-order-system_error.log"), ""},
		{"script spaces removed", []string{"script", "Fix Order"}, filepath.Join("storage", "log", "script", "{date}"), filepath.Join("info", "FixOrder.log"), filepath.Join("error", "FixOrder.log"), filepath.Join("error", "FixOrder-system_error.log"), ""},
		{"script path characters removed", []string{"script", "../../etc/passwd"}, filepath.Join("storage", "log", "script", "{date}"), filepath.Join("info", "etcpasswd.log"), filepath.Join("error", "etcpasswd.log"), filepath.Join("error", "etcpasswd-system_error.log"), ""},
		{"script list", []string{"script", "--list"}, filepath.Join("storage", "log", "script", "{date}"), filepath.Join("info", "info.log"), filepath.Join("error", "error.log"), filepath.Join("error", "system_error.log"), ""},
		{"worker group", []string{"worker", "--group=group1"}, filepath.Join("storage", "log", "worker", "{date}"), filepath.Join("info", "worker.log"), filepath.Join("error", "worker.log"), filepath.Join("error", "worker-system_error.log"), ""},
		{"worker name", []string{"worker", "--group=group1", "--name=order-scan"}, filepath.Join("storage", "log", "worker", "{date}"), filepath.Join("info", "order-scan.log"), filepath.Join("error", "order-scan.log"), filepath.Join("error", "order-scan-system_error.log"), ""},
		{"worker name value", []string{"worker", "--group=group1", "--name", "order-scan"}, filepath.Join("storage", "log", "worker", "{date}"), filepath.Join("info", "order-scan.log"), filepath.Join("error", "order-scan.log"), filepath.Join("error", "order-scan-system_error.log"), ""},
		{"worker names fall back to worker", []string{"worker", "--group=group1", "--name=order-scan", "--name=order-heartbeat"}, filepath.Join("storage", "log", "worker", "{date}"), filepath.Join("info", "worker.log"), filepath.Join("error", "worker.log"), filepath.Join("error", "worker-system_error.log"), ""},
		{"worker list", []string{"worker", "--list"}, filepath.Join("storage", "log", "worker", "{date}"), filepath.Join("info", "worker.log"), filepath.Join("error", "worker.log"), filepath.Join("error", "worker-system_error.log"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			os.Args = append([]string{"demo-service"}, tc.args...)
			cfg := &Config{}
			applyLogPaths(cfg)

			checks := []struct {
				name string
				got  string
				want string
			}{
				{"info", cfg.Log.Outputs[len(cfg.Log.Outputs)-1], filepath.Join(tc.dir, tc.info)},
				{"error", cfg.Log.ErrorOutputs[len(cfg.Log.ErrorOutputs)-1], filepath.Join(tc.dir, tc.error)},
				{"system", cfg.Log.SystemErrorOutputs[len(cfg.Log.SystemErrorOutputs)-1], filepath.Join(tc.dir, tc.system)},
			}
			for _, check := range checks {
				if check.got != check.want {
					t.Fatalf("%s log = %q, want %q", check.name, check.got, check.want)
				}
			}
			if tc.access == "" {
				if len(cfg.Log.AccessOutputs) != 0 {
					t.Fatalf("script mode must not write an access log, got %v", cfg.Log.AccessOutputs)
				}
				return
			}
			if got := cfg.Log.AccessOutputs[len(cfg.Log.AccessOutputs)-1]; got != tc.access {
				t.Fatalf("access log = %q, want %q", got, tc.access)
			}
		})
	}
}

func TestApplyLogPathsKeepsConfiguredOutputs(t *testing.T) {
	original := os.Args
	defer func() { os.Args = original }()
	os.Args = []string{"demo-service"}

	cfg := &Config{}
	cfg.Log.Outputs = []string{"stdout"}
	applyLogPaths(cfg)

	if len(cfg.Log.Outputs) != 2 || cfg.Log.Outputs[0] != "stdout" {
		t.Fatalf("outputs = %v", cfg.Log.Outputs)
	}
	if len(cfg.Log.AccessOutputs) != 1 {
		t.Fatalf("access outputs = %v", cfg.Log.AccessOutputs)
	}
}
