package image_test

import (
	"bytes"
	"errors"
	stdimage "image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/bingcool/gofy/support/image"
)

// writePNG 生成指定尺寸与颜色的 PNG 文件，返回路径。
func writePNG(t *testing.T, path string, width, height int, fill color.NRGBA) {
	t.Helper()
	canvas := stdimage.NewNRGBA(stdimage.Rect(0, 0, width, height))
	for i := 0; i < len(canvas.Pix); i += 4 {
		canvas.Pix[i] = fill.R
		canvas.Pix[i+1] = fill.G
		canvas.Pix[i+2] = fill.B
		canvas.Pix[i+3] = fill.A
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer func() { _ = file.Close() }()
	if err := png.Encode(file, canvas); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}

func TestCreateAndEncode(t *testing.T) {
	manager, err := image.NewManager(image.Config{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	editor, err := manager.Create(8, 6, "#336699")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if size := editor.Size(); size.Width != 8 || size.Height != 6 {
		t.Fatalf("size = %+v", size)
	}
	result, err := editor.Encode(&image.EncodeOptions{Format: "png"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if result.MIME != "image/png" || result.Extension != ".png" || result.Size <= 0 {
		t.Fatalf("result = %+v", result)
	}
	decoded, err := png.Decode(bytes.NewReader(result.Binary))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := color.NRGBAModel.Convert(decoded.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 0x33, G: 0x66, B: 0x99, A: 0xff}) {
		t.Fatalf("pixel = %v", got)
	}

	base64Text, err := editor.Base64(&image.EncodeOptions{Format: "png"})
	if err != nil || base64Text == "" {
		t.Fatalf("Base64 = %q (err %v)", base64Text, err)
	}
}

func TestOpenAndResizeModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.png")
	writePNG(t, path, 100, 50, color.NRGBA{R: 200, A: 255})

	manager, err := image.NewManager(image.Config{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cases := []struct {
		name   string
		mode   image.Mode
		width  int
		height int
		wantW  int
		wantH  int
	}{
		{"contain", image.ModeContain, 40, 40, 40, 20},
		{"contain no upscale", image.ModeContain, 400, 400, 100, 50},
		{"cover", image.ModeCover, 30, 30, 30, 30},
		{"scale up", image.ModeScale, 200, 0, 200, 100},
		{"stretch", image.ModeStretch, 30, 60, 30, 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			editor, err := manager.Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := editor.Resize(tc.width, tc.height, tc.mode); err != nil {
				t.Fatalf("Resize: %v", err)
			}
			if size := editor.Size(); size.Width != tc.wantW || size.Height != tc.wantH {
				t.Fatalf("size = %+v, want %dx%d", size, tc.wantW, tc.wantH)
			}
		})
	}

	if _, err := manager.Open(filepath.Join(dir, "missing.png")); err == nil {
		t.Fatal("Open accepted a missing file")
	}
	if _, err := manager.OpenBytes([]byte("not an image")); !errors.Is(err, image.ErrUnsupportedFormat) {
		t.Fatalf("OpenBytes = %v, want ErrUnsupportedFormat", err)
	}
}

func TestThumbnail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.png")
	writePNG(t, path, 100, 50, color.NRGBA{A: 255})

	manager, _ := image.NewManager(image.Config{})
	editor, err := manager.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := editor.Thumbnail(50, 50); err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if size := editor.Size(); size.Width != 50 || size.Height != 25 {
		t.Fatalf("size = %+v, want 50x25", size)
	}
	if err := editor.Thumbnail(500, 500); err != nil {
		t.Fatalf("upscale Thumbnail: %v", err)
	}
	if size := editor.Size(); size.Width != 50 || size.Height != 25 {
		t.Fatalf("thumbnail upscaled to %+v", size)
	}
}

func TestCrop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.png")
	writePNG(t, path, 100, 50, color.NRGBA{A: 255})
	manager, _ := image.NewManager(image.Config{})

	editor, _ := manager.Open(path)
	if err := editor.Crop(10, 5, 50, 20); err != nil {
		t.Fatalf("Crop: %v", err)
	}
	if size := editor.Size(); size.Width != 50 || size.Height != 20 {
		t.Fatalf("size = %+v", size)
	}
	if err := editor.Crop(90, 0, 20, 10); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("out-of-bounds Crop = %v", err)
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.png")
	writePNG(t, path, 100, 50, color.NRGBA{A: 255})
	manager, _ := image.NewManager(image.Config{})

	editor, _ := manager.Open(path)
	if err := editor.Rotate(90); err != nil {
		t.Fatalf("Rotate(90): %v", err)
	}
	if size := editor.Size(); size.Width != 50 || size.Height != 100 {
		t.Fatalf("size after 90 = %+v", size)
	}

	editor, _ = manager.Open(path)
	if err := editor.Rotate(45); err != nil {
		t.Fatalf("Rotate(45): %v", err)
	}
	if size := editor.Size(); size.Width <= 100 || size.Height <= 50 {
		t.Fatalf("rotated canvas = %+v, want it to grow", size)
	}

	editor, _ = manager.Open(path)
	if err := editor.Rotate(math.NaN()); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("Rotate(NaN) = %v", err)
	}
}

func TestGreyscale(t *testing.T) {
	manager, _ := image.NewManager(image.Config{})
	editor, err := manager.Create(2, 1, "#ff0000")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	editor.Greyscale()
	result, err := editor.Encode(&image.EncodeOptions{Format: "png"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(result.Binary))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := decoded.At(0, 0)
	r, g, b, _ := got.RGBA()
	if r != g || g != b {
		t.Fatalf("greyscale pixel = %v", got)
	}
}

func TestApplyPreset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.png")
	writePNG(t, path, 100, 50, color.NRGBA{A: 255})

	manager, err := image.NewManager(image.Config{
		Encode: image.EncodeOptions{Format: "jpeg"},
		Presets: map[string]image.Preset{
			"thumb": {Mode: image.ModeCover, Width: 20, Height: 20,
				Encode: &image.EncodeOptions{Format: "png"}},
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	editor, _ := manager.Open(path)
	if err := editor.ApplyPreset("thumb"); err != nil {
		t.Fatalf("ApplyPreset: %v", err)
	}
	if size := editor.Size(); size.Width != 20 || size.Height != 20 {
		t.Fatalf("size = %+v", size)
	}
	result, err := editor.Encode(nil)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if result.MIME != "image/png" {
		t.Fatalf("preset encode was not applied: %s", result.MIME)
	}
	if err := editor.ApplyPreset("missing"); !errors.Is(err, image.ErrInvalidImage) {
		t.Fatalf("missing preset = %v", err)
	}
}

func TestMaxPixels(t *testing.T) {
	manager, err := image.NewManager(image.Config{MaxPixels: 1000})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "big.png")
	writePNG(t, path, 100, 100, color.NRGBA{A: 255})
	if _, err := manager.Open(path); !errors.Is(err, image.ErrTooLarge) {
		t.Fatalf("Open big = %v, want ErrTooLarge", err)
	}
	if _, err := manager.Create(20, 20, ""); err != nil {
		t.Fatalf("Create small: %v", err)
	}
	if _, err := manager.Create(100, 100, ""); !errors.Is(err, image.ErrTooLarge) {
		t.Fatalf("Create big = %v, want ErrTooLarge", err)
	}

	small := filepath.Join(dir, "small.png")
	writePNG(t, small, 20, 20, color.NRGBA{A: 255})
	editor, err := manager.Open(small)
	if err != nil {
		t.Fatalf("Open small: %v", err)
	}
	if err := editor.Resize(100, 100, image.ModeScale); !errors.Is(err, image.ErrTooLarge) {
		t.Fatalf("Resize big = %v, want ErrTooLarge", err)
	}
}

func TestMaxPixelsUnlimited(t *testing.T) {
	manager, err := image.NewManager(image.Config{MaxPixels: -1})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := manager.Create(2000, 2000, ""); err != nil {
		t.Fatalf("Create with unlimited pixels: %v", err)
	}
}
