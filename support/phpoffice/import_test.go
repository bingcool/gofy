package phpoffice_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bingcool/gofy/support/phpoffice"
	"github.com/xuri/excelize/v2"
)

// writeOrderWorkbook 生成测试用工作簿：表头 + 两行数据 + 一行空白行。
func writeOrderWorkbook(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "orders.xlsx")
	file := excelize.NewFile()
	defer func() { _ = file.Close() }()

	headers := []string{"订单号", "金额", "状态", "下单时间"}
	for i, header := range headers {
		cell, err := excelize.CoordinatesToCellName(i+1, 1)
		if err != nil {
			t.Fatalf("header cell: %v", err)
		}
		if err := file.SetCellStr("Sheet1", cell, header); err != nil {
			t.Fatalf("set header: %v", err)
		}
	}
	if err := file.SetCellStr("Sheet1", "A2", "00123"); err != nil {
		t.Fatalf("A2: %v", err)
	}
	if err := file.SetCellFloat("Sheet1", "B2", 12.5, -1, 64); err != nil {
		t.Fatalf("B2: %v", err)
	}
	if err := file.SetCellStr("Sheet1", "C2", "paid"); err != nil {
		t.Fatalf("C2: %v", err)
	}
	if err := file.SetCellFloat("Sheet1", "D2", 45123.5, -1, 64); err != nil {
		t.Fatalf("D2: %v", err)
	}
	if err := file.SetCellStr("Sheet1", "A3", " "); err != nil {
		t.Fatalf("A3: %v", err)
	}
	if err := file.SetCellStr("Sheet1", "A4", "00124"); err != nil {
		t.Fatalf("A4: %v", err)
	}
	if err := file.SetCellFloat("Sheet1", "B4", 3, -1, 64); err != nil {
		t.Fatalf("B4: %v", err)
	}
	if err := file.SetCellStr("Sheet1", "C4", "pending"); err != nil {
		t.Fatalf("C4: %v", err)
	}
	if err := file.SetCellFloat("Sheet1", "D4", 45124, -1, 64); err != nil {
		t.Fatalf("D4: %v", err)
	}
	if err := file.SaveAs(path); err != nil {
		t.Fatalf("save workbook: %v", err)
	}
	return path
}

func TestReadWorkbook(t *testing.T) {
	path := writeOrderWorkbook(t)
	wantTime, err := excelize.ExcelDateToTime(45123.5, false)
	if err != nil {
		t.Fatalf("ExcelDateToTime: %v", err)
	}

	spec := phpoffice.ImportSpec{
		HeaderRow:       1,
		ExpectedHeaders: []string{"订单号", "金额", "状态", "下单时间"},
		CellTypes:       map[int]phpoffice.CellType{1: phpoffice.CellFloat, 3: phpoffice.CellTime},
	}
	result, err := phpoffice.Read(context.Background(), path, spec)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if result.RowCount != 2 || result.SkippedRowCount != 1 || result.LastRowNumber != 4 {
		t.Fatalf("unexpected counts: %+v", result)
	}
	if result.SheetName != "Sheet1" || result.HeaderRowNumber != 1 {
		t.Fatalf("unexpected meta: %+v", result)
	}
	row := result.Rows[0]
	if row.RowNumber != 2 {
		t.Fatalf("row number = %d, want 2", row.RowNumber)
	}
	if row.Cells[0] != "00123" {
		t.Fatalf("order number = %v, want the text 00123", row.Cells[0])
	}
	if amount, ok := row.Cells[1].(float64); !ok || amount != 12.5 {
		t.Fatalf("amount = %#v, want 12.5", row.Cells[1])
	}
	if status, ok := row.Cells[2].(string); !ok || status != "paid" {
		t.Fatalf("status = %#v, want paid", row.Cells[2])
	}
	created, ok := row.Cells[3].(time.Time)
	if !ok || !created.Equal(wantTime) {
		t.Fatalf("created = %#v, want %v", row.Cells[3], wantTime)
	}
	if row.Values["订单号"] != "00123" {
		t.Fatalf("values mapping = %#v", row.Values)
	}
	if row.IsEmpty() {
		t.Fatal("data row reported as empty")
	}
}

