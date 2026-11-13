// Package phpoffice provides Excel (.xlsx) export and import, ported from the
// swoolefy PHP PhpOffice\Excel module. The PHP version is built on the
// xlswriter extension; this one uses the pure-Go github.com/xuri/excelize/v2,
// so it builds with CGO_ENABLED=0 and needs no PHP extension.
//
// 导出：Export 写入 .xlsx，支持声明式表头/列宽/冻结/筛选与生命周期钩子；
// 导入：Read 全量读取，Import 用 excelize 的行迭代器流式交给回调。
package phpoffice

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Excel 单 sheet 的硬上限。
const (
	// MaxColumns 是 Excel 的最大列数（XFD）。
	MaxColumns = 16384
	// MaxRows 是 Excel 单 sheet 的最大行数（含表头）。
	MaxRows = 1048576
	// MaxDataRows 是导出时可写的数据行上限。
	MaxDataRows = MaxRows - 1
	// MaxHeaderRows 是多行表头允许的行数上限。
	MaxHeaderRows = 100
	// MaxCellLength 是单元格文本的最大字符数。
	MaxCellLength = 32767
	// MaxFileNameLength 是导出文件名（含 .xlsx）的字符上限。
	MaxFileNameLength = 120
)

var (
	// ErrInvalidExport 表示目录、表头、sheet、文件名、列宽或钩子返回值非法。
	ErrInvalidExport = errors.New("phpoffice: invalid export")
	// ErrInvalidRow 表示导出的数据行不合法。
	ErrInvalidRow = errors.New("phpoffice: invalid row")
	// ErrExportFailed 表示写工作簿或落盘失败。
	ErrExportFailed = errors.New("phpoffice: export failed")
	// ErrInvalidImport 表示文件、sheet、表头或导入配置非法。
	ErrInvalidImport = errors.New("phpoffice: invalid import")
	// ErrInvalidImportRow 表示导入的数据行不合法。
	ErrInvalidImportRow = errors.New("phpoffice: invalid import row")
	// ErrImportFailed 表示打开或读取工作簿失败。
	ErrImportFailed = errors.New("phpoffice: import failed")
)

// columnRange 是归一化后的列宽配置，start/end 都是列字母。
type columnRange struct {
	start string
	end   string
	width float64
}

var columnRangePattern = regexp.MustCompile(`^([A-Z]{1,3})(?::([A-Z]{1,3}))?$`)

// normalizeDirectory 校验导出目录存在、是目录且可写，返回绝对路径。
func normalizeDirectory(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", fmt.Errorf("%w: export directory must not be empty", ErrInvalidExport)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("%w: resolve export directory %q: %v", ErrInvalidExport, dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: export directory %q does not exist", ErrInvalidExport, dir)
	}
	// Probe writability with a temp file instead of relying on platform permission bits.
	// 用临时文件探测可写性，避免依赖平台权限位。
	probe, err := os.CreateTemp(abs, ".write-probe-*")
	if err != nil {
		return "", fmt.Errorf("%w: export directory %q is not writable", ErrInvalidExport, abs)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return abs, nil
}

// normalizeExportHeaders 校验并返回表头；表头不能为空、不能全是空白、不能超长。
func normalizeExportHeaders(headers []string) ([]string, error) {
	if len(headers) == 0 {
		return nil, fmt.Errorf("%w: headers must not be empty", ErrInvalidExport)
	}
	if len(headers) > MaxColumns {
		return nil, fmt.Errorf("%w: %d headers exceed the limit of %d columns",
			ErrInvalidExport, len(headers), MaxColumns)
	}
	out := make([]string, len(headers))
	for i, header := range headers {
		column, _ := columnLetter(i + 1)
		if strings.TrimSpace(header) == "" {
			return nil, fmt.Errorf("%w: header %d (%s) must not be empty", ErrInvalidExport, i+1, column)
		}
		if utf8.RuneCountInString(header) > MaxCellLength {
			return nil, fmt.Errorf("%w: header %d (%s) exceeds %d characters",
				ErrInvalidExport, i+1, column, MaxCellLength)
		}
		out[i] = header
	}
	return out, nil
}

