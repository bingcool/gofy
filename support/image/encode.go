package image

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// ProcessResult 是一次编码的结果。
type ProcessResult struct {
	Binary    []byte
	MIME      string
	Extension string
	Size      int64
}

// Encode 按「全局配置 → 预设 encode → 本次参数」的优先级合并后编码。
// 纯 Go 实现支持 jpeg、png、gif；webp/avif 仅支持解码（webp）或直接报
// ErrUnsupportedFormat。
func (e *Editor) Encode(opts *EncodeOptions) (*ProcessResult, error) {
	options, err := e.resolveEncodeOptions(opts)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	switch options.Format {
	case "jpeg":
		if err := jpeg.Encode(&buf, e.img, &jpeg.Options{Quality: options.Quality}); err != nil {
			return nil, fmt.Errorf("%w: encode jpeg: %v", ErrInvalidImage, err)
		}
	case "png":
		if err := png.Encode(&buf, e.img); err != nil {
			return nil, fmt.Errorf("%w: encode png: %v", ErrInvalidImage, err)
		}
	case "gif":
		if err := gif.Encode(&buf, e.img, nil); err != nil {
			return nil, fmt.Errorf("%w: encode gif: %v", ErrInvalidImage, err)
		}
	default:
		return nil, fmt.Errorf("%w: cannot encode %q", ErrUnsupportedFormat, options.Format)
	}

	binary := buf.Bytes()
	if options.MaxBytes > 0 && int64(len(binary)) > options.MaxBytes {
		return nil, fmt.Errorf("%w: encoded size %d exceeds %d bytes", ErrTooLarge, len(binary), options.MaxBytes)
	}
	info := formatInfos[options.Format]
	return &ProcessResult{
		Binary:    binary,
		MIME:      info.mime,
		Extension: info.extension,
		Size:      int64(len(binary)),
	}, nil
}

// Base64 返回编码后的标准 base64 字符串（不含 data URI 前缀）。
func (e *Editor) Base64(opts *EncodeOptions) (string, error) {
	result, err := e.Encode(opts)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(result.Binary), nil
}

// resolveEncodeOptions 合并三层配置并完成格式协商。
func (e *Editor) resolveEncodeOptions(opts *EncodeOptions) (EncodeOptions, error) {
	out := e.encode
	if e.presetEncode != nil {
		mergeEncodeOptions(&out, *e.presetEncode)
	}
	if opts != nil {
		mergeEncodeOptions(&out, *opts)
	}

	out.Format = normalizeFormat(out.Format)
	if out.Format == "" {
		switch e.sourceFormat {
		case "png", "webp", "gif":
			// 避免把透明通道压成 JPEG 的实色背景。
			out.Format = "png"
		default:
			out.Format = "jpeg"
		}
	}
	if !supportedEncodeFormat(out.Format) {
		return EncodeOptions{}, fmt.Errorf("%w: cannot encode %q (supported: jpeg, png, gif)",
			ErrUnsupportedFormat, out.Format)
	}
	if out.Quality == 0 {
		out.Quality = defaultQuality
	}
	if out.Quality < 1 || out.Quality > 100 {
		return EncodeOptions{}, fmt.Errorf("%w: quality must be between 1 and 100 (%d given)",
			ErrInvalidConfig, out.Quality)
	}
	if out.MaxBytes < 0 {
		return EncodeOptions{}, fmt.Errorf("%w: max bytes must not be negative", ErrInvalidConfig)
	}
	return out, nil
}

func mergeEncodeOptions(dst *EncodeOptions, src EncodeOptions) {
	if src.Format != "" {
		dst.Format = src.Format
	}
	if src.Quality != 0 {
		dst.Quality = src.Quality
	}
	if src.MaxBytes != 0 {
		dst.MaxBytes = src.MaxBytes
	}
}
