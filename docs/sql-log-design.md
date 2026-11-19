# SQL 日志设计：可配置、带 X-Trace-Id、可脱敏、可限流

- 状态：**已实现**（2026-11-19）；实现位置由 `logger` 迁至 `support/sqllog`（见下方说明）
- 关联：[library-design.md](./library-design.md)（logger 与 db 包定位）、
  [gorm-tenant-design.md](./gorm-tenant-design.md) §11（GORM 插件安装顺序）、
  [worker-guide.md](./worker-guide.md)（worker 进程的 ctx 来源）
- 参考：swoolefy 的 DB 组件（`bingcool/library` `src/Db/PDOConnection.php`
  `query()` / `registerSlowSqlFn()`）——借鉴点与不照搬点见 §8
- 目标读者：要排查线上慢查询与错误 SQL 的开发者；决定日志容量与脱敏口径的运维

> **实现位置（2026-11-19 晚）**：本方案落在 **`support/sqllog`**，不在 `logger`。三条导入守卫
> 决定了这件事：`logger` 必须保持叶子包（不得依赖任何 gofy 包），而链路字段需要
> `support/otel` 与 `support/headerpropagation`；`support/db`、`support/sqlite` 又不允许
> import `logger`。下文出现的旧名字按此对应阅读：`logger.Policy` → `sqllog.Policy`、
> `logger.NewSQLPolicy` → `sqllog.NewPolicy`、`logger.SQLLogConfig` → `sqllog.Config`、
> `logger.SQLMode*` → `sqllog.Mode*`、`logger.MaskConfig`/`MaskStrategy` → `sqllog.*`、
> `logger.GormLogger{,Config}` → `sqllog.GormLogger{,Config}`。设计本身（模式、脱敏、采样、
> 限流、配置语义）未变；包内错误前缀由 `logger:` 改为 `sqllog:`。

---

## 1. 问题与目标

今天 `gofy` 的 SQL 日志有三个问题：

| 问题 | 现状 | 后果 |
| --- | --- | --- |
| 没有链路 id | `logger/gorm.go:89` 与 `support/db/db.go:236` 的 `Trace` 第一个参数都是 `_ context.Context` | 慢查询无法关联到具体请求/消息，只能靠时间猜 |
| 只有"打/不打"两档 | `log_parameter_values` 与 `ParameterizedQueries` 都是 bool | 要么看不见值（排查不了），要么全量明文（不能细粒度控制） |
| 三份实现 | `logger.GormLogger`、`support/db` 私有 `gormLogger`、`support/sqlite` 私有 `gormLogger` | 行为不一致，改一处漏两处；`ParamsFilter` 只有 `support/db` 实现了 |

**目标**：

1. 配置化：**默认关闭**（`sql_log.enabled: false`，不产生任何 SQL 日志）；开启后默认不打 SQL 文本，
   按配置输出 `off` / `placeholder` / `full` 三档。
2. 每条 SQL 日志带链路 id：`X-Trace-Id` 与 OTel `trace_id` 分别输出，消息消费链路也带。
3. 脱敏是**独立开关**，默认关闭；开启后按列名与值模式替换。
4. 开启后，慢 SQL 与错误 SQL 始终可见（即使 `mode: off`）；错误 SQL 有每秒上限，防止错误风暴打爆磁盘。
5. 支持独立 SQL 日志通道（文件），不与业务日志抢同一个文件。

**非目标**：不做 SQL 审计（谁在什么时候查了哪一行）、不做结果集脱敏、不替代数据库自身的慢查询日志。

---

## 2. 现状盘点（带代码位置）

| 事实 | 位置 | 影响 |
| --- | --- | --- |
| `Trace(ctx, ...)` 丢弃 ctx | `logger/gorm.go:89`、`support/db/db.go:236`、`support/sqlite/sqlite.go:236`（三份） | 拿不到任何 trace/req id |
| `ParamsFilter` 返回 `nil` 参数 = 不展开 | `support/db/db.go:205` | 只能整体开关；无法"展开但脱敏" |
| 慢 SQL 走 `Warn`，错误走 `Error`，全量走 `Info` | 同上三处 | 分级逻辑正确，但字段里只有 `elapsed/rows/sql` |
| `logger.Config` 已有独立通道先例 | `logger/logger.go:29` `AccessOutputs` + `NewAccess` | 加 `SQLOutputs` + `NewSQL` 是同一模式，不需要新机制 |
| `logger.GormLoggerConfig` 对外公开 | `logger/gorm.go:16` | 扩展字段必须保持向后兼容 |
| OTel 的 trace 提取已存在 | `support/otel/log.go:14` `TraceIDs(ctx)` | SQL 日志直接复用，不重复实现 |
| 业务链路 id 已存在于 ctx | `support/middleware/req_trace_id.go:80` | SQL 日志取 `headerpropagation.HeaderFromContext` |
| **生产端不发 X-Trace-Id** | `examples/demo-service/worker/group1/producer.go:315` 只写 `message_id` | 消息链路断开，SQL 日志无法关联生产请求 |
| **框架内没有 `propagator.Inject`** | 全仓 grep 无结果 | 连 `traceparent` 也没真正注入过消息头 |
| 消费端只接续 OTel | `support/consumer/consume.go:657` `extract` 只做 `propagator.Extract` | `X-Trace-Id` 即使到了消息里也不会进 ctx |

---

## 3. 配置设计

### 3.1 位置：顶层 `sql_log`，所有连接共享

SQL 日志是**进程级策略**，不是连接属性，因此放在顶层，与 `db:` 平级：

```yaml
sql_log:
  enabled: false               # 总开关；false 时本节其余字段全部忽略（§3.1.2）
  mode: placeholder            # off | placeholder | full
  sample_rate: 0.01            # Info 行采样率；1 表示全打，0 或省略取 0.01
  max_length: 4096             # 单条 SQL 截断长度；-1 表示不截断
  log_schema_queries: false    # 是否连 information_schema 的元数据查询也打（默认过滤）
  error_rate_limit: 20         # 错误 SQL 每秒最多 20 条；-1 表示不限
  slow_rate_limit: 0           # 慢 SQL 每秒上限；0 或 -1 表示不限
  mask:
    enabled: false             # 默认不脱敏；开启后用内置表（§3.1.1）+ 追加项
    strategy: mask             # mask | hash | remove
    # columns / patterns 都可整段省略：省略即"只用内置表"
    # columns: [employee_no, license_plate]        # 追加到内置 16 列
    # patterns: ["re:^ORD[0-9]{12}$"]              # 追加内置名字或 re:<正则>

db:
  default:
    dsn: ...
    slow_threshold: 200ms        # 仍按连接，见下表
    log_level: warn              # 仍按连接
    log_parameter_values: false  # 既有，兼容映射见 §3.3
```

**为什么不做成 `db.<name>.sql_log`：**

1. **脱敏是合规策略，不是连接属性。** 按连接配置意味着 N 个实例有 N 处要写对，漏一处就是一条泄漏路径。这与 §3.1.1"安全基线只能加不能减"是同一个判断：失败模式是"悄悄少配了一处"的设置，只能有一个地方。
2. **日志要可比较。** 同一条语句经 `order` 与 `product` 两个池打出不同形态，排查时得先回查环境配置才能读懂一行日志。全局一份之后，每行的 `sql_mode` 就足以说明形态。
3. **`db:` 是实例映射表。** `db.<name>` 的键就是实例名，`appdb.Open` 按名字取配置、Reload 按名字重建；再插入一个非实例的 `sql_log` 键，会让这张表出现"不是实例的键"，命名查找与热更新都要特判。顶层不碰这个形状。

**仍留在连接上的两个字段：**

