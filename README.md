# gofy

`gofy` 是一个基于 [gin](https://github.com/gin-gonic/gin) 的轻量、约定优先的 Go 应用框架。
它提供应用生命周期、类型化 HTTP handler、请求绑定与校验、统一响应包络、日志、
MySQL（GORM）接入、维护脚本和 CLI，同时没有全局状态、`init()` 副作用和固定配置文件。

## 安装

```bash
go get github.com/bingcool/gofy@latest
```

要求 Go 1.26+。

## 快速开始

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/transport"
)

type helloRequest struct {
	Name string `form:"name" default:"world" validate:"required,max=32" message:"max:名称过长"`
}

type helloResponse struct {
	Message string `json:"message"`
}

func main() {
	ctx, stop := gofy.SignalContext(context.Background())
	defer stop()

	app, err := gofy.New(
		gofy.WithName("hello"),
		gofy.WithAPIPrefix("/api/v1"),
		gofy.WithHealthEndpoints(true),
		gofy.WithModules(helloModule{}),
	)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if err := app.Execute(ctx); err != nil {
		os.Exit(gofy.ExitCode(err))
	}
}

type helloModule struct{}

func (helloModule) Name() string { return "hello" }

func (helloModule) Register(r *transport.Router) error {
	transport.GET(r, "/greet", func(_ context.Context, req *helloRequest) (*helloResponse, error) {
		return &helloResponse{Message: "hello " + req.Name}, nil
	})
	return nil
}
```

```bash
go run .                     # 等价于 go run . start
go run . version
go run . stop                # 配置了 pid 文件时可用，需输入 yes 确认
go run . stop --force=1      # 跳过确认
```

路由挂载在 `/api/v1/hello/greet`：

```bash
curl 'http://127.0.0.1:8080/api/v1/hello/greet?name=gofy'
# {"code":0,"msg":"success","data":{"message":"hello gofy"},"req_id":"..."}
```

## 脚手架

```bash
go install github.com/bingcool/gofy/cmd/gofy@latest

gofy new demo-service \
  --module github.com/yourorg/demo-service \
  --dir ./demo-service \
  --with-db \
  --with-docker
```

本地开发时加 `--replace /path/to/gofy` 指向本地仓库。生成的项目不包含密钥，
也不预生成 `go.sum`；执行 `go mod tidy` 后再运行 `go run . start`。

## 配置

gofy 自身从不读取配置文件。配置由业务项目负责加载，再通过 option 传入。
可选的配置加载器会应用默认值、展开 `${ENV_VAR}` 并做校验：

```go
type Config struct {
	HTTP gofy.HTTPConfig     `yaml:"http"`
	Log  gofy.LogConfig      `yaml:"log"`
	DB   gofy.DatabaseConfig `yaml:"db"`
}

cfg, err := gofyconfig.Load[Config](ctx, "config.yaml")
```

```yaml
http:
  port: 8080
db:
  dsn: ${ORDER_DB_DSN}
```

密钥应通过环境变量注入，不要提交到 YAML。

## Nacos 配置中心

`support/nacos` 直接调用 Nacos Open API（不引入 SDK）：启动时把远端配置写成
`config.yaml`，运行期间通过长轮询保持同步。示例见
`examples/demo-service/main.go`。

```go
nx := nacos.FromEnv() // 或 nacos.LoadConfig("nacos.yaml")
if nx.Enabled {
    if _, err := nacos.Sync(ctx, nx); err != nil {
        log.Fatalf("sync nacos config: %v", err)
    }
}
cfg, err := gofyconfig.Load[Config](ctx, nx.File) // 之后照常加载
// ...
if nx.Enabled {
    go nacos.Watch(ctx, nx, nacos.WithRestartOnChange(cfg.PIDFile))
}
```

也可以写成 `nacos.yaml`（优先级：文件 > 环境变量 > 默认值）：

```yaml
nacos:
  enabled: true
  addr: http://127.0.0.1:8848
  namespace: dev          # 命名空间 ID，public 或空表示默认命名空间
  data_id: gofy-service
  group: GO_GROUP
  file: config.yaml
```

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `NACOS_ENABLED` | 是否启用，`1`/`true` 启用 | `false` |
| `NACOS_ADDR` | 服务地址，优先级高于 `NACOS_HOST`/`NACOS_PORT` | `http://127.0.0.1:8848` |
| `NACOS_HOST` / `NACOS_PORT` | 服务地址与端口 | `127.0.0.1` / `8848` |
| `NACOS_USERNAME` / `NACOS_PASSWORD` | 开启鉴权的集群才需要 | 空 |
| `NACOS_NAMESPACE` | 命名空间 ID（不是显示名称） | 空（public） |
| `NACOS_DATA_ID` / `NACOS_GROUP` | 配置项标识 | — / `DEFAULT_GROUP` |
| `NACOS_FILE` | 生成的本地配置文件 | `config.yaml` |
| `NACOS_TIMEOUT_MS` | 单次请求超时 | `10000` |
| `NACOS_LISTENER_TIMEOUT_MS` | 长轮询挂起时间 | `30000` |
| `NACOS_LISTENER_FAILED_MS` | 监听或应用失败后的重试间隔 | `3000` |

