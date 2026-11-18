//go:build windows

package gofy

// notifierFromEnv always returns nil on Windows: there is no startup channel to
// join.
//
// The channel works by handing the write end of a pipe to the child as an
// inherited descriptor, which os/exec does through ExtraFiles — a field Windows
// ignores. DetachWithNotify therefore returns ErrUnsupported there, no child is
// ever started in daemon mode, and no process ever receives process.NotifyEnv.
// Ignoring a stray copy of that variable is also the safe answer: descriptor 3
// is not the pipe, so writing to it would hit an unrelated handle.
//
// The nil notifier is not a special case downstream: every method on
// *startupNotifier is a no-op for a nil receiver, so the child-side call sites
// need no platform branch.
// notifierFromEnv 在 Windows 上始终返回 nil：没有可加入的启动通道。
//
// 该通道的做法是把管道写端作为继承描述符交给子进程，os/exec 通过 ExtraFiles 完成，而 Windows
// 忽略这个字段。因此 DetachWithNotify 在 Windows 返回 ErrUnsupported，后台模式不会拉起子进程，
// 也就没有任何进程会收到 process.NotifyEnv。忽略环境里偶然存在的同名变量同样是安全答案：
// 描述符 3 并不是那条管道，往它写会落到无关的句柄上。
//
// nil 通知器在下游不是特例：*startupNotifier 的所有方法对 nil 接收者都是 no-op，因此子进程侧
// 的调用点不需要平台分支。
func notifierFromEnv() *startupNotifier { return nil }