| 字段 | 位置 | 原因 |
| --- | --- | --- |
| `slow_threshold` | `db.<name>` | 性能基线因库而异——报表库与主库的"慢"不是同一个数 |
| `log_level` | `db.<name>` | 该连接 gorm 日志的 severity（silent/error/warn/info）；批处理库可以整条静音 |

`mode` 与 `log_level` 正交：前者决定**行里的文本形态**，后者决定**哪些行**。下表以 `enabled: true` 为前提——关闭时两者都不生效（§3.1.2）：

| `log_level` | `sql_log.mode` | 产出 |
| --- | --- | --- |
| `silent` | 任意 | 无任何 gorm 日志，`mode` 被忽略（启动期提示一次） |
| `error` | `off` | 只有错误行，无 `sql` 字段 |
| `error` | `full` | 只有错误行，带展开文本 |
| `warn` | `placeholder` | 错误行 + 慢 SQL 行，模板 |
| `info` | `placeholder` | 全量行（受 `sample_rate`），模板 |
| `info` | `off` | 全量行但无文本，只剩耗时与行数——通常不是本意，启动期提示 |

**一个实现后果（§7.6）**：配置全局化之后，采样器与限流器也必须**进程内共享**。每个连接各持一份计数器，`error_rate_limit: 20` 在三个池上就变成 60 条/秒，采样率同样被放大；而这两个数的语义本来就是"这个进程最多写多少"。

#### 3.1.1 内置脱敏表：配置只做追加

`columns` 与 `patterns` 的默认值放在框架里，不在每个项目的 `config.yaml` 里复制一份。否则框架升级时新增的敏感列永远到不了已有项目——而"哪些列敏感"恰恰是随业务增长、最容易漏的一类知识。

| 常量（`logger` 包） | 内容 |
| --- | --- |
| `DefaultMaskColumns` | password, passwd, pwd, token, access_token, refresh_token, secret, api_key, private_key, id_card, id_no, phone, mobile, email, bank_card, cvv |
| `DefaultMaskPatterns` | phone, email, id_card, bank_card, long_token |

合并只发生一次：构造 gorm logger 时 `effective := logger.DefaultMask().Merge(cfg.Mask)`，`Trace` 拿到的就是合并后的结构体，热路径上没有合并成本，也不需要每次比对两份表。`Merge` 对零值安全，因此未配置的 `columns` / `patterns` 传进来的是 nil 切片，结果就是内置表本身。

三条合并规则：

1. **归一化后比较**：列名转小写并去掉 `_` / `-`，所以 `api_key`、`apiKey`、`API-KEY` 命中同一条；内置表存的就是归一化键（`apikey`）。这也正是"大小写不敏感、支持下划线/驼峰"的实现方式，不需要在匹配时写多套规则。
2. **去重**：配置里再写一遍 `phone` 不会导致两次脱敏；重复项在 `Merge` 里按归一化键丢弃。
3. **不做减法**：配置无法把 `phone` 从黑名单里删掉。安全基线只能加不能减——否则"某次为了排查临时删一行"会永久留在配置里，而没人记得它原本是默认值。确有"某个默认列要看明文"的需求时，一期用 `mask.enabled: false` 整档关闭；等出现第二个真实需求再讨论 `exclude`，不为一个假设的场景加一层机制。

`patterns` 的每一项有两种写法：

| 写法 | 含义 |
| --- | --- |
| 内置名字（如 `phone`） | 引用代码里已评审的检测器；写未知名字启动失败，避免"配了却没生效" |
| `re:<RE2>` | 项目自定义。`re:` 前缀是刻意的分隔符：名字表由框架维护，正则由项目维护，两者不混在一个命名空间里 |

敢收配置里的正则，是因为 Go 的 `regexp` 是 **RE2**：线性时间、没有回溯爆炸，最坏成本可以算——"每 token 一次线性扫描 × 正则条数"。正则在 `Validate()` 阶段编译一次，语法错误直接让启动失败，不会留到第一条 SQL 才炸。代价也写在这里：`re:` 项越多，每条展开的 SQL 都多跑几遍；命中率低的规则应该写成更精确的锚定形式（`^...$`），而不是宽泛的子串匹配。

#### 3.1.2 总开关 `enabled`：关闭时什么都不做

`sql_log.enabled` 是这一节的总开关，默认 **false**：

| 写法 | 状态 |
| --- | --- |
| 整段不写 / `sql_log: {}` / `sql_log: null` | 关闭（默认） |
| `enabled: false`，其余字段保留 | 关闭——配置留在文件里但不生效，用于临时止血 |
| `enabled: true`，或写了 `enabled` 之外的任何字段 | 启用 |

第三行是有意的：只写 `mode: full` 却忘了 `enabled: true`，如果判定为"未启用"，配置会静默失效——那比"写了就算启用"更难查。规则一句话：**要么整段不写，要么写了就算启用；`enabled: false` 是唯一能保留配置却关掉功能的方式。**

判定按**键是否出现**，不按值：`SQLLogConfig` 用一个 `UnmarshalYAML` 记录"本节出现过哪些键"，因此 `log_schema_queries: false`、`sample_rate: 0` 这类零值也算"写了"。YAML 路径因此不能只用"任一字段非零"来近似——`sql_log: {log_schema_queries: false}` 会被误判成没写。`sql_log: {}` 一个键都没有，因此是关闭。

代码里构造 `sqllog.Config` 走**同一套判定**，只是没有键可数：`keys` 只在 YAML 路径上存在，所以程序化路径在"显式 `enabled`"之后按"任一字段非零"补一条。否则 `Config{Mode: "full"}` 会解析成关闭，而等价的 `sql_log: {mode: full}` 解析成启用——同一份配置两种行为。零值字段与未提及的字段在 Go 结构体里无法区分，因此 `Config{LogSchemaQueries: false}` 表达的仍是"不启用"；要明确关掉就写 `Enabled: &false`。

这条按值规则能成立，前提是**结构体不被提前填充默认值**：`config.Load` 会在解码前跑 `defaults.Set`，所以 `Mode` 刻意没有 `default` 标签，否则整段缺席时它也会被填成 `placeholder`，继而被读成"运维配置了东西"。默认值由 `normalize()` 在构造策略时补，那也是它唯一被真正需要的地方。

关闭时框架不做任何额外的事：

- 不构造 `Policy`：不采样、不限流、不预编译 `re:` 正则；
- 不构造 SQL logger、不打开 `sql_outputs`；
- gorm logger 直接返回，**不产生任何 SQL 日志行**——慢 SQL 与错误 SQL 都没有，`db.<name>.log_level` 与 `slow_threshold` 在关闭时一并失效；
- `Trace` 是空实现：零分配、不调用 `fc()`（§11 的性能断言以这一档为基准）。

"连错误行也没有"要接受一个前提：**查询失败不会因此静默**——错误照旧返回调用方并进应用日志，丢掉的只是 SQL 侧的语句、耗时与链路字段。需要保留错误行但不要文本时，用 `enabled: true` + `mode: off`（§3.2）：那正好是"只有 `err` 与耗时"。

**升级提示**：默认关闭意味着以前由 `log_level: warn` 输出的慢 SQL 日志不再出现。启动期若发现"没有 `sql_log` 段，但某个连接的 `log_level` 不是 `silent`"，打一条 Warn 说明如何恢复（`sql_log.enabled: true`，其余不用写，输出与旧版逐字一致），不阻断启动。

这与 2026-11-19 修掉的 `config: db.default is required` 是同一条原则：**没用到的能力不需要配置**，缺省不等于错误。

#### 3.1.3 启用后的默认值：两层兜底

`enabled: true` 之后，本节其余字段仍然可以全部省略：

