package image_test

import (
	"bytes"
	"errors"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/bingcool/gofy/support/image"
	"golang.org/x/image/font/gofont/goregular"
)

func encodePNG(t *testing.T, editor *image.Editor) []byte {
	t.Helper()
	result, err := editor.Encode(&image.EncodeOptions{Format: "png"})
	if err != nil {
		t.Fatalf("Encode png: %v", err)
	}
	return result.Binary
}

func decodePNGPixel(t *testing.T, data []byte, x, y int) color.NRGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

func TestWatermarkImage(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "logo.png"), 4, 4, color.NRGBA{R: 255, A: 255})

	manager, err := image.NewManager(image.Config{BasePath: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	editor, _ := manager.Create(20, 20, "#ffffff")
	if err := editor.WatermarkImage(image.ImageWatermark{
		Path:     "logo.png",
		Position: image.PositionBottomRight,
		OffsetX:  1,
		OffsetY:  1,
		Opacity:  1,
	}); err != nil {
		t.Fatalf("WatermarkImage: %v", err)
	}

	pixels := encodePNG(t, editor)
	if got := decodePNGPixel(t, pixels, 15, 15); got.R < 200 || got.G > 50 {
		t.Fatalf("watermark pixel = %v, want red", got)
	}
	if got := decodePNGPixel(t, pixels, 0, 0); got.R < 200 || got.G < 200 || got.B < 200 {
		t.Fatalf("background pixel = %v, want white", got)
	}
}

func TestWatermarkImageOpacity(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "logo.png"), 4, 4, color.NRGBA{R: 255, A: 255})
	manager, _ := image.NewManager(image.Config{BasePath: dir})
	editor, _ := manager.Create(10, 10, "#ffffff")
	if err := editor.WatermarkImage(image.ImageWatermark{
		Path: "logo.png", Position: image.PositionTopLeft, Opacity: 0.5,
	}); err != nil {
		t.Fatalf("WatermarkImage: %v", err)
	}
	got := decodePNGPixel(t, encodePNG(t, editor), 1, 1)
	if got.R < 240 || got.G < 115 || got.G > 140 {
		t.Fatalf("blended pixel = %v, want half-transparent red", got)
	}
}

func TestWatermarkPathSafety(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	writePNG(t, filepath.Join(other, "logo.png"), 2, 2, color.NRGBA{A: 255})
	writePNG(t, filepath.Join(root, "logo.png"), 2, 2, color.NRGBA{A: 255})

	manager, err := image.NewManager(image.Config{BasePath: root, AllowedRoots: []string{root}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	editor, _ := manager.Create(10, 10, "#fff")

	if err := editor.WatermarkImage(image.ImageWatermark{
		Path: "../logo.png", Opacity: 1,
	}); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("traversal = %v, want ErrInvalidImage", err)
	}
	if err := editor.WatermarkImage(image.ImageWatermark{
		Path: filepath.Join(other, "logo.png"), Opacity: 1,
	}); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("outside roots = %v, want ErrInvalidImage", err)
	}
	if err := editor.WatermarkImage(image.ImageWatermark{
		Path: "logo.png", Opacity: 1,
	}); err != nil {
		t.Fatalf("inside root: %v", err)
	}
}

func TestWatermarkText(t *testing.T) {
	dir := t.TempDir()
	fontPath := filepath.Join(dir, "font.ttf")
	if err := os.WriteFile(fontPath, goregular.TTF, 0o644); err != nil {
		t.Fatalf("write font: %v", err)
	}
	manager, err := image.NewManager(image.Config{BasePath: dir})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	editor, _ := manager.Create(320, 120, "#ffffff")
	if err := editor.WatermarkText(image.TextWatermark{
		Content:  "swoolefy",
		Font:     "font.ttf",
		Size:     32,
		Color:    "#000000",
		Opacity:  1,
		Position: image.PositionCenter,
	}); err != nil {
		t.Fatalf("WatermarkText: %v", err)
	}

	pixels := encodePNG(t, editor)
	changed := false
	for y := 20; y < 100 && !changed; y++ {
		for x := 20; x < 300; x++ {
			if got := decodePNGPixel(t, pixels, x, y); got.R < 128 {
				changed = true
				break
			}
		}
	}
	if !changed {
		t.Fatal("text watermark did not change any pixel")
	}
	if got := decodePNGPixel(t, pixels, 0, 0); got.R < 200 {
		t.Fatalf("corner pixel = %v, want white", got)
	}

	if err := editor.WatermarkText(image.TextWatermark{
		Content: "x", Font: "missing.ttf", Size: 12, Opacity: 1,
	}); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("missing font = %v, want ErrInvalidImage", err)
	}
}

func TestWatermarkTextRotated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "font.ttf"), goregular.TTF, 0o644); err != nil {
		t.Fatalf("write font: %v", err)
	}
	manager, _ := image.NewManager(image.Config{BasePath: dir})
	editor, _ := manager.Create(200, 200, "#ffffff")
	if err := editor.WatermarkText(image.TextWatermark{
		Content:  "sample",
		Font:     "font.ttf",
		Size:     24,
		Color:    "#333333",
		Opacity:  0.8,
		Position: image.PositionCenter,
		Angle:    45,
	}); err != nil {
		t.Fatalf("rotated WatermarkText: %v", err)
	}
}
