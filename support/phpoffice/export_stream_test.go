package phpoffice_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/bingcool/gofy/support/phpoffice"
	"github.com/xuri/excelize/v2"
)

func TestExportStreamWritesRows(t *testing.T) {
	dir := t.TempDir()
	const total = 1200
	result, err := phpoffice.ExportStream(context.Background(), dir, phpoffice.StreamExportSpec{
		FileName:     "stream",
		SheetName:    "大数据",
		Headers:      []string{"序号", "名称"},
		ColumnWidths: map[string]float64{"A": 10, "B:C": 20},
		Rows: func(yield func([]any) error) error {
			for i := 1; i <= total; i++ {
				if err := yield([]any{i, "name-" + strconv.Itoa(i)}); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ExportStream: %v", err)
	}
	if result.RowCount != total || result.SheetName != "大数据" || result.Size <= 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	file, err := excelize.OpenFile(result.Path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = file.Close() }()

	if got, _ := file.GetCellValue("大数据", "A1"); got != "序号" {
		t.Fatalf("A1 = %q", got)
	}
	if got, _ := file.GetCellValue("大数据", "A2"); got != "1" {
		t.Fatalf("A2 = %q", got)
	}
	if got, _ := file.GetCellValue("大数据", "B1201"); got != "name-1200" {
		t.Fatalf("B1201 = %q", got)
	}
	panes, err := file.GetPanes("大数据")
	if err != nil {
		t.Fatalf("GetPanes: %v", err)
	}
	if panes.YSplit != 1 {
		t.Fatalf("default freeze = %d, want 1", panes.YSplit)
	}
	if width, err := file.GetColWidth("大数据", "B"); err != nil || width != 20 {
		t.Fatalf("col B width = %v (err %v), want 20", width, err)
	}
}

func TestExportStreamNoFreeze(t *testing.T) {
	off := 0
	result, err := phpoffice.ExportStream(context.Background(), t.TempDir(), phpoffice.StreamExportSpec{
		Headers:    []string{"A"},
		FreezeRows: &off,
	})
	if err != nil {
		t.Fatalf("ExportStream: %v", err)
	}
	file, err := excelize.OpenFile(result.Path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = file.Close() }()
	if panes, _ := file.GetPanes("Sheet1"); panes.YSplit != 0 {
		t.Fatalf("YSplit = %d, want 0", panes.YSplit)
	}
}

func TestExportStreamRejectsInvalidRows(t *testing.T) {
	dir := t.TempDir()
	_, err := phpoffice.ExportStream(context.Background(), dir, phpoffice.StreamExportSpec{
		FileName: "bad",
		Headers:  []string{"A", "B"},
		Rows: func(yield func([]any) error) error {
			return yield([]any{"only-one"})
		},
	})
	if !errors.Is(err, phpoffice.ErrInvalidRow) {
		t.Fatalf("ExportStream = %v, want ErrInvalidRow", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("failed stream export left files behind: %v", entries)
	}
}

func TestExportStreamRejectsFreezeAboveHeader(t *testing.T) {
	freeze := 2
	dir := t.TempDir()
	_, err := phpoffice.ExportStream(context.Background(), dir, phpoffice.StreamExportSpec{
		Headers:    []string{"A"},
		FreezeRows: &freeze,
	})
	if !errors.Is(err, phpoffice.ErrInvalidExport) {
		t.Fatalf("ExportStream = %v, want ErrInvalidExport", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("rejected stream export left files behind: %v", entries)
	}
}

func TestExportStreamDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	spec := phpoffice.StreamExportSpec{FileName: "same", Headers: []string{"A"}}
	if _, err := phpoffice.ExportStream(context.Background(), dir, spec); err != nil {
		t.Fatalf("first ExportStream: %v", err)
	}
	if _, err := phpoffice.ExportStream(context.Background(), dir, spec); !errors.Is(err, phpoffice.ErrInvalidExport) {
		t.Fatalf("second ExportStream = %v, want ErrInvalidExport", err)
	}
}

func TestExportStreamCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := phpoffice.ExportStream(ctx, t.TempDir(), phpoffice.StreamExportSpec{
		Headers: []string{"A"},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExportStream = %v, want context.Canceled", err)
	}
}

func TestExportStreamRejectsUnsafeFileName(t *testing.T) {
	_, err := phpoffice.ExportStream(context.Background(), t.TempDir(), phpoffice.StreamExportSpec{
		FileName: "../x",
		Headers:  []string{"A"},
	})
	if !errors.Is(err, phpoffice.ErrInvalidExport) {
		t.Fatalf("ExportStream = %v, want ErrInvalidExport", err)
	}
	if strings.Contains(err.Error(), "output") {
		// Only make sure the error comes from argument validation, not from writing
		// to disk.
		// 只是确保错误来自参数校验而不是落盘。
		t.Fatalf("unexpected error: %v", err)
	}
}
