// Package console renders human-facing CLI output: colored status lines,
// confirmation prompts and simple tables. Color is disabled when the writer is
// not a terminal, when NO_COLOR is set, or on Windows.
// 包 console 渲染面向人的命令行输出：带颜色的状态行、确认提示和简单表格。
// 当 writer 不是终端、设置了 NO_COLOR 或运行在 Windows 上时，颜色会被关闭，
// 因此重定向到文件或管道的输出不会混入 ANSI 转义码。
package console

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// Console writes human-facing CLI output to Out and Err.
// Console 把面向人的命令行输出写到 Out 和 Err；Color 决定是否着色，可由调用方覆盖。
type Console struct {
	Out   io.Writer
	Err   io.Writer
	Color bool
}

// New builds a Console. Color is detected from Out.
// New 构建 Console，并依据 Out 是否为终端自动探测是否着色；传入 nil 时输出会 panic。
func New(out, errOut io.Writer) *Console {
	return &Console{Out: out, Err: errOut, Color: colorEnabled(out)}
}

func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || runtime.GOOS == "windows" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

func (c *Console) paint(code, s string) string {
	if !c.Color {
		return s
	}
	return code + s + reset
}

func (c *Console) line(code, label, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if label == "" {
		fmt.Fprintln(c.Out, c.paint(code, msg))
		return
	}
	fmt.Fprintf(c.Out, "%s %s\n", c.paint(code+bold, label), msg)
}

// Info prints a cyan informational line.
// Info 输出一行青色的提示信息。
func (c *Console) Info(format string, args ...any) { c.line(cyan, "[INFO]", format, args...) }

// Success prints a green success line.
// Success 输出一行绿色的成功信息。
func (c *Console) Success(format string, args ...any) { c.line(green, "[ OK ]", format, args...) }

// Warn prints a yellow warning line.
// Warn 输出一行黄色的警告信息。
func (c *Console) Warn(format string, args ...any) { c.line(yellow, "[WARN]", format, args...) }

// Error prints a red error line.
// Error 输出一行红色的错误信息。
func (c *Console) Error(format string, args ...any) { c.line(red, "[FAIL]", format, args...) }

// Note prints a plain line.
// Note 输出一行无色无前缀的普通文本。
func (c *Console) Note(format string, args ...any) { c.line("", "", format, args...) }

// Confirm writes a yellow prompt and reports whether the next input line is
// "yes". EOF and any other answer count as a refusal.
// Confirm 先写出黄色提示，再判断下一行输入是否为 "yes"。EOF 和其他任何回答都算拒绝，
// 调用方据此决定是否继续破坏性操作；r 为 nil 时直接返回 false。
func (c *Console) Confirm(r io.Reader, format string, args ...any) (bool, error) {
	if r == nil {
		return false, nil
	}
	fmt.Fprint(c.Out, c.paint(yellow+bold, fmt.Sprintf(format, args...)))
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			fmt.Fprintln(c.Out)
			return false, err
		}
		fmt.Fprintln(c.Out)
		return false, nil
	}
	fmt.Fprintln(c.Out)
	return strings.EqualFold(strings.TrimSpace(scanner.Text()), "yes"), nil
}

// Table renders rows as a bordered two-or-more column table.
// Table 把行渲染成带边框的多列表格；rows 为空时不输出任何内容，各列按显示宽度对齐。
func (c *Console) Table(rows [][]string) {
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	widths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row {
			if w := displayWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}

	c.border(widths, "┌", "┬", "┐")
	for i, row := range rows {
		var b strings.Builder
		b.WriteString(c.paint(cyan, "│"))
		for i, width := range widths {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			b.WriteByte(' ')
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", width-displayWidth(cell)))
			b.WriteString(" ")
			b.WriteString(c.paint(cyan, "│"))
		}
		fmt.Fprintln(c.Out, b.String())
		if i < len(rows)-1 {
			c.border(widths, "├", "┼", "┤")
		}
	}
	c.border(widths, "└", "┴", "┘")
}

func (c *Console) border(widths []int, left, mid, right string) {
	var b strings.Builder
	b.WriteString(c.paint(cyan, left))
	for i, w := range widths {
		b.WriteString(c.paint(cyan, strings.Repeat("─", w+2)))
		if i < len(widths)-1 {
			b.WriteString(c.paint(cyan, mid))
		}
	}
	b.WriteString(c.paint(cyan, right))
	fmt.Fprintln(c.Out, b.String())
}

// displayWidth approximates terminal columns, treating CJK runes as wide.
// displayWidth 近似计算终端列宽，把 CJK 字符按双宽处理，以保证表格对齐；
// 这只是估值，不处理组合字符和 emoji。
func displayWidth(s string) int {
	width := 0
	for _, r := range s {
		switch {
		case r < 32:
		case isWide(r):
			width += 2
		default:
			width++
		}
	}
	return width
}

func isWide(r rune) bool {
	return r >= 0x1100 && (r <= 0x115F ||
		r == 0x2329 || r == 0x232A ||
		(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6))
}