```bash
NACOS_ENABLED=1 NACOS_ADDR=http://127.0.0.1:8848 NACOS_NAMESPACE=dev \
NACOS_DATA_ID=gofy-service NACOS_GROUP=GO_GROUP go run . start
```

行为说明：

- 远端内容必须是合法 YAML，否则拒绝写入，本地文件保持不变；
- 文件通过临时文件 + rename 原子替换，权限 `0600`（可能包含密钥），建议加入 `.gitignore`；
- 内容未变化时不重写文件；监听失败按 `NACOS_LISTENER_FAILED_MS` 重试；
- `WithRestartOnChange(pidFile)` 在配置变更并落盘后向 pid 文件记录的进程发送 SIGTERM，
  重启交给 systemd、Kubernetes 或容器 `restartPolicy`，新进程启动时重新拉取配置；
  需要自定义热更新逻辑时改用 `WithOnChange`；
- 启动时 Nacos 不可达或配置非法会让进程启动失败，与 swoolefy 的行为一致；
- 只在 `start` 路径调用 `Sync`，避免 `stop` / `version` 也依赖 Nacos
  （示例见 `examples/demo-service/main.go` 的 `isStartInvocation`）。

## CLI

| 命令 | 行为 |
| --- | --- |
| `./service` / `./service start` | 前台运行，直到收到 SIGINT/SIGTERM |
| `./service start --daemon=1` | 派生后台子进程后父进程退出，日志与标准输出重定向到 `/dev/null`；Windows 不支持，启动时直接 panic |
| `./service stop [--pid-file path] [--timeout 10s] [--force=1]` | 发送 SIGTERM，超时后可发送 SIGKILL；默认要求输入 `yes` 确认 |
| `./service restart [--force=1]` | 先停止 pid 文件对应的实例（如有），再前台启动；默认要求输入 `yes` 确认 |
| `./service script <name> [flags]` | 执行维护脚本 |
| `./service script --list` | 列出已注册脚本 |
| `./service version [--json]` | 输出构建信息 |
| `./service <name>` | 通过 `WithCommands` 注册的自定义命令 |

退出码：`0` 成功，`1` 运行错误，`2` 用法错误。

`start`/`stop`/`restart` 会在终端输出彩色的状态信息和启动信息表格（非 TTY 或设置了 `NO_COLOR` 时自动关闭颜色）。`stop`/`restart` 默认要求输入 `yes` 才继续；非交互环境（systemd、容器、CI）请显式传入 `--force=1`，否则 stdin 读到 EOF 会按放弃处理。

## 测试

`gofytest` 在进程内构建 handler，不监听端口、不写 pid 文件、不连接真实数据库：

```go
app := gofytest.New(t, gofy.WithModules(orderModule{}))
rr := app.GET("/api/v1/order/orders/1")
gofytest.Status(t, rr, 200)
```

运行框架测试：

```bash
make test-race
make example   # 构建并测试 examples/demo-service
```

`examples/demo-service` 通过 `replace gofy/gofy-interface-service => ../../../gofy-interface-service`
消费契约模块，本地需要把 `gofy-interface-service` 仓库放在 `gofy` 的同级目录。

MySQL 集成测试默认跳过，需显式开启：

```bash
GOFY_TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
  go test ./support/mysql -run Integration -v
```

## 目录结构

```text
gofy.go, option.go, start.go, execute.go, stop.go   App、生命周期、CLI
apperr/        业务错误模型
core/          共享 Runtime（叶子包）
transport/     Router、类型化 handler、Responder、中间件
logging/       zap 日志构建与 GORM 适配
support/mysql/ GORM MySQL 连接管理
support/nacos/ Nacos 配置拉取、本地生成与变更监听
script/        维护脚本描述
command/       自定义 CLI 命令描述
config/        可选 YAML 加载器
gofytest/      HTTP 测试辅助
internal/      pid 文件、进程控制、版本、cobra 装配
cmd/gofy/      脚手架 CLI（不被库本身 import）
examples/demo-service/  完整示例服务（独立 Go module）
docs/          设计与迁移文档
```

公共包不调用 `init()`、不保留包级可变状态、不调用 `os.Exit` 或 `panic`。
App 持有 logger、DB 和 HTTP server，并在关停时释放。