| 字段 | 省略、`0` 或空串时的值 | 显式"关掉"的写法 |
| --- | --- | --- |
| `mode` | `placeholder` | `off` |
| `sample_rate` | `0.01` | `1` 表示全打，不是 `0` |
| `max_length` | `4096` | `-1`（不截断） |
| `log_schema_queries` | `false`（过滤） | 无需（false 就是关） |
| `error_rate_limit` | `20` | `-1`（不限） |
| `slow_rate_limit` | `0`（不限） | 无需 |
| `mask.enabled` | `false` | 无需 |
| `mask.strategy` | `mask` | 空串也按 `mask` |
| `mask.columns` / `patterns` | 空 | 无需（只用内置表） |

两件事必须同时成立，否则"不配置"和"配成默认值"会走出两种行为：

1. **`config.Load` 的 `defaults.Set` 先填一遍**（`default:"..."` 标签），YAML 里的显式值随后覆盖。注意它是"先填后解码"，所以 `error_rate_limit: 0` 仍能覆盖默认的 20——这也是它比 `if cfg == 0 { cfg = 20 }` 更可靠的原因。
2. **`logger.NewSQLPolicy` 再归一一次**。因为 `defaults.Set` 只在应用使用 `config.Load` 时执行；直接用 `yaml.Unmarshal`、从环境变量拼装、或在测试里构造 `SQLLogConfig{}` 都拿不到标签默认值。归一逻辑按上表实现，于是"有没有走 `config.Load`"不影响结果。

三个字段的零值与"关掉"冲突，因此用显式哨兵而不是猜：

- `sample_rate`: `0` 当"未设置"（取 1%），全打写 `1`。原先"0 或 1 都表示全打"的说法作废——`0` 是零值，必须留给"没写"。
- `max_length`: `-1` 不截断，`0` 取默认 4096。原骨架里 `maxLength > 0` 的判断随之改为 `>= 0` / `-1` 显式分支。
- `error_rate_limit`: `-1` 不限，`0` 取默认 20。错误风暴是故障态，默认要有上限；确实想不限就显式写 `-1`。

`ignore_schema_queries` 改名为 `log_schema_queries`，原因是零值方向：前者的默认值是 `true`，而 Go 的 `bool` 零值是 `false`，"不写"与"写了 false"无法区分，只能上 `*bool`；改成正向命名后零值即默认，字段保持 `bool`。

### 3.2 三档 `mode` 与独立的脱敏开关

| mode | 输出内容 | 展开参数 | 适用 |
| --- | --- | --- | --- |
| `off` | 不输出 SQL 文本，错误日志只保留 `err` | 否 | 无排查需求时压掉文本 |
| `placeholder` | `SELECT ... WHERE id = ?` | 否（`ParamsFilter` 返回 nil） | **默认值**，定位语句与索引问题 |
| `full` | 展开后的完整 SQL | 是 | 排查慢查询/错误 SQL |

**`off` 关的是 SQL 文本，不是 gorm 的 `Trace` 回调**，这一点容易被误解成"完全不打日志"：

| 场景 | `off` 下的产出 |
| --- | --- |
| 普通成功查询 | 不产生日志行，且**不调用 `fc()`**（零分配，热路径无开销） |
| 慢查询 | 一行 `slow sql`，含 `elapsed` / `rows` / `threshold` / `sql_mode=off`，**无 `sql`** |
| 错误查询 | 一行 `sql error`，含 `err` / `elapsed` / `rows` / `sql_mode=off`，**无 `sql`** |
| 限流汇总 | 仍输出 `suppressed` 计数（否则无法判断是"没打"还是"被打掉"） |

理由：`Trace` 同时是 gorm 的**执行结果上报通道**，不是纯文本管道。把它整体静音会连"连接断开 / 死锁 / 唯一键冲突"一起消失，而这些恰恰是不需要 SQL 文本也想知道的。慢与错的判定只用入参（`elapsed`、`err`），不需要 `fc()`，所以保留信号的成本接近零。

要真正完全静默，只有不注入这个 logger（`gorm.Discard`），代价是丢掉错误信号与 `sql_mode` 诊断——不推荐作为生产配置。

**脱敏与 mode 正交**：

| mode | `mask.enabled` | 结果 |
| --- | --- | --- |
| `full` | `false`（默认） | 原始 SQL，字段 `sql_masked=false` |
| `full` | `true` | 脱敏 SQL，字段 `sql_masked=true` |
| `placeholder` | 任意 | 模板（没有值可脱敏），`sql_masked=false` |
| `off` | 任意 | 无 `sql` 字段 |

这样"要不要看值"和"看了要不要脱敏"是两个独立决定，不会出现"想脱敏就必须先展开"的耦合。

启用后的默认值：`mode: placeholder` + `mask.enabled: false`——与旧版 `log_parameter_values: false` 时的输出逐字等价，所以"行为没变"的准确说法是：**`enabled: true` 且其余不写 = 今天的行为**；而 `enabled` 本身默认关闭（§3.1.2）。

### 3.3 兼容映射（关键）

顶层化 + 总开关之后，旧字段是"每连接一个"，新开关是"全局一个"，映射要按**旧字段是否出现**来判断：

| 旧配置 | 新行为 |
| --- | --- |
| 任一实例写了 `log_parameter_values`（`true` 或 `false` 都算） | `enabled: true`——写了这个字段说明本来就在用 SQL 日志；`mode` 再按全部实例的取值定：全 `false` → `placeholder`，全 `true` → `full`（**与旧行为逐字等价**） |
| 任一实例写了 `log_parameter_values`，但取值**不一致** | `enabled: true` + `mode: placeholder` + 启动期 **Error** 列出取值为 true 的实例，要求显式写 `sql_log.mode` |
| 完全没有 `log_parameter_values`，也没有 `sql_log` | `enabled: false`（§3.1.2），启动期一条 Warn 提示慢 SQL 日志已默认关闭 |
| 配置了 `sql_log.enabled` | 以它为准；同时配置 `log_parameter_values` 时启动期告警"后者已废弃" |

不一致时取 `placeholder` 而不是 `full`：展开会让本应停在占位符的库把参数写进日志（fail-closed）；反过来静默降级只是让原本在调试的人看不到值，而且告警已经指出该怎么改——这一类"配置已无法表达"的情况要求人工决定，框架不猜。

`logger.GormLoggerConfig.ParameterizedQueries` 的程序化映射同样按语义对齐，而不是按"要不要展开"倒着推：

| `ParameterizedQueries` | 语义 | 映射 |
| --- | --- | --- |
| `true` | 日志里保留 `?`，不展开参数 | `mode: placeholder` |
| `false` | 日志里是展开后的语句 | `mode: full` |

新增 `Mode` 字段优先于这个旧字段。

`Validate()` 增加：`mode` 必须是三个之一；`sample_rate ∈ [0,1]`；`max_length ≥ -1`；`error_rate_limit ≥ -1`；`strategy ∈ {mask,hash,remove}`；`columns` 每项归一化后非空；`patterns` 每项必须是内置名字或 `re:` 开头的可编译正则（§3.1.1）。**可选字段不校验存在性**：`enabled: false`、`sql_log`、`mask`、`columns`、`patterns` 任意一层缺席都走默认值，只有写了内容才逐项校验——空的 `columns: []` 与不写这一行等价，不报错；`enabled: false` 时其余字段一律跳过校验（写了非法值也不阻断启动，因为它们不生效）。非法值启动失败（与 `log_level` 一致，不静默纠正）。

### 3.4 日志通道

复用 `AccessOutputs` 的成熟模式：

```yaml
log:
  outputs: [stdout]
  access_outputs: [storage/log/access/access.log]
  sql_outputs: [storage/log/sql/sql.log]   # 新增；为空时复用应用 logger
```

