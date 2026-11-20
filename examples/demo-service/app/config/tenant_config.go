package config

import (
	"strings"

	"github.com/bingcool/gofy/support/sqlmodel"
	"github.com/bingcool/gofy/support/tenant"
	appdb "github.com/yourorg/demo-service/app/appio/db"
)

// TenantConfig controls row-level tenant isolation on the demo's GORM pools.
//
// There are two ways to be off, and they mean different things:
//
//   - the section is absent (Config.Tenant is nil): this deployment does not use
//     tenant isolation at all, so nothing is installed;
//   - the section is present but enabled is false: nothing is installed either,
//     but the deployment still declares that it has tenant columns.
//
// A present section without an enabled key means on. The asymmetry is deliberate:
// writing a tenant section says the deployment uses tenants, and dropping the
// filter because one key is missing is the only failure mode that stays silent —
// a table with tenant_id would return every tenant's rows, with no filter and no
// error.
//
// It lives in a file of its own for the same reason OrderConfig does: one
// section, one xxx_config.go, and Config only maps the section name. Adding a
// deployment switch means editing this file instead of growing config.go.
// TenantConfig 控制 demo 的 GORM 连接池是否启用行级租户隔离。
//
// 两种"关闭"含义不同：
//
//   - 整段缺失（Config.Tenant 为 nil）：这套部署完全不用租户隔离，什么都不装；
//   - 段在但 enabled 为 false：同样不装，但部署明说自己有租户列。
//
// 段在而不写 enabled 视为开启。这个不对称是有意的：写了 tenant 段就是在用租户，而因为
// 少写一个键就把过滤去掉，是本包唯一会静默失效的方向——带 tenant_id 的表会不加过滤地返回
// 所有租户的数据，且没有报错。
//
// 单独成文件与 OrderConfig 同理：一段配置一个 xxx_config.go，Config 只负责挂段名；
// 以后增加一个开关只改这里，不会让 config.go 继续膨胀。
type TenantConfig struct {
	// Enabled installs the plugin on every pool. Nil means enabled when the
	// section exists; false is the explicit opt-out. A nil receiver (no section
	// at all) is off — see IsEnabled.
	// Enabled 决定是否在每个池上安装插件。段存在时 nil 表示开启，false 才是显式关闭；
	// 接收者为 nil（整段缺失）时为关闭——见 IsEnabled。
	Enabled *bool  `yaml:"enabled"`
	Column  string `yaml:"column"`
	// OverwriteExplicit mirrors the plugin's tri-state switch. Nil means the
	// context tenant wins on INSERT, which is the safe default: the plugin
	// cannot tell an administrator from a request DTO that carries tenant_id.
	// OverwriteExplicit 对应插件的三态开关。nil 表示 INSERT 一律用上下文租户，是安全默认值：
	// 插件无从分辨管理员与带 tenant_id 的请求 DTO。
	OverwriteExplicit *bool `yaml:"overwrite_explicit"`
}

// IsEnabled reports whether isolation should be installed. A nil receiver means
// the section is absent — "this deployment does not use tenant isolation"; a
// present section without the enabled key means on.
// IsEnabled 报告是否应当安装隔离。接收者为 nil 表示整段缺失，即「这套部署不用租户隔离」；
// 段存在而没写 enabled 表示开启。
func (c *TenantConfig) IsEnabled() bool {
	if c == nil {
		return false
	}
	return c.Enabled == nil || *c.Enabled
}

// OverwritesExplicit reports the INSERT policy. Absent means overwrite, so an
// untrusted caller cannot choose another tenant by filling tenant_id; false is
// an explicit opt-out for a privileged connection only.
// OverwritesExplicit 报告 INSERT 策略。不写表示覆盖，不可信调用方因此无法靠填 tenant_id
// 选择别的租户；false 是特权连接的显式选择。
func (c *TenantConfig) OverwritesExplicit() bool {
	return c == nil || c.OverwriteExplicit == nil || *c.OverwriteExplicit
}

// Options turns the YAML section into plugin options. The bool is false when
// isolation is off — an absent section or an explicit false — so the caller
// simply does not pass WithTenant.
// Options 把 YAML 段转成插件选项；隔离关闭时（整段缺失或显式 false）bool 为 false，
// 调用方便不传 WithTenant。
func (c *TenantConfig) Options() (tenant.Options, bool) {
	if !c.IsEnabled() {
		return tenant.Options{}, false
	}
	overwrite := c.OverwritesExplicit()
	opts := tenant.Options{OverwriteExplicit: &overwrite}
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
func (c *TenantConfig) OpenOptions() []appdb.OpenOption {
	opts, enabled := c.Options()
	if !enabled {
		return nil
	}
	return []appdb.OpenOption{appdb.WithTenant(opts)}
}

// SQLModelOptions turns the same section into the option set of the builder
// resolver, so one deployment switch governs both isolation paths: the GORM
// plugin and support/sqlmodel. Disabled mirrors !IsEnabled, and Handler is shared
// so the column and the identity source are not configured twice — a mismatch
// there would filter one path by tenant_id and the other by org_id.
//
// SQLModelOptions 把同一段配置转成构造器解析器的选项，让一个部署开关同时管住两条隔离
// 路径：GORM 插件与 support/sqlmodel。Disabled 镜像 !IsEnabled，Handler 共用，列名与
// 身份来源因此不必配置两遍——两处不一致会让一条路径按 tenant_id 过滤、另一条按 org_id。
func (c *TenantConfig) SQLModelOptions() sqlmodel.Options {
	opts, enabled := c.Options()
	return sqlmodel.Options{
		Disabled: !enabled,
		Handler:  opts.Handler,
	}
}
