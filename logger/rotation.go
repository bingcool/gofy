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
	// dateToken 标记日期目录在输出路径中的位置，例如 storage/log/script/{date}/info/fix-order.log。
	// 只有独占一个路径段时才按日期目录处理并参与过期清理，写在文件名里则只是普通字面量。
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
// rotatingFileSink 写一个逻辑文件，例如 info.log。日期（hourly 模式下还包含小时）
// 变化时会重建底层 writer，因此长驻进程无需重启即可切换到下一个目录；
// 所有写操作串行化，调用方不必自行加锁。
type rotatingFileSink struct {
	target   string
	rotation RotationConfig
	mode     string
	perm     os.FileMode
	now      func() time.Time

	mu        sync.Mutex
	writer    io.WriteCloser
	path      string
	prunedDay string
}

func newRotatingFileSink(target string, rotation RotationConfig, mode string, perm os.FileMode) *rotatingFileSink {
	if mode == "" {
		mode = normalizeRotationMode(rotation.Mode)
	}
	if perm == 0 {
		perm = defaultFilePerm
	}
	return &rotatingFileSink{
		target:   target,
		rotation: rotation,
		mode:     mode,
		perm:     perm,
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
		// A {date} token puts the date directory before subdirectories such as info/error.
		// {date} 让日期目录出现在 info/error 等子目录之前。
		target = expandDateToken(target, s.rotation, now)
	} else if s.rotation.DateDir {
		target = filepath.Join(filepath.Dir(target), now.Format(s.dateLayout()), filepath.Base(target))
	}
	ext := filepath.Ext(target)
	name := strings.TrimSuffix(filepath.Base(target), ext)
	if s.mode == rotationModeHourly {
		// Same as swoolefy: 24 hourly files per day, h0 is not zero-padded.
		// 与 swoolefy 一致：每天 24 个小时文件，h0 不补零。
		name = fmt.Sprintf("%s-h%d", name, now.Hour())
	}
	return filepath.Join(filepath.Dir(target), name+ext)
}

// expandDateToken replaces every {date} with the date in DateLayout. It is
// used by both the rotating sink and the plain (rotation disabled) file sink.
// expandDateToken 把所有 {date} 替换为按 DateLayout 格式化的日期；滚动 sink 与
// 未开启滚动的直接写文件分支都会调用它，两处的日期目录规则因此保持一致。
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
// dateRoot 返回包含日期目录的父目录，以及是否需要执行保留期清理。只有 {date} 独占
// 一个完整路径段时才成立；否则它只是文件名的一部分，不存在日期目录。没有 token 时
// 由 DateDir 决定。false 表示不做清理，调用方不要据此删除任何目录。
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
		// lumberjack opens the file itself and keeps the mode of an existing
		// one, so create it with the configured mode first: otherwise a file
		// left at 0644 by an older release stays readable to everyone.
		// lumberjack 自己打开文件，并沿用已存在文件的权限，因此先用配置的权限建好：
		// 否则旧版本留下的 0644 文件会一直对所有人可读。
		if err := s.prepareFile(path); err != nil {
			return nil, err
		}
		return &lumberjack.Logger{
			Filename:   path,
			MaxSize:    s.rotation.MaxSizeMB,
			MaxAge:     s.rotation.MaxAgeDays,
			MaxBackups: s.rotation.MaxBackups,
			Compress:   s.rotation.Compress,
			LocalTime:  s.rotation.LocalTime,
		}, nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, s.perm)
	if err != nil {
		return nil, fmt.Errorf("logger: open %s: %w", path, err)
	}
	if err := f.Chmod(s.perm); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("logger: chmod %s: %w", path, err)
	}
	return f, nil
}

// prepareFile creates path with the configured mode and normalises an existing
// file to it. lumberjack needs the file to exist for the mode to stick.
// prepareFile 用配置的权限创建 path，并把已存在的文件归一化。lumberjack 需要文件
// 先存在，权限才会按这里的结果保留。
func (s *rotatingFileSink) prepareFile(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, s.perm)
	if err != nil {
		return fmt.Errorf("logger: open %s: %w", path, err)
	}
	if err := f.Chmod(s.perm); err != nil {
		_ = f.Close()
		return fmt.Errorf("logger: chmod %s: %w", path, err)
	}
	return f.Close()
}

func (s *rotatingFileSink) dateLayout() string {
	if s.rotation.DateLayout == "" {
		return defaultDateLayout
	}
	return s.rotation.DateLayout
}

// dateDirMaxAgeDays returns how many days date directories are kept. A zero
// value uses the package default of 7 days.
// dateDirMaxAgeDays 返回日期目录的保留天数；0 表示使用包内默认的 7 天。
// 负数视为非法配置，会在 Validate 阶段被拒绝。
func (s *rotatingFileSink) dateDirMaxAgeDays() int {
	if s.rotation.DateDirMaxAgeDays > 0 {
		return s.rotation.DateDirMaxAgeDays
	}
	return defaultDateDirMaxAgeDays
}

// pruneLocked removes whole date directories older than DateDirMaxAgeDays.
// Only names that parse with DateLayout are considered, so unrelated
// directories are kept.
// pruneLocked 删除超过 DateDirMaxAgeDays 的整个日期目录。只有能被 DateLayout 解析的
// 目录名才会被处理，其余目录保持原样，避免误删日志根目录下的其他内容。
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
