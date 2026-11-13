package phpoffice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// ExportSpec declares one .xlsx export.
// ExportSpec 声明一次 .xlsx 导出。
type ExportSpec struct {
	// FileName must be a basename and gets a .xlsx suffix appended; when empty a
	// name of the form export_<time>_<random>.xlsx is generated.
	// The export fails if the target file already exists; it is never overwritten.
	// FileName 只接受 basename，自动补 .xlsx；为空时生成 export_时间_随机串.xlsx。
	// 目标文件已存在时导出失败，绝不覆盖。
	FileName string
	// SheetName uses Sheet1 when empty.
	// SheetName 留空时使用 Sheet1。
	SheetName string
	// Headers is required and its length fixes the width of every row.
	// Headers 必填，表头数量决定每行宽度。
	Headers []string
	// Rows yields data row by row; an error returned by yield aborts the export.
	// When nil only the header is written.
	// Rows 逐行产出数据；yield 返回错误会中止导出。为 nil 时只写表头。
	Rows func(yield func([]any) error) error
	// ColumnWidths accepts both "A" and "B:D" forms with widths in 0.1-255.
	// ColumnWidths 支持 "A" 与 "B:D" 两种写法，宽度范围 0.1~255。
	ColumnWidths map[string]float64
	// FreezeRows is the number of header rows to freeze; nil freezes 1 row by
	// default and a pointer to 0 freezes none.
	// FreezeRows 冻结表头行数；nil 默认冻结 1 行，指向 0 表示不冻结。
	FreezeRows *int
	// AutoFilter adds an auto filter; nil means true.
	// AutoFilter 是否添加自动筛选；nil 默认 true。
	AutoFilter *bool
	// DateLayout is the time.Time layout, 2006-01-02 15:04:05 by default.
	// DateLayout 是 time.Time 的格式化布局，默认 2006-01-02 15:04:05。
	DateLayout string
	// Hooks is the optional export lifecycle hooks.
	// Hooks 是可选的导出生命周期钩子。
	Hooks ExportHooks
}

// ExportHooks provides hooks corresponding to configureSheet/writeHeader/afterRows
// in the PHP version.
// ExportHooks 提供与 PHP 版 configureSheet/writeHeader/afterRows 等对应的钩子。
type ExportHooks struct {
	// ConfigureSheet runs after the sheet is created and before any cell is
	// written, for non-cell settings such as gridlines.
	// ConfigureSheet 在建表后、写任何单元格前调用，用于网格线等非单元格配置。
	ConfigureSheet func(file *excelize.File) error
	// ConfigureHeaderStyle may modify the default header style (bold, fill,
	// border).
	// ConfigureHeaderStyle 可修改默认表头样式（加粗、底色、边框）。
	ConfigureHeaderStyle func(style *excelize.Style)
	// WriteHeader takes over header writing entirely and returns the number of
	// rows the header occupies (0 means no header).
	// WriteHeader 完全接管表头写入，返回表头实际占用行数（0 表示无表头）。
	WriteHeader func(file *excelize.File, sheet string, headers []string, styleID int) (int, error)
	// AfterRows runs after the rows are written and before the auto filter,
	// suited to totals, merges and formulas.
	// AfterRows 在数据写完后、自动筛选前调用，适合合计行/合并/公式。
	AfterRows func(file *excelize.File, ctx ExportContext) error
	// AutoFilterRange returns a custom filter range; no filter is added when the
	// second return value is false.
	// AutoFilterRange 返回自定义筛选范围；第二个返回值为 false 时不添加筛选。
	AutoFilterRange func(ctx ExportContext) (string, bool)
	// BeforeSave runs after the filter and before saving, the last chance for
	// cell-level adjustments.
	// BeforeSave 在筛选之后、保存之前调用，是最后的单元格级调整机会。
	BeforeSave func(file *excelize.File, ctx ExportContext) error
}

