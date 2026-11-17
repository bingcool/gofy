package nacos

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Result describes the outcome of a Sync.
// Result 描述一次 Sync 的结果。
type Result struct {
	// File is the local config path.
	// File 是本地配置文件路径。
	File string
	// Digest is the hex MD5 of the remote content.
	// Digest 是远端配置内容的十六进制 MD5。
	Digest string
	// Changed reports whether the local file was rewritten.
	// Changed 表示本地文件是否被重写。
	Changed bool
}

// Sync fetches the remote config and writes it to Config.File. The content
// must be valid YAML; the file is replaced atomically and left untouched when
// it already matches.
// Sync 拉取远端配置并写入 Config.File。内容必须是合法 YAML；文件以原子
// 替换方式写入，内容一致时不改动本地文件。
func Sync(ctx context.Context, cfg Config) (Result, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return Result{}, err
	}
	// The client owns a dedicated transport; without Close its idle
	// connections and read goroutines outlive this one-shot call.
	// 客户端持有独立的 transport；不 Close 的话，它的空闲连接与读 goroutine
	// 会比这次一次性调用活得更久。
	defer client.Close()
	return client.Sync(ctx)
}

// Sync fetches the remote config and writes it to Config.File.
// Sync 拉取远端配置并写入 Config.File。
func (c *Client) Sync(ctx context.Context) (Result, error) {
	if !c.cfg.Enabled {
		return Result{}, ErrDisabled
	}
	content, err := c.Get(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := validateYAML(content); err != nil {
		return Result{}, fmt.Errorf("nacos: %s: %w", c.cfg.File, err)
	}

	res := Result{File: c.cfg.File, Digest: digest(content)}
	existing, err := os.ReadFile(c.cfg.File)
	switch {
	case err == nil && digest(existing) == res.Digest:
		return res, nil
	case err != nil && !os.IsNotExist(err):
		return Result{}, fmt.Errorf("nacos: read %s: %w", c.cfg.File, err)
	}
	if err := writeFileAtomic(c.cfg.File, content); err != nil {
		return Result{}, err
	}
	res.Changed = true
	return res, nil
}

func validateYAML(content []byte) error {
	if len(content) == 0 {
		return fmt.Errorf("empty config")
	}
	var value any
	if err := yaml.Unmarshal(content, &value); err != nil {
		return fmt.Errorf("config is not valid YAML: %w", err)
	}
	return nil
}

func digest(content []byte) string {
	sum := md5.Sum(content)
	return hex.EncodeToString(sum[:])
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("nacos: create dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("nacos: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	// The config may contain credentials.
	// 配置里可能包含凭据，临时文件只授予属主读写权限。
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("nacos: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("nacos: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("nacos: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("nacos: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("nacos: replace %s: %w", path, err)
	}
	return nil
}
