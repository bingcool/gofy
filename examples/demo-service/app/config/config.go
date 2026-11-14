package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/bingcool/gofy"
	gofyconfig "github.com/bingcool/gofy/config"
	"github.com/bingcool/gofy/logger"
	appamqp "github.com/yourorg/demo-service/app/io/amqp"
	appkafka "github.com/yourorg/demo-service/app/io/kafka"
	"github.com/yourorg/demo-service/app/io/redis"
	approcketmq "github.com/yourorg/demo-service/app/io/rocketmq"
	appuuidint "github.com/yourorg/demo-service/app/io/uuidint"
	"github.com/yourorg/demo-service/app/middleware"
	"github.com/yourorg/demo-service/worker/group1"
)

// DefaultDatabase is the instance the framework manages and scripts use.
// DefaultDatabase 是框架托管、脚本也使用的实例名；其余实例由 io 模块自行建连。
const DefaultDatabase = "default"

// DefaultWorkerConfigPath is where the worker configuration is read from when
// WORKER_CONFIG_PATH is not set.
// DefaultWorkerConfigPath 是未设置 WORKER_CONFIG_PATH 时读取的 worker 配置路径。
const DefaultWorkerConfigPath = "worker/worker.yaml"

// WorkerConfigEnv overrides the worker configuration path, which is how a
// container mounts it as a ConfigMap without touching the image layout.
// WorkerConfigEnv 覆盖 worker 配置路径；容器把它挂成 ConfigMap 时不必改动镜像内的
// 文件布局。
const WorkerConfigEnv = "WORKER_CONFIG_PATH"

// Config 的 DB 按实例名分组：框架只托管 default，其余实例由 io 模块建连。
// Redis、AMQP、Kafka 和 RocketMQ 同样按实例名分组，由 io 模块统一建连和关闭。
type Config struct {
	Env      string                         `yaml:"env" default:"dev"`
	HTTP     gofy.HTTPConfig                `yaml:"http"`
	Log      logger.Config                  `yaml:"log"`
	DB       map[string]gofy.DatabaseConfig `yaml:"db"`
	Redis    redis.Config                   `yaml:"redis"`
	AMQP     appamqp.Config                 `yaml:"amqp"`
	Kafka    appkafka.Config                `yaml:"kafka"`
	RocketMQ approcketmq.Config             `yaml:"rocketmq"`
	UUIDInt  appuuidint.Config              `yaml:"uuidint"`
	OTel     gofy.TelemetryConfig           `yaml:"otel"`
	PIDFile  string                         `yaml:"pid_file"`
	JWT      middleware.JWTConfig           `yaml:"jwt"`
	// Worker carries the resident consumer settings. It is not decoded from
	// config.yaml: Load fills it from worker/worker.yaml, so a worker-only
	// deployment can ship its own file and the API process never reads it.
	// Worker 携带常驻消费者的设置。它不从 config.yaml 解码：Load 从 worker/worker.yaml
	// 读取后填入，因此只跑 worker 的部署可以单独发自己的文件，API 进程完全不读它。
	Worker group1.Config `yaml:"-"`
}

// Database returns the named instance; the caller must have checked it exists.
// Database 返回指定实例的配置；实例缺失时给回零值，存在性由调用方自行确认。
func (c *Config) Database(name string) gofy.DatabaseConfig {
	return c.DB[name]
}