// normalizeSheetName 按 Excel 规则校验 sheet 名。
func normalizeSheetName(name string) (string, error) {
	if n := utf8.RuneCountInString(name); n < 1 || n > 31 {
		return "", fmt.Errorf("%w: sheet name must contain 1 to 31 characters (%d given)",
			ErrInvalidExport, n)
	}
	if strings.ContainsAny(name, `[]:*?/\`) {
		return "", fmt.Errorf("%w: sheet name %q contains forbidden characters []:*?/\\",
			ErrInvalidExport, name)
	}
	if strings.HasPrefix(name, "'") || strings.HasSuffix(name, "'") {
		return "", fmt.Errorf("%w: sheet name %q must not start or end with an apostrophe",
			ErrInvalidExport, name)
	}
	return name, nil
}

// normalizeExportFileName 只接受 basename，自动补 .xlsx，拒绝路径分隔符与控制字符。
func normalizeExportFileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("%w: file name must not be empty, %q or %q", ErrInvalidExport, ".", "..")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			return "", fmt.Errorf("%w: file name %q must be a plain name without path separators, control characters or <>:\"/\\|?*",
				ErrInvalidExport, name)
		}
	}
	if utf8.RuneCountInString(name) > MaxFileNameLength {
		return "", fmt.Errorf("%w: file name exceeds %d characters", ErrInvalidExport, MaxFileNameLength)
	}
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		name += ".xlsx"
	}
	return name, nil
}

// defaultExportFileName 生成与 PHP 版类似的默认文件名：export_时间_随机串.xlsx。
func defaultExportFileName() string {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		// crypto/rand 失败极少见，退化到时间纳秒，仍保证目录内不重复。
		return fmt.Sprintf("export_%s_%06d.xlsx", time.Now().Format("20060102-150405"), time.Now().Nanosecond()%1_000_000)
	}
	return fmt.Sprintf("export_%s_%s.xlsx", time.Now().Format("20060102-150405"), hex.EncodeToString(suffix))
}

// normalizeColumnWidths 支持 A 与 B:D 两种写法，宽度范围 0.1~255。
func normalizeColumnWidths(widths map[string]float64) ([]columnRange, error) {
	out := make([]columnRange, 0, len(widths))
	for key, width := range widths {
		matches := columnRangePattern.FindStringSubmatch(key)
		if matches == nil {
			return nil, fmt.Errorf("%w: column range %q must look like A, C or B:D", ErrInvalidExport, key)
		}
		start, err := columnIndex(matches[1])
		if err != nil {
			return nil, fmt.Errorf("%w: column range %q: %v", ErrInvalidExport, key, err)
		}
		endLetter := matches[1]
		end := start
		if matches[2] != "" {
			endLetter = matches[2]
			end, err = columnIndex(matches[2])
			if err != nil {
				return nil, fmt.Errorf("%w: column range %q: %v", ErrInvalidExport, key, err)
			}
		}
		if start > end {
			return nil, fmt.Errorf("%w: column range %q must start before it ends", ErrInvalidExport, key)
		}
		if math.IsNaN(width) || math.IsInf(width, 0) || width < 0.1 || width > 255 {
			return nil, fmt.Errorf("%w: column width for %q must be between 0.1 and 255", ErrInvalidExport, key)
		}
		out = append(out, columnRange{start: matches[1], end: endLetter, width: width})
	}
	return out, nil
}

// normalizeExportRow 把一行归一化为 excelize 可写入的类型。
// nil 写空、bool 写布尔、所有整数/浮点写数字、string 保持文本、
// time.Time 按 layout 格式化；其它类型报错。
func normalizeExportRow(row []any, rowNumber int, headers []string, layout string) ([]any, error) {
	if len(row) != len(headers) {
		return nil, fmt.Errorf("%w: row %d has %d columns, expected %d",
			ErrInvalidRow, rowNumber, len(row), len(headers))
	}
	out := make([]any, len(row))
	for i, value := range row {
		column, _ := columnLetter(i + 1)
		switch v := value.(type) {
		case nil:
			out[i] = ""
		case bool:
			out[i] = v
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			out[i] = v
		case float32:
			out[i] = float64(v)
		case float64:
			out[i] = v
		case time.Time:
			out[i] = v.Format(layout)
		case string:
			if utf8.RuneCountInString(v) > MaxCellLength {
				return nil, fmt.Errorf("%w: row %d column %d (%s) exceeds %d characters",
					ErrInvalidRow, rowNumber, i+1, column, MaxCellLength)
			}
			out[i] = v
		case fmt.Stringer:
			text := v.String()
			if utf8.RuneCountInString(text) > MaxCellLength {
				return nil, fmt.Errorf("%w: row %d column %d (%s) exceeds %d characters",
					ErrInvalidRow, rowNumber, i+1, column, MaxCellLength)
			}
			out[i] = text
		default:
			return nil, fmt.Errorf("%w: row %d column %d (%s) has unsupported type %T",
				ErrInvalidRow, rowNumber, i+1, column, value)
		}
	}
	return out, nil
}

// columnLetter 把 1 基列序号转成列字母（1=A，16384=XFD）。
func columnLetter(index int) (string, error) {
	if index < 1 || index > MaxColumns {
		return "", fmt.Errorf("column index must be between 1 and %d (%d given)", MaxColumns, index)
	}
	letters := make([]byte, 0, 3)
	for index > 0 {
		index--
		letters = append([]byte{byte('A' + index%26)}, letters...)
		index /= 26
	}
	return string(letters), nil
}

// columnIndex 把列字母转成 1 基序号，并检查 Excel 上限。
func columnIndex(letters string) (int, error) {
	if len(letters) < 1 || len(letters) > 3 {
		return 0, fmt.Errorf("invalid column letters %q", letters)
	}
	index := 0
	for _, r := range letters {
		if r < 'A' || r > 'Z' {
			return 0, fmt.Errorf("invalid column letters %q", letters)
		}
		index = index*26 + int(r-'A'+1)
	}
	if index > MaxColumns {
		return 0, fmt.Errorf("column %q exceeds the Excel limit of %d columns", letters, MaxColumns)
	}
	return index, nil
}

// lastColumnLetter 返回给定列数的最后一列字母。
func lastColumnLetter(columns int) string {
	letter, _ := columnLetter(columns)
	return letter
}

// isBlankValue 判断单元格是否为空（nil 或纯空白字符串）。
func isBlankValue(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func isBlankRow(cells []any) bool {
	for _, cell := range cells {
		if !isBlankValue(cell) {
			return false
		}
	}
	return true
}

func nonEmptyList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func joinList(values []string) string {
	return strings.Join(values, " | ")
}
