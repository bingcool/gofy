package image

import (
	"bytes"
	"errors"
	"fmt"
	stdimage "image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"

	_ "golang.org/x/image/webp" // 注册 WebP 解码
	_ "image/gif"               // 注册 GIF 解码
	_ "image/jpeg"              // 注册 JPEG 解码
	_ "image/png"               // 注册 PNG 解码
)

// encodeFormats 是当前纯 Go 实现可以导出的格式。
var encodeFormats = map[string]struct{}{
	"jpeg": {},
	"png":  {},
	"gif":  {},
}

// formatInfo 是格式对应的 MIME 与扩展名。
type formatInfo struct {
	mime      string
	extension string
}

var formatInfos = map[string]formatInfo{
	"jpeg": {mime: "image/jpeg", extension: ".jpg"},
	"png":  {mime: "image/png", extension: ".png"},
	"gif":  {mime: "image/gif", extension: ".gif"},
}

// normalizeFormat 统一格式名，jpg 归一到 jpeg。
func normalizeFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "jpg" {
		return "jpeg"
	}
	return format
}

func supportedEncodeFormat(format string) bool {
	_, ok := encodeFormats[format]
	return ok
}

// pixelsExceeded 用 int64 计算像素数，避免宽高相乘溢出。
func pixelsExceeded(width, height, maxPixels int) bool {
	if maxPixels <= 0 {
		return false
	}
	return int64(width)*int64(height) > int64(maxPixels)
}

// newNRGBA 把任意图像复制成 0 基的 NRGBA，保留透明通道。
func newNRGBA(src stdimage.Image) *stdimage.NRGBA {
	bounds := src.Bounds()
	dst := stdimage.NewNRGBA(stdimage.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	xdraw.Draw(dst, dst.Bounds(), src, bounds.Min, xdraw.Src)
	return dst
}

// decodeError 把标准库的格式错误映射到哨兵错误。
func decodeError(err error) error {
	if errors.Is(err, stdimage.ErrFormat) {
		return fmt.Errorf("%w: %v", ErrUnsupportedFormat, err)
	}
	return fmt.Errorf("%w: %v", ErrInvalidImage, err)
}

// decodeBytes 解码内存中的图片。
func decodeBytes(data []byte, maxPixels int) (*stdimage.NRGBA, string, error) {
	if len(data) == 0 {
		return nil, "", fmt.Errorf("%w: image data is empty", ErrInvalidImage)
	}
	cfg, format, err := stdimage.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", decodeError(err)
	}
	if pixelsExceeded(cfg.Width, cfg.Height, maxPixels) {
		return nil, "", fmt.Errorf("%w: %dx%d exceeds %d pixels", ErrTooLarge, cfg.Width, cfg.Height, maxPixels)
	}
	src, _, err := stdimage.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", decodeError(err)
	}
	bounds := src.Bounds()
	if pixelsExceeded(bounds.Dx(), bounds.Dy(), maxPixels) {
		return nil, "", fmt.Errorf("%w: %dx%d exceeds %d pixels", ErrTooLarge, bounds.Dx(), bounds.Dy(), maxPixels)
	}
	return newNRGBA(src), normalizeFormat(format), nil
}

// decodeFile 打开文件解码；先 DecodeConfig 预检，避免超大图直接进内存。
func decodeFile(path string, maxPixels int) (*stdimage.NRGBA, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("%w: open %q: %v", ErrInvalidImage, path, err)
	}
	cfg, format, err := stdimage.DecodeConfig(file)
	_ = file.Close()
	if err != nil {
		return nil, "", decodeError(err)
	}
	if pixelsExceeded(cfg.Width, cfg.Height, maxPixels) {
		return nil, "", fmt.Errorf("%w: %q is %dx%d, above %d pixels", ErrTooLarge, path, cfg.Width, cfg.Height, maxPixels)
	}

	file, err = os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("%w: open %q: %v", ErrInvalidImage, path, err)
	}
	defer func() { _ = file.Close() }()
	src, _, err := stdimage.Decode(file)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %q: %v", ErrInvalidImage, path, err)
	}
	bounds := src.Bounds()
	if pixelsExceeded(bounds.Dx(), bounds.Dy(), maxPixels) {
		return nil, "", fmt.Errorf("%w: %q is %dx%d, above %d pixels", ErrTooLarge, path, bounds.Dx(), bounds.Dy(), maxPixels)
	}
	return newNRGBA(src), normalizeFormat(format), nil
}

