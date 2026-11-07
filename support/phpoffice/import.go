package phpoffice

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

// CellType 决定导入时一列单元格的读取类型。
type CellType int

const (
	// CellString 按字符串读取（默认），保留前导零与长编号。
	CellString CellType = iota
	// CellInt 读取整数，小数或非数字报错。
	CellInt
	// CellFloat 读取浮点数，整数也会返回 float64。
	CellFloat
	// CellTime 把 Excel 日期序列号转成 time.Time，要求单元格是真实日期。
	CellTime
)

// ImportSpec 声明一次 Excel 读取或导入。
type ImportSpec struct {
	// SheetName 为空时使用第一个 sheet；指定名称必须存在。
	SheetName string
	// HeaderRow 是表头所在行（1 基）；0 表示没有表头。
	HeaderRow int
	// DataStartRow 是数据起始行（1 基）；0 表示表头下一行（无表头时第一行）。
	DataStartRow int
	// ExpectedHeaders 非空时与文件表头逐列严格比较，用于模板校验。
	ExpectedHeaders []string
	// CellTypes 按 0 基列号覆盖读取类型，未覆盖的列按字符串读取。
	CellTypes map[int]CellType
	// SkipEmptyRows 是否跳过整行为空的行；nil 默认 true。
	SkipEmptyRows *bool
	// NormalizeCells 在交给 HandleRow 前做行级归一化，返回列数必须与表头一致。
	NormalizeCells func(cells []any, rowNumber int) ([]any, error)
	// HandleRow 逐行处理数据，仅在 Import 中使用；Read 不需要它。
	HandleRow func(row Row) error
	// AfterImport 在全部行处理完后调用，用于刷最后一批数据。
	AfterImport func(rowCount int) error
}

// Row 是一行导入数据。
type Row struct {
	// RowNumber 是 Excel 界面行号（1 基），可直接用于日志或错误提示。
	RowNumber int
	// Cells 是 0 基的单元格列表，已按表头宽度补齐。
	Cells []any
	// Values 是表头文本到值的映射；没有表头时为空 map。
	Values map[string]any
}

// Cell 按 0 基列号取值，缺失时返回 def。
func (r Row) Cell(index int, def any) any {
	if index < 0 || index >= len(r.Cells) {
		return def
	}
	if value := r.Cells[index]; value != nil {
		return value
	}
	return def
}

// Value 按表头文本取值，缺失时返回 def。
func (r Row) Value(header string, def any) any {
	if value, ok := r.Values[header]; ok && value != nil {
		return value
	}
	return def
}

// IsEmpty 判断整行是否为空（所有单元格为 nil 或空白文本）。
func (r Row) IsEmpty() bool {
	return isBlankRow(r.Cells)
}

// ReadResult 是 Read 的结果，全部数据行驻留内存。
type ReadResult struct {
	Path            string
	SheetName       string
	Headers         []string
	HeaderRowNumber int
	Rows            []Row
	RowCount        int
	SkippedRowCount int
	LastRowNumber   int
}

// ImportResult 是 Import 的结果，只保留计数与表头。
type ImportResult struct {
	Path            string
	SheetName       string
	Headers         []string
	HeaderRowNumber int
	RowCount        int
	SkippedRowCount int
	LastRowNumber   int
}

// readMeta 是 Read/Import 共用的读取结果。
type readMeta struct {
	path            string
	sheetName       string
	headers         []string
	headerRowNumber int
	skippedRowCount int
	lastRowNumber   int
}

