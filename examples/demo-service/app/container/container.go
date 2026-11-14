// Package container 是 demo-service 的装配根。每一代装配（启动一次、Reload
// 一次）新建一个 Container，service 在首次取用时按依赖链惰性构造：每个服务
// 都有自己的构造槽，模块工厂只触发本模块子树，未挂载的能力不会被构建。
// 依赖只在构造期显式注入：跨模块能力以 innerapi 接口传入，模块内协作 service
// 由容器直接构造，不存在包级单例或运行期查找。
package container

import (
	"fmt"
	"sync"

	gofyadapter "github.com/bingcool/gofy-adapter/adapter/gofy"
	"github.com/bingcool/gofy-interface-api/contracts/common/endpoint"

	appmiddleware "github.com/yourorg/demo-service/app/middleware"
	"go.uber.org/zap"
)

// Container 持有本次服务的仓库、service 构造槽与 logger。它不是包级全局：装配方每代
// 新建一个实例，模块工厂闭包捕获它，因此热更新代际隔离、测试之间也互不影响。
type Container struct {
	repos    RepositoryDeclare
	services ServiceDeclare

	loggerMu  sync.Mutex
	zapLogger *zap.Logger
}

// NewContainer 创建一个尚未构建任何 service 的容器。
//
// 启动和重启（热更新）都会重新走一遍装配：启动时 appOptions 调
// reload.factories(cfg)，热更新时 app.Reload 再用 reloader.factories(cfg)；
// 两者都新建容器，并把本次服务的 ModuleFactories() 与 middlewareRegistrar 注册到
// 当次 App。因此每代各有一套仓库与构造槽，旧代实例只服务在途请求。
func NewContainer() *Container {
	return &Container{
		repos:    NewRepositoryDeclare(),
		services: NewServiceDeclare(),
	}
}

// BindOptions 返回契约路由的绑定选项，与具体模块无关，所有模块共用。
//
// 限流中间件按每条路由声明的 Spec.RateLimit 动态生成：哪些路由要限流、
// 限多少、按什么主体计数、用哪个 Redis 实例（RedisAlias）全部写在契约里。
// 这里只交出回调，不持有 Store 或连接：adapter 在绑定路由时逐条调用回调，
// 中间件在首次请求时按别名自行解析连接（路由绑定早于 app/appio 模块打开
// Redis）。每个模块工厂各调用一次本方法，得到的都是同一个无状态回调。
//
// AuthValidator 让“契约声明需要认证、实际没有安装认证”在启动期就失败，
// 而不是上线后才发现；它只检查声明，不创建中间件。
func (c *Container) BindOptions() gofyadapter.BindOptions {
	return gofyadapter.BindOptions{
		RateLimit:     appmiddleware.RateLimiter{}.Callback(),
		AuthValidator: authValidator,
	}
}

// authValidator 要求 AuthRequired 路由在分组或路由上声明 NameAuth，
// AuthOptional 路由声明 NameOptionalAuth。Optional 不能拿 Required 的别名
// 顶替：前者必须支持匿名放行，后者必须拒绝匿名请求。
//
// AuthNone（零值）不产生认证要求，adapter 本来就不会调用到这里，显式放行
// 是为了直接调用时语义一致。校验失败会带上路由名让启动或 Reload 中止。
func authValidator(spec endpoint.Spec, declared []string) error {
	if spec.Auth == endpoint.AuthNone {
		return nil
	}
	want := appmiddleware.NameAuth
	if spec.Auth == endpoint.AuthOptional {
		want = appmiddleware.NameOptionalAuth
	}
	for _, name := range declared {
		if name == want {
			return nil
		}
	}
	return fmt.Errorf("auth %q requires middleware %q in the group or route declaration", spec.Auth, want)
}

// Init 记录本次服务 Runtime 的 logger，供后续惰性构造使用。模块工厂在取用任何
// service 之前调用；重复调用保留第一次的值，nil 退化为 zap.NewNop()。
func (c *Container) Init(logger *zap.Logger) {
	c.loggerMu.Lock()
	defer c.loggerMu.Unlock()
	if c.zapLogger != nil {
		return
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	c.zapLogger = logger
}

// init logger
// init logger 取 Init 记录的 logger；未初始化时返回 zap.NewNop()，调用方不必判空。
func (c *Container) logger() *zap.Logger {
	c.loggerMu.Lock()
	defer c.loggerMu.Unlock()
	if c.zapLogger == nil {
		return zap.NewNop()
	}
	return c.zapLogger
}
