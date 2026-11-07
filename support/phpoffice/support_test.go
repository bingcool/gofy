package phpoffice

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestColumnLetterRoundTrip(t *testing.T) {
	cases := map[int]string{1: "A", 26: "Z", 27: "AA", 52: "AZ", 53: "BA", MaxColumns: "XFD"}
	for index, want := range cases {
		got, err := columnLetter(index)
		if err != nil || got != want {
			t.Fatalf("columnLetter(%d) = %q (err %v), want %q", index, got, err, want)
		}
		back, err := columnIndex(got)
		if err != nil || back != index {
			t.Fatalf("columnIndex(%q) = %d (err %v), want %d", got, back, err, index)
		}
	}
	if _, err := columnLetter(0); err == nil {
		t.Fatal("columnLetter(0) accepted")
	}
	if _, err := columnLetter(MaxColumns + 1); err == nil {
		t.Fatal("columnLetter above the Excel limit accepted")
	}
	for _, letters := range []string{"", "a", "ABCD", "XFE", "1A"} {
		if _, err := columnIndex(letters); err == nil {
			t.Fatalf("columnIndex(%q) accepted", letters)
		}
	}
}

func TestNormalizeColumnWidths(t *testing.T) {
	widths, err := normalizeColumnWidths(map[string]float64{"A": 24, "B:D": 18})
	if err != nil {
		t.Fatalf("normalizeColumnWidths: %v", err)
	}
	got := make(map[string]columnRange, len(widths))
	for _, width := range widths {
		got[width.start+":"+width.end] = width
	}
	if got["A:A"].width != 24 || got["B:D"].width != 18 {
		t.Fatalf("normalized = %+v", got)
	}

	cases := []map[string]float64{
		{"a": 10},
		{"D:B": 10},
		{"A": 0},
		{"A": 256},
	}
	for _, bad := range cases {
		if _, err := normalizeColumnWidths(bad); !errors.Is(err, ErrInvalidExport) {
			t.Fatalf("normalizeColumnWidths(%v) = %v, want ErrInvalidExport", bad, err)
		}
	}
}

func TestNormalizeSheetName(t *testing.T) {
	if _, err := normalizeSheetName("订单"); err != nil {
		t.Fatalf("valid sheet rejected: %v", err)
	}
	for _, name := range []string{"", strings.Repeat("s", 32), "a/b", "a:b", "'x", "x'"} {
		if _, err := normalizeSheetName(name); !errors.Is(err, ErrInvalidExport) {
			t.Fatalf("sheet %q = %v, want ErrInvalidExport", name, err)
		}
	}
}

func TestNormalizeExportFileName(t *testing.T) {
	for input, want := range map[string]string{
		"orders":      "orders.xlsx",
		"orders.xlsx": "orders.xlsx",
		"orders.XLSX": "orders.XLSX",
	} {
		got, err := normalizeExportFileName(input)
		if err != nil || got != want {
			t.Fatalf("normalizeExportFileName(%q) = %q (err %v), want %q", input, got, err, want)
		}
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a:b", "a?b", "a\x00b"} {
		if _, err := normalizeExportFileName(bad); !errors.Is(err, ErrInvalidExport) {
			t.Fatalf("file name %q = %v, want ErrInvalidExport", bad, err)
		}
	}
	if _, err := normalizeExportFileName(strings.Repeat("x", MaxFileNameLength+1)); !errors.Is(err, ErrInvalidExport) {
		t.Fatal("too long file name accepted")
	}
}

func TestNormalizeExportHeaders(t *testing.T) {
	headers, err := normalizeExportHeaders([]string{"A", "B"})
	if err != nil || len(headers) != 2 {
		t.Fatalf("headers = %v (err %v)", headers, err)
	}
	if _, err := normalizeExportHeaders(nil); !errors.Is(err, ErrInvalidExport) {
		t.Fatal("empty headers accepted")
	}
	if _, err := normalizeExportHeaders([]string{"A", " "}); !errors.Is(err, ErrInvalidExport) {
		t.Fatal("blank header accepted")
	}
	if _, err := normalizeExportHeaders([]string{strings.Repeat("x", MaxCellLength+1)}); !errors.Is(err, ErrInvalidExport) {
		t.Fatal("too long header accepted")
	}
}

type stringerValue string

func (s stringerValue) String() string { return string(s) }

func TestNormalizeExportRow(t *testing.T) {
	stamp := time.Date(2026, 11, 7, 8, 9, 10, 0, time.UTC)
	row, err := normalizeExportRow(
		[]any{nil, true, 3, int64(4), float32(1.5), "text", stamp, stringerValue("str")},
		2,
		[]string{"a", "b", "c", "d", "e", "f", "g", "h"},
		"2006-01-02",
	)
	if err != nil {
		t.Fatalf("normalizeExportRow: %v", err)
	}
	if row[0] != "" || row[1] != true || row[2] != 3 || row[3] != int64(4) {
		t.Fatalf("normalized scalars = %#v", row)
	}
	if row[4] != 1.5 || row[5] != "text" || row[6] != "2026-11-07" || row[7] != "str" {
		t.Fatalf("normalized values = %#v", row)
	}

	if _, err := normalizeExportRow([]any{"a"}, 1, []string{"a", "b"}, ""); !errors.Is(err, ErrInvalidRow) {
		t.Fatal("wrong width accepted")
	}
	if _, err := normalizeExportRow([]any{struct{}{}}, 1, []string{"a"}, ""); !errors.Is(err, ErrInvalidRow) {
		t.Fatal("unsupported type accepted")
	}
	if _, err := normalizeExportRow([]any{strings.Repeat("x", MaxCellLength+1)}, 1, []string{"a"}, ""); !errors.Is(err, ErrInvalidRow) {
		t.Fatal("too long text accepted")
	}
}

func TestIsBlankRow(t *testing.T) {
	if !isBlankRow([]any{nil, "", "  "}) {
		t.Fatal("blank row not detected")
	}
	if isBlankRow([]any{"", 0}) {
		t.Fatal("row with zero value reported as blank")
	}
}
