package container

import (
	"context"
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/tenant"
	"github.com/yourorg/demo-service/app/config"
)

// tenantModel 是一张带租户列的表：解析器对它做判定，容器测试因此能看出开关的两种结果。
type tenantModel struct {
	ID       uint64
	TenantID string
}

func (tenantModel) TableName() string { return "tenant_models" }

// TestSQLModelFollowsTenantConfig 是本切换存在的理由：租户开关只有一份。
// enabled=false 时构造器不加条件、缺租户也不报错，与没装插件的连接一致；
// enabled=true 时缺租户报 ErrTenantRequired，带租户才追加条件。两边共用
// tenantConfig().SQLModelOptions()，不会出现「插件关了、构造器还过滤」。
func TestSQLModelFollowsTenantConfig(t *testing.T) {
	off := NewContainer(&config.Config{Tenant: config.TenantConfig{Enabled: boolPtr(false)}})
	ref, scope, err := off.SQLModel().Table(context.Background(), tenantModel{})
	if err != nil {
		t.Fatalf("disabled resolver: %v", err)
	}
	if scope != nil {
		t.Fatalf("disabled resolver returned a tenant scope: %v", scope)
	}
	if ref.C("id") != "`tenant_models`.`id`" {
		t.Fatalf("ref.C(id) = %q", ref.C("id"))
	}

	on := NewContainer(&config.Config{Tenant: config.TenantConfig{Enabled: boolPtr(true)}})
	if _, _, err := on.SQLModel().Table(context.Background(), tenantModel{}); !errors.Is(err, tenant.ErrTenantRequired) {
		t.Fatalf("enabled resolver without a tenant: err = %v, want tenant.ErrTenantRequired", err)
	}

	ctx := tenant.WithTenant(context.Background(), "t-9")
	_, scope, err = on.SQLModel().Table(ctx, tenantModel{})
	if err != nil {
		t.Fatalf("enabled resolver with a tenant: %v", err)
	}
	if scope == nil {
		t.Fatal("enabled resolver returned no tenant scope")
	}
}

// TestSQLModelResolverIsPerGeneration 固定「每代构造一次、同代复用」：
// 解析器缓存 schema 且开关在构造时读取，重复取用必须是同一个实例。
func TestSQLModelResolverIsPerGeneration(t *testing.T) {
	c := NewContainer(&config.Config{})
	if c.SQLModel() != c.SQLModel() {
		t.Fatal("SQLModel built a second resolver within one generation")
	}
	if other := NewContainer(&config.Config{}); other.SQLModel() == c.SQLModel() {
		t.Fatal("a new generation reused the previous resolver")
	}
}

// boolPtr 给三态开关赋值：nil 表示"未设置"，也就是开启。
func boolPtr(v bool) *bool { return &v }