func TestImportStreamsRows(t *testing.T) {
	path := writeOrderWorkbook(t)
	var handled []phpoffice.Row
	var afterCount int
	result, err := phpoffice.Import(context.Background(), path, phpoffice.ImportSpec{
		HeaderRow: 1,
		HandleRow: func(row phpoffice.Row) error {
			handled = append(handled, row)
			return nil
		},
		AfterImport: func(rowCount int) error {
			afterCount = rowCount
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(handled) != 2 || result.RowCount != 2 || afterCount != 2 {
		t.Fatalf("handled=%d result=%d after=%d", len(handled), result.RowCount, afterCount)
	}
	if result.SkippedRowCount != 1 {
		t.Fatalf("skipped = %d, want 1", result.SkippedRowCount)
	}
	if handled[1].Value("订单号", "") != "00124" {
		t.Fatalf("second row value = %#v", handled[1].Values)
	}
}

func TestReadWithoutHeader(t *testing.T) {
	path := writeOrderWorkbook(t)
	result, err := phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{HeaderRow: 0})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if result.RowCount != 3 || result.SkippedRowCount != 1 {
		t.Fatalf("row count = %d, skipped = %d, want 3 and 1", result.RowCount, result.SkippedRowCount)
	}
	if len(result.Headers) != 0 || result.HeaderRowNumber != 0 {
		t.Fatalf("headers = %v, row = %d", result.Headers, result.HeaderRowNumber)
	}
	if len(result.Rows[0].Values) != 0 {
		t.Fatalf("values without header = %#v", result.Rows[0].Values)
	}
	if result.Rows[0].Cells[0] != "订单号" {
		t.Fatalf("first cell = %v", result.Rows[0].Cells[0])
	}
}

func TestReadDataStartRow(t *testing.T) {
	path := writeOrderWorkbook(t)
	result, err := phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{
		HeaderRow:    1,
		DataStartRow: 3,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if result.RowCount != 1 || result.Rows[0].RowNumber != 4 {
		t.Fatalf("rows = %+v", result.Rows)
	}
}

func TestReadExpectedHeaderMismatch(t *testing.T) {
	path := writeOrderWorkbook(t)
	_, err := phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{
		HeaderRow:       1,
		ExpectedHeaders: []string{"订单号", "金额"},
	})
	if !errors.Is(err, phpoffice.ErrInvalidImport) {
		t.Fatalf("Read = %v, want ErrInvalidImport", err)
	}
}

func TestReadUnknownSheet(t *testing.T) {
	path := writeOrderWorkbook(t)
	_, err := phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{
		SheetName: "missing",
	})
	if !errors.Is(err, phpoffice.ErrInvalidImport) {
		t.Fatalf("Read = %v, want ErrInvalidImport", err)
	}
}

func TestReadRejectsExtraColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extra.xlsx")
	file := excelize.NewFile()
	defer func() { _ = file.Close() }()
	for i, header := range []string{"A", "B"} {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = file.SetCellStr("Sheet1", cell, header)
	}
	_ = file.SetCellStr("Sheet1", "A2", "a")
	_ = file.SetCellStr("Sheet1", "B2", "b")
	_ = file.SetCellStr("Sheet1", "C2", "orphan")
	if err := file.SaveAs(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err := phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{HeaderRow: 1})
	if !errors.Is(err, phpoffice.ErrInvalidImportRow) {
		t.Fatalf("Read = %v, want ErrInvalidImportRow", err)
	}
}

func TestImportNormalizeCells(t *testing.T) {
	path := writeOrderWorkbook(t)
	result, err := phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{
		HeaderRow: 1,
		NormalizeCells: func(cells []any, rowNumber int) ([]any, error) {
			cells[2] = "normalized"
			return cells, nil
		},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if result.Rows[0].Cells[2] != "normalized" {
		t.Fatalf("cells = %#v", result.Rows[0].Cells)
	}

	_, err = phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{
		HeaderRow: 1,
		NormalizeCells: func(cells []any, rowNumber int) ([]any, error) {
			return cells[:2], nil
		},
	})
	if !errors.Is(err, phpoffice.ErrInvalidImportRow) {
		t.Fatalf("width mismatch = %v, want ErrInvalidImportRow", err)
	}
}

