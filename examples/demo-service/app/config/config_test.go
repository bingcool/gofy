package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appamqp "github.com/yourorg/demo-service/app/io/amqp"
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

// TestLoadWorkerConfigFromSeparateFile pins the split: consumer settings come
// from worker/worker.yaml, not from config.yaml, and the defaults declared on
// group1.Config still apply to the fields the file omits.
// TestLoadWorkerConfigFromSeparateFile 固定这次拆分：消费者设置来自
// worker/worker.yaml 而非 config.yaml，同时 group1.Config 上声明的默认值对文件里
// 省略的字段仍然生效。
func TestLoadWorkerConfigFromSeparateFile(t *testing.T) {
	mainPath := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
`)
	workerPath := filepath.Join(t.TempDir(), "worker.yaml")
	body := `
consumers:
  kafka:
    enabled: true
    instance: default
    concurrency: 2
    topic: demo.order.events
    group: demo-service-order-consumer
    max_attempts: 4
`
	if err := os.WriteFile(workerPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write worker config: %v", err)
	}
	t.Setenv(WorkerConfigEnv, workerPath)

	cfg, err := Load(context.Background(), mainPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	kafka := cfg.Worker.Consumers.Kafka
	if !kafka.Enabled {
		t.Fatal("kafka consumer is not enabled")
	}
	if kafka.Topic != "demo.order.events" || kafka.Group != "demo-service-order-consumer" {
		t.Fatalf("kafka = %+v", kafka)
	}
	if kafka.Concurrency != 2 {
		t.Fatalf("concurrency = %d, want 2", kafka.Concurrency)
	}
	// Defaults come from the struct tags, not from the file.
	// 默认值来自结构体标签，而不是文件内容。
	if kafka.CommitMode != "manual" {
		t.Fatalf("commit_mode = %q, want the default manual", kafka.CommitMode)
	}
	if !kafka.Idempotency {
		t.Fatal("idempotency defaulted to false, want true")
	}
	if cfg.Worker.Consumers.AMQP.Enabled {
		t.Fatal("amqp consumer was enabled by a file that never mentioned it")
	}
}

// TestLoadWithoutWorkerConfigDisablesConsumers keeps an API-only deployment
// working: no worker file means every consumer is off, not a startup failure.
// TestLoadWithoutWorkerConfigDisablesConsumers 让只跑 API 的部署照常启动：没有
// worker 文件意味着消费者全关，而不是启动失败。
func TestLoadWithoutWorkerConfigDisablesConsumers(t *testing.T) {
	mainPath := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
`)
	t.Setenv(WorkerConfigEnv, filepath.Join(t.TempDir(), "absent.yaml"))

	cfg, err := Load(context.Background(), mainPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	consumers := cfg.Worker.Consumers
	if consumers.Kafka.Enabled || consumers.AMQP.Enabled || consumers.RocketMQ.Enabled || consumers.Queues.Enabled {
		t.Fatalf("a missing worker file enabled a consumer: %+v", consumers)
	}
}

// TestLoadRejectsInlineWorkerBlock points a stale copy of the block at the new
// file instead of failing with "field worker not found", which reads like a
// typo.
// TestLoadRejectsInlineWorkerBlock 把残留的配置段指向新文件，而不是用
// "field worker not found" 让人以为拼错了字段。
func TestLoadRejectsInlineWorkerBlock(t *testing.T) {
	mainPath := writeConfig(t, `
db:
  default:
    dsn: user:pass@tcp(127.0.0.1:3306)/main
worker:
  consumers:
    kafka:
      enabled: true
`)

	_, err := Load(context.Background(), mainPath)
	if err == nil {
		t.Fatal("Load accepted an inline worker block")
	}
	if !strings.Contains(err.Error(), "moved to") {
		t.Fatalf("error = %v, want it to point at the new file", err)
	}
}
