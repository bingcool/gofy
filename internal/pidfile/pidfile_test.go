package pidfile_test

import (
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
