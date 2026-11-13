package image

import (
	stdimage "image"
	stddraw "image/draw"
	"math"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// WatermarkImage overlays a logo on the canvas; the path is confined to
// BasePath/AllowedRoots.
// WatermarkImage 在画布上叠加 Logo；路径受 BasePath/AllowedRoots 限制。
func (e *Editor) WatermarkImage(wm ImageWatermark) error {
	if err := wm.validate(); err != nil {
		return err
	}
	path, err := resolveResourcePath(e.basePath, wm.Path, e.allowedRoots)
	if err != nil {
		return err
	}
	logo, _, err := decodeFile(path, e.maxPixels)
	if err != nil {
		return err
	}
	position, err := parsePosition(wm.Position)
	if err != nil {
		return err
	}
	e.composite(logo, position, wm.OffsetX, wm.OffsetY, wm.Opacity)
	return nil
}

// WatermarkText renders a TTF/OTF text watermark; the angle follows the Rotate
// direction.
// WatermarkText 渲染 TTF/OTF 文字水印；角度与 Rotate 方向一致。
func (e *Editor) WatermarkText(wm TextWatermark) error {
	if err := wm.validate(); err != nil {
		return err
	}
	fontPath, err := resolveResourcePath(e.basePath, wm.Font, e.allowedRoots)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(fontPath)
	if err != nil {
		return ErrInvalidImage
	}
	parsed, err := opentype.Parse(data)
	if err != nil {
		return ErrInvalidImage
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{
		Size:    wm.Size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return ErrInvalidImage
	}
	defer func() { _ = face.Close() }()

	text := strings.TrimSpace(wm.Content)
	textWidth := font.MeasureString(face, text).Ceil()
	if textWidth <= 0 {
		return ErrInvalidImage
	}
	metrics := face.Metrics()
	ascent, descent := metrics.Ascent.Ceil(), metrics.Descent.Ceil()
	layer := stdimage.NewNRGBA(stdimage.Rect(0, 0, textWidth+2, ascent+descent+2))

	textColor, err := parseHexColor(wm.Color)
	if err != nil {
		return err
	}
	drawer := &font.Drawer{
		Dst:  layer,
		Src:  stdimage.NewUniform(textColor),
		Face: face,
		Dot:  fixed.P(1, 1+ascent),
	}
	drawer.DrawString(text)

	if wm.Angle = math.Mod(wm.Angle, 360); wm.Angle != 0 {
		layer = rotate(layer, wm.Angle)
	}
	position, err := parsePosition(wm.Position)
	if err != nil {
		return err
	}
	e.composite(layer, position, wm.OffsetX, wm.OffsetY, wm.Opacity)
	return nil
}

// composite overlays the layer onto the canvas at the anchor; opacity is
// implemented with a uniform alpha mask.
// composite 按锚点把图层叠加到画布；opacity 通过统一 alpha 蒙版实现。
func (e *Editor) composite(layer *stdimage.NRGBA, position Position, offsetX, offsetY int, opacity float64) {
	dstW, dstH := e.img.Bounds().Dx(), e.img.Bounds().Dy()
	layerW, layerH := layer.Bounds().Dx(), layer.Bounds().Dy()
	x, y := placeOffset(dstW, dstH, layerW, layerH, position, offsetX, offsetY)

	rect := stdimage.Rect(x, y, x+layerW, y+layerH)
	mask := stdimage.NewAlpha(rect)
	alpha := uint8(math.Round(opacity * 255))
	for i := range mask.Pix {
		mask.Pix[i] = alpha
	}
	stddraw.DrawMask(e.img, rect, layer, stdimage.Point{}, mask, rect.Min, stddraw.Over)
}
