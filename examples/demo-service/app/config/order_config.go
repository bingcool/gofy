package config

// OrderConfig 是订单模块的业务配置。
//
// 它不传给 service：装配根 app/container 把它翻译成 orderservice.Settings 再注入，
// 因此订单模块只依赖自己用到的字段，不依赖整份应用配置；新增一个无关配置段不会牵动
// 业务包，service 也无从读到别的模块或基础设施的配置。
//
// 单独成文件是为了让「一个模块一段配置」有固定的落点：再接入产品、库存等模块时，
// 各自建一份 xxx_config.go，Config 只负责把段名挂上去，文件不会长成一个什么都往里
// 塞的巨型配置结构。
// OrderConfig is the order module's business configuration. It is translated by the
// assembly root into orderservice.Settings; the service never sees this type.
type OrderConfig struct {
	AuthRequired bool `yaml:"auth_required" default:"false"`
}
