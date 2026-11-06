package console

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirmColorsPrompt(t *testing.T) {
	var out bytes.Buffer
	c := &Console{Out: &out, Err: &out, Color: true}
	ok, err := c.Confirm(strings.NewReader("yes\n"), "你确定 [%s] 应用【%s】? (yes or no): ", "停止", "demo-service")
	if err != nil || !ok {
		t.Fatalf("Confirm = %v, %v", ok, err)
	}
	want := "\x1b[33m\x1b[1m你确定 [停止] 应用【demo-service】? (yes or no): \x1b[0m"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("prompt not colored: %q", out.String())
	}
}

func TestTableDrawsRowDividers(t *testing.T) {
	var out bytes.Buffer
	c := &Console{Out: &out, Err: &out}
	c.Table([][]string{
		{"NAME", "DESCRIPTION"},
		{"fix-order", "fix inconsistent order data"},
	})
	got := out.String()
	for _, want := range []string{"├", "┼", "┤", "NAME", "fix-order"} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "├") != 1 {
		t.Fatalf("table dividers = %d, want 1:\n%s", strings.Count(got, "├"), got)
	}
}

func TestConfirmPlainWhenColorDisabled(t *testing.T) {
	var out bytes.Buffer
	c := &Console{Out: &out, Err: &out}
	ok, err := c.Confirm(strings.NewReader("no\n"), "你确定 [%s] 应用【%s】? (yes or no): ", "停止", "demo-service")
	if err != nil || ok {
		t.Fatalf("Confirm = %v, %v", ok, err)
	}
	if got, want := out.String(), "你确定 [停止] 应用【demo-service】? (yes or no): \n"; got != want {
		t.Fatalf("Confirm wrote %q, want %q", got, want)
	}
}
