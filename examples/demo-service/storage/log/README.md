# storage/log

demo-service 的日志目录，按执行模式分目录，参考 swoolefy 的
`cli` / `script` / `daemon` 日志划分：

```
storage/log/
├── http/                     # start / stop / restart / 无子命令启动
│   └── 20261106/             # 日期目录，由 rotation.date_dir 控制
│       ├── info-h0.log       # info 流按小时切分
│       ├── info-h1.log
│       ├── ...
│       ├── info-h23.log
│       ├── error.log         # 按大小切分
│       └── system_error.log  # 按大小切分
├── access/                   # HTTP 访问日志，与运行态日志分开
│   └── 20261106/
│       └── access-h14.log    # 跟随 rotation.mode
├── script/                   # script <name>
│   └── 20261106/
│       ├── info/
│       │   └── fix-order-h16.log
│       └── error/
│           ├── fix-order.log
│           └── fix-order-system_error.log
└── daemon/                   # worker --group
    └── 20261106/
        ├── info/
        │   └── order-consumer-h1.log
        └── error/
            ├── order-consumer.log
            └── order-consumer-system_error.log
```

| 文件 | 内容 | 切割方式 |
| --- | --- | --- |
| `info-h<H>.log` | 当前级别及以上的全部日志（默认 `info`） | 每小时一个文件，`h0` 不补零 |
| `error.log` | `error` 及以上，便于快速排查失败请求 | 日期目录内按大小 |
| `system_error.log` | 框架自动捕获的系统级错误，目前是 HTTP recovery 记录的 panic | 日期目录内按大小 |
| `access-h<H>.log` | 每条 HTTP 请求的访问记录（方法、路径、状态码、耗时等） | 跟随 `rotation.mode`，默认每小时 |

HTTP 模式把 `info` / `error` / `system_error` 三个运行态文件直接放在日期目录
下，访问日志单独放在 `access/`。脚本模式在日期目录下再分出 `info/` 和
`error/` 两个流目录，并用脚本名作为文件名，做到**一个脚本一个日志文件**：

```
script/20261106/info/fix-order-h16.log
script/20261106/error/fix-order.log
script/20261106/error/fix-order-system_error.log
```

`info/` 跟随 `rotation.mode`：`hourly` 生成 `fix-order-h<H>.log`，`size`
生成 `fix-order.log` 并按大小切分。`error/` 始终按大小切分：
`fix-order.log` 是脚本的 error 级日志，`fix-order-system_error.log` 是框架
自动捕获的 panic。两个文件分开，避免业务错误和系统异常混在一起。

daemon 模式（`worker` 子命令）使用与脚本模式相同的 `info/` + `error/` 布局，文件名
是**运行中的 worker 名**，做到一个 worker 一组文件：

```
daemon/20261106/info/order-consumer-h1.log
daemon/20261106/error/order-consumer.log
daemon/20261106/error/order-consumer-system_error.log
```

名字来自 `systemenv.WorkerNames()`（`--name`，支持 `--name=a`、`--name a` 与
`--name=a,b`）。进程只跑一个 worker 时文件名就是它；整组启动时没有唯一的 worker 名，
退回分组名（如 `order-h1.log`），具体实例仍由日志里的 `worker` 字段区分；
`worker --list` 这类没有分组的调用落在 `default`。分组名与 worker 名都会先清洗，
只保留字母、数字、`-` 和 `_`。

脚本名来自 `systemenv.ScriptName()`：取 `script` 后面的第一个非 flag 参数，
去掉空白，只保留字母、数字、`-` 和 `_`，防止命令行参数影响目录结构。
`script --list` 这类没有脚本名的调用仍使用原始文件名（`info/info.log`、
`error/error.log`、`error/system_error.log`）。

写入路径由 `app/config.Load` 在每次加载配置时追加，模式目录按
`systemenv.IsScriptService()` 选择：脚本执行写 `script/`，其余命令写 `http/`
并额外写 `access/`。因此 Nacos 热更新重新加载配置后仍然落在同一个模式目录和
同一个脚本文件，不会混写。

`config.yaml` 里的 `log.outputs` 会保留，例如 `stdout` 仍然输出到终端；
模式目录是追加的，不会覆盖已有输出。`access/` 通过 `log.access_outputs`
独立配置，未配置时访问日志回落到主 logger（即写进 `http/info.log`、
`http/error.log`）。

## 滚动配置

```yaml
log:
  outputs:
    - stdout
  rotation:
    enabled: true
    mode: hourly        # info.log：size | hourly
    date_dir: true      # 20261106 子目录
    date_layout: "20060102"
    date_dir_max_age_days: 7  # 超过 7 天删除整个日期目录
    max_size_mb: 100    # size 模式及 error/system 生效
    max_age_days: 30    # lumberjack 备份文件保留天数
    max_backups: 7
    compress: true
    local_time: true
```

- `mode: hourly`：`info.log` 变成 `info-h0.log` ... `info-h23.log`，跨小时、
  跨天都由进程自动切换，不需要重启；`access/access.log` 同样按小时切分；
- `mode: size`：`info.log` 在当天的日期目录内由 lumberjack 按 `max_size_mb`
  切分，备份文件也留在同一个日期目录；`access.log` 与 `info.log` 一致；
- `error.log` 和 `system_error.log` 始终使用 `size` 模式，即使 `mode: hourly`，
  避免每小时产生一个几乎空白的错误文件；
- `date_dir: false` 时退回平铺结构（如 `http/info-h14.log`），保持旧行为；
- 路径中的 `{date}` 会替换成 `date_layout` 格式的日期。`app/config` 用它把
  日期目录放到 `info/`、`error/` 之前，例如
  `script/{date}/info/fix-order.log`，否则日期目录会追加在最后一层
  （`script/info/20261106/fix-order.log`）；关闭滚动时占位符在打开文件时
  展开一次；
- `date_dir_max_age_days` 默认 7 天（配置为 0 也按 7 天处理），过期后会在
  启动和跨天时删除整个日期目录；`{date}` 位于整个路径段时，只扫描它前面的
  目录（如 `storage/log/script/`），`http`、`script` 等普通目录不会被动到；
- `max_age_days` 只控制 lumberjack 在**当前日期目录内**的备份文件清理，
  不影响日期目录本身；
- 文件采用惰性打开：`error.log` / `system_error.log` 在第一条对应级别日志
  出现时才创建，因此刚启动时可能只看到 `info-h<H>.log`。

日志文件由 `*.log` 规则忽略，目录本身通过 `.gitkeep` 保留。相对路径以进程
工作目录为基准，因此请在 `examples/demo-service` 下启动服务或执行脚本。

## 系统错误日志

`system_error.log` 与 `error.log` 的区别：

- `error.log` 记录业务代码和框架组件的所有 `error` 级日志；
- `system_error.log` 只记录框架自动拦截的异常，目前来自 HTTP 中间件的
  `recover()`（panic 内容、请求方法、路径、堆栈和 `req_id`）。

未配置 `log.system_error_outputs` 时，框架错误会回落到主 logger，仍写入
`error.log`，行为与之前一致。
