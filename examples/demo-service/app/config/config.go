package config

import (
	"context"
	"path/filepath"

	"github.com/bingcool/gofy"
	gofyconfig "github.com/bingcool/gofy/config"
	"github.com/bingcool/gofy/core/systemenv"
	"github.com/bingcool/gofy/logging"
	"github.com/yourorg/demo-service/app/module/order"
)

type Config struct {
	Env     string              `yaml:"env" default:"dev"`
	HTTP    gofy.HTTPConfig     `yaml:"http"`
	Log     logging.Config      `yaml:"log"`
	DB      gofy.DatabaseConfig `yaml:"db"`
	PIDFile string              `yaml:"pid_file"`
	Order   order.Config        `yaml:"order"`
}

func Load(ctx context.Context, path string) (*Config, error) {
	cfg, err := gofyconfig.Load[Config](ctx, path, gofyconfig.WithStrict(true))
	if err != nil {
		return nil, err
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
