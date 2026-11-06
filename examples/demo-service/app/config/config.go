package config

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bingcool/gofy"
	gofyconfig "github.com/bingcool/gofy/config"
	"github.com/bingcool/gofy/core/systemenv"
	"github.com/bingcool/gofy/logging"
	"github.com/yourorg/demo-service/app/io/redis"
	"github.com/yourorg/demo-service/app/module/order"
)

// DefaultDatabase is the instance the framework manages and scripts use.
const DefaultDatabase = "default"

// Config 的 DB 按实例名分组：框架只托管 default，其余实例由 io 模块建连。
// Redis 同样按实例名分组，由 io 模块统一建连和关闭。
type Config struct {
	Env     string                         `yaml:"env" default:"dev"`
	HTTP    gofy.HTTPConfig                `yaml:"http"`
	Log     logging.Config                 `yaml:"log"`
	DB      map[string]gofy.DatabaseConfig `yaml:"db"`
	Redis   redis.Config                   `yaml:"redis"`
	PIDFile string                         `yaml:"pid_file"`
	Order   order.Config                   `yaml:"order"`
}

// Database returns the named instance; the caller must have checked it exists.
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
	applyLogPaths(cfg)
	return cfg, nil
}

// applyLogPaths 按执行模式把日志落到 storage/log/http 或 storage/log/script，
// 与 swoolefy 的 cli/script 分目录一致。脚本模式使用
// script/{date}/info/<脚本>.log 和 script/{date}/error/<脚本>.log，
// 一个脚本对应一组日志文件；{date} 由 logging 按日期替换。
// 追加而不是覆盖，config.yaml 里的 stdout 等输出仍然保留；热更新重新 Load
// 时会再次追加到新配置上。
func applyLogPaths(cfg *Config) {
	if systemenv.IsScriptService() {
		dateDir := filepath.Join("storage", "log", "script", "{date}")
		infoName, errorName, systemName := "info.log", "error.log", "system_error.log"
		if name := systemenv.ScriptName(); name != "" {
			infoName = name + ".log"
			errorName = name + ".log"
			systemName = name + "-system_error.log"
		}
		cfg.Log.Outputs = append(cfg.Log.Outputs, filepath.Join(dateDir, "info", infoName))
		cfg.Log.ErrorOutputs = append(cfg.Log.ErrorOutputs, filepath.Join(dateDir, "error", errorName))
		cfg.Log.SystemErrorOutputs = append(cfg.Log.SystemErrorOutputs, filepath.Join(dateDir, "error", systemName))
		return
	}
	dir := filepath.Join("storage", "log", "http")
	cfg.Log.Outputs = append(cfg.Log.Outputs, filepath.Join(dir, "info.log"))
	cfg.Log.ErrorOutputs = append(cfg.Log.ErrorOutputs, filepath.Join(dir, "error.log"))
	cfg.Log.SystemErrorOutputs = append(cfg.Log.SystemErrorOutputs, filepath.Join(dir, "system_error.log"))
}
