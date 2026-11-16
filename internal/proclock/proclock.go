// Package proclock gives a worker group one owner per host.
//
// A pid file answers "which process claimed this group" but cannot answer "may
// I claim it" atomically: two starts both read an absent file, both see no live
// pid and both write. The lock is the missing primitive — the kernel grants it
// to exactly one open file description and releases it when that process dies,
// including after SIGKILL, so there is no stale state to clean up.
//
// Scope: the lock lives on the local filesystem, so it prevents two processes
// on the same host from owning one group. It says nothing about two pods with
// separate filesystems — that needs replica limits or a distributed lease.
//
// The lock file is never removed, and that is deliberate. Deleting a lock file
// on release is the classic unlink race: a process that already opened the old
// inode acquires the lock on that now-nameless inode, while a later process
// creates a fresh file and locks that one — two owners, no error. Leaving the
// file costs zero bytes and cannot go stale, because ownership lives on the
// open file description. A tree without the file is not a goal worth the bug;
// point the pid file at a runtime directory instead.
// Package proclock 保证一个 worker 分组在一台主机上只有一个所有者。
//
// pid 文件回答"哪个进程占了分组"，却无法原子地回答"我可以占吗"：两个启动同时读到
// 不存在的文件、都没看到活着的 pid、然后都写入。锁补的正是这一块——内核只把锁授予
// 一个打开的文件描述，并在进程退出时释放（包括 SIGKILL），因此不存在需要清理的锁状态。
//
// 作用域：锁在本地文件系统上，因此它只能阻止同一主机上的两个进程占同一个分组。它管不了
// 文件系统各自独立的两个 Pod——那需要副本数约束或分布式租约。
//
// 锁文件永不删除，这是刻意的。退出时删锁文件正是经典的 unlink 竞态：已经打开旧 inode 的
// 进程会在释放后锁住这个没有名字的 inode，而之后的进程新建文件、锁住新的 inode——两个
// 所有者并存，谁都不报错。留着文件不花空间、也不会过期，因为归属在打开的文件描述上。
// "目录里没有这个文件"不值得用这个 bug 去换；要干净就把 pid 文件指到运行时目录。
package proclock

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrHeld reports that another live process already owns the lock.
// ErrHeld 表示另一个活着的进程已经持有该锁。
var ErrHeld = errors.New("proclock: lock is held by another process")

// Lock is an exclusive claim on one path, held for as long as the process keeps
// the file open.
// Lock 是对一个路径的独占声明，进程保持文件打开期间一直有效。
type Lock struct {
	file *os.File
	path string
}

// Acquire takes an exclusive lock on path without blocking. It creates the file
// when missing, so the caller only has to pick a stable path — the pid file
// plus a suffix is the natural choice.
//
// A successful Acquire returns a lock that must be closed; a held lock returns
// ErrHeld, which callers translate into "another instance is running".
// Acquire 以非阻塞方式对 path 加独占锁。文件不存在时创建，调用方只需给出稳定路径——
// pid 文件加后缀是自然的选择。
//
// 成功时返回需要关闭的锁；锁被占用时返回 ErrHeld，调用方据此判定"已有实例在运行"。
func Acquire(path string) (*Lock, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("proclock: path is required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("proclock: open %s: %w", path, err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if isHeld(err) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("proclock: lock %s: %w", path, err)
	}
	return &Lock{file: file, path: path}, nil
}

// Path returns the locked path, for logs and error messages.
// Path 返回被锁的路径，用于日志与错误信息。
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Close releases the lock. It is safe on a nil lock and safe to call twice, so
// a `defer lock.Close()` covers both the acquired and the failed branch. The
// file stays on disk on purpose — see the package comment for the race that
// removing it would reintroduce.
// Close 释放锁。对 nil 锁安全，重复调用也安全，因此 `defer lock.Close()` 可以同时
// 覆盖成功与失败两条分支。文件按设计留在磁盘上——删除它会带回哪个竞态，见包注释。
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := unlockFile(file)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("proclock: unlock %s: %w", l.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("proclock: close %s: %w", l.path, closeErr)
	}
	return nil
}
