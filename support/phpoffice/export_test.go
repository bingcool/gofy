package phpoffice_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/phpoffice"
	"github.com/xuri/excelize/v2"
)

func TestExportWritesWorkbook(t *testing.T) {
	dir := t.TempDir()
	freeze := 1
	created := time.Date(2026, 11, 7, 10, 30, 0, 0, time.UTC)
	result, err := phpoffice.Export(context.Background(), dir, phpoffice.ExportSpec{
		FileName:  "orders",
		SheetName: "订单",
		Headers:   []string{"订单号", "金额", "状态", "下单时间"},
		Rows: func(yield func([]any) error) error {
			if err := yield([]any{"00123", 12.5, true, created}); err != nil {
				return err
			}
			return yield([]any{"00124", 3, false, created.Add(time.Hour)})
		},
		ColumnWidths: map[string]float64{"A": 24, "B:C": 18},
		FreezeRows:   &freeze,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.FileName != "orders.xlsx" || result.RowCount != 2 || result.Size <= 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("output file: %v", err)
	}

	file, err := excelize.OpenFile(result.Path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = file.Close() }()

	if got := file.GetSheetName(0); got != "订单" {
		t.Fatalf("sheet = %q, want 订单", got)
	}
	if got, _ := file.GetCellValue("订单", "A2"); got != "00123" {
		t.Fatalf("A2 = %q, want the text 00123", got)
	}
	if got, _ := file.GetCellValue("订单", "B2"); got != "12.5" {
		t.Fatalf("B2 = %q, want 12.5", got)
	}
	if got, _ := file.GetCellValue("订单", "D2"); got != "2026-11-07 10:30:00" {
		t.Fatalf("D2 = %q, want the formatted date", got)
	}
	panes, err := file.GetPanes("订单")
	if err != nil {
		t.Fatalf("GetPanes: %v", err)
	}
	if panes.YSplit != 1 {
		t.Fatalf("YSplit = %d, want 1", panes.YSplit)
	}
	if width, err := file.GetColWidth("订单", "A"); err != nil || width != 24 {
		t.Fatalf("col A width = %v (err %v), want 24", width, err)
	}
	if width, err := file.GetColWidth("订单", "C"); err != nil || width != 18 {
		t.Fatalf("col C width = %v (err %v), want 18", width, err)
	}
}

func TestExportDefaults(t *testing.T) {
	dir := t.TempDir()
	result, err := phpoffice.Export(context.Background(), dir, phpoffice.ExportSpec{
		Headers: []string{"A"},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.HasPrefix(result.FileName, "export_") || !strings.HasSuffix(result.FileName, ".xlsx") {
		t.Fatalf("default file name = %q", result.FileName)
	}
	if result.SheetName != "Sheet1" || result.RowCount != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	file, err := excelize.OpenFile(result.Path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = file.Close() }()
	panes, err := file.GetPanes("Sheet1")
	if err != nil {
		t.Fatalf("GetPanes: %v", err)
	}
	if panes.YSplit != 1 {
		t.Fatalf("default freeze = %d, want 1", panes.YSplit)
	}
}

func TestExportDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	spec := phpoffice.ExportSpec{FileName: "same", Headers: []string{"A"}}
	first, err := phpoffice.Export(context.Background(), dir, spec)
	if err != nil {
		t.Fatalf("first Export: %v", err)
	}
	before, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}

	if _, err := phpoffice.Export(context.Background(), dir, spec); !errors.Is(err, phpoffice.ErrInvalidExport) {
		t.Fatalf("second Export = %v, want ErrInvalidExport", err)
	}
	after, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("the existing file was modified")
	}
}

func TestExportRejectsInvalidRows(t *testing.T) {
	cases := map[string]struct {
		row []any
	}{
		"wrong width":      {row: []any{"only-one"}},
		"unsupported type": {row: []any{struct{}{}, "b"}},
		"too long cell":    {row: []any{strings.Repeat("x", phpoffice.MaxCellLength+1), "b"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := phpoffice.Export(context.Background(), dir, phpoffice.ExportSpec{
				FileName: "bad",
				Headers:  []string{"A", "B"},
				Rows: func(yield func([]any) error) error {
					return yield(tc.row)
				},
			})
			if !errors.Is(err, phpoffice.ErrInvalidRow) {
				t.Fatalf("Export = %v, want ErrInvalidRow", err)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("failed export left files behind: %v", entries)
			}
		})
	}
}