// Read 读取整份文件并返回全部数据行；大文件请改用 Import 流式处理。
func Read(ctx context.Context, path string, spec ImportSpec) (*ReadResult, error) {
	var rows []Row
	meta, err := readWorkbook(ctx, path, spec, func(row Row) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ReadResult{
		Path:            meta.path,
		SheetName:       meta.sheetName,
		Headers:         meta.headers,
		HeaderRowNumber: meta.headerRowNumber,
		Rows:            rows,
		RowCount:        len(rows),
		SkippedRowCount: meta.skippedRowCount,
		LastRowNumber:   meta.lastRowNumber,
	}, nil
}

// Import 流式读取并逐行调用 spec.HandleRow，适合大数据量落库。
func Import(ctx context.Context, path string, spec ImportSpec) (*ImportResult, error) {
	if spec.HandleRow == nil {
		return nil, fmt.Errorf("%w: Import requires HandleRow; use Read for a one-shot read", ErrInvalidImport)
	}
	rowCount := 0
	meta, err := readWorkbook(ctx, path, spec, func(row Row) error {
		if err := spec.HandleRow(row); err != nil {
			return err
		}
		rowCount++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if spec.AfterImport != nil {
		if err := spec.AfterImport(rowCount); err != nil {
			return nil, err
		}
	}
	return &ImportResult{
		Path:            meta.path,
		SheetName:       meta.sheetName,
		Headers:         meta.headers,
		HeaderRowNumber: meta.headerRowNumber,
		RowCount:        rowCount,
		SkippedRowCount: meta.skippedRowCount,
		LastRowNumber:   meta.lastRowNumber,
	}, nil
}

// readWorkbook 完成文件、sheet、表头与行校验，把合法行交给 onRow。
func readWorkbook(ctx context.Context, path string, spec ImportSpec, onRow func(Row) error) (*readMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%w: file path must not be empty", ErrInvalidImport)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: file %q does not exist", ErrInvalidImport, path)
	}
	if info.Size() <= 0 {
		return nil, fmt.Errorf("%w: file %q is empty", ErrInvalidImport, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	if spec.HeaderRow < 0 || spec.HeaderRow > MaxRows {
		return nil, fmt.Errorf("%w: header row must be 0 to %d (%d given)",
			ErrInvalidImport, MaxRows, spec.HeaderRow)
	}
	if spec.DataStartRow < 0 || spec.DataStartRow > MaxRows {
		return nil, fmt.Errorf("%w: data start row must be 0 to %d (%d given)",
			ErrInvalidImport, MaxRows, spec.DataStartRow)
	}
	dataStartRow := spec.DataStartRow
	if spec.HeaderRow == 0 {
		if dataStartRow == 0 {
			dataStartRow = 1
		}
	} else if dataStartRow == 0 {
		dataStartRow = spec.HeaderRow + 1
	} else if dataStartRow <= spec.HeaderRow {
		return nil, fmt.Errorf("%w: data start row must be greater than header row (%d given)",
			ErrInvalidImport, dataStartRow)
	}
	skipEmpty := true
	if spec.SkipEmptyRows != nil {
		skipEmpty = *spec.SkipEmptyRows
	}
	cellTypes, err := normalizeCellTypes(spec.CellTypes)
	if err != nil {
		return nil, err
	}
	expected, err := normalizeExpectedHeaders(spec.ExpectedHeaders)
	if err != nil {
		return nil, err
	}
	if expected != nil && spec.HeaderRow == 0 {
		return nil, fmt.Errorf("%w: ExpectedHeaders requires HeaderRow greater than 0", ErrInvalidImport)
	}

	file, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open %q: %v", ErrImportFailed, path, err)
	}
	defer func() { _ = file.Close() }()

	sheetName, err := resolveSheetName(file, spec.SheetName)
	if err != nil {
		return nil, err
	}
	rows, err := file.Rows(sheetName)
	if err != nil {
		return nil, fmt.Errorf("%w: open sheet %q: %v", ErrImportFailed, sheetName, err)
	}
	defer func() { _ = rows.Close() }()

	meta := &readMeta{
		path:            abs,
		sheetName:       sheetName,
		headerRowNumber: spec.HeaderRow,
	}
	var headers []string
	headerSeen := spec.HeaderRow == 0
	currentRow := 0

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		currentRow++
		raw, err := rows.Columns(excelize.Options{RawCellValue: true})
		if err != nil {
			return nil, fmt.Errorf("%w: read sheet %q row %d: %v", ErrImportFailed, sheetName, currentRow, err)
		}

		if spec.HeaderRow > 0 && currentRow == spec.HeaderRow {
			headers, err = normalizeImportHeaders(raw, currentRow)
			if err != nil {
				return nil, err
			}
			if expected != nil && !slices.Equal(headers, expected) {
				return nil, fmt.Errorf("%w: header mismatch in sheet %q row %d; expected [%s], actual [%s]",
					ErrInvalidImport, sheetName, currentRow, joinList(expected), joinList(headers))
			}
			headerSeen = true
			continue
		}
		if currentRow < dataStartRow {
			continue
		}

		cells, err := normalizeImportCells(raw, headers, currentRow, cellTypes)
		if err != nil {
			return nil, err
		}
		if skipEmpty && isBlankRow(cells) {
			meta.skippedRowCount++
			continue
		}
		if spec.NormalizeCells != nil {
			cells, err = spec.NormalizeCells(cells, currentRow)
			if err != nil {
				return nil, err
			}
			if len(headers) > 0 && len(cells) != len(headers) {
				return nil, fmt.Errorf("%w: NormalizeCells returned %d columns for row %d, expected %d",
					ErrInvalidImportRow, len(cells), currentRow, len(headers))
			}
		}
		values := make(map[string]any, len(headers))
		for i, header := range headers {
			values[header] = cells[i]
		}
		if err := onRow(Row{RowNumber: currentRow, Cells: cells, Values: values}); err != nil {
			return nil, err
		}
	}
	if err := rows.Error(); err != nil {
		return nil, fmt.Errorf("%w: read sheet %q: %v", ErrImportFailed, sheetName, err)
	}
	if !headerSeen {
		return nil, fmt.Errorf("%w: header row %d does not exist in sheet %q",
			ErrInvalidImport, spec.HeaderRow, sheetName)
	}
	meta.headers = headers
	meta.lastRowNumber = currentRow
	return meta, nil
}

// resolveSheetName 选择第一个 sheet 或校验指定名称存在。
func resolveSheetName(file *excelize.File, requested string) (string, error) {
	names := nonEmptyList(file.GetSheetList())
	if len(names) == 0 {
		return "", fmt.Errorf("%w: workbook has no readable sheets", ErrInvalidImport)
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return names[0], nil
	}
	for _, name := range names {
		if name == requested {
			return requested, nil
		}
	}
	return "", fmt.Errorf("%w: sheet %q does not exist; available sheets: %s",
		ErrInvalidImport, requested, strings.Join(names, ", "))
}

// normalizeImportHeaders 去掉表头首尾空白，拒绝空表头、重复表头与超长表头。
func normalizeImportHeaders(raw []string, rowNumber int) ([]string, error) {
	headers := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, value := range raw {
		column, _ := columnLetter(i + 1)
		header := strings.TrimSpace(value)
		if header == "" {
			return nil, fmt.Errorf("%w: header row %d column %d (%s) must not be empty",
				ErrInvalidImport, rowNumber, i+1, column)
		}
		if utf8.RuneCountInString(header) > MaxCellLength {
			return nil, fmt.Errorf("%w: header row %d column %d (%s) exceeds %d characters",
				ErrInvalidImport, rowNumber, i+1, column, MaxCellLength)
		}
		if _, dup := seen[header]; dup {
			return nil, fmt.Errorf("%w: header row %d column %d (%s) duplicates header %q",
				ErrInvalidImport, rowNumber, i+1, column, header)
		}
		seen[header] = struct{}{}
		headers = append(headers, header)
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("%w: header row %d is empty", ErrInvalidImport, rowNumber)
	}
	return headers, nil
}

// normalizeExpectedHeaders 校验收据方声明的表头列表。
func normalizeExpectedHeaders(headers []string) ([]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(headers))
	out := make([]string, 0, len(headers))
	for i, value := range headers {
		header := strings.TrimSpace(value)
		if header == "" {
			return nil, fmt.Errorf("%w: expected header %d must not be empty", ErrInvalidImport, i+1)
		}
		if _, dup := seen[header]; dup {
			return nil, fmt.Errorf("%w: expected header %q is duplicated", ErrInvalidImport, header)
		}
		seen[header] = struct{}{}
		out = append(out, header)
	}
	return out, nil
}

