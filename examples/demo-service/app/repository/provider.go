package apprepository

import (
	"sync"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/support/registry"
)

// Provider 保证一次装配只构建一份注册表。启动和每次 Reload 都会重新执行
// reloader.factories，得到新的 Provider，因此新旧两代 repository 不会共享状态。
//
// 构建是懒执行的：所有模块工厂拿到的是同一个 Provider，谁先调用都一样，
// 不依赖工厂顺序。
type Provider struct {
	once sync.Once
	reg  *registry.Registry
	err  error
}

// NewProvider 创建一份空的 Provider，不接触 Runtime。
func NewProvider() *Provider {
	return &Provider{}
}

// Repositories 在首次调用时构建注册表并缓存结果；构造失败的错误也会被缓存，
// 后续调用返回同一个错误。
func (p *Provider) Repositories(rt *gofy.Runtime) (*registry.Registry, error) {
	p.once.Do(func() {
		p.reg, p.err = Assemble(rt)
	})
	return p.reg, p.err
}
