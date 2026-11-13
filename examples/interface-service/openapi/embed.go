// Package openapi embeds the generated specifications so services can mount
// them without reading files at runtime.
// 包 openapi 内嵌生成好的规范文档，服务无需在运行时读文件即可挂载。
package openapi

import _ "embed"

var (
	// AllYAML is the aggregated specification of every contract module.
	// AllYAML 是所有契约模块聚合后的规范文档，由 docgen 生成，请勿手工修改。
	//go:embed all.yaml
	AllYAML []byte

	// AllJSON is the JSON form of AllYAML.
	// AllJSON 是 AllYAML 的 JSON 形式，同样由 docgen 生成。
	//go:embed all.json
	AllJSON []byte

	// DemoServiceYAML is the demo service specification with every deployment
	// prefix (order /api/v1/order, product /api/v1/product).
	// DemoServiceYAML 是 demo 服务的规范文档，覆盖全部部署前缀（订单
	// /api/v1/order、产品 /api/v1/product）。
	//go:embed demoservice.yaml
	DemoServiceYAML []byte

	// DemoServiceJSON is the JSON form of DemoServiceYAML.
	// DemoServiceJSON 是 DemoServiceYAML 的 JSON 形式。
	//go:embed demoservice.json
	DemoServiceJSON []byte
)