- `logger.Config` 加 `SQLOutputs []string`；
- `logger` 包加 `NewSQL(cfg Config) (*zap.Logger, error)`，与 `NewAccess` 同构（`Info` 级别、`component=sql` 基础字段、同样的 rotation 规则）；
- `App.buildLoggers` 的返回值从 4 个值改为一个结构体（`loggers{app, system, access, sql}`），避免第五个返回值；
- `buildDB` / `appdb.Open` 拿到 `sqlLogger` 后注入 `db.New(..., db.WithSQLLogger(sqlLogger))`；未配置 `sql_outputs` 时该选项不传，回落到主 logger。

这样 SQL 日志可以单独轮转、单独清理、单独限流，不会淹没业务日志。

两个段的分工写死：**`log:` 管日志去哪**（outputs / rotation，与 access 日志同构），**`sql_log:` 管 SQL 日志记什么**（形态、采样、脱敏、限流）。`sql_outputs` 留在 `log:` 是因为它和 `access_outputs` 是同一类东西——落盘通道；把它挪进 `sql_log:` 会让"输出"与"策略"两件事混在一个段里。

---

## 4. 输出格式

### 4.1 字段

| 字段 | 类型 | 何时出现 | 说明 |
| --- | --- | --- | --- |
| `msg` | string | 总是 | `sql` / `slow sql` / `sql error` / `sql error suppressed` |
| `component` | string | 总是 | `gorm`（保持不变，兼容现有检索） |
| `sql` | string | mode ≠ off | 语句文本（可能已脱敏/截断） |
| `sql_mode` | string | 总是 | 实际生效的档位，便于区分"没打"与"打了但为空" |
| `sql_masked` | bool | 有 `sql` 时 | `mask.enabled` 的实际值 |
| `elapsed` | duration | 总是 | 执行耗时 |
| `rows` | int64 | 总是 | 影响行数 |
| `x_trace_id` | string | ctx 有 `X-Trace-Id` | 业务链路 id（跨服务一致） |
| `trace_id` / `span_id` | string | ctx 有有效 OTel span | 与 `support/otel.TraceFields` 语义一致 |
| `truncated` | bool | 超长时 | 配合 `max_length` |
| `threshold` | string | 慢 SQL | 现有字段，保留 |
| `suppressed` | int64 | 限流汇总行 | 上一个窗口被丢弃的条数 |

### 4.2 示例

`enabled: true` + `mode: full` + 不脱敏：

```json
{"level":"warn","ts":"2026-11-19 15:40:12","msg":"slow sql","component":"gorm",
 "sql_mode":"full","sql_masked":false,"elapsed":0.312,"rows":1,
 "sql":"SELECT * FROM users WHERE phone = '13800138000'",
 "x_trace_id":"4f1c2b7e-9a3d-4c1e-b2a1-7d5e8f0a1b2c","threshold":"200ms"}
```

开启脱敏后同一行：

```json
{"level":"warn","ts":"2026-11-19 15:40:12","msg":"slow sql","component":"gorm",
 "sql_mode":"full","sql_masked":true,"elapsed":0.312,"rows":1,
 "sql":"SELECT * FROM users WHERE phone = '138****8000'",
 "x_trace_id":"4f1c2b7e-9a3d-4c1e-b2a1-7d5e8f0a1b2c","threshold":"200ms"}
```

`sql_masked` 让日志平台能对"生产环境出现未脱敏 SQL"单独告警。

---

## 5. trace 设计

### 5.1 两个 id 不合并

gofy 里同时存在两套 id，来源与用途不同：

| id | 产生位置 | 用途 |
| --- | --- | --- |
| `X-Trace-Id` | `support/middleware/req_trace_id.go`：复用上游头，否则生成 UUID | 业务链路，跨服务/跨消息透传（`headerpropagation` 契约） |
| OTel `trace_id` | `support/otel` 中间件从 `traceparent` 提取 | 分布式追踪后端（Jaeger/Tempo） |

两者可能不同（OTel 未开启时只有前者；上游只传了 `traceparent` 时只有后者）。**把它们塞进同一个字段会让"这条日志属于哪个链路"变得不可判定**，所以分开输出：

- 始终输出 `x_trace_id`（如果有）；
- 有 OTel span 时额外输出 `trace_id`、`span_id`；
- 二者都没有（如 worker 的定时 tick、script 的启动期）→ 不输出，不报错，不打空字符串。

### 5.2 SQL 日志的取值实现

```go
// sqlTraceFields returns the ids of the current statement, keeping the two
// trace systems separate.
// sqlTraceFields 返回当前语句的链路 id，两套 trace 体系保持分开。
func sqlTraceFields(ctx context.Context) []zap.Field {
	var fields []zap.Field
	if id := headerpropagation.HeaderFromContext(ctx, headerpropagation.HeaderTraceID); id != "" {
		fields = append(fields, zap.String("x_trace_id", id))
	}
	if traceID, spanID := otel.TraceIDs(ctx); traceID != "" {
		fields = append(fields, zap.String("trace_id", traceID), zap.String("span_id", spanID))
	}
	return fields
}
```

`ctx` 来自 gorm 的 `db.Statement.Context`：业务代码只要写了 `db.WithContext(ctx)`，`Trace` 就能拿到（这是 gorm 的既有行为，不需要改业务）。

没有单独的 `trace` 开关：ctx 里没有 id 时不输出字段，成本为零；有 id 却不输出才是反直觉的。

### 5.3 消息链路的注入（本次一并做）

上面取值的另一半是"消费消息时 ctx 里得先有 X-Trace-Id"。现状是断的（§2 最后三行），需要两端改，且都应在框架里，不能让每个业务生产者自己写头。

**生产端**：`support/headerpropagation` 增加一个出口函数：

```go
// Outgoing returns the headers that should travel with an outgoing message:
// the allowlisted headers already in ctx, plus the W3C traceparent extracted
// by p (nil skips it). Broker publish helpers merge the result into their
// message attributes before sending.
// Outgoing 返回应当随消息出行的头：ctx 中已有的白名单头，加上 p 从 ctx 提取的
// W3C traceparent（p 为 nil 时跳过）。各 broker 的 publish 在发送前把结果并进
// 消息属性。
func Outgoing(ctx context.Context, p propagation.TextMapPropagator) map[string]string
```

四个 broker 的 `Publish*`（`support/kafka`、`support/amqp`、`support/rocketmq`、`support/queues`）在构造消息属性时合并：

```go
headers := headerpropagation.Outgoing(ctx, client.propagator)
for k, v := range opts.Headers { // 调用方显式设置的同名头优先
	headers[k] = v
}
```

**消费端**：`support/consumer` 的 `Processor.extract` 增加一步：

```go
func (p *Processor) extract(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	ctx = p.propagator.Extract(ctx, propagation.MapCarrier(headers))
	// 业务链路 id 与 OTel 是两条链路，各自接续。
	if id := strings.TrimSpace(headers[headerpropagation.HeaderTraceID]); id != "" {
		ctx = headerpropagation.ContextWithHeader(ctx, headerpropagation.HeaderTraceID, id)
	}
	return ctx
}
```

要点：

- header key 用 `headerpropagation.HeaderTraceID` 常量（`"X-Trace-Id"`），生产/消费两端一致，不依赖大小写规范化；
- 生产者显式设置的 `opts.Headers` 覆盖自动注入值（业务有更准的链路 id 时可以指定）；
- 这一步只影响消息链路，HTTP 链路已经由 `req_trace_id` 中间件写好。

落点集中在框架三处（`headerpropagation`、四个 broker 的 Publish、`consumer.extract`），业务代码零改动。

---

## 6. 脱敏设计

### 6.1 默认关闭的取舍

`mask.enabled` 默认 `false`：先保证排查手段可用，脱敏作为"要开就能开"的能力。

风险与对应的运维动作：

