// Package container 是 demo-service 的装配根。每一代装配（启动一次、Reload
// 一次）新建一个 Container，service 在首次取用时按依赖链惰性构造：每个服务
// 都有自己的构造槽，模块工厂只触发本模块子树，未挂载的能力不会被构建。
// 依赖只在构造期显式注入：跨模块能力以 innerapi 接口传入，模块内协作 service
// 由容器直接构造，不存在包级单例或运行期查找。
package container

import (
	"sync"

	"go.uber.org/zap"
)

// Container 持有本代仓库、service 构造槽与 logger。它不是包级全局：装配方每代
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
// 两者都新建容器，并把本代 ModuleFactories() 与 middlewareRegistrar 注册到
// 当次 App。因此每代各有一套仓库与构造槽，旧代实例只服务在途请求。
func NewContainer() *Container {
	return &Container{
		repos:    NewRepositoryDeclare(),
		services: NewServiceDeclare(),
	}
}

// Init 记录本代 Runtime 的 logger，供后续惰性构造使用。模块工厂在取用任何
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

func (c *Container) logger() *zap.Logger {
	c.loggerMu.Lock()
	defer c.loggerMu.Unlock()
	if c.zapLogger == nil {
		return zap.NewNop()
	}
	return c.zapLogger
}
