package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	rotationModeSize         = "size"
	rotationModeHourly       = "hourly"
	defaultDateLayout        = "20060102"
	defaultDateDirMaxAgeDays = 7
	// dateToken marks where the date directory goes in an output path.
	// Example: storage/log/script/{date}/info/fix-order.log.
	dateToken = "{date}"
)

func normalizeRotationMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", rotationModeSize:
		return rotationModeSize
	case rotationModeHourly:
		return rotationModeHourly
	default:
		return mode
	}
}

// rotatingFileSink writes one logical file such as info.log. It recreates the
// underlying writer when the date (and hour, in hourly mode) changes, so a
// long-running process moves into the next directory without a restart.
type rotatingFileSink struct {
	target   string
	rotation RotationConfig
	mode     string
	now      func() time.Time

	mu        sync.Mutex
	writer    io.WriteCloser
	path      string
	prunedDay string
}

func newRotatingFileSink(target string, rotation RotationConfig, mode string) *rotatingFileSink {
	if mode == "" {
		mode = normalizeRotationMode(rotation.Mode)
	}
	return &rotatingFileSink{
		target:   target,
		rotation: rotation,
		mode:     mode,
		now:      time.Now,
	}
}

func (s *rotatingFileSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rotateLocked(); err != nil {
		return 0, err
	}
	return s.writer.Write(p)
}

func (s *rotatingFileSink) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.writer.(*os.File); ok {
		return f.Sync()
	}
	return nil
}

func (s *rotatingFileSink) rotateLocked() error {
	now := s.now()
	if !s.rotation.LocalTime {
		now = now.UTC()
	}
	path := s.pathFor(now)
	if path == s.path && s.writer != nil {
		return nil
	}
	if s.writer != nil {
		_ = s.writer.Close()
		s.writer = nil
	}
	writer, err := s.open(path)
	if err != nil {
		return err
	}
	s.writer = writer
	s.path = path

	if root, ok := s.dateRoot(); ok {
		day := now.Format(s.dateLayout())
		if day != s.prunedDay {
			s.pruneLocked(now, root)
			s.prunedDay = day
		}
	}
	return nil
}

func (s *rotatingFileSink) pathFor(now time.Time) string {
	target := s.target
	if strings.Contains(target, dateToken) {
		// {date} 让日期目录出现在 info/error 等子目录之前。
		target = expandDateToken(target, s.rotation, now)
	} else if s.rotation.DateDir {
		target = filepath.Join(filepath.Dir(target), now.Format(s.dateLayout()), filepath.Base(target))
	}
	ext := filepath.Ext(target)
	name := strings.TrimSuffix(filepath.Base(target), ext)
	if s.mode == rotationModeHourly {
		// 与 swoolefy 一致：每天 24 个小时文件，h0 不补零。
		name = fmt.Sprintf("%s-h%d", name, now.Hour())
	}
	return filepath.Join(filepath.Dir(target), name+ext)
}

// expandDateToken replaces every {date} with the date in DateLayout. It is
// used by both the rotating sink and the plain (rotation disabled) file sink.
func expandDateToken(target string, rotation RotationConfig, now time.Time) string {
	if !strings.Contains(target, dateToken) {
		return target
	}
	if !rotation.LocalTime {
		now = now.UTC()
	}
	layout := rotation.DateLayout
	if layout == "" {
		layout = defaultDateLayout
	}
	return strings.ReplaceAll(target, dateToken, now.Format(layout))
}

// dateRoot returns the directory that contains the date directories and
// whether retention applies. A {date} token only counts when it occupies a
// whole path segment; otherwise the token is part of a file name and no date
// directory exists. Without a token, DateDir decides.
func (s *rotatingFileSink) dateRoot() (string, bool) {
	index := strings.Index(s.target, dateToken)
	if index < 0 {
		if s.rotation.DateDir {
			return filepath.Dir(s.target), true
		}
		return "", false
	}
	before, after := s.target[:index], s.target[index+len(dateToken):]
	beforeOK := before == "" || isPathSeparator(before[len(before)-1])
	afterOK := after == "" || isPathSeparator(after[0])
	if !beforeOK || !afterOK {
		return "", false
	}
	root := strings.TrimRightFunc(before, func(r rune) bool { return isPathSeparator(byte(r)) })
	if root == "" {
		root = "."
	}
	return filepath.Clean(root), true
}

func isPathSeparator(c byte) bool { return c == '/' || c == '\\' }

func (s *rotatingFileSink) open(path string) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("logger: create dir for %s: %w", path, err)
	}
	if s.mode == rotationModeSize {
		// lumberjack 在当天目录内按大小轮转，跨天后由本 sink 换到新目录。
		return &lumberjack.Logger{
			Filename:   path,
			MaxSize:    s.rotation.MaxSizeMB,
			MaxAge:     s.rotation.MaxAgeDays,
			MaxBackups: s.rotation.MaxBackups,
			Compress:   s.rotation.Compress,
			LocalTime:  s.rotation.LocalTime,
		}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("logger: open %s: %w", path, err)
	}
	return f, nil
}

func (s *rotatingFileSink) dateLayout() string {
	if s.rotation.DateLayout == "" {
		return defaultDateLayout
	}
	return s.rotation.DateLayout
}

// dateDirMaxAgeDays returns how many days date directories are kept. A zero
// value uses the package default of 7 days.
func (s *rotatingFileSink) dateDirMaxAgeDays() int {
	if s.rotation.DateDirMaxAgeDays > 0 {
		return s.rotation.DateDirMaxAgeDays
	}
	return defaultDateDirMaxAgeDays
}

// pruneLocked removes whole date directories older than DateDirMaxAgeDays.
// Only names that parse with DateLayout are considered, so unrelated
// directories are kept.
func (s *rotatingFileSink) pruneLocked(now time.Time, parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	layout := s.dateLayout()
	cutoff := now.AddDate(0, 0, -s.dateDirMaxAgeDays())
	cutoff = time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, now.Location())
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		day, err := time.ParseInLocation(layout, entry.Name(), now.Location())
		if err != nil {
			continue
		}
		if day.Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
		}
	}
}
