//go:build !windows

package proclock

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock without blocking. flock is tied to
// the open file description, so two descriptors — even in one process — conflict,
// which is exactly the property the pid file lacks.
// lockFile 以非阻塞方式加独占建议锁。flock 绑定在打开的文件描述上，因此两个描述符
// ——即使在同一进程里——也会冲突，这正是 pid 文件缺少的性质。
func lockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

// isHeld reports whether err means "someone else holds it", as opposed to a
// filesystem or permission failure.
// isHeld 判断 err 是不是"别人持有"，以区别于文件系统或权限错误。
func isHeld(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
