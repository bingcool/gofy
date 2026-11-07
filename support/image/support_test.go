package image

import (
	stdimage "image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func TestTargetSize(t *testing.T) {
	cases := []struct {
		name          string
		srcW, srcH    int
		width, height int
		mode          Mode
		wantW, wantH  int
	}{
		{"contain", 100, 50, 40, 40, ModeContain, 40, 20},
		{"contain no upscale", 100, 50, 400, 400, ModeContain, 100, 50},
		{"contain width only", 100, 50, 40, 0, ModeContain, 40, 20},
		{"cover", 100, 50, 30, 30, ModeCover, 60, 30},
		{"cover tall", 100, 50, 10, 100, ModeCover, 200, 100},
		{"scale up", 100, 50, 200, 0, ModeScale, 200, 100},
		{"scale height", 100, 50, 0, 25, ModeScale, 50, 25},
		{"stretch", 100, 50, 30, 60, ModeStretch, 30, 60},
		{"stretch single", 100, 50, 30, 0, ModeStretch, 30, 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH, err := targetSize(tc.srcW, tc.srcH, tc.width, tc.height, tc.mode)
			if err != nil {
				t.Fatalf("targetSize: %v", err)
			}
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Fatalf("targetSize = %dx%d, want %dx%d", gotW, gotH, tc.wantW, tc.wantH)
			}
		})
	}

	bad := []struct {
		width, height int
		mode          Mode
	}{
		{0, 0, ModeContain},
		{-1, 10, ModeContain},
		{0, 10, ModeCover},
		{10, 10, "zoom"},
	}
	for _, tc := range bad {
		if _, _, err := targetSize(100, 50, tc.width, tc.height, tc.mode); err == nil {
			t.Fatalf("targetSize(%d,%d,%q) accepted", tc.width, tc.height, tc.mode)
		}
	}
}

func TestPlaceOffset(t *testing.T) {
	cases := map[Position][2]int{
		PositionTopLeft:     {2, 2},
		PositionTop:         {47, 2},
		PositionTopRight:    {88, 2},
		PositionLeft:        {2, 47},
		PositionCenter:      {47, 47},
		PositionRight:       {88, 47},
		PositionBottomLeft:  {2, 88},
		PositionBottom:      {47, 88},
		PositionBottomRight: {88, 88},
	}
	for position, want := range cases {
		x, y := placeOffset(100, 100, 10, 10, position, 2, 2)
		if x != want[0] || y != want[1] {
			t.Fatalf("placeOffset(%s) = %d,%d, want %d,%d", position, x, y, want[0], want[1])
		}
	}
}

func TestParseHexColor(t *testing.T) {
	for value, want := range map[string]color.NRGBA{
		"#fff":    {R: 255, G: 255, B: 255, A: 255},
		"#336699": {R: 0x33, G: 0x66, B: 0x99, A: 255},
		"336699":  {R: 0x33, G: 0x66, B: 0x99, A: 255},
		"":        {R: 255, G: 255, B: 255, A: 255},
	} {
		got, err := parseHexColor(value)
		if err != nil || got != want {
			t.Fatalf("parseHexColor(%q) = %v (err %v), want %v", value, got, err, want)
		}
	}
	for _, bad := range []string{"#ggg", "#12", "#12345"} {
		if _, err := parseHexColor(bad); err == nil {
			t.Fatalf("parseHexColor(%q) accepted", bad)
		}
	}
}

func TestPixelsExceeded(t *testing.T) {
	if pixelsExceeded(10000, 10000, 0) {
		t.Fatal("max pixels 0 must mean unlimited")
	}
	if pixelsExceeded(10, 10, 100) {
		t.Fatal("exactly at the limit reported as exceeded")
	}
	if !pixelsExceeded(11, 10, 100) {
		t.Fatal("above the limit not reported")
	}
}

func TestResolveResourcePath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "logo.png")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	other := t.TempDir()
	outside := filepath.Join(other, "logo.png")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	got, err := resolveResourcePath(root, "logo.png", []string{root})
	if err != nil {
		t.Fatalf("inside root: %v", err)
	}
	want, _ := filepath.EvalSymlinks(inside)
	if got != want {
		t.Fatalf("resolved = %q, want %q", got, want)
	}
	if _, err := resolveResourcePath(root, "../logo.png", []string{root}); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := resolveResourcePath(root, outside, []string{root}); err == nil {
		t.Fatal("path outside roots accepted")
	}
	if _, err := resolveResourcePath(root, "missing.png", []string{root}); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestRotate90(t *testing.T) {
	src := stdimage.NewNRGBA(stdimage.Rect(0, 0, 2, 3))
	src.SetNRGBA(0, 0, color.NRGBA{R: 1, A: 255})
	src.SetNRGBA(1, 0, color.NRGBA{R: 2, A: 255})
	src.SetNRGBA(0, 2, color.NRGBA{R: 3, A: 255})
	dst := rotate(src, 90)
	if dst.Bounds().Dx() != 3 || dst.Bounds().Dy() != 2 {
		t.Fatalf("rotated size = %v", dst.Bounds())
	}
	// 顺时针 90：(x,y) → (srcH-1-y, x)
	if got := dst.NRGBAAt(2, 0).R; got != 1 {
		t.Fatalf("pixel (2,0) = %d, want 1", got)
	}
	if got := dst.NRGBAAt(2, 1).R; got != 2 {
		t.Fatalf("pixel (2,1) = %d, want 2", got)
	}
	if got := dst.NRGBAAt(0, 0).R; got != 3 {
		t.Fatalf("pixel (0,0) = %d, want 3", got)
	}
}
