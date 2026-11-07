package image

import (
	"fmt"
	"math"
	"strings"
)

// Mode 是缩放方式。
type Mode string

const (
	// ModeContain 等比缩进框内，只缩不放、不补白。
	ModeContain Mode = "contain"
	// ModeCover 等比铺满框并居中裁剪，宽高都必须大于 0。
	ModeCover Mode = "cover"
	// ModeScale 等比缩放（允许放大）；单维时按该维等比。
	ModeScale Mode = "scale"
	// ModeStretch 双维时强制拉伸；单维时退化为等比缩放。
	ModeStretch Mode = "stretch"
)

// Position 是水印锚点，对应九宫格。
type Position string

const (
	PositionTopLeft     Position = "top-left"
	PositionTop         Position = "top"
	PositionTopRight    Position = "top-right"
	PositionLeft        Position = "left"
	PositionCenter      Position = "center"
	PositionRight       Position = "right"
	PositionBottomLeft  Position = "bottom-left"
	PositionBottom      Position = "bottom"
	PositionBottomRight Position = "bottom-right"
)

func parseMode(value Mode) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(string(value)))) {
	case ModeContain:
		return ModeContain, nil
	case ModeCover:
		return ModeCover, nil
	case ModeScale:
		return ModeScale, nil
	case ModeStretch, "fill":
		return ModeStretch, nil
	case "":
		return "", fmt.Errorf("%w: resize mode must not be empty", ErrInvalidConfig)
	default:
		return "", fmt.Errorf("%w: unknown resize mode %q", ErrInvalidConfig, value)
	}
}

func parsePosition(value Position) (Position, error) {
	switch Position(strings.ToLower(strings.TrimSpace(string(value)))) {
	case "":
		return PositionBottomRight, nil
	case PositionTopLeft, PositionTop, PositionTopRight, PositionLeft,
		PositionCenter, PositionRight, PositionBottomLeft, PositionBottom, PositionBottomRight:
		return Position(value), nil
	default:
		return "", fmt.Errorf("%w: unknown watermark position %q", ErrInvalidConfig, value)
	}
}

// EncodeOptions 控制一次编码；零值字段继承上一层的配置。
type EncodeOptions struct {
	// Format 是输出格式：jpeg、png 或 gif；留空时按来源/配置协商。
	Format string `yaml:"format"`
	// Quality 是 jpeg 质量（1~100）；0 表示继承默认值 82。
	Quality int `yaml:"quality"`
	// MaxBytes 是编码后体积上限；0 表示不限制。
	MaxBytes int64 `yaml:"max_bytes"`
}

func (o EncodeOptions) validate() error {
	if o.Quality != 0 && (o.Quality < 1 || o.Quality > 100) {
		return fmt.Errorf("%w: quality must be between 1 and 100 (%d given)", ErrInvalidConfig, o.Quality)
	}
	if o.MaxBytes < 0 {
		return fmt.Errorf("%w: max bytes must not be negative", ErrInvalidConfig)
	}
	if format := normalizeFormat(o.Format); format != "" && !supportedEncodeFormat(format) {
		return fmt.Errorf("%w: cannot encode %q (supported: jpeg, png, gif)", ErrUnsupportedFormat, o.Format)
	}
	return nil
}

// Preset 是按名字保存的尺寸模板。
type Preset struct {
	Mode   Mode           `yaml:"mode"`
	Width  int            `yaml:"width"`
	Height int            `yaml:"height"`
	Encode *EncodeOptions `yaml:"encode"`
}

func (p Preset) validate(name string) error {
	mode, err := parseMode(p.Mode)
	if err != nil {
		return fmt.Errorf("preset %q: %w", name, err)
	}
	if p.Width < 0 || p.Height < 0 {
		return fmt.Errorf("%w: preset %q dimensions must not be negative", ErrInvalidConfig, name)
	}
	switch mode {
	case ModeCover:
		if p.Width <= 0 || p.Height <= 0 {
			return fmt.Errorf("%w: preset %q with cover requires both dimensions", ErrInvalidConfig, name)
		}
	default:
		if p.Width <= 0 && p.Height <= 0 {
			return fmt.Errorf("%w: preset %q needs at least one dimension", ErrInvalidConfig, name)
		}
	}
	if p.Encode != nil {
		if err := p.Encode.validate(); err != nil {
			return fmt.Errorf("preset %q: %w", name, err)
		}
	}
	return nil
}

// ImageWatermark 是 Logo 水印配置。
type ImageWatermark struct {
	// Path 是 Logo 文件路径；相对路径以 Config.BasePath 为基准。
	Path string `yaml:"path"`
	// Position 是九宫格锚点，默认 bottom-right。
	Position Position `yaml:"position"`
	// OffsetX/OffsetY 是相对锚点的像素边距。
	OffsetX int `yaml:"offset_x"`
	OffsetY int `yaml:"offset_y"`
	// Opacity 是可见度（0,1]，越大越不透明。
	Opacity float64 `yaml:"opacity"`
}