// ExportContext describes the row and column layout of this export; indexes are
// 0-based and row numbers are 1-based.
// ExportContext 描述本次导出的行列布局；索引为 0 基，行号为 1 基。
type ExportContext struct {
	Headers           []string
	HeaderRowCount    int
	DataRowCount      int
	FirstDataRowIndex int
	NextRowIndex      int
}

// NextRowNumber returns the 1-based Excel row number of the next writable row.
// NextRowNumber 返回下一个可写行的 Excel 行号（1 基）。
func (c ExportContext) NextRowNumber() int {
	return c.NextRowIndex + 1
}

// DefaultAutoFilterRange returns the default A1:last-column+last-row filter
// range.
// DefaultAutoFilterRange 返回 A1:最后列+最后一行的默认筛选范围。
func (c ExportContext) DefaultAutoFilterRange() string {
	lastColumn := lastColumnLetter(len(c.Headers))
	lastRow := c.HeaderRowCount + c.DataRowCount
	if lastRow < 1 {
		lastRow = 1
	}
	return fmt.Sprintf("A1:%s%d", lastColumn, lastRow)
}

// ExportResult is the file information of a successful export.
// ExportResult 是导出成功后的文件信息。
type ExportResult struct {
	Path      string
	FileName  string
	SheetName string
	RowCount  int
	Size      int64
}

// exportPlan is the validated configuration shared by Export and ExportStream;
// the target file is already reserved exclusively.
// exportPlan 是 Export 与 ExportStream 共用的已校验配置；目标文件已排他预留。
type exportPlan struct {
	dir        string
	path       string
	sheetName  string
	fileName   string
	headers    []string
	widths     []columnRange
	freezeRows int
	layout     string
}

// prepareExport validates the export arguments and reserves the target file with
// O_EXCL.
// prepareExport 校验导出参数并以 O_EXCL 预留目标文件。
func prepareExport(dir, fileName, sheetName string, headers []string, widths map[string]float64, freeze *int, layout string) (*exportPlan, error) {
	dir, err := normalizeDirectory(dir)
	if err != nil {
		return nil, err
	}
	headers, err = normalizeExportHeaders(headers)
	if err != nil {
		return nil, err
	}
	sheetName = strings.TrimSpace(sheetName)
	if sheetName == "" {
		sheetName = "Sheet1"
	}
	if sheetName, err = normalizeSheetName(sheetName); err != nil {
		return nil, err
	}
	if strings.TrimSpace(fileName) == "" {
		fileName = defaultExportFileName()
	}
	if fileName, err = normalizeExportFileName(fileName); err != nil {
		return nil, err
	}
	ranges, err := normalizeColumnWidths(widths)
	if err != nil {
		return nil, err
	}
	freezeRows := 1
	if freeze != nil {
		freezeRows = *freeze
	}
	if freezeRows < 0 {
		return nil, fmt.Errorf("%w: freeze rows must not be negative", ErrInvalidExport)
	}
	layout = strings.TrimSpace(layout)
	if layout == "" {
		layout = "2006-01-02 15:04:05"
	}

	path := filepath.Join(dir, fileName)
	reserved, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("%w: file %q already exists or is not writable", ErrInvalidExport, path)
	}
	_ = reserved.Close()

	return &exportPlan{
		dir:        dir,
		path:       path,
		sheetName:  sheetName,
		fileName:   fileName,
		headers:    headers,
		widths:     ranges,
		freezeRows: freezeRows,
		layout:     layout,
	}, nil
}

