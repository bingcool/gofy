package image

import (
	stdimage "image"
	"image/color"
	"math"
	"strings"
)

// Manager is the stateless configuration entry point; call Open repeatedly to
// derive several sizes from the same source image.
// Manager 是配置入口，无状态；同一原图派生多档尺寸时重复 Open。
type Manager struct {
	cfg          Config
	maxPixels    int
	presets      map[string]Preset
	encode       EncodeOptions
	watermark    WatermarkConfig
	basePath     string
	allowedRoots []string
}

// NewManager validates the configuration and returns a Manager; when max_pixels
// is unset it defaults to 40 million.
// NewManager 校验配置并返回 Manager；未配置 max_pixels 时使用 4000 万。
func NewManager(cfg Config) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	maxPixels := cfg.MaxPixels
	if maxPixels == 0 {
		maxPixels = defaultMaxPixels
	}
	presets := make(map[string]Preset, len(cfg.Presets))
	for name, preset := range cfg.Presets {
		presets[name] = preset
	}
	return &Manager{
		cfg:          cfg,
		maxPixels:    maxPixels,
		presets:      presets,
		encode:       cfg.Encode,
		watermark:    cfg.Watermark,
		basePath:     cfg.BasePath,
		allowedRoots: append([]string(nil), cfg.AllowedRoots...),
	}, nil
}

// Config returns a copy of the configuration so callers can read the watermark
// and preset defaults.
// Config 返回配置副本，便于业务读取水印/预设默认值。
func (m *Manager) Config() Config {
	cfg := m.cfg
	cfg.Presets = make(map[string]Preset, len(m.presets))
	for name, preset := range m.presets {
		cfg.Presets[name] = preset
	}
	return cfg
}

// Open decodes a file path and returns an Editor. The pixel count is checked
// before the full decode.
// Open 解码文件路径并返回 Editor。像素数在完整解码前预检。
func (m *Manager) Open(path string) (*Editor, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, ErrInvalidImage
	}
	pixels, format, err := decodeFile(path, m.maxPixels)
	if err != nil {
		return nil, err
	}
	return m.newEditor(pixels, format), nil
}

// OpenBytes decodes an in-memory image.
// OpenBytes 解码内存中的二进制图片。
func (m *Manager) OpenBytes(data []byte) (*Editor, error) {
	pixels, format, err := decodeBytes(data, m.maxPixels)
	if err != nil {
		return nil, err
	}
	return m.newEditor(pixels, format), nil
}

// Create returns a blank canvas of the given size and background colour; an
// empty background yields transparency.
// Create 返回指定尺寸和背景色的空白画布；background 为空时透明。
func (m *Manager) Create(width, height int, background string) (*Editor, error) {
	if width <= 0 || height <= 0 {
		return nil, ErrInvalidImage
	}
	if pixelsExceeded(width, height, m.maxPixels) {
		return nil, ErrTooLarge
	}
	fill := color.NRGBA{}
	if strings.TrimSpace(background) != "" {
		parsed, err := parseHexColor(background)
		if err != nil {
			return nil, err
		}
		fill = parsed
	}
	canvas := stdimage.NewNRGBA(stdimage.Rect(0, 0, width, height))
	for i := 0; i < len(canvas.Pix); i += 4 {
		canvas.Pix[i] = fill.R
		canvas.Pix[i+1] = fill.G
		canvas.Pix[i+2] = fill.B
		canvas.Pix[i+3] = fill.A
	}
	return m.newEditor(canvas, ""), nil
}

func (m *Manager) newEditor(pixels *stdimage.NRGBA, sourceFormat string) *Editor {
	return &Editor{
		img:          pixels,
		sourceFormat: sourceFormat,
		maxPixels:    m.maxPixels,
		encode:       m.encode,
		presets:      m.presets,
		basePath:     m.basePath,
		allowedRoots: m.allowedRoots,
	}
}

// ImageSize is the current canvas size.
// ImageSize 是当前画布尺寸。
type ImageSize struct {
	Width  int
	Height int
}

// Editor is a request-scoped image editor and must not be shared across
// goroutines.
// Editor 是请求级的图像编辑器，禁止跨 goroutine 共享。
type Editor struct {
	img          *stdimage.NRGBA
	sourceFormat string
	maxPixels    int
	encode       EncodeOptions
	presetEncode *EncodeOptions
	presets      map[string]Preset
	basePath     string
	allowedRoots []string
}

// Size returns the current size.
// Size 返回当前尺寸。
func (e *Editor) Size() ImageSize {
	bounds := e.img.Bounds()
	return ImageSize{Width: bounds.Dx(), Height: bounds.Dy()}
}