| 风险 | 缓解 |
| --- | --- |
| 明文进入日志文件与日志平台 | `sql_masked=false` 字段可检索；对生产环境的该字段做告警，发现即为配置错误 |
| 日志文件被下载扩散 | `sql_outputs` 独立文件 + 更短的 `max_backups`/`max_age_days` |
| 合规审计要求 | 上线前把 `mask.enabled` 置 true；内置表（§3.1.1）已覆盖常见 PII，项目只需追加自己的列 |

建议的默认口径：**本地与预发 false，生产 true**。这条写进 `config.yaml` 注释，而不是代码里的隐式判断。

### 6.2 为什么不能只做正则

`fc()` 返回的是**已插值的 SQL 文本**（`ExplainSQL` 的产物）。纯正则替换会遇到三个坑：

1. 字符串字面量里可能包含 `=`、`,`、`)`，按结构切分会被打断；
2. 列名与值的配对需要上下文（`phone = 'x'` 与 `'phone'` 作为值出现是两件事）；
3. 批量 `INSERT INTO t (a,b) VALUES (...),(...)` 需要按列位置映射。

所以实现是**一个轻量扫描器**（状态机，约 80 行），不是若干条正则：

```
扫描 SQL 文本 → 切出 token（标识符 / 字符串字面量 / 数字 / 标点）
  ↓
维护"上一个标识符 + 上一个运算符"的上下文
  ↓
遇到值 token：
  1) 上一个标识符命中 columns 黑名单 → 按 strategy 脱敏
  2) 值本身命中 patterns（手机号/身份证/邮箱/银行卡/长 token）→ 脱敏
  3) 否则原样保留
```

字符串字面量必须处理转义：MySQL 的 `''` 与 `\'`；`ExplainSQL` 产出的字面量统一用单引号。

### 6.3 三种策略

| strategy | 输出 | 可读性 | 可关联性 |
| --- | --- | --- | --- |
| `mask`（默认） | `138****8000`、`110***********1234` | 可核对格式 | 部分 |
| `hash` | `sha256:3f2a9c1b`（前 8 位） | 不可读 | **同一值恒定**，可 GROUP BY 排查 |
| `remove` | `***` | 无 | 无 |

### 6.4 优先级与例外

- 列名命中优先于值模式；两者都命中只脱敏一次。
- `mask` 对长度 < 6 的值退化为 `***`（避免"掩码后等于原文"）。
- `null`、`0`、`''` 不脱敏（没有信息量，脱敏反而干扰）。
- 二进制/大字段（`[]byte`）在开启脱敏时强制替换为 `0x<len bytes>`——展开后的十六进制没有排查价值，只有体积。

### 6.5 不做什么

- 不改写 SQL，只在日志文本上处理；
- 不依赖数据库列注释或 `information_schema`；
- 不自动推断"哪些列敏感"——内置表加配置追加的列名共同决定，覆盖常见 PII；也不做减法（§3.1.1）。

---

## 7. 实现方案

### 7.1 模块划分

```
logger/
  gorm.go        # GormLogger 扩展：mode / mask / trace / sample / rate limit / max_length
  sqlmask.go     # 新增：SQL 文本扫描器 + 三种策略 + 内置脱敏表（DefaultMask/Merge）
  sqllimit.go    # 新增：每秒窗口计数器（错误/慢 SQL 限流）
  sqlmask_test.go
  logger.go      # Config 增加 SQLOutputs；NewSQL 与 NewAccess 同构

support/db/
  db.go          # Config 不加 SQLLog（策略是进程级）；新增 WithSQLPolicy(*logger.Policy)
                 # newGormLogger 改为调用 logger.GormLogger，删除私有实现（三份合一）

support/sqlite/
  sqlite.go      # 同样改为调用 logger.GormLogger，删除私有实现

support/headerpropagation/
  headerpropagation.go  # 增加 Outgoing(ctx, propagator)

support/{kafka,amqp,rocketmq,queues}/
  *.go           # Publish 前合并 Outgoing 的 header

support/consumer/
  consume.go     # extract 增加 X-Trace-Id 接续
```

依赖方向：`support/db → logger`、`support/sqlite → logger`、`logger → support/otel`、
`logger → support/headerpropagation`。已用 `go list -deps` 确认无环：`logger` 目前不 import
任何 gofy 包；`support/otel` 与 `support/headerpropagation` 都是叶子（不 import 任何 gofy 包）；
`support` 不 import `logger`。

### 7.2 `Trace` 的骨架

```go
func (g *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin) // 先算耗时：它不需要 fc()，而"是否慢"决定要不要调用 fc()
	if g.mode == ModeOff && err == nil &&
		!(g.slowThreshold > 0 && elapsed > g.slowThreshold) {
		return // 不调用 fc()，避免 ExplainSQL 的字符串拼接开销
	}
	sql, rows := fc()

	fields := g.traceFields(ctx) // x_trace_id / trace_id / span_id
	fields = append(fields,
		zap.Duration("elapsed", elapsed),
		zap.Int64("rows", rows),
		zap.String("sql_mode", g.mode.String()),
	)

	switch {
	case err != nil && !(g.ignoreRecordNotFound && errors.Is(err, gorm.ErrRecordNotFound)):
		// 新窗口的第一条一定被允许，所以 dropped 在这一支里读到的就是上个窗口的丢弃数。
		if ok, dropped := g.errorLimiter.allow(); ok {
			g.logSuppressed("sql error suppressed", dropped)
			g.logger.Error("sql error", append(fields, zap.Error(err), g.sqlFields(sql)...)...)
		}
	case g.slowThreshold > 0 && elapsed > g.slowThreshold:
		if ok, dropped := g.slowLimiter.allow(); ok {
			g.logSuppressed("slow sql suppressed", dropped)
			g.logger.Warn("slow sql", append(fields,
				zap.String("threshold", g.slowThreshold.String()), g.sqlFields(sql)...)...)
		}
	case g.mode != ModeOff && g.sampled():
		g.logger.Info("sql", append(fields, g.sqlFields(sql)...)...)
	}
}

// sqlFields renders the statement for this mode plus the flag that says whether
// it was masked. Nil means "no sql field at all" (mode off).
// sqlFields 按档位渲染语句，并带上"是否脱敏"的标志。返回 nil 表示完全不输出
// sql 字段（mode=off）。
func (g *gormLogger) sqlFields(sql string) []zap.Field {
	if g.mode == ModeOff {
		return nil
	}
	if g.maskEnabled {
		sql = maskSQL(sql, g.mask)
	}
	return []zap.Field{
		zap.String("sql", g.truncate(sql)),
		zap.Bool("sql_masked", g.maskEnabled),
	}
}

// logSuppressed records how many entries the previous window dropped, so a storm
// leaves one line instead of nothing.
// logSuppressed 记录上一个窗口丢弃了多少条，让风暴留下一条痕迹而不是毫无声息。
func (g *gormLogger) logSuppressed(msg string, dropped int) {
	if dropped > 0 {
		g.logger.Warn(msg, zap.Int("suppressed", dropped), zap.String("window", "1s"))
	}
}
```

展开与否仍由 `ParamsFilter` 一处决定：`mode == full` 返回原参数（`fc()` 得到展开文本），
`placeholder` 返回 nil（`fc()` 得到模板）。两条路径互斥，不存在"同一语句既给模板又给展开值"的状态。

**结论**：在 gorm 现有接口下，"模板 + 只对慢 SQL 展开"无法精确实现，因此本方案**不提供
"只对慢 SQL 展开"的配置项**：

- `ParamsFilter` 的返回值决定 `fc()` 是模板还是展开文本，它是**语句执行前**的决策，
  而耗时只有执行后才知道；