func TestExportRejectsInvalidSpec(t *testing.T) {
	dir := t.TempDir()
	freeze := 2
	cases := map[string]phpoffice.ExportSpec{
		"no headers":        {Headers: nil},
		"blank header":      {Headers: []string{"A", " "}},
		"bad sheet":         {Headers: []string{"A"}, SheetName: "bad/name"},
		"long sheet":        {Headers: []string{"A"}, SheetName: strings.Repeat("s", 32)},
		"path in file name": {Headers: []string{"A"}, FileName: "a/b.xlsx"},
		"bad width range":   {Headers: []string{"A"}, ColumnWidths: map[string]float64{"a": 10}},
		"reversed range":    {Headers: []string{"A"}, ColumnWidths: map[string]float64{"C:A": 10}},
		"small width":       {Headers: []string{"A"}, ColumnWidths: map[string]float64{"A": 0.05}},
		"freeze too big":    {Headers: []string{"A"}, FreezeRows: &freeze},
		"no header freeze": {Headers: []string{"A"}, Hooks: phpoffice.ExportHooks{
			WriteHeader: func(*excelize.File, string, []string, int) (int, error) { return 0, nil },
		}},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := phpoffice.Export(context.Background(), dir, spec); !errors.Is(err, phpoffice.ErrInvalidExport) {
				t.Fatalf("Export = %v, want ErrInvalidExport", err)
			}
		})
	}
}

func TestExportHooks(t *testing.T) {
	dir := t.TempDir()
	var styleSize float64
	result, err := phpoffice.Export(context.Background(), dir, phpoffice.ExportSpec{
		FileName: "hooked",
		Headers:  []string{"订单号", "金额", "状态", "时间"},
		Rows: func(yield func([]any) error) error {
			return yield([]any{"A1", 1, "paid", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
		},
		Hooks: phpoffice.ExportHooks{
			ConfigureHeaderStyle: func(style *excelize.Style) {
				style.Font.Size = 12
				styleSize = style.Font.Size
			},
			WriteHeader: func(file *excelize.File, sheet string, headers []string, styleID int) (int, error) {
				if err := file.SetCellStr(sheet, "A1", "订单报表"); err != nil {
					return 0, err
				}
				if err := file.MergeCell(sheet, "A1", "D1"); err != nil {
					return 0, err
				}
				for i, header := range headers {
					cell, err := excelize.CoordinatesToCellName(i+1, 2)
					if err != nil {
						return 0, err
					}
					if err := file.SetCellStr(sheet, cell, header); err != nil {
						return 0, err
					}
				}
				if err := file.SetCellStyle(sheet, "A2", "D2", styleID); err != nil {
					return 0, err
				}
				return 2, nil
			},
			AfterRows: func(file *excelize.File, ctx phpoffice.ExportContext) error {
				if ctx.FirstDataRowIndex != 2 || ctx.NextRowNumber() != 4 {
					t.Fatalf("context = %+v", ctx)
				}
				return file.SetCellStr("Sheet1", "A4", "合计")
			},
			AutoFilterRange: func(ctx phpoffice.ExportContext) (string, bool) {
				if ctx.DefaultAutoFilterRange() != "A1:D3" {
					t.Fatalf("default filter = %q", ctx.DefaultAutoFilterRange())
				}
				return "A2:D3", true
			},
			BeforeSave: func(file *excelize.File, ctx phpoffice.ExportContext) error {
				return file.SetCellStr("Sheet1", "F1", "saved")
			},
		},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if styleSize != 12 {
		t.Fatalf("custom header style was not applied, font size = %v", styleSize)
	}

	file, err := excelize.OpenFile(result.Path)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer func() { _ = file.Close() }()
	for cell, want := range map[string]string{
		"A1": "订单报表",
		"A2": "订单号",
		"A3": "A1",
		"A4": "合计",
		"F1": "saved",
	} {
		if got, _ := file.GetCellValue("Sheet1", cell); got != want {
			t.Fatalf("%s = %q, want %q", cell, got, want)
		}
	}
	if panes, _ := file.GetPanes("Sheet1"); panes.YSplit != 1 {
		t.Fatalf("freeze default = %d, want the 1-row default", panes.YSplit)
	}
}

func TestExportCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := phpoffice.Export(ctx, t.TempDir(), phpoffice.ExportSpec{
		Headers: []string{"A"},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Export = %v, want context.Canceled", err)
	}
}

func TestExportResultPathIsAbsolute(t *testing.T) {
	result, err := phpoffice.Export(context.Background(), t.TempDir(), phpoffice.ExportSpec{
		Headers: []string{"A"},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !filepath.IsAbs(result.Path) {
		t.Fatalf("result path %q is not absolute", result.Path)
	}
}
