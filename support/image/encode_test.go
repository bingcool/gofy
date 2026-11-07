package image_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	stdimage "image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/bingcool/gofy/support/image"
)

func TestEncodeFormatNegotiation(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "source.png")
	writePNG(t, pngPath, 10, 10, color.NRGBA{R: 10, G: 20, B: 30, A: 255})

	jpegPath := filepath.Join(dir, "source.jpg")
	rgba := stdimage.NewRGBA(stdimage.Rect(0, 0, 10, 10))
	file, err := os.Create(jpegPath)
	if err != nil {
		t.Fatalf("create jpeg: %v", err)
	}
	if err := jpeg.Encode(file, rgba, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	_ = file.Close()

	manager, err := image.NewManager(image.Config{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	pngEditor, _ := manager.Open(pngPath)
	result, err := pngEditor.Encode(nil)
	if err != nil || result.MIME != "image/png" {
		t.Fatalf("png source = %+v (err %v)", result, err)
	}

	jpegEditor, _ := manager.Open(jpegPath)
	result, err = jpegEditor.Encode(nil)
	if err != nil || result.MIME != "image/jpeg" || result.Extension != ".jpg" {
		t.Fatalf("jpeg source = %+v (err %v)", result, err)
	}

	created, _ := manager.Create(4, 4, "")
	result, err = created.Encode(nil)
	if err != nil || result.MIME != "image/jpeg" {
		t.Fatalf("created canvas = %+v (err %v)", result, err)
	}

	created, _ = manager.Create(4, 4, "")
	result, err = created.Encode(&image.EncodeOptions{Format: "jpg"})
	if err != nil || result.MIME != "image/jpeg" {
		t.Fatalf("jpg alias = %+v (err %v)", result, err)
	}

	created, _ = manager.Create(4, 4, "")
	result, err = created.Encode(&image.EncodeOptions{Format: "gif"})
	if err != nil || result.MIME != "image/gif" || result.Extension != ".gif" {
		t.Fatalf("gif = %+v (err %v)", result, err)
	}
}

func TestEncodeErrors(t *testing.T) {
	manager, _ := image.NewManager(image.Config{})
	editor, _ := manager.Create(4, 4, "")

	for _, format := range []string{"webp", "avif", "bmp"} {
		if _, err := editor.Encode(&image.EncodeOptions{Format: format}); !errors.Is(err, image.ErrUnsupportedFormat) {
			t.Fatalf("Encode(%s) = %v, want ErrUnsupportedFormat", format, err)
		}
	}
	if _, err := editor.Encode(&image.EncodeOptions{Quality: 101}); !errors.Is(err, image.ErrInvalidConfig) {
		t.Fatalf("quality 101 = %v, want ErrInvalidConfig", err)
	}
	if _, err := editor.Encode(&image.EncodeOptions{MaxBytes: 1}); !errors.Is(err, image.ErrTooLarge) {
		t.Fatalf("max bytes = %v, want ErrTooLarge", err)
	}
	if _, err := editor.Encode(&image.EncodeOptions{MaxBytes: 1 << 20}); err != nil {
		t.Fatalf("max bytes above size: %v", err)
	}
}

func TestEncodeBase64(t *testing.T) {
	manager, _ := image.NewManager(image.Config{})
	editor, _ := manager.Create(6, 3, "#102030")
	result, err := editor.Encode(&image.EncodeOptions{Format: "png"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	encoded, err := editor.Base64(&image.EncodeOptions{Format: "png"})
	if err != nil {
		t.Fatalf("Base64: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if !bytes.Equal(decoded, result.Binary) {
		t.Fatal("base64 payload differs from Encode")
	}
	if _, err := png.Decode(bytes.NewReader(decoded)); err != nil {
		t.Fatalf("base64 payload is not a png: %v", err)
	}
}