- `placeholder` 就是模板（不展开），要看值就切 `full`，用 `sample_rate` 控制量；
- 若将来确有强需求，需要 fork `ExplainSQL` 或在 `ParamsFilter` 里缓存参数（并发不安全），
  那是另一份方案，不在本次范围内。

### 7.3 采样

```go
func (g *gormLogger) sampled() bool {
	if g.sampleRate <= 0 || g.sampleRate >= 1 {
		return true // 0 视为不打折；1 视为全打
	}
	return rand.Float64() < g.sampleRate // math/rand/v2，无锁
}
```

用 `math/rand/v2`（Go 1.22+，无全局锁）。采样只作用于 `Info` 行；`Warn`/`Error` 由限流管，不采样。

### 7.4 错误 SQL 限流

需求是"错误 SQL 每秒最多 N 条"。用窗口计数器而不是 `golang.org/x/time/rate`：

- 语义精确（每满一秒重置），不会像令牌桶那样允许突发；
- 不新增依赖（`x/time` 目前只在 `go.sum` 里作为间接依赖出现）；
- 实现约 25 行，两条路径（错误/慢）各一个实例。

```go
// limiter caps how many entries may be written per second. When the window is
// exhausted it counts the drops; the caller logs one summary line per window so
// a storm is visible without filling the disk.
// limiter 限制每秒最多写多少条。窗口用尽后只累计丢弃数；调用方每个窗口打一条
// 汇总，让风暴可见而不写满磁盘。
type limiter struct {
	mu     sync.Mutex
	limit  int
	window int64 // unix 秒
	count  int
	drops  int
}

// allow reports whether this entry may be written, and how many were dropped in
// the window that just closed (non-zero only on the first call of a new window).
// allow 报告本条可否写出，并返回上一个窗口的丢弃数（只有新窗口的第一次调用返回非零）。
func (l *limiter) allow() (ok bool, dropped int) {
	if l == nil || l.limit <= 0 {
		return true, 0
	}
	now := time.Now().Unix()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now != l.window {
		dropped = l.drops
		l.window, l.count, l.drops = now, 0, 0
	}
	if l.count < l.limit {
		l.count++
		return true, dropped
	}
	l.drops++
	return false, dropped
}
```

调用方在 `dropped > 0` 时补一条：

```json
{"level":"warn","msg":"sql error suppressed","component":"gorm","suppressed":37,"window":"1s"}
```

这样"限流"不等于"静默"：能看出有 37 条被压掉，需要时再临时调大 `error_rate_limit`。

### 7.5 截断与控制字符

```go
func (g *gormLogger) truncate(sql string) string {
	// maxLength: -1 不截断；0 已在归一阶段变成 4096，因此这里只会遇到 >= 0
	if g.maxLength >= 0 && len(sql) > g.maxLength {
		return sql[:g.maxLength] + "…" // 同时置 truncated=true
	}
	return sanitizeControl(sql) // \n \r \t → 空格，防日志注入
}
```

JSON encoder 会转义换行，但 console encoder 不会——`sanitizeControl` 对两种 encoder 都成立。

### 7.6 共享策略对象（顶层配置的实现后果）

`sql_log` 全局化之后，每个连接不能再各建一个采样器与限流器——`error_rate_limit: 20` 会在三个池上变成 60 条/秒，`sample_rate: 0.01` 同理被放大三倍。策略对象因此是进程级单例，构造一次，注入给所有连接：

```go
// Policy is the process-wide SQL log policy: one sampler and one pair of
// limiters shared by every connection. Shared on purpose — the numbers mean
// "this process writes at most N per second", not "per pool".
// Policy 是进程级策略：一个采样器、一对限流器，被所有连接共享。共享是有意的——
// 这两个数意味着"本进程每秒最多写 N 条"，不是"每个连接池 N 条"。
type Policy struct {
	cfg          SQLLogConfig
	sampler      *sampler
	errorLimiter *limiter
	slowLimiter  *limiter
}

func NewSQLPolicy(cfg SQLLogConfig) *Policy
func (p *Policy) Trace(...)  // 或者由 gormLogger 持有 *Policy 并调用 p.allowError()/p.sample()
```

注入路径两条，都很短：

| 入口 | 代码 |
| --- | --- |
| HTTP `buildDB` | `gofydb.New(ctx, cfg, gofydb.WithSQLPolicy(policy), gofydb.WithSQLLogger(sqlLogger))` |
| 应用 `appdb.Open`（含 worker） | `appdb.Open(ctx, cfg, appdb.WithSQLPolicy(cfg.SQLLog), appdb.WithSQLLogger(sqlLogger))` |

`Policy` 只由装配层构造一次，业务与 `db.Config` 都看不到它——这与 worker 的"只装配、不持有连接"是同一条线：**策略属于进程，连接只是策略的执行者**。

---

## 8. 与 swoolefy 的对照

swoolefy 的 DB 组件（`library/src/Db/PDOConnection.php`）在 `query()` 末尾做了两件事：

```php
if ($this->isEnableDebug()) {
    $realSql = $this->getRealSql($this->queryStr, $this->bind);
    $sqlLog = "【{$dateTime}】【Runtime:{$runTime}】【X-Trace-Id: {$traceId}】【sql-cid-{$cid}】: ".$realSql;
    LogManager::getInstance()->getLogger(LogManager::SQL_LOG)->info($sqlLog);
}
if ($runTime > static::$slowSqlNoticeCallback['query_time']) {
    $this->callSlowSqlFn($runTime, $realSql);
}
```

| swoolefy 的做法 | 采纳 | 说明 |
| --- | --- | --- |
| `print_sql` 配置开关 + `dynamicDebug` 运行时开关 | ✅ | 对应 `mode`；运行时开关在 gofy 用 `LogMode`（gorm 已支持） |
| trace id 从协程上下文取（`OPENTELEMETRY_X_TRACE_ID`） | ✅ | gofy 取 `headerpropagation` 的 `X-Trace-Id` + OTel `trace_id`，两套分开 |
| SQL 日志独立文件（`LogManager::SQL_LOG`） | ✅ | `log.sql_outputs` + `logger.NewSQL` |
| 记录 `Runtime`（耗时）与 `cid` | ✅ / ❌ | 耗时采纳；`cid` 是协程概念，Go 用 `x_trace_id` 替代 |
| 慢 SQL 注册回调（`registerSlowSqlFn`） | ⚠️ 部分 | gofy 用 `slow_threshold` 直接分级打 `Warn`，不引入回调注册（避免包级状态） |
| 直接打印 `$realSql` | ✅（默认一致） | 默认不脱敏，与 swoolefy 相同；gofy 多一个 `mask.enabled` 开关（§6） |
| `SHOW FULL COLUMNS` 过滤 | ✅ | 迁移/`AutoMigrate` 的元数据查询单独降级为 `Debug`，不占 SQL 日志 |

值得单独说明：gorm 在 `AutoMigrate` 时会发大量 `SELECT ... FROM information_schema`，这些语句没有排查价值，但会淹没日志。方案在 `Trace` 里按 `strings.Contains(sql, "information_schema")` 直接丢弃（`log_schema_queries: false`，默认过滤）。字段名是正向的：`log_schema_queries: true` 才打出来——这样"不配置"的零值恰好等于"过滤"，不需要指针或 `default` 标签去区分"没写"与"写了 false"。

---

## 9. 性能与容量

| 措施 | 效果 |
| --- | --- |
| `mode=off` 且非慢/错时**不调用 `fc()`** | 避免 `ExplainSQL` 的字符串拼接，热路径接近零成本 |
| 采样只作用于 `Info` | 1% 采样下容量约为全量的 1/100 |
| 错误/慢 SQL 限流 | 错误风暴下写入量有硬上限（N 条/秒 + 每秒一条汇总） |
| `max_length` 截断 | 单条上限可控，批量 INSERT 不会写爆磁盘 |
| 脱敏只在打印时执行 | 未打印的语句零成本 |
| 独立 `sql_outputs` | 与业务日志分文件，可单独轮转/清理 |