// targetSize 根据缩放模式计算目标尺寸；0 表示该维不限制。
func targetSize(srcW, srcH, width, height int, mode Mode) (int, int, error) {
	if srcW <= 0 || srcH <= 0 {
		return 0, 0, fmt.Errorf("%w: source size is empty", ErrInvalidImage)
	}
	if width < 0 || height < 0 {
		return 0, 0, fmt.Errorf("%w: target dimensions must not be negative", ErrInvalidImage)
	}
	mode, err := parseMode(mode)
	if err != nil {
		return 0, 0, err
	}
	if width == 0 && height == 0 {
		return 0, 0, fmt.Errorf("%w: resize needs at least one dimension", ErrInvalidImage)
	}

	switch mode {
	case ModeCover:
		if width <= 0 || height <= 0 {
			return 0, 0, fmt.Errorf("%w: cover requires both dimensions", ErrInvalidImage)
		}
		scale := math.Max(float64(width)/float64(srcW), float64(height)/float64(srcH))
		return maxInt(1, int(math.Round(float64(srcW)*scale))), maxInt(1, int(math.Round(float64(srcH)*scale))), nil
	case ModeStretch:
		if width > 0 && height > 0 {
			return width, height, nil
		}
		return scaledSize(srcW, srcH, width, height)
	case ModeScale:
		return scaledSize(srcW, srcH, width, height)
	default: // contain：只缩不放
		tw, th, err := scaledSize(srcW, srcH, width, height)
		if err != nil {
			return 0, 0, err
		}
		if tw > srcW || th > srcH {
			return srcW, srcH, nil
		}
		return tw, th, nil
	}
}

// scaledSize 按给定维度等比计算，允许放大；单维时按该维。
func scaledSize(srcW, srcH, width, height int) (int, int, error) {
	scale := 1.0
	switch {
	case width > 0 && height > 0:
		scale = math.Min(float64(width)/float64(srcW), float64(height)/float64(srcH))
	case width > 0:
		scale = float64(width) / float64(srcW)
	case height > 0:
		scale = float64(height) / float64(srcH)
	default:
		return 0, 0, fmt.Errorf("%w: resize needs at least one dimension", ErrInvalidImage)
	}
	return maxInt(1, int(math.Round(float64(srcW)*scale))), maxInt(1, int(math.Round(float64(srcH)*scale))), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// resize 用 CatmullRom 重采样。
func resize(src *stdimage.NRGBA, width, height int) *stdimage.NRGBA {
	dst := stdimage.NewNRGBA(stdimage.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

// crop 截取矩形区域；调用方已校验边界。
func crop(src *stdimage.NRGBA, x, y, width, height int) *stdimage.NRGBA {
	dst := stdimage.NewNRGBA(stdimage.Rect(0, 0, width, height))
	xdraw.Draw(dst, dst.Bounds(), src, stdimage.Pt(x, y), xdraw.Src)
	return dst
}

// rotate 旋转图像：90 的倍数走精确重排，其余角度用双线性插值并扩展画布。
func rotate(src *stdimage.NRGBA, degrees float64) *stdimage.NRGBA {
	angle := math.Mod(degrees, 360)
	if angle < 0 {
		angle += 360
	}
	switch angle {
	case 0:
		return src
	case 90:
		return rotate90(src)
	case 180:
		return rotate90(rotate90(src))
	case 270:
		return rotate90(rotate90(rotate90(src)))
	}

	radians := angle * math.Pi / 180
	cos, sin := math.Cos(radians), math.Sin(radians)
	srcW, srcH := src.Bounds().Dx(), src.Bounds().Dy()
	dstW := int(math.Ceil(math.Abs(float64(srcW)*cos) + math.Abs(float64(srcH)*sin)))
	dstH := int(math.Ceil(math.Abs(float64(srcW)*sin) + math.Abs(float64(srcH)*cos)))
	dst := stdimage.NewNRGBA(stdimage.Rect(0, 0, dstW, dstH))

	cxSrc, cySrc := float64(srcW)/2, float64(srcH)/2
	cxDst, cyDst := float64(dstW)/2, float64(dstH)/2
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			dx, dy := float64(x)-cxDst, float64(y)-cyDst
			sx := cos*dx + sin*dy + cxSrc
			sy := -sin*dx + cos*dy + cySrc
			if sx < -1 || sy < -1 || sx > float64(srcW) || sy > float64(srcH) {
				continue
			}
			dst.SetNRGBA(x, y, bilinear(src, sx, sy))
		}
	}
	return dst
}

// rotate90 顺时针旋转 90 度。
func rotate90(src *stdimage.NRGBA) *stdimage.NRGBA {
	srcW, srcH := src.Bounds().Dx(), src.Bounds().Dy()
	dst := stdimage.NewNRGBA(stdimage.Rect(0, 0, srcH, srcW))
	for y := 0; y < srcH; y++ {
		for x := 0; x < srcW; x++ {
			dst.SetNRGBA(srcH-1-y, x, src.NRGBAAt(x, y))
		}
	}
	return dst
}

// bilinear 在源图上做双线性采样，越界像素按透明处理。
func bilinear(src *stdimage.NRGBA, x, y float64) color.NRGBA {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	x1, y1 := x0+1, y0+1
	fx, fy := x-float64(x0), y-float64(y0)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()

	at := func(px, py int) color.NRGBA {
		if px < 0 || py < 0 || px >= w || py >= h {
			return color.NRGBA{}
		}
		return src.NRGBAAt(px, py)
	}
	c00, c10, c01, c11 := at(x0, y0), at(x1, y0), at(x0, y1), at(x1, y1)
	blend := func(a, b uint8) float64 {
		return float64(a)*(1-fx) + float64(b)*fx
	}
	topR := blend(c00.R, c10.R)
	topG := blend(c00.G, c10.G)
	topB := blend(c00.B, c10.B)
	topA := blend(c00.A, c10.A)
	botR := blend(c01.R, c11.R)
	botG := blend(c01.G, c11.G)
	botB := blend(c01.B, c11.B)
	botA := blend(c01.A, c11.A)
	interp := func(top, bottom float64) uint8 {
		return uint8(math.Round(top*(1-fy) + bottom*fy))
	}
	return color.NRGBA{
		R: interp(topR, botR),
		G: interp(topG, botG),
		B: interp(topB, botB),
		A: interp(topA, botA),
	}
}

// greyscale 按亮度转灰度，保留 alpha。
func greyscale(src *stdimage.NRGBA) {
	bounds := src.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			c := src.NRGBAAt(x, y)
			lum := uint8(math.Round(0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)))
			src.SetNRGBA(x, y, color.NRGBA{R: lum, G: lum, B: lum, A: c.A})
		}
	}
}

