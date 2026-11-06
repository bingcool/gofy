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
