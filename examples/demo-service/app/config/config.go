package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bingcool/gofy"
	gofyconfig "github.com/bingcool/gofy/config"
	"github.com/bingcool/gofy/logger"
	supportai "github.com/bingcool/gofy/support/ai"
	gofylock "github.com/bingcool/gofy/support/lock"
	"github.com/bingcool/gofy/support/tenant"
	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
	appdb "github.com/yourorg/demo-service/app/appio/db"
	appkafka "github.com/yourorg/demo-service/app/appio/kafka"
	"github.com/yourorg/demo-service/app/appio/redis"
	approcketmq "github.com/yourorg/demo-service/app/appio/rocketmq"
	appuuidint "github.com/yourorg/demo-service/app/appio/uuidint"
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
	Lock     gofylock.Config                `yaml:"lock"`
	Order    OrderConfig                    `yaml:"order"`
	Client   ClientConfig                   `yaml:"client"`
	OTel     gofy.TelemetryConfig           `yaml:"otel"`
	// Diagnostics 是独立运维端口上的 Prometheus 指标与 pprof 配置；默认关闭。
	// Diagnostics configures the operational listener (metrics + pprof); off by default.
	Diagnostics gofy.DiagnosticsConfig `yaml:"diagnostics"`
	// LLMAI 是 LLM 接入配置，由 llm_ai.enabled 控制启停：整段缺失或 enabled 为
	// false 时没有任何使用方去构造客户端，启动也不受影响（demo 的用法见
	// worker/group3/ai_check.go，不额外挂 HTTP 路由）。
	// LLMAI configures the LLM client, gated by llm_ai.enabled: a missing
	// section or enabled: false means nothing builds a client, and startup is
	// unaffected. The demo exercises it from worker/group3/ai_check.go instead
	// of exposing routes.
	LLMAI   supportai.Config     `yaml:"llm_ai"`
	Tenant  TenantConfig         `yaml:"tenant"`
	PIDFile string               `yaml:"pid_file"`
	JWT     middleware.JWTConfig `yaml:"jwt"`
}

// TenantConfig controls row-level tenant isolation on the demo's GORM pools.
// It is off by default: enabling it without a tenant-aware table changes no
// SQL, because the plugin skips models that do not declare the column.
// TenantConfig 控制 demo 的 GORM 连接池是否启用行级租户隔离。默认关闭；即使打开，
// 没有声明租户列的表也不会被改写，插件会跳过这类模型。
type TenantConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Column            string `yaml:"column"`
	OverwriteExplicit bool   `yaml:"overwrite_explicit"`
}

// Options turns the YAML section into plugin options. The bool is false when
// isolation is disabled, so the caller simply does not pass WithTenant.
// Options 把 YAML 段转成插件选项；隔离关闭时 bool 为 false，调用方便不传 WithTenant。
func (c TenantConfig) Options() (tenant.Options, bool) {
	if !c.Enabled {
		return tenant.Options{}, false
	}
	opts := tenant.Options{OverwriteExplicit: c.OverwriteExplicit}
	if col := strings.TrimSpace(c.Column); col != "" && col != tenant.DefaultColumn {
		opts.Handler = tenant.FuncHandler{
			ColumnName:   col,
			TenantIDFunc: (tenant.ContextHandler{}).TenantID,
		}
	}
	return opts, true
}

// OpenOptions is the single definition every entry point uses to pass tenant
// isolation to the io generation. HTTP reload and scripts call it directly;
// workers take Options() because they install the plugin on the framework's
// pool instead of opening a generation. Keeping the three entries on one
// function is what prevents "HTTP filters, worker does not".
// OpenOptions 是各入口把租户隔离传给 io 装配代的唯一定义。HTTP Reload 与脚本直接
// 调用；worker 用 Options()，因为它在框架的连接池上安装插件、不打开装配代。三个入口
// 共用一处实现，才能避免"HTTP 过滤、worker 不过滤"。
func (c TenantConfig) OpenOptions() []appdb.OpenOption {
	opts, enabled := c.Options()
	if !enabled {
		return nil
	}
	return []appdb.OpenOption{appdb.WithTenant(opts)}
}

// Database returns the named instance; the caller must have checked it exists.
// Database 返回指定实例的配置；实例缺失时给回零值，存在性由调用方自行确认。
func (c *Config) Database(name string) gofy.DatabaseConfig {
	return c.DB[name]
}

// warnMissingEnv writes the missing-variable warning. It is a variable so tests
// can capture it; both the startup load and the reload path go through Load,
// so one place covers both. Config loading happens before the logger exists, so
// the warning goes to stderr rather than through the application logger.
var warnMissingEnv = func(names []string) {
	fmt.Fprintf(os.Stderr, "warning: config: missing environment variables %s; expanded to empty strings\n", strings.Join(names, ", "))
}

func Load(ctx context.Context, path string) (*Config, error) {
	cfg, err := gofyconfig.Load[Config](ctx, path,
		gofyconfig.WithStrict(true),
		gofyconfig.WithAllowMissingEnv(true),
		gofyconfig.WithEnvWarning(warnMissingEnv),
	)
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
	if err := cfg.Lock.Validate(); err != nil {
		return nil, fmt.Errorf("config: lock: %w", err)
	}
	if err := cfg.OTel.Validate(); err != nil {
		return nil, fmt.Errorf("config: otel: %w", err)
	}
	// An absent llm_ai section decodes to the zero value, which Validate treats
	// as "disabled" and accepts: a deployment without LLM access must not need
	// the section at all. When it is enabled, typos and missing credentials fail
	// the startup instead of the first user request.
	// 缺少 llm_ai 段时解码得到零值，Validate 视其为"未启用"并放行：不接大模型的
	// 部署根本不需要这一段。启用之后，拼写错误与凭证缺失都会让启动失败，而不是等到
	// 第一个用户请求。
	if err := cfg.LLMAI.Validate(); err != nil {
		return nil, fmt.Errorf("config: llm_ai: %w", err)
	}
	cfg.Client.Normalize()
	applyLogPaths(cfg)
	return cfg, nil
}