func TestImportCellTypeErrors(t *testing.T) {
	path := writeOrderWorkbook(t)
	if _, err := phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{
		HeaderRow: 1,
		CellTypes: map[int]phpoffice.CellType{2: phpoffice.CellInt},
	}); !errors.Is(err, phpoffice.ErrInvalidImportRow) {
		t.Fatalf("CellInt on text = %v, want ErrInvalidImportRow", err)
	}
	if _, err := phpoffice.Read(context.Background(), path, phpoffice.ImportSpec{
		HeaderRow: 1,
		CellTypes: map[int]phpoffice.CellType{2: phpoffice.CellTime},
	}); !errors.Is(err, phpoffice.ErrInvalidImportRow) {
		t.Fatalf("CellTime on text = %v, want ErrInvalidImportRow", err)
	}
}

func TestImportValidation(t *testing.T) {
	path := writeOrderWorkbook(t)
	empty := filepath.Join(t.TempDir(), "empty.xlsx")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}

	cases := map[string]struct {
		path string
		spec phpoffice.ImportSpec
		want error
	}{
		"missing file":            {path: filepath.Join(t.TempDir(), "nope.xlsx"), spec: phpoffice.ImportSpec{}, want: phpoffice.ErrInvalidImport},
		"empty file":              {path: empty, spec: phpoffice.ImportSpec{}, want: phpoffice.ErrInvalidImport},
		"header out of range":     {path: path, spec: phpoffice.ImportSpec{HeaderRow: 99}, want: phpoffice.ErrInvalidImport},
		"data before header":      {path: path, spec: phpoffice.ImportSpec{HeaderRow: 2, DataStartRow: 1}, want: phpoffice.ErrInvalidImport},
		"expected without header": {path: path, spec: phpoffice.ImportSpec{ExpectedHeaders: []string{"A"}}, want: phpoffice.ErrInvalidImport},
		"bad cell type index":     {path: path, spec: phpoffice.ImportSpec{CellTypes: map[int]phpoffice.CellType{phpoffice.MaxColumns: phpoffice.CellInt}}, want: phpoffice.ErrInvalidImport},
		"unknown cell type":       {path: path, spec: phpoffice.ImportSpec{CellTypes: map[int]phpoffice.CellType{0: phpoffice.CellType(99)}}, want: phpoffice.ErrInvalidImport},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := phpoffice.Read(context.Background(), tc.path, tc.spec); !errors.Is(err, tc.want) {
				t.Fatalf("Read = %v, want %v", err, tc.want)
			}
		})
	}

	if _, err := phpoffice.Import(context.Background(), path, phpoffice.ImportSpec{}); !errors.Is(err, phpoffice.ErrInvalidImport) {
		t.Fatalf("Import without HandleRow = %v, want ErrInvalidImport", err)
	}
}

func TestRowHelpers(t *testing.T) {
	row := phpoffice.Row{
		RowNumber: 2,
		Cells:     []any{"a", nil, 3},
		Values:    map[string]any{"A": "a", "C": 3},
	}
	if got := row.Cell(0, "def"); got != "a" {
		t.Fatalf("Cell(0) = %v", got)
	}
	if got := row.Cell(1, "def"); got != "def" {
		t.Fatalf("Cell(1) = %v, want default", got)
	}
	if got := row.Cell(9, "def"); got != "def" {
		t.Fatalf("Cell(9) = %v, want default", got)
	}
	if got := row.Value("A", "def"); got != "a" {
		t.Fatalf("Value(A) = %v", got)
	}
	if got := row.Value("B", "def"); got != "def" {
		t.Fatalf("Value(B) = %v, want default", got)
	}
	if row.IsEmpty() {
		t.Fatal("row with values reported as empty")
	}
	if !(phpoffice.Row{Cells: []any{" ", nil, ""}}).IsEmpty() {
		t.Fatal("blank row not detected as empty")
	}
}

func TestReadCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := phpoffice.Read(ctx, writeOrderWorkbook(t), phpoffice.ImportSpec{HeaderRow: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read = %v, want context.Canceled", err)
	}
}
