package pidfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bingcool/gofy/internal/pidfile"
)

func TestWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "app.pid")

	if err := pidfile.Write(path, 4242, 0o640); err != nil {
		t.Fatalf("Write: %v", err)
	}
	pid, err := pidfile.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if pid != 4242 {
		t.Fatalf("pid = %d, want 4242", pid)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("perm = %#o, want 0640", got)
	}

	// A rewrite replaces the previous value and leaves no temporary files
	// behind.
	// 重写会替换旧值，且不残留任何临时文件。
	if err := pidfile.Write(path, 7, 0o644); err != nil {
		t.Fatalf("Write overwrite: %v", err)
	}
	if pid, _ := pidfile.Read(path); pid != 7 {
		t.Fatalf("pid after overwrite = %d, want 7", pid)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries, want only the pid file", len(entries))
	}
}

func TestReadRejectsInvalidContents(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"non numeric": "abc",
		"zero":        "0",
		"negative":    "-2",
		"empty":       "",
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name+".pid")
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := pidfile.Read(path); err == nil {
				t.Fatal("Read accepted invalid contents")
			}
		})
	}
}

func TestRemoveIfOwnerOnlyRemovesOwnFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.pid")
	if err := pidfile.Write(path, 100, 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := pidfile.RemoveIfOwner(path, 999); err != nil {
		t.Fatalf("RemoveIfOwner(other): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file was removed by a non-owner: %v", err)
	}

	if err := pidfile.RemoveIfOwner(path, 100); err != nil {
		t.Fatalf("RemoveIfOwner(owner): %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after owner removal: %v", err)
	}

	if err := pidfile.RemoveIfOwner(path, 100); err != nil {
		t.Fatalf("RemoveIfOwner on missing file: %v", err)
	}
}

func TestResolve(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "app.pid")
	if got := pidfile.Resolve("  ", "/tmp"); got != "" {
		t.Fatalf("Resolve(empty) = %q, want empty", got)
	}
	if got := pidfile.Resolve(abs, "/tmp"); got != abs {
		t.Fatalf("Resolve(abs) = %q, want %q", got, abs)
	}
	if got, want := pidfile.Resolve("run/app.pid", "/srv"), filepath.Join("/srv", "run", "app.pid"); got != want {
		t.Fatalf("Resolve(relative) = %q, want %q", got, want)
	}
}

// TestWriteRecordCarriesIdentity pins the upgrade: the file is no longer a bare
// number, and the record it holds can be verified against its own process.
// TestWriteRecordCarriesIdentity 固定这次升级：文件不再是裸数字，它保存的记录能被它自己
// 的进程验证为匹配。
func TestWriteRecordCarriesIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.pid")
	if err := pidfile.WriteRecord(path, pidfile.NewRecord(os.Getpid()), 0o644); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	record, err := pidfile.ReadRecord(path)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if record.PID != os.Getpid() {
		t.Fatalf("pid = %d, want %d", record.PID, os.Getpid())
	}
	if record.Executable == "" {
		t.Fatal("record has no executable; a reader cannot tell what wrote it")
	}
	if record.ClaimedAt == "" {
		t.Fatal("record has no claimed_at; an operator cannot tell how old the claim is")
	}
	if record.Token == "" {
		t.Skip("platform provides no process token; identity is unknown by design")
	}
	if got := record.Verify(); got != pidfile.IdentityMatch {
		t.Fatalf("Verify = %v, want IdentityMatch for the writing process", got)
	}
}

// TestReadRecordAcceptsLegacyInteger keeps rolling upgrades working: a pid file
// written by an older build is a bare number and must still be readable.
// TestReadRecordAcceptsLegacyInteger 让滚动升级可用：旧版本写下的 pid 文件是裸数字，
// 必须仍能读出。
func TestReadRecordAcceptsLegacyInteger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.pid")
	if err := os.WriteFile(path, []byte("1234\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	record, err := pidfile.ReadRecord(path)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if record.PID != 1234 {
		t.Fatalf("pid = %d, want 1234", record.PID)
	}
	if record.Token != "" {
		t.Fatalf("legacy record invented a token %q", record.Token)
	}
	if got := record.Verify(); got != pidfile.IdentityUnknown {
		t.Fatalf("Verify = %v, want IdentityUnknown for a legacy record", got)
	}
}

// TestVerifyDetectsRecycledPID is the reason the format changed: the pid still
// exists but the process behind it is not the one that wrote the file, so stop
// must refuse to signal it.
// TestVerifyDetectsRecycledPID 是改变格式的理由：pid 仍然存在，但背后的进程已不是写下
// 文件的那个，因此 stop 必须拒绝向它发信号。
func TestVerifyDetectsRecycledPID(t *testing.T) {
	record := pidfile.NewRecord(os.Getpid())
	if record.Token == "" {
		t.Skip("platform provides no process token; the case cannot be constructed")
	}
	record.Token = "different-process"
	if got := record.Verify(); got != pidfile.IdentityMismatch {
		t.Fatalf("Verify = %v, want IdentityMismatch for a token that cannot be the current process", got)
	}
}

// TestWriteRecordIfOwnerRefusesAForeignFile pins the guard that keeps a refresh
// from overwriting a newer instance's record.
// TestWriteRecordIfOwnerRefusesAForeignFile 固定"刷新不得覆盖新实例记录"这一保护。
func TestWriteRecordIfOwnerRefusesAForeignFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.pid")
	if err := pidfile.Write(path, 4242, 0o640); err != nil {
		t.Fatalf("Write: %v", err)
	}

	err := pidfile.WriteRecordIfOwner(path, pidfile.NewRecord(9999), 0o640)
	if !errors.Is(err, pidfile.ErrNotOwner) {
		t.Fatalf("WriteRecordIfOwner(foreign) = %v, want ErrNotOwner", err)
	}
	pid, readErr := pidfile.Read(path)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}
	if pid != 4242 {
		t.Fatalf("pid = %d, want the original 4242", pid)
	}
}

// TestWriteRecordIfOwnerCreatesAndRewritesItsOwnFile covers the two allowed
// paths: the first write and a later refresh.
// TestWriteRecordIfOwnerCreatesAndRewritesItsOwnFile 覆盖两条允许路径：首次写入与
// 之后的刷新。
func TestWriteRecordIfOwnerCreatesAndRewritesItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.pid")

	if err := pidfile.WriteRecordIfOwner(path, pidfile.NewRecord(4242), 0o640); err != nil {
		t.Fatalf("WriteRecordIfOwner(missing file): %v", err)
	}
	if err := pidfile.WriteRecordIfOwner(path, pidfile.NewRecord(4242), 0o640); err != nil {
		t.Fatalf("WriteRecordIfOwner(own file): %v", err)
	}
	pid, err := pidfile.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if pid != 4242 {
		t.Fatalf("pid = %d, want 4242", pid)
	}
}
