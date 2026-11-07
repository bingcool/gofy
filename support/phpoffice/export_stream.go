package phpoffice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xuri/excelize/v2"
)

// StreamExportSpec 声明一次流式 .xlsx 导出，适合大数据量。
//
// 与 ExportSpec 的差异：固定单行表头，不支持 ExportHooks 与自动筛选；
// 数据行通过 excelize StreamWriter 逐行写盘，工作簿不会整体驻留内存。
type StreamExportSpec struct {
	// FileName 只接受 basename，自动补 .xlsx；为空时生成默认文件名。
	// 目标文件已存在时导出失败，绝不覆盖。
	FileName string
	// SheetName 留空时使用 Sheet1。
	SheetName string
	// Headers 必填，表头数量决定每行宽度。
	Headers []string
	// Rows 逐行产出数据；yield 返回错误会中止导出。为 nil 时只写表头。
	Rows func(yield func([]any) error) error
	// ColumnWidths 支持 "A" 与 "B:D" 两种写法，宽度范围 0.1~255。
	ColumnWidths map[string]float64
	// FreezeRows 冻结行数；nil 默认冻结 1 行，指向 0 表示不冻结。
	// 流式模式只有一行表头，因此取值只能是 0 或 1。
	FreezeRows *int
	// DateLayout 是 time.Time 的格式化布局，默认 2006-01-02 15:04:05。
	DateLayout string
}

// ExportStream 用 excelize StreamWriter 生成 .xlsx：表头与数据行逐行写入，
// 内存占用与数据行数无关。小数据量或需要钩子/自动筛选时用 Export。
//
// 与 Export 一样：文件排他预留、同名文件不覆盖、失败删除半成品文件。
func ExportStream(ctx context.Context, dir string, spec StreamExportSpec) (*ExportResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	plan, err := prepareExport(dir, spec.FileName, spec.SheetName, spec.Headers, spec.ColumnWidths, spec.FreezeRows, spec.DateLayout)
	if err != nil {
		return nil, err
	}
	path := plan.path
	if plan.freezeRows > 1 {
		_ = os.Remove(path)
		return nil, fmt.Errorf("%w: stream export freezes at most the single header row (got %d)",
			ErrInvalidExport, plan.freezeRows)
	}

	file := excelize.NewFile()
	defer func() { _ = file.Close() }()

	succeed := false
	defer func() {
		if !succeed {
			_ = os.Remove(path)
		}
	}()

	if plan.sheetName != "Sheet1" {
		if err := file.SetSheetName("Sheet1", plan.sheetName); err != nil {
			return nil, fmt.Errorf("%w: rename sheet: %v", ErrExportFailed, err)
		}
	}
	styleID, err := newHeaderStyle(file, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create header style: %v", ErrExportFailed, err)
	}

	writer, err := file.NewStreamWriter(plan.sheetName)
	if err != nil {
		return nil, fmt.Errorf("%w: create stream writer: %v", ErrExportFailed, err)
	}
	for _, col := range plan.widths {
		start, err := columnIndex(col.start)
		if err != nil {
			return nil, fmt.Errorf("%w: column range %s:%s: %v", ErrInvalidExport, col.start, col.end, err)
		}
		end, err := columnIndex(col.end)
		if err != nil {
			return nil, fmt.Errorf("%w: column range %s:%s: %v", ErrInvalidExport, col.start, col.end, err)
		}
		if err := writer.SetColWidth(start, end, col.width); err != nil {
			return nil, fmt.Errorf("%w: set column width %s:%s: %v", ErrExportFailed, col.start, col.end, err)
		}
	}
	if plan.freezeRows > 0 {
		panes := &excelize.Panes{
			Freeze:      true,
			YSplit:      plan.freezeRows,
			TopLeftCell: fmt.Sprintf("A%d", plan.freezeRows+1),
			ActivePane:  "bottomLeft",
		}
		if err := writer.SetPanes(panes); err != nil {
			return nil, fmt.Errorf("%w: freeze rows: %v", ErrExportFailed, err)
		}
	}

	headers := make([]any, len(plan.headers))
	for i, header := range plan.headers {
		headers[i] = header
	}
	if err := writer.SetRow("A1", headers, excelize.RowOpts{StyleID: styleID}); err != nil {
		return nil, fmt.Errorf("%w: write header: %v", ErrExportFailed, err)
	}

	dataRows := 0
	if spec.Rows != nil {
		err := spec.Rows(func(row []any) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			dataRows++
			if dataRows > MaxDataRows {
				return fmt.Errorf("%w: data rows exceed the limit of %d", ErrInvalidExport, MaxDataRows)
			}
			values, err := normalizeExportRow(row, dataRows, plan.headers, plan.layout)
			if err != nil {
				return err
			}
			cell, err := excelize.CoordinatesToCellName(1, dataRows+1)
			if err != nil {
				return fmt.Errorf("%w: resolve row %d: %v", ErrExportFailed, dataRows, err)
			}
			if err := writer.SetRow(cell, values); err != nil {
				return fmt.Errorf("%w: write row %d: %v", ErrExportFailed, dataRows, err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if err := writer.Flush(); err != nil {
		return nil, fmt.Errorf("%w: flush stream writer: %v", ErrExportFailed, err)
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
		FileName:  plan.fileName,
		SheetName: plan.sheetName,
		RowCount:  dataRows,
		Size:      info.Size(),
	}, nil
}
