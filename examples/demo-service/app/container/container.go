// Package container 是 demo-service 的装配根。每一代装配（启动一次、Reload
// 一次）新建一个 Container，service 在首次取用时按依赖链惰性构造：每个服务
// 都有自己的构造槽，模块工厂只触发本模块子树，未挂载的能力不会被构建。
// 依赖只在构造期显式注入：跨模块能力以 innerapi 接口传入，模块内协作 service
// 由容器直接构造，不存在包级单例或运行期查找。
package container

import (
	"sync"

	gofyadapter "github.com/bingcool/gofy-adapter/adapter/gofy"

	"github.com/bingcool/gofy/support/sqlmodel"
	"github.com/yourorg/demo-service/app/config"
	appmiddleware "github.com/yourorg/demo-service/app/middleware"
	"go.uber.org/zap"
)

// Container 持有本次服务的仓库、service 构造槽、业务配置与 logger。它不是包级全局：
// 装配方每代新建一个实例，模块工厂闭包捕获它，因此热更新代际隔离、测试之间也互不影响。
//
// 下游客户端不在这里：它由 main 建一次、按进程存活，交给真正的消费者（worker 与
// 未来的 service）。把它塞进每代新建的容器，会让人误以为它跟着 Reload 重建，而
// resolver 的缓存与轮询游标恰恰不能重建。
type Container struct {
	cfg      *config.Config
	repos    RepositoryDeclare
	services ServiceDeclare
	models   lazyValue[*sqlmodel.Resolver]

	loggerMu  sync.Mutex
	zapLogger *zap.Logger
}

// NewContainer 创建一个尚未构建任何 service 的容器。cfg 是本次装配加载的配置，
// 业务配置由访问器按段交给 service 作为构造参数；传 nil 表示只验证接线，此时各业务
// 配置段按零值处理（生产路径 reload.factories 一定传入完整配置）。
//
// 启动和热更新各走一次装配：启动时 appOptions 调 reload.factories(cfg)，热更新时
// app.Reload 再用 reloader.factories(cfg)；两者都新建容器并注册本次的
// ModuleFactories 与中间件注册表。因此每代各有一套仓库、构造槽与业务配置，旧代实例
// 只服务在途请求。
func NewContainer(cfg *config.Config) *Container {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &Container{
		cfg:      cfg,
		repos:    NewRepositoryDeclare(),
		services: NewServiceDeclare(),
		models:   newLazyValue[*sqlmodel.Resolver]("sqlmodel resolver"),
	}
}

// orderConfig 返回本次装配的订单配置。
//
// 容器是唯一持有整份 config.Config 的地方；各模块只经这类访问器拿到自己那一段，
// 因此业务包看不到 db、kafka、nacos 这些段，也无法在请求期重新读文件。返回的是值
// 拷贝，调用方改它不会影响本代快照。
func (c *Container) orderConfig() config.OrderConfig {
	if c == nil || c.cfg == nil {
		return config.OrderConfig{}
	}
	return c.cfg.Order
}

// tenantConfig 返回本次装配的租户配置，整段缺失时为 nil——nil 表示这套部署不用租户隔离，
// TenantConfig 的方法在 nil 接收者上返回"关闭"。与其它访问器一样，它只交出本代的一段配置，
// 业务包看不到整份 config.Config。
func (c *Container) tenantConfig() *config.TenantConfig {
	if c == nil || c.cfg == nil {
		return nil
	}
	return c.cfg.Tenant
}

// SQLModel 返回本次装配的构造器解析器：表名与租户条件都从这里来，供 query 层注入。
//
// 它每代只构造一次——解析器缓存 model 的 schema，而租户开关在构造时读一次，与插件在
// Open 时安装一次同理。选项由 tenantConfig().SQLModelOptions() 给出，因此「插件关了、
// 构造器还开着」这种半开状态不会出现。
func (c *Container) SQLModel() *sqlmodel.Resolver {
	return c.models.Get(func() *sqlmodel.Resolver {
		return sqlmodel.New(c.tenantConfig().SQLModelOptions())
	})
}

// BindOptions 返回契约路由的绑定选项，与具体模块无关，所有模块共用。
//
// 限流中间件按每条路由声明的 Spec.RateLimit 动态生成：哪些路由要限流、
// 限多少、按什么主体计数、用哪个 Redis 实例（RedisAlias）全部写在契约里。
// 这里只交出回调，不持有 Store 或连接：adapter 在绑定路由时逐条调用回调，
// 中间件在首次请求时按别名自行解析连接（路由绑定早于 app/appio 模块打开
// Redis）。每个模块工厂各调用一次本方法，得到的都是同一个无状态回调。
// adapter 调用回调时同时注入 Router 的 Responder，限流的 429/503 与其它
// 错误共用同一信封。
//
// 认证不在这里：契约没有认证字段，需要登录的路由由分组或路由上的 "auth"
// 中间件决定，公开的路由就不装。装配根因此不提供任何认证策略对象。
func (c *Container) BindOptions() gofyadapter.BindOptions {
	return gofyadapter.BindOptions{
		RateLimit: appmiddleware.RateLimiter{}.Callback(),
	}
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