func Load(ctx context.Context, path string) (*Config, error) {
	if err := rejectInlineWorkerBlock(ctx, path); err != nil {
		return nil, err
	}
	cfg, err := gofyconfig.Load[Config](ctx, path, gofyconfig.WithStrict(true))
	if err != nil {
		return nil, err
	}
	// The consumer settings live in their own file. Reading them here keeps the
	// caller unchanged and makes "missing file" mean "every consumer off"
	// rather than a startup failure for an API-only deployment.
	// 消费者设置在自己的文件里。在这里读取让调用方保持不变，并让"文件缺失"表示
	// "所有消费者关闭"，而不是让只跑 API 的部署启动失败。
	worker, err := loadWorkerConfig(ctx, WorkerConfigPath())
	if err != nil {
		return nil, err
	}
	cfg.Worker = worker
	if _, ok := cfg.DB[DefaultDatabase]; !ok {
		return nil, fmt.Errorf("config: db.%s is required", DefaultDatabase)
	}
	for name, conf := range cfg.DB {
		if err := conf.Validate(); err != nil {
			return nil, fmt.Errorf("config: db.%s: %w", name, err)
		}
	}
	for name, inst := range cfg.Redis {
		if !inst.Enabled {
			continue
		}
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("config: redis instance name must not be empty")
		}
		if err := inst.Config.Validate(); err != nil {
			return nil, fmt.Errorf("config: redis.%s: %w", name, err)
		}
	}
	for name, inst := range cfg.AMQP {
		if !inst.Enabled {
			continue
		}
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("config: amqp instance name must not be empty")
		}
		if err := inst.Config.Validate(); err != nil {
			return nil, fmt.Errorf("config: amqp.%s: %w", name, err)
		}
	}
	for name, inst := range cfg.Kafka {
		if !inst.Enabled {
			continue
		}
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("config: kafka instance name must not be empty")
		}
		if err := inst.Config.Validate(); err != nil {
			return nil, fmt.Errorf("config: kafka.%s: %w", name, err)
		}
	}
	for name, inst := range cfg.RocketMQ {
		if !inst.Enabled {
			continue
		}
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("config: rocketmq instance name must not be empty")
		}
		if err := inst.Config.Validate(); err != nil {
			return nil, fmt.Errorf("config: rocketmq.%s: %w", name, err)
		}
	}
	if err := cfg.UUIDInt.Validate(); err != nil {
		return nil, fmt.Errorf("config: uuidint: %w", err)
	}
	// Worker validation happens while worker/worker.yaml is decoded: that file
	// maps straight onto group1.Config, which implements Validate, so
	// gofyconfig.Load runs it before returning.
	// Worker 的校验在解码 worker/worker.yaml 时完成：该文件直接映射到实现了
	// Validate 的 group1.Config，gofyconfig.Load 会在返回前调用它。
	if err := cfg.OTel.Validate(); err != nil {
		return nil, fmt.Errorf("config: otel: %w", err)
	}
	applyLogPaths(cfg)
	return cfg, nil
}

// WorkerConfigPath returns the worker configuration path: WORKER_CONFIG_PATH
// wins, otherwise worker/worker.yaml under the working directory.
// WorkerConfigPath 返回 worker 配置路径：WORKER_CONFIG_PATH 优先，否则使用工作
// 目录下的 worker/worker.yaml。
func WorkerConfigPath() string {
	if p := strings.TrimSpace(os.Getenv(WorkerConfigEnv)); p != "" {
		return p
	}
	return DefaultWorkerConfigPath
}

// loadWorkerConfig reads the consumer settings.
//
// A missing file yields the zero Config — every consumer disabled — because an
// API-only deployment has no reason to ship worker settings. Anything else,
// including a strict-decode or Validate failure, is returned as-is: a file that
// exists but is wrong must not be mistaken for "no consumers".
// loadWorkerConfig 读取消费者设置。
//
// 文件缺失时返回零值 Config——所有消费者关闭——因为只跑 API 的部署没有理由携带
// worker 配置。其它错误（严格解码失败、Validate 失败）原样返回：文件存在但写错，
// 不能被当成"没有消费者"。
func loadWorkerConfig(ctx context.Context, path string) (group1.Config, error) {
	cfg, err := gofyconfig.Load[group1.Config](ctx, path, gofyconfig.WithStrict(true))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return group1.Config{}, nil
		}
		return group1.Config{}, err
	}
	return *cfg, nil
}

// rejectInlineWorkerBlock turns a stale `worker:` block in config.yaml into a
// pointed error. Strict decoding would reject it anyway, but as "field worker
// not found", which reads like a typo rather than "this block moved".
//
// A read failure is ignored here and left to the main decode, so a missing or
// unparsable config.yaml keeps its existing error message.
// rejectInlineWorkerBlock 把 config.yaml 里残留的 `worker:` 段变成一个明确的错误。
// 严格解码本来也会拒绝它，但错误是 "field worker not found"，读起来像拼写错误，
// 而不是"这段配置搬家了"。
//
// 读取失败在这里忽略，交给主解码报告，缺失或无法解析的 config.yaml 保持原有错误。
func rejectInlineWorkerBlock(ctx context.Context, path string) error {
	doc, err := gofyconfig.LoadDocument(ctx, path)
	if err != nil {
		return nil
	}
	if _, ok := doc.Get("worker"); ok {
		return fmt.Errorf(
			"config: %s: the worker block moved to %s (override with %s=%s)",
			path, WorkerConfigPath(), WorkerConfigEnv, "path/to/worker.yaml",
		)
	}
	return nil
}
