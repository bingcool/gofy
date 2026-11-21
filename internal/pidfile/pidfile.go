// Package pidfile implements atomic pid file handling.
// 包 pidfile 实现原子化的 pid 文件读写，供 daemon、stop、restart 等流程共享。
package pidfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bingcool/gofy/internal/process"
)

// Record is what a pid file stores. The pid alone cannot be trusted days later —
// the kernel recycles pids — so the record also carries the owner's identity and
// when the claim was written.
// Record 是 pid 文件保存的内容。单靠 pid 几天后已不可信——内核会回收 pid——因此记录里
// 还带着所有者的身份与写入这份声明的时刻。
type Record struct {
	// PID is the process that owns the file.
	// PID 是拥有该文件的进程。
	PID int `json:"pid"`
	// Executable is the owner's path at claim time, for humans.
	// Executable 是声明时所有者的路径，供人阅读。
	Executable string `json:"executable,omitempty"`
	// ClaimedAt is when the record was written, in RFC 3339 with nanoseconds.
	// ClaimedAt 是记录写入时刻，RFC 3339 纳秒精度。
	ClaimedAt string `json:"claimed_at,omitempty"`
	// Token is the platform identity of the process. Empty means the platform
	// could not provide one, which is "unknown", never "matches".
	// Token 是进程在该平台上的身份。空值表示平台无法提供，语义是"未知"，绝不是"匹配"。
	Token string `json:"token,omitempty"`
}

// Verdict is the outcome of comparing a record with the process that currently
// owns its pid.
// Verdict 是记录与"当前占用该 pid 的进程"比对的结果。
type Verdict int

const (
	// IdentityUnknown means either side has no token: the record predates this
	// format, the platform is unsupported, or the process cannot be inspected.
	// Callers may proceed with a warning, but must not claim it matched.
	// IdentityUnknown 表示有一侧没有 token：记录早于当前格式、平台不支持，或进程无法读取。
	// 调用方可以带警告继续，但不能声称匹配。
	IdentityUnknown Verdict = iota
	// IdentityMatch means the pid is still the process that wrote the record.
	// IdentityMatch 表示该 pid 仍是写下这条记录的进程。
	IdentityMatch
	// IdentityMismatch means the pid now belongs to another process.
	// IdentityMismatch 表示该 pid 现在属于另一个进程。
	IdentityMismatch
)

// NewRecord describes this process, or pid when it is not this one.
// NewRecord 描述当前进程；pid 不是当前进程时描述它。
func NewRecord(pid int) Record {
	identity := process.Identify(pid)
	record := Record{
		PID:        identity.PID,
		Executable: identity.Executable,
		Token:      identity.Token,
	}
	if !identity.ClaimedAt.IsZero() {
		record.ClaimedAt = identity.ClaimedAt.Format(time.RFC3339Nano)
	}
	return record
}

// Verify compares the record with the process that currently owns its pid. It
// never signals anything: it only answers whether stopping that pid would hit
// the process this file describes.
// Verify 把记录与当前占用该 pid 的进程做比较。它不发任何信号：只回答"停止这个 pid 会不会
// 打到这份文件描述的那个进程"。
func (r Record) Verify() Verdict {
	if r.PID <= 0 || r.Token == "" {
		return IdentityUnknown
	}
	current := process.Token(r.PID)
	if current == "" {
		return IdentityUnknown
	}
	if current == r.Token {
		return IdentityMatch
	}
	return IdentityMismatch
}

// Resolve turns path into an absolute path relative to workDir when needed.
// Resolve 在需要时把 path 转成相对 workDir 的绝对路径；空路径直接返回空串，
// workDir 为空时按 "." 处理，调用方无需预先规整参数。
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

