package gofy

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bingcool/gofy/internal/pidfile"
)

func TestDuplicatePID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.pid")
	self := os.Getpid()

	if _, ok := duplicatePID("", self); ok {
		t.Fatal("empty pid path reported a duplicate")
	}
	if _, ok := duplicatePID(filepath.Join(dir, "missing.pid"), self); ok {
		t.Fatal("missing pid file reported a duplicate")
	}

	if err := pidfile.Write(path, self, 0o600); err != nil {
		t.Fatalf("write own pid: %v", err)
	}
	if _, ok := duplicatePID(path, self); ok {
		t.Fatal("own pid reported a duplicate after a self re-exec")
	}

	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getppid())), 0o600); err != nil {
		t.Fatalf("write parent pid: %v", err)
	}
	if _, ok := duplicatePID(path, self); !ok {
		t.Fatal("a live pid belonging to another process was not reported")
	}
}
