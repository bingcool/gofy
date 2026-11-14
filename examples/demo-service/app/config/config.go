package config

import (
	"context"
	"errors"
	"fmt"
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
)

// DefaultDatabase is the instance the framework manages and scripts use.
// DefaultDatabase 是框架托管、脚本也使用的实例名；其余实例由 io 模块自行建连。
const DefaultDatabase = "default"

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
}

// Database returns the named instance; the caller must have checked it exists.
// Database 返回指定实例的配置；实例缺失时给回零值，存在性由调用方自行确认。
func (c *Config) Database(name string) gofy.DatabaseConfig {
	return c.DB[name]
}

func Load(ctx context.Context, path string) (*Config, error) {
	cfg, err := gofyconfig.Load[Config](ctx, path, gofyconfig.WithStrict(true))
	if err != nil {
		return nil, err
	}
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
	if err := cfg.OTel.Validate(); err != nil {
		return nil, fmt.Errorf("config: otel: %w", err)
	}
	applyLogPaths(cfg)
	return cfg, nil
}
