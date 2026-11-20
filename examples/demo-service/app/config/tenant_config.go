package config

import (
	"strings"

	"github.com/bingcool/gofy/support/sqlmodel"
	"github.com/bingcool/gofy/support/tenant"
	appdb "github.com/yourorg/demo-service/app/appio/db"
)

// TenantConfig controls row-level tenant isolation on the demo's GORM pools.
//
// Enabled defaults to true: an absent key means "on", so a deployment that forgot
// the section cannot end up reading every tenant's rows. Turning it off is an
// explicit act, and it only makes sense for a database that has no tenant
// columns at all — with isolation off, a table that does carry tenant_id returns
// every tenant's rows, without a filter and without an error.
//
// It lives in a file of its own for the same reason OrderConfig does: one
// section, one xxx_config.go, and Config only maps the section name. Adding a
// deployment switch means editing this file instead of growing config.go.
// TenantConfig 控制 demo 的 GORM 连接池是否启用行级租户隔离。
//
// Enabled 默认开启：不写这个键就是开启，忘记配一段不等于关掉隔离——否则带租户列的表会
// 不加过滤地返回所有租户的数据，且没有任何报错。关闭是显式动作，只对完全没有租户列的库
// 才合理。
//
// 单独成文件与 OrderConfig 同理：一段配置一个 xxx_config.go，Config 只负责挂段名；
// 以后增加一个开关只改这里，不会让 config.go 继续膨胀。
type TenantConfig struct {
	// Enabled installs the plugin on every pool. Nil means enabled; false is the
	// explicit opt-out.
	// Enabled 决定是否在每个池上安装插件。为 nil 表示开启，false 才是显式关闭。
	Enabled           *bool  `yaml:"enabled"`
	Column            string `yaml:"column"`
	OverwriteExplicit bool   `yaml:"overwrite_explicit"`
}

// IsEnabled reports whether isolation should be installed. An absent key means
// enabled, which is the safe default: forgetting the section must not silently
// remove the filter.
// IsEnabled 报告是否应当安装隔离。键缺失表示开启，这是安全的默认值：漏配一段不该等于
// 悄悄去掉过滤。
func (c TenantConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// Options turns the YAML section into plugin options. The bool is false when
// isolation is disabled, so the caller simply does not pass WithTenant.
// Options 把 YAML 段转成插件选项；隔离关闭时 bool 为 false，调用方便不传 WithTenant。
func (c TenantConfig) Options() (tenant.Options, bool) {
	if !c.IsEnabled() {
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
// SQLModelOptions turns the same section into the option set of the builder
// resolver, so one deployment switch governs both isolation paths: the GORM
// plugin and support/sqlmodel. Disabled mirrors !Enabled, and Handler is shared
// so the column and the identity source are not configured twice — a mismatch
// there would filter one path by tenant_id and the other by org_id.
//
// SQLModelOptions 把同一段配置转成构造器解析器的选项，让一个部署开关同时管住两条隔离
// 路径：GORM 插件与 support/sqlmodel。Disabled 镜像 !Enabled，Handler 共用，列名与身份
// 来源因此不必配置两遍——两处不一致会让一条路径按 tenant_id 过滤、另一条按 org_id。
func (c TenantConfig) SQLModelOptions() sqlmodel.Options {
	opts, enabled := c.Options()
	return sqlmodel.Options{
		Disabled: !enabled,
		Handler:  opts.Handler,
	}
}

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