// parseHexColor 支持 #rgb 与 #rrggbb，空值默认为白色。
func parseHexColor(value string) (color.NRGBA, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}, nil
	}
	value = strings.TrimPrefix(value, "#")
	switch len(value) {
	case 3:
		r, okR := hexNibble(value[0])
		g, okG := hexNibble(value[1])
		b, okB := hexNibble(value[2])
		if !okR || !okG || !okB {
			return color.NRGBA{}, fmt.Errorf("%w: color %q must be #rgb or #rrggbb", ErrInvalidConfig, value)
		}
		return color.NRGBA{R: r * 17, G: g * 17, B: b * 17, A: 255}, nil
	case 6:
		var r, g, b uint8
		if _, err := fmt.Sscanf(value, "%02x%02x%02x", &r, &g, &b); err != nil {
			return color.NRGBA{}, fmt.Errorf("%w: color %q must be #rgb or #rrggbb", ErrInvalidConfig, value)
		}
		return color.NRGBA{R: r, G: g, B: b, A: 255}, nil
	default:
		return color.NRGBA{}, fmt.Errorf("%w: color %q must be #rgb or #rrggbb", ErrInvalidConfig, value)
	}
}

func hexNibble(b byte) (uint8, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	default:
		return 0, false
	}
}

// resolveResourcePath 解析水印 Logo/字体路径：拒绝 .. 段、要求文件存在，
// 并限制在 allowedRoots（未配置时限制在 BasePath）内。
func resolveResourcePath(basePath, value string, allowedRoots []string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: path must not be empty", ErrInvalidImage)
	}
	for _, segment := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return "", fmt.Errorf("%w: path %q must not contain .. segments", ErrInvalidImage, value)
		}
	}
	if !filepath.IsAbs(value) && basePath != "" {
		value = filepath.Join(basePath, value)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%w: resolve %q: %v", ErrInvalidImage, value, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: file %q does not exist", ErrInvalidImage, value)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", ErrInvalidImage, value)
	}

	roots := allowedRoots
	if len(roots) == 0 && basePath != "" {
		roots = []string{basePath}
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if !filepath.IsAbs(root) && basePath != "" {
			root = filepath.Join(basePath, root)
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if resolvedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
			rootAbs = resolvedRoot
		}
		rel, err := filepath.Rel(rootAbs, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	if len(roots) > 0 {
		return "", fmt.Errorf("%w: path %q is outside the allowed roots", ErrInvalidImage, value)
	}
	return resolved, nil
}

// placeOffset 按九宫格锚点计算左上角坐标，offset 从锚点向内偏移。
func placeOffset(dstW, dstH, srcW, srcH int, position Position, offsetX, offsetY int) (int, int) {
	var x, y int
	switch position {
	case PositionTopLeft, PositionLeft, PositionBottomLeft:
		x = offsetX
	case PositionTop, PositionCenter, PositionBottom:
		x = (dstW-srcW)/2 + offsetX
	case PositionTopRight, PositionRight, PositionBottomRight:
		x = dstW - srcW - offsetX
	}
	switch position {
	case PositionTopLeft, PositionTop, PositionTopRight:
		y = offsetY
	case PositionLeft, PositionCenter, PositionRight:
		y = (dstH-srcH)/2 + offsetY
	case PositionBottomLeft, PositionBottom, PositionBottomRight:
		y = dstH - srcH - offsetY
	}
	return x, y
}
