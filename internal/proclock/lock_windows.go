//go:build windows

package proclock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on the first byte of the file. LockFileEx is
// the Windows equivalent of flock: the lock belongs to the handle and the
// system drops it when the process exits, so a crash leaves nothing to clean
// up.
// lockFile 锁定文件的第一个字节。LockFileEx 是 Windows 上 flock 的等价物：锁属于
// 句柄，进程退出时由系统释放，崩溃因此不会留下需要清理的状态。
func lockFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0,
		overlapped,
	)
}

func unlockFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
}

// isHeld reports whether err means "someone else holds it", as opposed to a
// filesystem or permission failure.
// isHeld 判断 err 是不是"别人持有"，以区别于文件系统或权限错误。
func isHeld(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
