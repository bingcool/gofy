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

## HTTP 安全与限流

三个 option 都是可选的，只作用于业务模块路由，`/healthz`、`/readyz` 和未匹配路径不受影响：

```go
parser, err := jwtauth.NewParser(jwtauth.Config{
	Secret:        []byte(os.Getenv("JWT_SECRET")),
	Issuer:        "demo",
	RequireExpiry: true,
})
if err != nil {
	return err
}

app, err := gofy.New(
	gofy.WithSecurityHeaders(transport.SecurityHeadersConfig{
		HSTS: 24 * time.Hour, // 仅在确认全站 HTTPS 后开启
	}),
	gofy.WithRateLimit(transport.RateLimitConfig{
		Store:  ratelimit.NewMemoryStore(),
		Limit:  100,
		Window: time.Minute,
	}),
	gofy.WithJWTAuth(transport.JWTAuthConfig{Parser: parser}),
	// ...
)
```

- **JWT**：失败固定返回 `401 / 40100`，成功后可 `transport.JWTClaims(c)` 读取 claims；
  `Optional: true` 时无 token 也放行，由业务自己判断。
- **限流**：滑动窗口，按 `客户端 IP + 方法 + 路由` 分桶；响应带 `X-RateLimit-*`，
  超限返回 `429 / 42900` 和 `Retry-After`。单进程用
  `ratelimit.NewMemoryStore()`；多实例用 `support/redis` +
  `ratelimit/redisstore` 共享计数。存储故障默认返回 `503 / 50300`，
  `FailOpen: true` 可放行。
- **安全响应头**：默认 `nosniff`、`DENY`、`strict-origin-when-cross-origin`，
  可追加 HSTS、CSP、Permissions-Policy 和自定义头；含换行的值在启动时报错。
- **404 / 405**：未匹配路径和错误方法统一走响应包络（`40400` / `40500`），405 带
  `Allow` 头。
- **日志脱敏**：访问日志记录 query 时，`password`、`token`、`authorization`、
  `api_key`、`signature` 等值替换为 `***`；无法解析的 query 整体记为 `***`。

JWT 只做校验，不签发 token；签发由业务或独立认证服务完成。

## 热更新

`App.Reload` 在运行中的进程内重建运行时资源，不重启进程，也不重新监听端口：

```go
err := app.Reload(ctx, gofy.ReloadSpec{
    Env:             cfg.Env,           // 空字符串表示保持当前值
    Log:             &cfg.Log,          // 重新构建 logger
    Database:        &cfg.DB,           // 重新连接数据库
    ModuleFactories: factories,         // 重新注册路由与模块
})
```

执行顺序：校验新配置 → 构建新 logger/DB → 构建新模块并启动 → 原子替换 handler 与
`Runtime` → 停止旧模块 → 关闭旧的 DB 和 logger。新模块启动失败时回滚，旧服务继续可用。
`Reload` 只能在服务已启动后调用，否则返回 `ErrNotRunning`；并发调用会串行化。

HTTP 监听地址与超时、pid 文件不在 `ReloadSpec` 中，修改这些内容需要重启进程。
通过 `WithModules` 注入的实例不会被重建：它们继续运行，只在新路由上重新注册，
因此仍持有创建时捕获的 `Runtime`；需要跟随配置变化的模块请改用 `WithModuleFactories`。
中间件和 responder 由启动时的选项重新应用，同样不能在 `Reload` 中更换。

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

数据库连接用 `support/mysql`（GORM）：`WithDatabase` 交给 App 托管连接池，
`WithDB` 注入已有连接。配置项、默认值和日志行为见
[support/mysql/README.md](support/mysql/README.md)。

## Nacos 配置中心

`support/nacos` 直接调用 Nacos Open API（不引入 SDK）：启动时把远端配置写成
`config.yaml`，运行期间通过长轮询保持同步。示例见
`examples/demo-service/main.go`；完整 API 见
[support/nacos/README.md](support/nacos/README.md)。

```go
// swoolefy 风格：连接信息与配置中心项分开；没有 nacos.yaml 时可用 nacos.FromEnv()
nx, err := nacos.Load("nacos.yaml", "application.yaml")
if err != nil {
    return err
}
if nx.Enabled {
    if _, err := nacos.Sync(ctx, nx); err != nil {
        log.Fatalf("sync nacos config: %v", err)
    }
}
cfg, err := gofyconfig.Load[Config](ctx, nx.File) // 之后照常加载
// ...
if nx.Enabled && nx.Watch {
    go nacos.Watch(ctx, nx, nacos.WithLogger(logger),
        nacos.WithOnChange(func(ctx context.Context, _ nacos.Result) error {
            cfg, err := gofyconfig.Load[Config](ctx, nx.File)
            if err != nil {
                return err
            }
            // 进程内热更新：监听端口不变，重建日志、数据库与模块
            return app.Reload(ctx, gofy.ReloadSpec{
                Env:             cfg.Env,
                Log:             &cfg.Log,
                Database:        &cfg.DB,
                ModuleFactories: factories,
            })
        }))
}
```

