package config

import (
	"strings"

	"github.com/bingcool/gofy/support/tenant"
	appdb "github.com/yourorg/demo-service/app/appio/db"
)

// TenantConfig controls row-level tenant isolation on the demo's GORM pools.
// It is off by default: enabling it without a tenant-aware table changes no
// SQL, because the plugin skips models that do not declare the column.
//
// It lives in a file of its own for the same reason OrderConfig does: one
// section, one xxx_config.go, and Config only maps the section name. Adding a
// deployment switch means editing this file instead of growing config.go.
// TenantConfig 控制 demo 的 GORM 连接池是否启用行级租户隔离。默认关闭；即使打开，
// 没有声明租户列的表也不会被改写，插件会跳过这类模型。
//
// 单独成文件与 OrderConfig 同理：一段配置一个 xxx_config.go，Config 只负责挂段名；
// 以后增加一个开关只改这里，不会让 config.go 继续膨胀。
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
