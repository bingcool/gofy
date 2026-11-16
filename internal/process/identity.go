package process

import (
	"os"
	"time"
)

// Identity answers "who is this pid", which is a different question from "is it
// alive". A pid is recycled by the kernel, so a pid file read days after a crash
// can name a process that has nothing to do with this application. The token is
// the part the kernel guarantees: an opaque value derived from when the process
// started, which a reused pid cannot reproduce.
// Identity 回答"这个 pid 是谁"，与"它是否活着"是两个问题。pid 会被内核回收，崩溃几天后
// 读到的 pid 文件可能指向一个与本应用无关的进程。token 是内核能保证的部分：由进程启动
// 时刻派生，被复用的 pid 无法复制出同一个值。
type Identity struct {
	// PID is the number the pid file stores.
	// PID 是 pid 文件里保存的进程号。
	PID int
	// Executable is the path this process was started from, for humans reading
	// the file. It is not used for matching: upgrades and symlinks move it.
	// Executable 是本进程启动时的路径，供人读文件时参考；不参与匹配——升级和软链接都
	// 会让它变化。
	Executable string
	// ClaimedAt records when the record was written, not when the process
	// started: it answers "how old is this claim", which is what an operator
	// asks first.
	// ClaimedAt 记录写入时刻而不是进程启动时刻：它回答"这份声明多久了"，这是运维最先
	// 会问的问题。
	ClaimedAt time.Time
	// Token is the platform identity of the process, empty when the platform
	// cannot provide one. An empty token means "unknown", never "matches".
	// Token 是进程在该平台上的身份，平台无法提供时为空。空 token 表示"未知"，绝不表示
	// "匹配"。
	Token string
}

// Identify describes the process that owns this call. Callers write pid files for
// themselves, so the executable is this image's own path.
// Identify 描述发起调用的进程。pid 文件由进程为自己写入，因此可执行文件就是本映像的路径。
func Identify(pid int) Identity {
	identity := Identity{PID: pid, ClaimedAt: time.Now(), Token: processToken(pid)}
	if executable, err := os.Executable(); err == nil {
		identity.Executable = executable
	}
	return identity
}

// Token returns the identity token of pid, or "" when the platform or the
// process cannot be inspected.
// Token 返回 pid 的身份 token；平台不支持或无法读取该进程时返回空串。
func Token(pid int) string {
	if pid <= 0 {
		return ""
	}
	return processToken(pid)
}
