package config

// OrderConfig is the order module's business configuration. It is passed to
// orderservice.NewOrderConfigService as a constructor argument, so the service
// sees this section and nothing else — never the whole application config.
// OrderConfig 是订单模块的业务配置。它作为构造参数传给
// orderservice.NewOrderConfigService，因此 service 只看到这一段，永远拿不到整份应用配置。
//
// Keeping it in a file of its own gives "one module, one config section" a fixed
// home: product, inventory and the rest each get their own xxx_config.go, and
// Config only maps section names instead of growing into one struct that holds
// everything.
// 单独成文件是为了让「一个模块一段配置」有固定的落点：再接入产品、库存等模块时，
// 各自建一份 xxx_config.go，Config 只负责把段名挂上去，文件不会长成一个什么都往里
// 塞的巨型配置结构。
type OrderConfig struct {
	AuthRequired bool `yaml:"auth_required" default:"false"`
}
