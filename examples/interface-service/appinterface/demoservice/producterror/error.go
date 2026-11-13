// Package producterror declares the product contract errors. It is a leaf
// package so both the route descriptors and the demoservice facade can share
// the codes without an import cycle.
// 包 producterror 声明产品契约错误。它是叶子包，路由描述符与 demoservice
// 门面都能共享错误码而不产生 import 环。
package producterror

import "github.com/bingcool/gofy-interface-api/contracts/common/errcode"

// Product error codes. They live in the 60000-60999 product band.
// 产品错误码，占用 60000-60999 的产品错误码段。
const (
	ErrCodeProductNotFound errcode.Code = 60001
)

var (
	// ErrProductNotFound is returned when a product does not exist.
	// ErrProductNotFound 产品不存在，错误码 60001，HTTP 404。
	ErrProductNotFound = errcode.New(ErrCodeProductNotFound, 404, "product not found")
)
