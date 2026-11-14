# core/systemenv

进程环境、运行系统和 goroutine 的只读判定，参考 swoolefy `SystemEnv` 的用法，
但保持无状态：没有 `init()`，没有包级变量，每次调用读取当前进程状态。

## 快速开始

```go
import "github.com/bingcool/gofy/core/systemenv"

if systemenv.IsProd() {
    // 生产环境才注册的指标、告警等
}

logger := zap.NewProduction(zap.Fields(
    zap.String("env", systemenv.GetEnv()),
    zap.Uint64("goroutine_id", systemenv.GetGoroutineID()),
))
```

环境变量：

```bash
GOFY_ENV=prod ./demo-service start
```

## 环境判定

环境值来自 `GOFY_ENV`：先 `TrimSpace` 再转小写，未设置或为空时按 `dev` 处理。

| 常量 | 值 | 判定方法 |
| --- | --- | --- |
| `EnvDev` | `dev` | `IsDev()` |
| `EnvTest` | `test` | `IsTest()` |
| `EnvGrad` | `grad` | `IsGrad()` |
| `EnvProd` | `prod` | `IsProd()` |

`GetEnv()` 返回规范化后的环境名。未知取值会被原样返回，四个 `Is*` 方法均为
`false`，不会 panic。

注意：`gofy.WithEnv(cfg.Env)` 设置的是 `Runtime.Env`，供应用运行时使用；
`systemenv` 读取的是进程环境变量。两者需要由业务保证一致，例如在 `main.go`
里用同一个值：`gofy.WithEnv(systemenv.GetEnv())`。

## 系统判定

| 方法 | 成立条件 |
| --- | --- |
| `IsWindows()` | `runtime.GOOS == "windows"` |
| `IsLinux()` | `runtime.GOOS == "linux"` |
| `IsMacos()` | `runtime.GOOS == "darwin"` |

## 服务类型判定

入口子命令决定当前进程的服务类型。这些方法读取 `os.Args[1:]` 的第一个非 flag
参数，不缓存结果：

| 启动方式 | `IsHTTPService()` | `IsScriptService()` | `IsWorkerService()` |
| --- | --- | --- | --- |
| `demo-service`（无子命令） | `true` | `false` | `false` |
| `demo-service start` | `true` | `false` | `false` |
| `demo-service stop` | `true` | `false` | `false` |
| `demo-service restart` | `true` | `false` | `false` |
| `demo-service script fix-order` | `false` | `true` | `false` |
| `demo-service worker --group=order` | `false` | `false` | `true` |
| `version` / `--help` | `false` | `false` | `false` |

`start`、`stop`、`restart` 和默认启动都作用于 HTTP 服务：`restart` 会先停旧实例
再就地启动，`stop` 通过 pid 文件停止 HTTP 实例。`version`、`help` 等命令三者
都不成立。判定基于进程真实的启动参数：用 `App.ExecuteArgs` 在测试里传入自定义
参数，不会改变这里的结果。

### ScriptName

`ScriptName()` 返回 `script` 子命令后面的脚本名，供按脚本拆分日志等场景使用：

| 启动参数 | 返回值 |
| --- | --- |
| `demo-service script fix-order --order-id=1` | `fix-order` |
| `demo-service script "Fix Order"` | `FixOrder` |
| `demo-service script ../../etc/passwd` | `etcpasswd` |
| `demo-service script --list` / `script --help` | `""` |
| `demo-service start` / 无子命令 | `""` |

返回值会去掉空白，只保留字母、数字、`-` 和 `_`。日志文件名会直接使用它，
因此过滤路径分隔符和 `..`，避免命令行参数把日志写到其他目录。

### WorkerGroup / WorkerNames

`worker` 子命令的两个参数读取器，供按分组/按 worker 拆分日志与 pid 文件使用：

| 启动参数 | `WorkerGroup()` | `WorkerNames()` |
| --- | --- | --- |
| `demo-service worker --group=order` | `order` | `nil` |
| `demo-service worker --group=order --name=order-consumer` | `order` | `[order-consumer]` |
| `demo-service worker --name order-scan --name order-consumer` | `""` | `[order-scan order-consumer]` |
| `demo-service worker --name=order-scan,order-consumer` | `""` | `[order-scan order-consumer]` |
| `demo-service worker --list` / `worker --help` | `""` | `nil` |
| `demo-service start` | `""` | `nil` |

`--name` 支持 `--name=a`、`--name a` 以及 pflag `StringSlice` 的逗号分隔形式，重复项
会被去掉。两个函数都会清洗返回值（去空白，只保留字母、数字、`-` 和 `_`），因为结果
会直接拼进文件名或 pid 路径。

`WorkerNames()` 返回全部名字而不是一个：进程可能同时跑多个 worker。日志实现据此决定
文件名——只有一个名字时用它，否则退回分组名，见 `examples/demo-service/app/config/log.go`。

## GetGoroutineID

`runtime` 没有公开 goroutine ID，本包解析 `runtime.Stack` 输出的第一行
（`goroutine 123 [running]:`），解析失败返回 `0`。它只适合写日志、排查问题：

- goroutine ID 会被 runtime 复用，不能作为缓存键或身份标识；
- 不要用它做同步或锁，`context.Context` 才是跨调用传递信息的正确方式；
- 每次调用都要抓取一次栈，热路径上不要频繁调用。

## 测试

```bash
go test ./core/systemenv/ -race
```