**两文件布局（与 swoolefy 一致）**

`nacos.yaml` 只放服务器连接：

```yaml
nacos:
  host: 127.0.0.1
  port: 8848
  username: ''
  password: ''
  namespace: dev          # 配置中心命名空间 ID；public 或空表示默认命名空间
```

`application.yaml` 放配置中心项和监听开关：

```yaml
nacos:
  service_config:
    data_id: gofy-service
    group: GO_GROUP
    tenant: ''            # 仅当 nacos.yaml 未配置 namespace 时生效
  enable_nacos_config_change_restart: true
  monitor_config_change:
    listener_timeout_ms: 30000
    failed_wait_ms: 3000
```

规则：

- `nacos.Load` 合并两个文件，优先级为 文件 > 环境变量 > 默认值；
- 该布局默认启用 Nacos（与 swoolefy 一样，文件存在即视为配置由远端托管），
  `nacos.enabled: false` 或 `NACOS_ENABLED=0` 可关闭；
- 命名空间优先级：`nacos.yaml` 的 `namespace`（或 `NACOS_NAMESPACE`）→
  `service_config.tenant`（或 `NACOS_TENANT`）→ public；
- `enable_nacos_config_change_restart` 对应 `Config.Watch`，控制启动后是否继续监听；
  配置变更后由回调调用 `App.Reload` 做进程内热更新，不再重启进程（键名沿用 swoolefy）；
- `monitor_config_change` 覆盖 `NACOS_LISTENER_TIMEOUT_MS` / `NACOS_LISTENER_FAILED_MS`；
- `nacos.yaml` 不存在时 `Load` 返回的错误满足 `errors.Is(err, fs.ErrNotExist)`，
  可回退到 `nacos.FromEnv()`（示例见 `examples/demo-service/main.go`）。

**单文件写法**

所有键也可以写进同一个 `nacos.yaml`（需显式 `enabled: true` 或 `NACOS_ENABLED=1`）：

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
| `NACOS_ENABLED` | 是否启用，`1`/`true` 启用 | `false`（两文件布局下默认 `true`） |
| `NACOS_ADDR` | 服务地址，优先级高于 `NACOS_HOST`/`NACOS_PORT` | `http://127.0.0.1:8848` |
| `NACOS_HOST` / `NACOS_PORT` | 服务地址与端口 | `127.0.0.1` / `8848` |
| `NACOS_USERNAME` / `NACOS_PASSWORD` | 开启鉴权的集群才需要 | 空 |
| `NACOS_NAMESPACE` | 命名空间 ID（不是显示名称） | 空（public） |
| `NACOS_TENANT` | 命名空间 ID 的兜底值，仅当 `NACOS_NAMESPACE` 为空时生效 | 空（public） |
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
- 配置变更后的推荐处理是 `App.Reload`：在同一个进程里重建日志、数据库连接和模块，
  先启动新模块再停止旧模块，最后原子替换 HTTP handler，监听 socket 保持不变；
- 需要在变更时自定义动作（或不使用 `App.Reload`）时用 `nacos.WithOnChange`；
  旧版本示例里的 `WithRestartOnChange` 会发送 SIGTERM，仍然保留但不再是推荐做法；
- 热更新范围有限：HTTP 地址与超时、pid 文件、中间件和 responder 不会重建；
  通过 `WithModules` 注入的模块只重新注册、不重启；Nacos 配置里改了 host/port
  只会打警告，需要真正重启进程才会换监听地址；
- 启动时 Nacos 不可达或配置非法会让进程启动失败，与 swoolefy 的行为一致；
- 两文件布局是 demo-service 的默认方式：`nacos.yaml` 管连接，`application.yaml`
  管 `service_config` 和监听开关，详见上文的规则；
- 只在 `start` 路径调用 `Sync`，避免 `stop` / `version` 也依赖 Nacos
  （示例见 `examples/demo-service/main.go` 的 `isStartInvocation`）。

## Nacos 服务注册与发现

`support/nacos` 同时实现了 Naming API：`App` 在监听端口可用后把自身注册到 Nacos，
退出时注销。客户端侧（`gofy-interface-service/client/nacos`）按服务名解析实例并
做负载均衡。

**服务注册**