// Write atomically writes a pid file holding the number alone. It is the legacy
// shape, kept for callers that only need the number; new code should write a
// Record so a later `stop` can tell a recycled pid from the original process.
// Write 原子地写入只含 pid 的文件：这是旧格式，只为只关心数字的调用方保留。新代码应写
// Record，这样之后的 `stop` 才能分辨被复用的 pid 与原进程。
func Write(path string, pid int, perm os.FileMode) error {
	return WriteRecord(path, Record{PID: pid}, perm)
}

// ErrNotOwner is returned by WriteRecordIfOwner when the file names another
// process.
// ErrNotOwner 表示文件属于另一个进程，WriteRecordIfOwner 因此拒绝写入。
var ErrNotOwner = errors.New("pidfile: owned by another process")

// WriteRecordIfOwner writes record only when path is absent or already names
// record.PID.
//
// A plain rewrite is wrong for a periodic refresh: cleanup may have just
// removed the file, or a new instance may have written its own record, and
// renaming over either undoes that work. The read and the rename are not one
// atomic step, so this narrows rather than eliminates the window — the caller
// still has to stop refreshing before it deletes.
// WriteRecordIfOwner 只在文件不存在或已记录 record.PID 时写入。
//
// 周期性刷新不能用无条件重写：cleanup 可能刚删掉文件，或新实例已写入自己的记录，
// rename 覆盖过去就把对方的工作抹掉了。读取与 rename 不是一步原子操作，因此这只能
// 缩小而不是消除窗口——调用方仍必须在删除前停止刷新。
func WriteRecordIfOwner(path string, record Record, perm os.FileMode) error {
	current, err := Read(path)
	switch {
	case err == nil:
		if current != record.PID {
			return ErrNotOwner
		}
	case !os.IsNotExist(err):
		return err
	}
	return WriteRecord(path, record, perm)
}

// WriteRecord atomically writes record to path. The parent directory is created
// with 0755 and the content goes to a temporary file in the same directory
// before the rename, so a reader never sees half a record. perm applies to the
// temporary file and is therefore the final mode.
// WriteRecord 原子地把 record 写入 path：父目录以 0755 创建，内容先写入同目录的临时
// 文件，再重命名覆盖目标，因此读方不会看到半截记录。perm 作用于临时文件，重命名后即为
// pid 文件的权限。
func WriteRecord(path string, record Record, perm os.FileMode) error {
	if record.PID <= 0 {
		return fmt.Errorf("pidfile: refusing to write pid %d to %s", record.PID, path)
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("pidfile: encode record: %w", err)
	}
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
	if _, err := tmp.Write(data); err != nil {
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

// ReadRecord parses the record stored in path. Both shapes are accepted: the
// current one-line JSON object and the older bare integer, which comes back as a
// record with no token — an honest "unknown" rather than a guess.
// ReadRecord 解析 path 中保存的记录。两种形态都接受：当前的单行 JSON 对象，以及更早的
// 纯整数——后者返回不带 token 的记录，语义是诚实的"未知"而不是猜测。
func ReadRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "{") {
		var record Record
		if err := json.Unmarshal([]byte(trimmed), &record); err != nil {
			return Record{}, fmt.Errorf("pidfile: invalid contents in %s: %w", path, err)
		}
		if record.PID <= 0 {
			return Record{}, fmt.Errorf("pidfile: invalid pid in %s", path)
		}
		return record, nil
	}
	pid, err := strconv.Atoi(trimmed)
	if err != nil || pid <= 0 {
		return Record{}, fmt.Errorf("pidfile: invalid contents in %s", path)
	}
	return Record{PID: pid}, nil
}

// Read parses the pid stored in path.
// Read 解析 path 中保存的 pid；内容非法或非正数时返回错误，不会返回可疑的进程号。
func Read(path string) (int, error) {
	record, err := ReadRecord(path)
	if err != nil {
		return 0, err
	}
	return record.PID, nil
}

// RemoveIfOwner removes path only when it still names pid.
// RemoveIfOwner 只在 path 中仍是 pid 时才删除它，避免误删新实例写入的 pid 文件；
// 文件已不存在视为成功。
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