// Export builds the .xlsx described by spec in dir and returns the file
// information.
//
// The target file is reserved exclusively: an existing file with the same name
// fails the export, and an error midway deletes the reserved or partial file so
// no empty shell is left behind.
// Export 按 spec 生成 .xlsx 并写入 dir，返回文件信息。
//
// 目标文件以排他方式预留：同名文件已存在时直接失败；导出中途出错会删除
// 预留/半成品文件，不会留下空壳。
func Export(ctx context.Context, dir string, spec ExportSpec) (*ExportResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	plan, err := prepareExport(dir, spec.FileName, spec.SheetName, spec.Headers, spec.ColumnWidths, spec.FreezeRows, spec.DateLayout)
	if err != nil {
		return nil, err
	}
	dir, path := plan.dir, plan.path
	headers, sheetName := plan.headers, plan.sheetName
	fileName, widths := plan.fileName, plan.widths
	freezeRows, layout := plan.freezeRows, plan.layout
	autoFilter := true
	if spec.AutoFilter != nil {
		autoFilter = *spec.AutoFilter
	}

	file := excelize.NewFile()
	defer func() { _ = file.Close() }()

	cleanup := func() { _ = os.Remove(path) }
	succeed := false
	defer func() {
		if !succeed {
			cleanup()
		}
	}()

	if sheetName != "Sheet1" {
		if err := file.SetSheetName("Sheet1", sheetName); err != nil {
			return nil, fmt.Errorf("%w: rename sheet: %v", ErrExportFailed, err)
		}
	}
	if spec.Hooks.ConfigureSheet != nil {
		if err := spec.Hooks.ConfigureSheet(file); err != nil {
			return nil, err
		}
	}

	styleID, err := newHeaderStyle(file, spec.Hooks.ConfigureHeaderStyle)
	if err != nil {
		return nil, fmt.Errorf("%w: create header style: %v", ErrExportFailed, err)
	}

	headerRows := 1
	if spec.Hooks.WriteHeader != nil {
		headerRows, err = spec.Hooks.WriteHeader(file, sheetName, headers, styleID)
		if err != nil {
			return nil, err
		}
	} else if err := writeDefaultHeader(file, sheetName, headers, styleID); err != nil {
		return nil, fmt.Errorf("%w: write header: %v", ErrExportFailed, err)
	}
	if headerRows < 0 || headerRows > MaxHeaderRows {
		return nil, fmt.Errorf("%w: WriteHeader returned %d rows, want 0 to %d",
			ErrInvalidExport, headerRows, MaxHeaderRows)
	}
	if headerRows == 0 && freezeRows != 0 {
		return nil, fmt.Errorf("%w: freeze rows must be 0 when there is no header", ErrInvalidExport)
	}
	if freezeRows > headerRows {
		return nil, fmt.Errorf("%w: freeze rows %d exceed the %d header rows",
			ErrInvalidExport, freezeRows, headerRows)
	}

	for _, col := range widths {
		if err := file.SetColWidth(sheetName, col.start, col.end, col.width); err != nil {
			return nil, fmt.Errorf("%w: set column width %s:%s: %v", ErrExportFailed, col.start, col.end, err)
		}
	}
	if freezeRows > 0 {
		panes := &excelize.Panes{
			Freeze:      true,
			YSplit:      freezeRows,
			TopLeftCell: fmt.Sprintf("A%d", freezeRows+1),
			ActivePane:  "bottomLeft",
		}
		if err := file.SetPanes(sheetName, panes); err != nil {
			return nil, fmt.Errorf("%w: freeze rows: %v", ErrExportFailed, err)
		}
	}

	dataRows, err := writeRows(ctx, file, sheetName, headers, headerRows, layout, spec.Rows)
	if err != nil {
		return nil, err
	}
	if headerRows+dataRows > MaxRows {
		return nil, fmt.Errorf("%w: sheet exceeds %d rows (%d header + %d data)",
			ErrInvalidExport, MaxRows, headerRows, dataRows)
	}

	exportCtx := ExportContext{
		Headers:           headers,
		HeaderRowCount:    headerRows,
		DataRowCount:      dataRows,
		FirstDataRowIndex: headerRows,
		NextRowIndex:      headerRows + dataRows,
	}
	if spec.Hooks.AfterRows != nil {
		if err := spec.Hooks.AfterRows(file, exportCtx); err != nil {
			return nil, err
		}
	}
	if autoFilter {
		filterRange := exportCtx.DefaultAutoFilterRange()
		if spec.Hooks.AutoFilterRange != nil {
			custom, ok := spec.Hooks.AutoFilterRange(exportCtx)
			if !ok {
				filterRange = ""
			} else {
				filterRange = strings.TrimSpace(custom)
				if filterRange == "" {
					return nil, fmt.Errorf("%w: AutoFilterRange must return a non-empty range or false", ErrInvalidExport)
				}
			}
		}
		if filterRange != "" {
			if err := file.AutoFilter(sheetName, filterRange, nil); err != nil {
				return nil, fmt.Errorf("%w: auto filter %s: %v", ErrExportFailed, filterRange, err)
			}
		}
	}
	if spec.Hooks.BeforeSave != nil {
		if err := spec.Hooks.BeforeSave(file, exportCtx); err != nil {
			return nil, err
		}
	}

	if err := file.SaveAs(path); err != nil {
		return nil, fmt.Errorf("%w: save %q: %v", ErrExportFailed, path, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		return nil, fmt.Errorf("%w: output file %q is missing or empty", ErrExportFailed, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	succeed = true
	return &ExportResult{
		Path:      abs,
		FileName:  fileName,
		SheetName: sheetName,
		RowCount:  dataRows,
		Size:      info.Size(),
	}, nil
}

// newHeaderStyle builds the default header style: bold, white text on a blue
// fill, centred, with thin borders.
// newHeaderStyle 生成默认表头样式：加粗、蓝底白字、居中、细边框。
func newHeaderStyle(file *excelize.File, customize func(*excelize.Style)) (int, error) {
	style := &excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "FFFFFF", Size: 11},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"4F81BD"}},
		Alignment: &excelize.Alignment{
			Horizontal: "center",
			Vertical:   "center",
		},
		Border: []excelize.Border{
			{Type: "left", Color: "000000", Style: 1},
			{Type: "right", Color: "000000", Style: 1},
			{Type: "top", Color: "000000", Style: 1},
			{Type: "bottom", Color: "000000", Style: 1},
		},
	}
	if customize != nil {
		customize(style)
	}
	return file.NewStyle(style)
}

