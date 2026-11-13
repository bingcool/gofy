# Repository conventions

## Comments / 注释约定

Go 注释统一双语：英文段在前，中文段紧随其后，位于同一个注释块内，中间不插空 `//` 行。

```go
// ListProductsRequest pages through products. The list includes off-sale rows;
// callers filter on the on_sale field.
// ListProductsRequest 产品列表请求，包含已下架记录，调用方按 on_sale 字段过滤。
type ListProductsRequest struct {
	paging.PageRequest
}
```

规则：

1. 只给已存在的注释块补译文：英文块在其后补中文，纯中文块在**上方**补英文；不给原本没有注释的声明新增注释。
2. 英文原文逐行保留，不重写、不重排；中文按语义对应，术语（TTL、gRPC、OpenAPI、Registry、Reload、Lease）保留英文。
3. 不翻译、不改动：`//go:build`、`// +build`、`//line`、`//nolint`、以及其他工具指令；注释中的代码示例保持原样。
4. `// Deprecated:` 段落保持在最后且仅英文，中文段落放在它之前。
5. 行尾注释保持单行：以中文为主；原文是英文时在同一行用「；」连接中文，不拆成独立注释块。
6. 包文档：`// Package foo ...` 后接 `// 包 foo ...`。
7. 只改注释；不动代码、struct tag、字符串和 import。
8. 中文说明解释语义、约束和原因，不写"这里做了什么"的复述；每行尽量不超过 60 个汉字。
