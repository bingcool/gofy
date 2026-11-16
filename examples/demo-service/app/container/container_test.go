package container_test

import (
	"context"
	"testing"

	"github.com/bingcool/gofy"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/app/container"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func testRuntime() *gofy.Runtime {
	return &gofy.Runtime{DB: &gorm.DB{}, Logger: zap.NewNop()}
}

func TestAccessorsReturnServices(t *testing.T) {
	ctr := container.NewContainer(nil)
	ctr.Init(zap.NewNop())
	if ctr.ProductService() == nil || ctr.OrderConfigService() == nil || ctr.OrderService() == nil {
		t.Fatal("container returned a nil service")
	}
}

// 业务配置由装配根按段交给 service：容器拿到 config.yaml 的 order 段，原样作为构造
// 参数传下去，因此改这一段的类型或值只需动容器这一处，中间没有同形状的复制品。
func TestOrderConfigReachesTheService(t *testing.T) {
	ctr := container.NewContainer(&config.Config{
		Order: config.OrderConfig{AuthRequired: true},
	})
	ctr.Init(zap.NewNop())
	if got := ctr.OrderConfigService().GetConfig(context.Background()); !got.AuthRequired {
		t.Fatal("order config did not reach the service")
	}
}

// Each construction slot takes effect only once: repeated access returns the
// same instance.
// 每个构造槽只生效一次：重复取用返回同一实例。
func TestAccessorsAreIdempotent(t *testing.T) {
	ctr := container.NewContainer(nil)
	ctr.Init(zap.NewNop())
	first := ctr.OrderService()
	ctr.Init(zap.NewNop())
	if ctr.OrderService() != first {
		t.Fatal("container rebuilt the service on a second access")
	}
}

func TestModuleFactoriesRequireDatabase(t *testing.T) {
	ctr := container.NewContainer(nil)
	for _, factory := range ctr.ModuleFactories() {
		if _, err := factory(&gofy.Runtime{Logger: zap.NewNop()}); err == nil {
			t.Fatal("module factory should fail without database")
		}
	}
}

// 订单 service 取用时先构造产品 service，因此订单工厂单独执行也不依赖工厂次序。
func TestOrderFactoryDoesNotDependOnProductFactoryOrder(t *testing.T) {
	ctr := container.NewContainer(nil)
	module, err := ctr.OrderModuleFactory()(testRuntime())
	if err != nil {
		t.Fatalf("order factory: %v", err)
	}
	if module.Name() != "order" {
		t.Fatalf("module = %q, want order", module.Name())
	}
}

// 每个装配代用独立容器：两次装配得到不同的 service。
func TestGenerationsUseIndependentContainers(t *testing.T) {
	rt := testRuntime()
	first := container.NewContainer(nil)
	second := container.NewContainer(nil)
	for i, ctr := range []*container.Container{first, second} {
		for _, factory := range ctr.ModuleFactories() {
			if _, err := factory(rt); err != nil {
				t.Fatalf("assembly %d: %v", i, err)
			}
		}
	}
	if first.OrderService() == second.OrderService() {
		t.Fatal("generations share the same order service")
	}
	if first.ProductService() == second.ProductService() {
		t.Fatal("generations share the same product service")
	}
}