// writeDefaultHeader writes one header row and applies the header style.
// writeDefaultHeader 写一行表头并应用表头样式。
func writeDefaultHeader(file *excelize.File, sheet string, headers []string, styleID int) error {
	for i, header := range headers {
		cell, err := excelize.CoordinatesToCellName(i+1, 1)
		if err != nil {
			return err
		}
		if err := file.SetCellStr(sheet, cell, header); err != nil {
			return err
		}
	}
	lastCell, err := excelize.CoordinatesToCellName(len(headers), 1)
	if err != nil {
		return err
	}
	return file.SetCellStyle(sheet, "A1", lastCell, styleID)
}

// writeRows validates and writes the data rows one by one and returns the number
// of data rows.
// writeRows 逐行校验并写入数据区，返回数据行数。
func writeRows(ctx context.Context, file *excelize.File, sheet string, headers []string, headerRows int, layout string, rows func(yield func([]any) error) error) (int, error) {
	if rows == nil {
		return 0, nil
	}
	dataRows := 0
	err := rows(func(row []any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dataRows++
		if dataRows > MaxDataRows {
			return fmt.Errorf("%w: data rows exceed the limit of %d", ErrInvalidExport, MaxDataRows)
		}
		values, err := normalizeExportRow(row, dataRows, headers, layout)
		if err != nil {
			return err
		}
		cell, err := excelize.CoordinatesToCellName(1, headerRows+dataRows)
		if err != nil {
			return fmt.Errorf("%w: resolve row %d: %v", ErrExportFailed, dataRows, err)
		}
		if err := file.SetSheetRow(sheet, cell, &values); err != nil {
			return fmt.Errorf("%w: write row %d: %v", ErrExportFailed, dataRows, err)
		}
		return nil
	})
	if err != nil {
		return dataRows, err
	}
	return dataRows, nil
}
