package logging

import (
	"os"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestColorErrorEncoder(t *testing.T) {
	enc := newColorErrorEncoder(newEncoder(Config{Encoding: "console"}))

	errBuf, err := enc.EncodeEntry(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "boom"}, nil)
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer errBuf.Free()
	out := errBuf.String()
	if !strings.HasPrefix(out, "\x1b[31m") || !strings.HasSuffix(out, "\x1b[0m") {
		t.Fatalf("error entry is not wrapped in red: %q", out)
	}

	warnBuf, err := enc.EncodeEntry(zapcore.Entry{Level: zapcore.WarnLevel, Message: "slow"}, nil)
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer warnBuf.Free()
	if strings.Contains(warnBuf.String(), "\x1b[") {
		t.Fatalf("warn entry contains ANSI codes: %q", warnBuf.String())
	}
}

func TestColorEnabledIgnoresNonTerminals(t *testing.T) {
	if colorEnabled(nil) {
		t.Fatal("colorEnabled(nil) = true")
	}
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()
	if colorEnabled(f) {
		t.Fatal("colorEnabled(regular file) = true")
	}
}

func TestColorErrorEncoderClone(t *testing.T) {
	enc := newColorErrorEncoder(newEncoder(Config{Encoding: "json"}))
	clone, ok := enc.Clone().(colorErrorEncoder)
	if !ok {
		t.Fatalf("Clone returned %T, want colorErrorEncoder", enc.Clone())
	}
	buf, err := clone.EncodeEntry(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "boom"}, nil)
	if err != nil {
		t.Fatalf("EncodeEntry: %v", err)
	}
	defer buf.Free()
	if !strings.HasPrefix(buf.String(), "\x1b[31m") {
		t.Fatalf("cloned encoder lost the red wrapper: %q", buf.String())
	}
}