容量估算：一条 SQL 日志约 300–600 字节。1000 QPS × 1% 采样 ≈ 10 条/秒 ≈ 0.5 MB/分钟。
`rotation.max_size_mb: 100` 下约 3 小时一个文件，需要按环境调 `max_backups`。
错误路径即使全错，也被 `error_rate_limit` 压到 N 条/秒以内。

---

## 10. 兼容与迁移

1. **默认改为关闭**（这是一处行为变化，必须写在升级说明里）：不配置 `sql_log` 时 `enabled: false`，
   **不产生任何 SQL 日志行**，也不报错、不告警。缺段、`sql_log: {}`、`sql_log: null` 三者等价（§3.1.2）。
   要保留今天的行为，写 `sql_log: {enabled: true}`——其余字段全走默认值，输出与旧版逐字一致；
   `log_parameter_values` 存在时按 §3.3 自动视作已启用，因此显式设置过该字段的项目升级后行为不变。
   `sql_log`、`mask`、`columns`、`patterns` 任意一层都可以整体省略，缺席即取默认值（§3.1.1）；写空数组
   `columns: []` 与不写这一行等价，不报错——否则"注释掉配置"和"留空数组"会变成两种行为。
2. **`log_parameter_values: true` 的用户**：按 §3.3 判断全部实例的取值——全为 true 映射为 `full` +
   `mask.enabled: false`（日志内容逐字不变，只多 `sql_mode`/`sql_masked` 两个字段）；实例间不一致则
   回落 `placeholder` 并在启动期报错，要求显式写 `sql_log.mode`。无论哪种情况都打一条"该字段已废弃"。
3. **三份 logger 合一**：`support/db` 与 `support/sqlite` 的私有实现删除后，行为统一到 `logger.GormLogger`。差异点（`silent` 处理、`ignoreNotFound`）在合并时逐项对齐，并补测试。
4. **`logger.GormLoggerConfig` 扩展**：新增字段全部有零值语义，旧调用方不传即保持原行为。
5. **消息头增加**：生产端会自动多带 `X-Trace-Id`，消费端会把它写进 ctx。对业务透明；如果某个下游对未知消息头敏感（极罕见），可用 `opts.Headers` 覆盖。

---

## 11. 测试计划

| 用例 | 断言 |
| --- | --- |
| 三档 mode | off 无 `sql` 字段；placeholder 有 `?`；full 有值 |
| 脱敏开关 | `mask.enabled=false` 时原文输出且 `sql_masked=false`；`true` 时替换且 `sql_masked=true` |
| 零配置 | 不写 `sql_log` / `mask` / `columns` / `patterns`，启动成功；与 `columns: []` 输出逐字相同 |
| 缺段等价 | `sql_log` 整段缺席、`{}`、`null` 三种配置产出逐字相同（都无任何 SQL 日志行）；`SQLLogConfig{}` 直接构造同样走默认值 |
| 关闭零输出 | `enabled: false` 时慢 SQL、错误 SQL 均无日志行；`Trace` 零分配且不调用 `fc()`；`log_level: info` 也不改变 |
| 隐含启用 | 只写 `mode: full` 而没写 `enabled` → 生效（§3.1.2 第三行）；`enabled: false` + 非法 `mode` → 启动成功，因为不生效 |
| 零值哨兵 | `sample_rate: 0` 等同 1%（不是全打）、`1` 全打；`max_length: -1` 不截断；`error_rate_limit: -1` 不限 |
| 共享策略 | 两个连接共用一个 `Policy`：`error_rate_limit: 2` 时两池合计 2 条/秒，不是各 2 条；`sample_rate` 同理 |
| 全局兼容 | `log_parameter_values` 全 false→placeholder、全 true→full、混合→placeholder+启动期 Error 列出冲突实例 |
| 正交 | `log_level: silent` + `mode: full` 无输出；`log_level: warn` + `mode: off` 有慢/错行但无 `sql` 字段 |
| 追加合并 | 内置 `phone` 与配置追加的列同时生效；重复项不导致二次脱敏；未知 `patterns` 名字启动失败 |
| 列名黑名单 | `phone = '13800138000'` → `138****8000`；`'phone'` 作为值出现不被误伤 |
| 值模式兜底 | 未在黑名单列上的手机号也被掩码 |
| 转义字面量 | `name = 'a''b'`、`note = 'x,y)'` 不被切断 |
| 批量 INSERT | `(a,b) VALUES ('x','y'),('z','w')` 按列位置处理（二期） |
| 三种 strategy | mask/hash/remove 的输出形态；hash 同值恒定 |
| trace | 只有 X-Trace-Id、只有 OTel、两者都有、都没有——四种组合的字段集合 |
| 消息链路 | 生产者发出的消息头含 `X-Trace-Id`；消费者处理后 ctx 里能读到同一个值 |
| 采样 | `sample_rate=0` 全打、`1` 全打、`0.01` 近似 1%；错误与慢 SQL 不受影响 |
| 错误限流 | 第 N+1 条被丢弃；下一秒第一条补一条 `suppressed` 汇总；`limit=0` 不限 |
| 截断 | 超 `max_length` 时 `truncated=true` 且长度符合预期 |
| 控制字符 | 值中的换行被替换，console encoder 输出仍是单行 |
| 兼容 | `log_parameter_values: true` 映射为 `full` + 不脱敏；启动告警出现 |
| 性能 | 基准测试：off 模式 `Trace` 的分配次数为 0；full+mask 下 1KB SQL 的脱敏耗时可接受（目标 < 20µs） |

---

## 12. 落地批次

| 批次 | 内容 | 验证 |
| --- | --- | --- |
| 0 | `logger/sqlmask.go` + `sqllimit.go` + 单测（纯函数，无 gorm 依赖） | 表驱动覆盖 §11 的脱敏与限流用例 |
| 1 | `logger.GormLogger` 扩展（mode/mask/trace/采样/限流/截断）+ 内置脱敏表与 `Merge`（§3.1.1） | `zaptest/observer` 断言字段；`Merge` 归一化/去重用例 |
| 2 | `support/db`、`support/sqlite` 改为复用 `logger.GormLogger`，删除两份私有实现 | 现有 db/sqlite 测试全绿 |
| 3 | 顶层 `sql_log` 配置段 + `enabled` 总开关（§3.1.2）+ 默认值两层兜底（§3.1.3）+ `logger.Policy` + 校验 + 旧字段映射（§3.1、§3.3） | 配置表驱动测试；缺段/`{}`/`null` 等价；`enabled: false` 零输出；隐含启用；零值哨兵；`columns`/`patterns` 追加；共享采样/限流；demo `config.yaml` 注释更新 |
| 4 | `logger.SQLOutputs` + `NewSQL` + `buildLoggers` 结构体化 | 独立文件落盘验证；未配置时回落主 logger |
| 5 | 消息链路：`headerpropagation.Outgoing` + 四个 broker 的 Publish + `consumer.extract` | 生产→消费一条消息，两端 `x_trace_id` 相同 |
| 6 | demo 接线与文档（`config.yaml` 注释、`library-design.md`） | `make check`；真实请求抓一条 SQL 日志核对 trace |

批次 0/1 可以独立合入（纯新增），2/3 需要一次全量回归；5 与 SQL 日志无代码依赖，可并行。

---

## 13. 已决策与遗留

2026-11-19 确认：

1. **消息头里的 `X-Trace-Id` 要注入 ctx** —— 落地为 §5.3：生产端 `headerpropagation.Outgoing`
   自动带出，消费端 `Processor.extract` 写回 ctx。顺带修掉"框架内没有 `propagator.Inject`"这个既有缺口。
