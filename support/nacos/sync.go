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
type Result struct {
	// File is the local config path.
	File string
	// Digest is the hex MD5 of the remote content.
	Digest string
	// Changed reports whether the local file was rewritten.
	Changed bool
}

// Sync fetches the remote config and writes it to Config.File. The content
// must be valid YAML; the file is replaced atomically and left untouched when
// it already matches.
func Sync(ctx context.Context, cfg Config) (Result, error) {
	client, err := NewClient(cfg)
	if err != nil {
		return Result{}, err
	}
	return client.Sync(ctx)
}

// Sync fetches the remote config and writes it to Config.File.
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
