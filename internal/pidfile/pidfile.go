// Package pidfile implements atomic pid file handling.
package pidfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Resolve turns path into an absolute path relative to workDir when needed.
func Resolve(path, workDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	return filepath.Join(workDir, path)
}

// Write atomically writes pid to path. The parent directory is created with
// 0755 and the file is written to a temporary file in the same directory
// before being renamed.
func Write(path string, pid int, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("pidfile: create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".pid-*")
	if err != nil {
		return fmt.Errorf("pidfile: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("pidfile: chmod temp file: %w", err)
	}
	if _, err := tmp.WriteString(strconv.Itoa(pid)); err != nil {
		cleanup()
		return fmt.Errorf("pidfile: write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("pidfile: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pidfile: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("pidfile: rename to %s: %w", path, err)
	}
	return nil
}

// Read parses the pid stored in path.
func Read(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("pidfile: invalid contents in %s", path)
	}
	return pid, nil
}

// RemoveIfOwner removes path only when it still contains pid.
func RemoveIfOwner(path string, pid int) error {
	current, err := Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if current != pid {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
