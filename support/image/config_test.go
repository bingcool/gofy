package image_test

import (
	"errors"
	"testing"

	"github.com/bingcool/gofy/support/image"
)

func TestNewManagerValidatesConfig(t *testing.T) {
	cases := map[string]struct {
		cfg  image.Config
		want error
	}{
		"bad max pixels": {cfg: image.Config{MaxPixels: -2}, want: image.ErrInvalidConfig},
		"bad quality":    {cfg: image.Config{Encode: image.EncodeOptions{Quality: 101}}, want: image.ErrInvalidConfig},
		"unsupported encode": {cfg: image.Config{Encode: image.EncodeOptions{Format: "webp"}},
			want: image.ErrUnsupportedFormat},
		"bad preset mode": {cfg: image.Config{Presets: map[string]image.Preset{
			"x": {Mode: "zoom", Width: 10},
		}}, want: image.ErrInvalidConfig},
		"cover one dim": {cfg: image.Config{Presets: map[string]image.Preset{
			"x": {Mode: image.ModeCover, Width: 10},
		}}, want: image.ErrInvalidConfig},
		"preset no dims": {cfg: image.Config{Presets: map[string]image.Preset{
			"x": {Mode: image.ModeContain},
		}}, want: image.ErrInvalidConfig},
		"preset bad encode": {cfg: image.Config{Presets: map[string]image.Preset{
			"x": {Mode: image.ModeContain, Width: 10, Encode: &image.EncodeOptions{Format: "bmp"}},
		}}, want: image.ErrUnsupportedFormat},
		"empty preset name": {cfg: image.Config{Presets: map[string]image.Preset{
			" ": {Mode: image.ModeContain, Width: 10},
		}}, want: image.ErrInvalidConfig},
		"watermark image": {cfg: image.Config{Watermark: image.WatermarkConfig{Image: image.ImageWatermark{
			Path: "logo.png", Opacity: 0,
		}}}, want: image.ErrInvalidConfig},
		"watermark text": {cfg: image.Config{Watermark: image.WatermarkConfig{Text: image.TextWatermark{
			Content: "x", Font: "font.ttf", Size: 0, Opacity: 1,
		}}}, want: image.ErrInvalidConfig},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := image.NewManager(tc.cfg); !errors.Is(err, tc.want) {
				t.Fatalf("NewManager = %v, want %v", err, tc.want)
			}
		})
	}

	cfg := image.Config{
		Presets: map[string]image.Preset{
			"thumb": {Mode: image.ModeCover, Width: 20, Height: 20},
		},
	}
	manager, err := image.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	copied := manager.Config()
	copied.Presets["thumb"] = image.Preset{Mode: image.ModeScale, Width: 1}
	if manager.Config().Presets["thumb"].Width != 20 {
		t.Fatal("Config() returned the internal preset map")
	}
}
