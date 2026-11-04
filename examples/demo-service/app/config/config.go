package config

import (
	"context"

	"github.com/bingcool/gofy"
	gofyconfig "github.com/bingcool/gofy/config"
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
	return gofyconfig.Load[Config](ctx, path, gofyconfig.WithStrict(true))
}
