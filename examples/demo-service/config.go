package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/bingcool/gofy/support/nacos"
)

// configPath 返回配置文件路径：ORDER_CONFIG 优先，否则使用工作目录下的 config.yaml。
func configPath() string {
	if p := os.Getenv("ORDER_CONFIG"); p != "" {
		return p
	}
	return "config.yaml"
}

// loadNacosConfig prefers the swoolefy-style nacos.yaml + application.yaml
// pair and falls back to NACOS_* environment variables when nacos.yaml is
// absent.
func loadNacosConfig() (nacos.Config, error) {
	cfg, err := nacos.Load("nacos.yaml", "application.yaml")
	if errors.Is(err, fs.ErrNotExist) {
		return nacos.FromEnv(), nil
	}
	return cfg, err
}

// isStartInvocation reports whether args select the foreground start (or no
// subcommand at all).
func isStartInvocation(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			if arg == "-h" || arg == "--help" {
				return false
			}
			continue
		}
		return arg == "start"
	}
	return true
}