// Resize scales by mode: contain only shrinks, cover fills then crops from the
// centre, scale may enlarge, stretch stretches both dimensions; 0 leaves that
// dimension unconstrained (except for cover).
// Resize 按 mode 缩放：contain 只缩不放，cover 铺满后居中裁剪，scale 允许
// 放大，stretch 双维拉伸；0 表示该维不限制（cover 除外）。
func (e *Editor) Resize(width, height int, mode Mode) error {
	srcW, srcH := e.img.Bounds().Dx(), e.img.Bounds().Dy()
	targetW, targetH, err := targetSize(srcW, srcH, width, height, mode)
	if err != nil {
		return err
	}
	if pixelsExceeded(targetW, targetH, e.maxPixels) {
		return ErrTooLarge
	}
	resized := e.img
	if targetW != srcW || targetH != srcH {
		resized = resize(e.img, targetW, targetH)
	}
	if Mode(strings.ToLower(strings.TrimSpace(string(mode)))) == ModeCover {
		offsetX := (targetW - width) / 2
		offsetY := (targetH - height) / 2
		resized = crop(resized, offsetX, offsetY, width, height)
	}
	e.img = resized
	return nil
}

// Thumbnail only shrinks, fitting proportionally inside width×height; both
// dimensions must be positive.
// Thumbnail 只缩小不放大，按比例放进 width×height；两维都必须大于 0。
func (e *Editor) Thumbnail(width, height int) error {
	if width <= 0 || height <= 0 {
		return ErrInvalidImage
	}
	srcW, srcH := e.img.Bounds().Dx(), e.img.Bounds().Dy()
	scale := math.Min(float64(width)/float64(srcW), float64(height)/float64(srcH))
	if scale >= 1 {
		return nil
	}
	targetW := maxInt(1, int(math.Round(float64(srcW)*scale)))
	targetH := maxInt(1, int(math.Round(float64(srcH)*scale)))
	e.img = resize(e.img, targetW, targetH)
	return nil
}

// Crop cuts out a rectangle; the coordinates and size must lie inside the
// current canvas.
// Crop 截取矩形；坐标与尺寸必须在当前画布内。
func (e *Editor) Crop(x, y, width, height int) error {
	bounds := e.img.Bounds()
	if x < 0 || y < 0 || width <= 0 || height <= 0 ||
		x+width > bounds.Dx() || y+height > bounds.Dy() {
		return ErrInvalidImage
	}
	if pixelsExceeded(width, height, e.maxPixels) {
		return ErrTooLarge
	}
	e.img = crop(e.img, x, y, width, height)
	return nil
}

// Rotate rotates by degrees, expanding the canvas to fit the rotated image;
// multiples of 90 are permuted exactly, other angles use bilinear
// interpolation.
// Rotate 按角度旋转（度），画布扩展到容纳旋转后的图像；90 的倍数精确重排，
// 其余角度双线性插值。
func (e *Editor) Rotate(degrees float64) error {
	if math.IsNaN(degrees) || math.IsInf(degrees, 0) {
		return ErrInvalidImage
	}
	angle := math.Mod(degrees, 360)
	if angle == 0 {
		return nil
	}
	srcW, srcH := e.img.Bounds().Dx(), e.img.Bounds().Dy()
	radians := math.Abs(angle * math.Pi / 180)
	cos, sin := math.Abs(math.Cos(radians)), math.Abs(math.Sin(radians))
	targetW := int(math.Ceil(float64(srcW)*cos + float64(srcH)*sin))
	targetH := int(math.Ceil(float64(srcW)*sin + float64(srcH)*cos))
	if pixelsExceeded(targetW, targetH, e.maxPixels) {
		return ErrTooLarge
	}
	e.img = rotate(e.img, degrees)
	return nil
}

// Greyscale converts to greyscale by luminance while preserving the alpha
// channel.
// Greyscale 按亮度转灰度，保留透明通道。
func (e *Editor) Greyscale() {
	greyscale(e.img)
}

// ApplyPreset applies the size template with the given configuration name; the
// preset's own encode becomes the default format of subsequent Encode calls. An
// unknown name returns ErrInvalidImage.
// ApplyPreset 按配置名应用尺寸模板；预设自带的 encode 会成为后续 Encode 的
// 默认格式。未知名称返回 ErrInvalidImage。
func (e *Editor) ApplyPreset(name string) error {
	preset, ok := e.presets[strings.TrimSpace(name)]
	if !ok {
		return ErrInvalidImage
	}
	if err := e.Resize(preset.Width, preset.Height, preset.Mode); err != nil {
		return err
	}
	if preset.Encode != nil {
		merge := *preset.Encode
		e.presetEncode = &merge
	}
	return nil
}
