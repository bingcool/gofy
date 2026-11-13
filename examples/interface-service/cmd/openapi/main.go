// Command openapi generates this project's OpenAPI documents.
// 命令 openapi 生成本项目的 OpenAPI 文档。
package main

import (
	"fmt"
	"os"

	"github.com/bingcool/gofy-interface-api/docgen"
	"github.com/yourorg/interface-service/appinterface"
)

func main() {
	err := docgen.Command(appinterface.Routes(), docgen.Config{
		Title:   "Interface Service API",
		Version: "1.0.0",
		Prefixes: map[string]string{
			"demoservice":         "/api/v1/order",
			"demoservice.product": "/api/v1/product",
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}
}