func (w ImageWatermark) validate() error {
	if strings.TrimSpace(w.Path) == "" {
		return fmt.Errorf("%w: watermark image path must not be empty", ErrInvalidConfig)
	}
	if _, err := parsePosition(w.Position); err != nil {
		return err
	}
	if w.Opacity <= 0 || w.Opacity > 1 {
		return fmt.Errorf("%w: watermark image opacity must be in (0,1] (%v given)", ErrInvalidConfig, w.Opacity)
	}
	if w.OffsetX < 0 || w.OffsetY < 0 {
		return fmt.Errorf("%w: watermark offsets must not be negative", ErrInvalidConfig)
	}
	return nil
}

// TextWatermark 是文字水印配置。
type TextWatermark struct {
	// Content 是水印文字，不能为空。
	Content string `yaml:"content"`
	// Font 是 TTF/OTF 字体路径；相对路径以 Config.BasePath 为基准。
	Font string `yaml:"font"`
	// Size 是字号（像素）。
	Size float64 `yaml:"size"`
	// Color 是 #rgb 或 #rrggbb 颜色。
	Color string `yaml:"color"`
	// Opacity 是可见度（0,1]。
	Opacity float64 `yaml:"opacity"`
	// Position 是九宫格锚点，默认 bottom-right。
	Position Position `yaml:"position"`
	OffsetX  int
	OffsetY  int
	// Angle 是文字旋转角度（度），与 Rotate 方向一致：90 为顺时针。
	Angle float64 `yaml:"angle"`
}

func (w TextWatermark) validate() error {
	if strings.TrimSpace(w.Content) == "" {
		return fmt.Errorf("%w: watermark text content must not be empty", ErrInvalidConfig)
	}
	if strings.TrimSpace(w.Font) == "" {
		return fmt.Errorf("%w: watermark text font must not be empty", ErrInvalidConfig)
	}
	if w.Size <= 0 || math.IsNaN(w.Size) || math.IsInf(w.Size, 0) {
		return fmt.Errorf("%w: watermark text size must be positive", ErrInvalidConfig)
	}
	if _, err := parseHexColor(w.Color); err != nil {
		return err
	}
	if w.Opacity <= 0 || w.Opacity > 1 {
		return fmt.Errorf("%w: watermark text opacity must be in (0,1] (%v given)", ErrInvalidConfig, w.Opacity)
	}
	if _, err := parsePosition(w.Position); err != nil {
		return err
	}
	if w.OffsetX < 0 || w.OffsetY < 0 {
		return fmt.Errorf("%w: watermark offsets must not be negative", ErrInvalidConfig)
	}
	if math.IsNaN(w.Angle) || math.IsInf(w.Angle, 0) {
		return fmt.Errorf("%w: watermark text angle must be finite", ErrInvalidConfig)
	}
	return nil
}

// WatermarkConfig 汇总图片与文字水印的默认配置。
type WatermarkConfig struct {
	Image ImageWatermark `yaml:"image"`
	Text  TextWatermark  `yaml:"text"`
}

// Config 是图像组件的运行配置。
type Config struct {
	// BasePath 是相对路径基准目录；为空时相对路径按当前进程目录解析。
	BasePath string `yaml:"base_path"`
	// MaxPixels 是单张图允许的最大像素数；0 使用默认 4000 万，-1 表示不限制。
	MaxPixels int `yaml:"max_pixels"`
	// Encode 是编码默认值。
	Encode EncodeOptions `yaml:"encode"`
	// Watermark 是水印默认配置，不会自动应用。
	Watermark WatermarkConfig `yaml:"watermark"`
	// Presets 是按名字保存的尺寸模板。
	Presets map[string]Preset `yaml:"presets"`
	// AllowedRoots 限制水印 Logo 与字体可读取的根目录；为空时只限制在 BasePath 内。
	AllowedRoots []string `yaml:"allowed_roots"`
}

// Validate 校验配置取值；不会读取水印文件。
func (c Config) Validate() error {
	if c.MaxPixels < -1 {
		return fmt.Errorf("%w: max pixels must be -1, 0 or positive (%d given)", ErrInvalidConfig, c.MaxPixels)
	}
	if err := c.Encode.validate(); err != nil {
		return err
	}
	if c.Watermark.Image.Path != "" {
		if err := c.Watermark.Image.validate(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Watermark.Text.Content) != "" || strings.TrimSpace(c.Watermark.Text.Font) != "" {
		if err := c.Watermark.Text.validate(); err != nil {
			return err
		}
	}
	for name, preset := range c.Presets {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: preset name must not be empty", ErrInvalidConfig)
		}
		if err := preset.validate(name); err != nil {
			return err
		}
	}
	return nil
}

// defaultMaxPixels 是未配置时的像素上限。
const defaultMaxPixels = 40_000_000

// defaultQuality 是未配置时的 jpeg 质量。
const defaultQuality = 82