2. **脱敏由独立字段控制，默认不脱敏** —— `sql_log.mask.enabled: false`。`mode` 只管"打不打值"，
   `mask.enabled` 只管"打出来要不要替换"，两者正交（§3.2）。默认口径写进 `config.yaml` 注释：
   本地/预发 false，生产 true。
3. **错误 SQL 需要每秒上限** —— `sql_log.error_rate_limit: N`，用窗口计数器（§7.4）；
   超限不是静默，下一秒补一条 `suppressed` 汇总。慢 SQL 用同一机制，`slow_rate_limit` 默认 0（不限）。
4. **默认脱敏表进框架，配置只追加** —— `logger.DefaultMaskColumns` / `DefaultMaskPatterns` 是代码里的
   常量，`Merge` 在构造 logger 时合并一次（§3.1.1）。理由有两条：安全基线不该靠每个项目抄一份，
   否则升级时新增的敏感列永远到不了已有项目；而"临时删一行"留下的配置会让基线与默认值悄悄分叉。
   归一化键让 `api_key` / `apiKey` / `API-KEY` 命中同一条，`patterns` 支持内置名字与 `re:` 自定义正则。
5. **`sql_log` 顶层化，所有连接共享**（§3.1）——脱敏是合规策略而不是连接属性：按连接配置是 N 个地方要
   写对，漏一处就是泄漏路径，这与"安全基线只能加不能减"是同一个判断。`db:` 保持"实例名 → 连接配置"的
   映射形状，`slow_threshold` 与 `log_level` 仍按连接（性能基线与噪声控制本来就因库而异）。
   代价是失去了"某个库单独看全量"的能力：一期用全局切换应对，确有第二个真实需求时加的是**单个字段**
   （`db.<name>.sql_log_mode`），不是让每处都能写整套。
6. **策略对象全进程一份**（§7.6）——`sample_rate` 与 `error_rate_limit` 的语义是"本进程最多写多少"，
   每个连接各持一份计数器会把实际速率乘以池的数量。
7. **`sql_log.enabled` 是总开关，默认关闭**（§3.1.2）——关闭时整节配置不生效：不构造 `Policy`、
   不打开 `sql_outputs`、不产生任何 SQL 日志行（慢/错都没有），`Trace` 零分配。缺段、`{}`、`null`
   与 `enabled: false` 等价，启动不报错、不告警，与 `db` / `redis` 等顶级段一致。
   写了 `enabled` 之外任何字段即视为启用，避免"配了 `mode: full` 却没生效"；`enabled: false` 是唯一
   能保留配置却关掉功能的方式（临时止血）。默认值走两层：`config.Load` 的 `defaults.Set` 填一遍
   （显式值覆盖），`logger.NewSQLPolicy` 再归一一次（不依赖应用用哪种加载方式）。为此三处改掉零值歧义：
   `sample_rate: 0` 视为未设置（全打写 `1`）、`max_length: -1` 表示不截断、`error_rate_limit: -1`
   表示不限；`ignore_schema_queries` 改名为正向的 `log_schema_queries`，让零值即默认。

遗留：

- **`hash` 策略的盐**：`sha256` 不加盐时手机号这类低熵值可被彩虹表反推；加盐则重启后不可关联。
  默认策略是 `mask`，`hash` 属于进阶用法，盐的问题在使用时再定。
- **批量 INSERT 的按列位置脱敏**：一期只做"列名=值"与值模式兜底，批量插入的列位置映射列为二期。
- **性能基准**：§11 的性能目标需要在真实 SQL 形态上校准，批次 1 完成后补基准。

---

## 14. 实施记录（2026-11-19）

批次 0～6 已落地，与方案的差异逐条记在这里。

### 14.1 落点

| 层 | 文件 | 内容 |
| --- | --- | --- |
| `logger` | `sqlmask.go` | 内置脱敏表（`DefaultMaskColumns` / `DefaultMaskPatterns`）、`Merge`、扫描器、三种策略 |
| `logger` | `sqllimit.go` | 采样器、窗口限流器、控制字符清理、截断 |
| `logger` | `sqlpolicy.go` | `SQLLogConfig`（含 `UnmarshalYAML` 键存在性）、`SQLMode`、`Policy`、trace 字段 |
| `logger` | `gorm.go` | `Trace` 按 mode 渲染、采样/限流/脱敏接入、schema 查询过滤 |
| `logger` | `logger.go` | `SQLOutputs` + `NewSQL`（与 `NewAccess` 同构） |
| `support/db`、`support/sqlite` | `db.go` / `sqlite.go` / `io.go` | 删除私有 gorm logger，改为复用 `logger.GormLogger`；新增 `WithSQLPolicy` / `WithSQLLogger`，`Registry` 透传 |
| 框架 | `option.go`、`gofy.go` | `WithSQLPolicy`；`buildLoggers` 返回 `loggers` 结构体；`buildDB` 注入策略与 SQL 通道 |
| 框架 | `support/consumer`、`support/{kafka,amqp}`、`support/headerpropagation` | `Outgoing` + 生产端合并 + 消费端写回 `X-Trace-Id` |
| demo | `app/config`、`options.go`、`reload.go`、`main.go`、`app/appio/db`、`config.yaml` | 顶层 `sql_log` 段、策略构造与共享、`sql_outputs`、关闭提示 |

### 14.2 与方案的四处差异

1. **`Policy` 由应用构造，不进 `Runtime`。** `core` 是叶子包（不得 import 其他 gofy 包），
   把 `*logger.Policy` 放进去就要破坏这条约束。改为应用在 `main` 里 `logger.NewSQLPolicy(cfg.SQLLog)`
   构造一次，把**同一个指针**交给 `gofy.WithSQLPolicy` 与 `appdb.WithSQLPolicy`；`reloader`
   持有它，Reload 重建连接池时继续传同一份，限流仍是进程级。
2. **关闭的实现方式是"不产生条目"，而不是不装 logger。** 关闭时 `Policy.Disabled()` 为真，
   `Trace` 在调用 `fc()` 之前返回，`ParamsFilter` 仍返回 nil 参数；因此零分配、零文本、
   零日志行，同时不必为旧行为保留第二份 logger 实现。
3. **`Registry` 需要透传选项。** 命名实例由 `db.NewRegistry` 按需打开，策略必须跟到那一层，
   否则 `db.order` 会绕过采样与脱敏。`NewRegistry` / `Configure` 因此都加了可变选项。
4. **消息链路只覆盖有 Header 概念的传输。** Kafka（`Headers`）与 AMQP（`Table`）已带
   `X-Trace-Id`；RocketMQ 的 `Properties` 有键名约束（不允许大写与连字符），Redis 队列没有
   Header 概念，二者未接线。`traceparent` 的注入仍未做——消费端 `extract` 早就在接续，
   生产端不写，这是遗留项而不是本次宣称的能力。

### 14.3 验证

| 场景 | 结果 |
| --- | --- |
| 框架 `go test ./...` | 全绿（新增 mask/merge/policy/limit/Outgoing 用例） |
| demo `make check` | 全绿（29 包，含 `-race`） |
| 真实运行：`sql_log.enabled: true` + `log_level: info` + `sample_rate: 1` | 输出 `{"msg":"sql","sql_mode":"placeholder","sql":"SELECT count(*) FROM `orders` WHERE status = ?"}`，无参数值 |
| 真实运行：`enabled: false` | 同一路径 0 条 SQL 条目，并在启动时打印 `warning: sql_log is disabled...` |
| 配置语义 | `sql_log` 缺席 / `{}` / `null` / `enabled: false` 均不报错；`mode: full` 单独出现即启用 |