// normalizeCellTypes 校验列号与类型。
func normalizeCellTypes(types map[int]CellType) (map[int]CellType, error) {
	if len(types) == 0 {
		return nil, nil
	}
	out := make(map[int]CellType, len(types))
	for index, cellType := range types {
		if index < 0 || index >= MaxColumns {
			return nil, fmt.Errorf("%w: CellTypes column index must be 0 to %d (%d given)",
				ErrInvalidImport, MaxColumns-1, index)
		}
		switch cellType {
		case CellString, CellInt, CellFloat, CellTime:
		default:
			return nil, fmt.Errorf("%w: CellTypes for column %d has unknown type %d",
				ErrInvalidImport, index, cellType)
		}
		out[index] = cellType
	}
	return out, nil
}

// normalizeImportCells 把原始单元格按表头宽度补齐/截断，并按列类型转换。
func normalizeImportCells(raw []string, headers []string, rowNumber int, types map[int]CellType) ([]any, error) {
	width := len(headers)

	// headerRow=0 时不建立表头映射，行宽可变；列类型仍然生效。
	if width == 0 {
		cells := make([]any, len(raw))
		for i, value := range raw {
			converted, err := convertImportCell(value, types[i], rowNumber, i+1)
			if err != nil {
				return nil, err
			}
			cells[i] = converted
		}
		return cells, nil
	}

	if len(raw) > width {
		for i := width; i < len(raw); i++ {
			if strings.TrimSpace(raw[i]) == "" {
				continue
			}
			column, _ := columnLetter(i + 1)
			return nil, fmt.Errorf("%w: row %d column %d (%s) has a value but no matching header",
				ErrInvalidImportRow, rowNumber, i+1, column)
		}
		raw = raw[:width]
	}

	cells := make([]any, width)
	for i := range cells {
		rawValue := ""
		if i < len(raw) {
			rawValue = raw[i]
		}
		value, err := convertImportCell(rawValue, types[i], rowNumber, i+1)
		if err != nil {
			return nil, err
		}
		cells[i] = value
	}
	return cells, nil
}

// convertImportCell 按列类型转换单个单元格；空单元格统一返回空字符串。
func convertImportCell(raw string, cellType CellType, rowNumber, columnIndex int) (any, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	column, _ := columnLetter(columnIndex)
	switch cellType {
	case CellInt:
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Trunc(f) != f {
			return nil, fmt.Errorf("%w: row %d column %d (%s) value %q is not an integer",
				ErrInvalidImportRow, rowNumber, columnIndex, column, raw)
		}
		return int64(f), nil
	case CellFloat:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%w: row %d column %d (%s) value %q is not a number",
				ErrInvalidImportRow, rowNumber, columnIndex, column, raw)
		}
		return f, nil
	case CellTime:
		serial, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(serial) || math.IsInf(serial, 0) {
			return nil, fmt.Errorf("%w: row %d column %d (%s) value %q is not an Excel date",
				ErrInvalidImportRow, rowNumber, columnIndex, column, raw)
		}
		parsed, err := excelize.ExcelDateToTime(serial, false)
		if err != nil {
			return nil, fmt.Errorf("%w: row %d column %d (%s) value %q is not an Excel date",
				ErrInvalidImportRow, rowNumber, columnIndex, column, raw)
		}
		return parsed, nil
	default:
		return raw, nil
	}
}
