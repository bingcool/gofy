// Package image provides image decoding, resizing, cropping, rotation,
// greyscale, watermarks and encoding, ported from the swoolefy PHP Image
// module (Intervention Image). It is implemented with the Go standard library
// and golang.org/x/image only: pure Go, no cgo and no external image
// extensions.
//
// Manager 是无状态的配置入口，Editor 持有一次编辑的位图；同一原图派生多档
// 尺寸时应多次 Open，而不是复用一个 Editor。
package image

import "errors"

var (
	// ErrInvalidImage reports a source that cannot be decoded or arguments
	// outside the image bounds.
	// ErrInvalidImage 表示来源无法解码或参数超出图像范围。
	ErrInvalidImage = errors.New("image: invalid image")
	// ErrTooLarge reports a pixel count or encoded size above the configured
	// limit.
	// ErrTooLarge 表示像素数或编码后体积超过配置上限。
	ErrTooLarge = errors.New("image: image too large")
	// ErrInvalidConfig reports an invalid driver, preset, watermark or encode
	// configuration.
	// ErrInvalidConfig 表示 driver/预设/水印/编码等配置不合法。
	ErrInvalidConfig = errors.New("image: invalid config")
	// ErrUnsupportedFormat reports an unsupported input or output format.
	// ErrUnsupportedFormat 表示输入或输出格式不受支持。
	ErrUnsupportedFormat = errors.New("image: unsupported format")
)
