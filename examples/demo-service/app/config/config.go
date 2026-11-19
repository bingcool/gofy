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
	"github.com/bingcool/gofy/support/sqllog"
	appamqp "github.com/yourorg/demo-service/app/appio/amqp"
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
	Env  string          `yaml:"env" default:"dev"`
	HTTP gofy.HTTPConfig `yaml:"http"`
	Log  logger.Config   `yaml:"log"`
	// SQLLog is the top-level SQL log policy, shared by every connection. It is
	// intentionally not per-database: masking is a compliance policy, and N
	// pools would mean N places to get it right. See sqllog.Config.
	// SQLLog 是顶层的 SQL 日志策略，被所有连接共享。刻意不按数据库拆分：脱敏是合规策略，
	// 每个池一处配置就意味着有 N 处需要配对。
	SQLLog   sqllog.Config                  `yaml:"sql_log"`
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

// Database returns the named instance and whether the section configures it.
//
// The bool is the point: every section in this file is optional, so a caller
// must not read a zero-value config as "configured with empty DSN". `db` absent,
// or a `db` block without `default`, means the deployment uses no
// framework-managed database — the App then starts with a nil Runtime.DB and
// requests that need one fail with ErrNotConfigured instead of the process
// refusing to boot.
// Database 返回指定实例，以及该段是否配置了它。
//
// 布尔返回值是关键：本文件每一段都是可选的，调用方不能把零值配置当成"已配置但
// DSN 为空"。缺少 `db` 段、或 `db` 段里没有 `default`，表示这套部署不使用框架托管
// 的数据库——App 会以 nil 的 Runtime.DB 启动，需要数据库的请求得到 ErrNotConfigured，
// 而不是整个进程拒绝启动。
func (c *Config) Database(name string) (gofy.DatabaseConfig, bool) {
	if c == nil {
		return gofy.DatabaseConfig{}, false
	}
	cfg, ok := c.DB[name]
	return cfg, ok
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
	// Every top-level section is optional: an absent `db`, `redis`, `amqp`,
	// `kafka`, `rocketmq`, `uuidint`, `lock`, `otel`, `tenant` or `llm_ai`
	// block means this deployment does not use it, not that the file is wrong.
	// The loops below therefore validate what is present and skip what is not;
	// a missing `db.default` leaves the App without a framework-managed pool,
	// which the framework already supports (nil Runtime.DB, /readyz stays 200).
	// 每一段顶层配置都是可选的：缺少 `db`、`redis`、`amqp`、`kafka`、`rocketmq`、
	// `uuidint`、`lock`、`otel`、`tenant`、`llm_ai` 表示这套部署用不到它，而不是
	// 文件有问题。因此下面只校验出现的段、跳过缺失的段；缺少 `db.default` 时 App 没有
	// 框架托管的连接池，这一点框架本就支持（Runtime.DB 为 nil，/readyz 仍返回 200）。
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
