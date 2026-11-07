# support/phpoffice

Excel（.xlsx）导出与导入，移植自 swoolefy 的 PHP `PhpOffice\Excel`。PHP 版基于
`ext-xlswriter`；本包改用纯 Go 的
[excelize](https://github.com/xuri/excelize)（`github.com/xuri/excelize/v2`），
因此无需安装扩展，`CGO_ENABLED=0` 也能构建。

- 导出：`Export` 写 .xlsx，支持声明式表头、列宽、冻结、筛选和生命周期钩子；
- 导入：`Read` 全量读取，`Import` 用行迭代器流式交给回调，适合大数据量落库。

## 内存模型与接口选择（重要）

**小数据用常规 API，大数据必须用流式接口。** 两者的内存行为完全不同：

| 场景 | 导出 | 导入 | 内存占用 |
| --- | --- | --- | --- |
| 小数据（几千行以内） | `Export` | `Read` | 常规 API 会把工作簿/全部行驻留内存直到保存或返回 |
| 大数据（上万行、受内存约束） | `ExportStream`（excelize `StreamWriter`） | `Import`（excelize `Rows` 流式迭代） | 与行数无关，逐行写盘/读取 |

- 导出原则上优先 `Export`：它提供钩子、自动筛选、多行表头等完整能力；
- 超过几千行，或进程内存有硬约束时，**必须**改用 `ExportStream`：它固定单行
  表头，不支持钩子与自动筛选，但用 `StreamWriter` 逐行写入，不会把整张表
  留在内存里；
- 导入小文件用 `Read`（返回全部 `Row`），大文件**必须**用 `Import`：它通过
  `excelize.Rows` 流式迭代，逐行回调，内存只与当前行有关；
- 不要把 `Read` 的结果再长期持有；`Import` 的回调里也不要把所有行堆积到切片，
  应分批落库后丢弃。

**Go 版本兼容性**：本包依赖 `github.com/xuri/excelize/v2 v2.11.1`，其
`go.mod` 要求 **Go 1.26**，与仓库当前 Go 版本一致。excelize 升级可能提高最低
Go 版本；升级依赖前先确认 `go.mod` 的 `go` 指令与 CI 矩阵，避免业务项目在旧
Go 上无法构建。本包只使用 excelize 的稳定公开 API，不使用实验性接口。

## 快速开始

### 导出

```go
import "github.com/bingcool/gofy/support/phpoffice"

result, err := phpoffice.Export(ctx, "storage/exports", phpoffice.ExportSpec{
    FileName:  "orders",
    SheetName: "订单",
    Headers:   []string{"订单号", "金额", "状态", "下单时间"},
    Rows: func(yield func([]any) error) error {
        for _, order := range orders {
            if err := yield([]any{order.No, order.Amount, order.Status, order.CreatedAt}); err != nil {
                return err
            }
        }
        return nil
    },
    ColumnWidths: map[string]float64{"A": 24, "B:C": 18},
})
if err != nil {
    return err
}
// result.Path / FileName / SheetName / RowCount / Size
```

### 大数据量导出

```go
result, err := phpoffice.ExportStream(ctx, "storage/exports", phpoffice.StreamExportSpec{
    FileName: "orders-large",
    Headers:  []string{"订单号", "金额"},
    Rows: func(yield func([]any) error) error {
        for row := range rows { // 数据源可以是 SQL 游标 / 生成器
            if err := yield(row); err != nil {
                return err
            }
        }
        return nil
    },
})
```

`ExportStream` 用 excelize `StreamWriter` 逐行写盘，内存占用与行数无关；
限制是固定单行表头、不支持 `ExportHooks` 与自动筛选，冻结行数只能是 0 或 1。
需要完整格式能力时用 `Export`（适合小数据量）。

### 导入

```go
// 一次读取（全部行驻留内存）
result, err := phpoffice.Read(ctx, "storage/imports/orders.xlsx", phpoffice.ImportSpec{
    HeaderRow:       1,
    ExpectedHeaders: []string{"订单号", "金额", "状态", "下单时间"},
    CellTypes:       map[int]phpoffice.CellType{1: phpoffice.CellFloat, 3: phpoffice.CellTime},
})
// result.Headers / Rows / RowCount / SkippedRowCount / LastRowNumber

// 流式导入（适合大文件）
_, err = phpoffice.Import(ctx, path, phpoffice.ImportSpec{
    HeaderRow: 1,
    HandleRow: func(row phpoffice.Row) error {
        // row.Value("订单号") / row.Cell(0) / row.IsEmpty()
        return save(row)
    },
    AfterImport: func(rowCount int) error { return flush() },
})
```

## 导出配置（ExportSpec）

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `FileName` | `export_时间_随机串.xlsx` | 只接受 basename，自动补 `.xlsx`；同名文件已存在时失败，绝不覆盖 |
| `SheetName` | `Sheet1` | 1~31 字符，禁止 `[]:*?/\` 和首尾单引号 |
| `Headers` | 必填 | 非空、不少于一个字符；决定每行宽度 |
| `Rows` | 无（只写表头） | `yield` 返回错误会中止导出；业务错误原样返回 |
| `ColumnWidths` | `{}` | `"A"` 或 `"B:D"`，宽度 0.1~255 |
| `FreezeRows` | 1 | 指向 0 表示不冻结；不能超过表头行数 |
| `AutoFilter` | true | 默认范围 `A1:最后列最后行` |
| `DateLayout` | `2006-01-02 15:04:05` | `time.Time` 的格式化布局 |

### 单元格类型

`nil` 写空、`bool` 写布尔、所有整数/浮点写数字、`string` 保持文本（长编号和前导
零不会变成数字）、`time.Time` 按 `DateLayout` 格式化、`fmt.Stringer` 取
`String()`；其它类型返回 `ErrInvalidRow`。每行宽度必须与表头一致，文本上限
32,767 字符。

### 钩子（ExportHooks）

| 钩子 | 时机 | 典型用途 |
| --- | --- | --- |
| `ConfigureSheet` | 建表后、写单元格前 | 网格线、标签颜色等非单元格配置 |
| `ConfigureHeaderStyle` | 建默认表头样式后 | 字体、底色、边框 |
| `WriteHeader` | 写表头 | 多行表头、合并标题、完全自绘；返回真实表头行数 |
| `AfterRows` | 数据写完后、筛选前 | 合计行、合并、公式、插图 |
| `AutoFilterRange` | 添加筛选前 | 自定义范围；返回 `false` 不添加 |
| `BeforeSave` | 筛选后、保存前 | 最后的单元格级调整 |

`ExportContext` 提供 `HeaderRowCount`、`DataRowCount`、`FirstDataRowIndex`、
`NextRowIndex`（0 基）、`NextRowNumber()`（1 基）和 `DefaultAutoFilterRange()`。

### 硬约束

- 目标文件以 `O_CREATE|O_EXCL` 排他预留；导出失败会删除预留/半成品文件；
- 数据行上限 1,048,575，单 sheet 总行数不超过 1,048,576；
- `Export` 会把工作簿保存在内存中直到 `SaveAs`；大数据必须改用
  `ExportStream`（见“内存模型与接口选择”）；
- 筛选范围必须是合法单元格区域，写法错误返回 `ErrExportFailed`。

## 导入配置（ImportSpec）

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `SheetName` | 第一个 sheet | 指定名称必须存在 |
| `HeaderRow` | 0（无表头） | 1 基；0 表示没有表头，`Values` 为空 |
| `DataStartRow` | 表头下一行 | 1 基；无表头时默认第 1 行；显式值必须大于表头行 |
| `ExpectedHeaders` | 无 | 非空时与文件表头逐列严格比较（要求 `HeaderRow > 0`） |
| `CellTypes` | 全部字符串 | 0 基列号 → `CellString`/`CellInt`/`CellFloat`/`CellTime` |
| `SkipEmptyRows` | true | 整行空白时不回调，计入 `SkippedRowCount` |
| `NormalizeCells` | 原样返回 | 行级归一化，返回列数必须与表头一致 |
| `HandleRow` | 仅 `Import` 必填 | 逐行处理，异常原样返回 |
| `AfterImport` | 无 | 全部行处理完后调用 |

### 行对象

`Row` 提供 `RowNumber`（Excel 1 基行号）、`Cells`（0 基、已补齐宽度）、
`Values`（表头 → 值）、`Cell(index, default)`、`Value(header, default)`、
`IsEmpty()`。

### 读取类型

- 默认按字符串读取，保留前导零和长编号；
- `CellInt` 要求整数（`12.0` 可转，`12.5` 报错）；
- `CellFloat` 返回 `float64`；
- `CellTime` 要求单元格是真实日期（Excel 序列号），返回 `time.Time`；文本日期
  不会自动解析。

### 行宽与校验

- 表头首尾空白会被 trim；空表头、重复表头直接拒绝；
- 数据行短于表头时补 `""`；超出的列有值时报 `ErrInvalidImportRow`，纯空白多余
  列允许并截断；
- 文件、sheet、表头、行宽逐层校验，非法行不会进入 `HandleRow`；
- 只支持 xlsx/xlsm 系列，CSV 不支持；后缀不强制。

## 错误

```text
ErrInvalidExport      目录/表头/sheet/文件名/列宽/钩子返回值非法
ErrInvalidRow         导出行宽度、类型或长度非法
ErrExportFailed       写工作簿或落盘失败
ErrInvalidImport      文件/sheet/表头/导入配置非法
ErrInvalidImportRow   导入行宽度、多余列或类型转换失败
ErrImportFailed       打开或读取工作簿失败
```

全部用 `errors.Is` 判断；`ctx` 取消时返回 `context.Canceled`。

## 与 PHP 版的差异

- 无 `ext-xlswriter` 依赖，纯 Go，交叉编译/静态镜像无需 C 工具链；
- PHP 的 `useConstMemory` 对应 `ExportStream`（excelize `StreamWriter`），
  但流式模式固定单行表头、无钩子与自动筛选；`batchSize` 不适用；
- 导出/导入是无状态的函数，不强制「单实例只能执行一次」（PHP 的抽象类会记录
  已导出/已读取状态）；
- 枚举与钩子一一对应，但 Go 用函数字段代替继承覆写；
- 导入通过 `excelize.Rows` 流式读取，行为与 PHP `nextRow()` 等价。

## 测试

```bash
go test ./support/phpoffice/
```

覆盖导出（默认值、防覆盖、非法行清理、列宽/冻结/筛选、多行表头、合计行、
自定义筛选与保存前钩子、绝对路径）、流式导出（1200 行、列宽、冻结、防覆盖、
失败清理）与导入（表头映射、前导零、空行跳过、无表头、起始行、模板表头校验、
sheet 选择、多余列、类型转换、归一化、行对象、取消）。