`application.yaml` 沿用 swoolefy 的键：

```yaml
nacos:
  enable_nacos_register: true
  service_register:
    ip: ${POD_IP}          # NACOS_SERVICE_REGISTER_HOST → POD_IP → 自动探测
    service_name: demo-service
    namespace_id: dev
    group_name: swoolefy-group
    weight: 1
    heartbeat_interval: 5
    metadata:
      version: 1.0.0
  discovery_service_client:
    load_balancer: random  # random | round_robin | weight
    cache_ttl: 60
    stale_ttl: 180
    healthy_only: true
    namespace_id: dev
    group_name: swoolefy-group
```

把合并后的配置交给 `App`：

```go
nx, err := nacos.Load("nacos.yaml", "application.yaml")
if err != nil {
    return err
}
app, err := gofy.New(
    gofy.WithHTTP(httpCfg),
    gofy.WithNacosRegistration(nx), // 端口默认取实际 HTTP 监听端口
    // ...
)
```

行为说明：

- 监听端口开始接受连接后才注册，避免流量打到尚未就绪的实例；端口优先级为
  `NACOS_SERVICE_REGISTER_PORT` > `service_register.port` > 实际监听端口；
- 实例固定为临时实例（ephemeral），按 `heartbeat_interval` 发送心跳，Nacos 可在
  心跳响应里调整间隔（限制在 1s–30s）；连续失败后先补一次完整心跳，仍失败则重新注册；
- `App.Shutdown` / `stop` 先注销实例再关闭监听，注销失败只记录警告；
- 注册对象挂在 `App` 上，`App.Reload` 不会重复注册或注销，热更新期间服务发现保持连续；
- `WithNacosRegistration` 在 `service_register.service_name` 为空时直接返回错误。

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `NACOS_SERVICE_REGISTER_HOST` | 注册 IP，优先级最高 | 空 |
| `POD_IP` | Kubernetes Downward API 提供的 Pod IP | 空 |
| `NACOS_SERVICE_REGISTER_PORT` | 注册端口 | 实际监听端口 |
| `NACOS_SERVICE_NAMESPACE_ID` / `NACOS_SERVICE_GROUP_NAME` | 注册命名空间 / 分组 | 连接命名空间 / `DEFAULT_GROUP` |
| `NACOS_DISCOVERY_LOAD_BALANCER` | 客户端负载均衡策略 | `random` |
| `NACOS_DISCOVERY_CACHE_TTL` / `NACOS_DISCOVERY_STALE_TTL` | 实例缓存时间（秒） | `60` / `max(3×cache, 180)` |
| `NACOS_DISCOVERY_HEALTHY_ONLY` | 是否只返回健康实例 | `true` |
| `NACOS_DISCOVERY_CLUSTERS` | 只发现指定集群，逗号分隔 | 空（全部） |

**客户端服务发现（gofy-interface-service）**

`client/nacos.Resolver` 实现了 `client.Resolver`：按服务名调用 `instance/list`，
在本地缓存实例列表；Nacos 不可达时在 `stale_ttl` 内继续使用最后一次成功的结果；
请求失败会清空缓存，重试时重新解析，从而切换到其他实例。

```go
import (
    "gofy/gofy-interface-service/appinterface/demoservice/demoserviceclient"
    "gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
    resolvernacos "gofy/gofy-interface-service/client/nacos"
)

resolver, err := resolvernacos.NewFromFiles("nacos.yaml", "application.yaml")
if err != nil {
    return err
}
orders, err := demoserviceclient.NewWithResolver(resolver) // serviceName = demo-service
if err != nil {
    return err
}
res, err := orders.GetOrder(ctx, &orderrequest.GetOrderRequest{ID: 1})
```

`demoserviceclient.NewWithResolver` 固定使用 `demoservice.ServiceName`（`demo-service`）。
需要自定义客户端配置时用
`client.New(client.Config{Service: demoservice.ServiceName, Resolver: resolver})`
再交给 `demoserviceclient.New`。

不使用 Nacos 时用固定地址（域名或 IP，省略 scheme 默认 http）：

```go
orders, err := demoserviceclient.NewWithBaseURL("https://orders.example.com")
```

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
transport/     Router、类型化 handler、Responder、中间件、限流
logging/       zap 日志构建与 GORM 适配
core/systemenv/ 环境、系统与 goroutine 判定
support/mysql/ GORM MySQL 连接管理
support/redis/ Redis 客户端（go-redis）
support/nacos/ Nacos 配置拉取、本地生成与变更监听
support/jwtauth/ HMAC JWT 校验
transport/ratelimit/redisstore/  Redis 共享限流存储
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
