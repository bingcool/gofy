package gofy

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// HTTPConfig configures the HTTP server.
// HTTPConfig 配置 HTTP 服务器；零值字段会先补成框架默认值再整体校验。
type HTTPConfig struct {
	Host              string        `yaml:"host" default:"0.0.0.0"`
	Port              int           `yaml:"port" default:"8080"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout" default:"5s"`
	ReadTimeout       time.Duration `yaml:"read_timeout" default:"15s"`
	WriteTimeout      time.Duration `yaml:"write_timeout" default:"30s"`
	IdleTimeout       time.Duration `yaml:"idle_timeout" default:"60s"`
	MaxHeaderBytes    int           `yaml:"max_header_bytes" default:"1048576"`
	Mode              string        `yaml:"mode" default:"release"` // debug|release|test；可选值，默认 release，大小写不敏感
}

// Validate checks the HTTP configuration.
// Validate 校验 HTTP 配置：端口范围、超时与请求头上限不能为负，mode 只接受
// debug、release 或 test（大小写不敏感）。
func (c HTTPConfig) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("gofy: invalid http port %d", c.Port)
	}
	if c.ReadHeaderTimeout < 0 || c.ReadTimeout < 0 || c.WriteTimeout < 0 || c.IdleTimeout < 0 {
		return errors.New("gofy: http timeouts must not be negative")
	}
	if c.MaxHeaderBytes < 0 {
		return errors.New("gofy: max header bytes must not be negative")
	}
	switch strings.ToLower(c.Mode) {
	case "debug", "release", "test":
	default:
		return fmt.Errorf("gofy: invalid http mode %q (want debug, release or test)", c.Mode)
	}
	return nil
}

func defaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		Host:              "0.0.0.0",
		Port:              8080,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		Mode:              "release",
	}
}

func (c HTTPConfig) withDefaults() HTTPConfig {
	out := c
	def := defaultHTTPConfig()
	if strings.TrimSpace(out.Host) == "" {
		out.Host = def.Host
	}
	if out.Port == 0 {
		out.Port = def.Port
	}
	if out.ReadHeaderTimeout == 0 {
		out.ReadHeaderTimeout = def.ReadHeaderTimeout
	}
	if out.ReadTimeout == 0 {
		out.ReadTimeout = def.ReadTimeout
	}
	if out.WriteTimeout == 0 {
		out.WriteTimeout = def.WriteTimeout
	}
	if out.IdleTimeout == 0 {
		out.IdleTimeout = def.IdleTimeout
	}
	if out.MaxHeaderBytes == 0 {
		out.MaxHeaderBytes = def.MaxHeaderBytes
	}
	if strings.TrimSpace(out.Mode) == "" {
		out.Mode = def.Mode
	}
	out.Mode = strings.ToLower(out.Mode)
	return out
}
