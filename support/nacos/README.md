# support/nacos

Nacos 集成包，直接调用 Nacos Open API，不引入 SDK，包含三部分：

- **配置中心**：启动时把远端配置同步成本地 YAML 文件，之后用长轮询保持更新；
- **服务注册**：把当前进程注册为临时实例并维持心跳，退出时注销；
- **服务发现相关配置**：解析 `application.yaml` 的 `discovery_service_client`，
  供客户端（如 `gofy-adapter/client/nacos`）使用。

推荐沿用 swoolefy 的两文件布局：`nacos.yaml` 只放服务器连接，`application.yaml`
放配置中心项、注册信息、发现策略与监听开关。

## 快速开始

```go
import (
    "context"
    "errors"
    "io/fs"

    "github.com/bingcool/gofy"
    "github.com/bingcool/gofy/support/nacos"
)

// 1. 合并 nacos.yaml + application.yaml；没有 nacos.yaml 时回退到环境变量
nx, err := nacos.Load("nacos.yaml", "application.yaml")
if errors.Is(err, fs.ErrNotExist) {
    nx, err = nacos.FromEnv(), nil
}
if err != nil {
    return err
}

// 2. start 路径同步远端配置到本地文件
if nx.Enabled {
    if _, err := nacos.Sync(ctx, nx); err != nil {
        return err
    }
}

// 3. 本地加载 synced 的 config.yaml
cfg, err := config.Load(ctx, nx.File)
if err != nil {
    return err
}

// 4. 注册到 Nacos：监听端口可用后才注册，Shutdown 前注销
app, err := gofy.New(
    gofy.WithHTTP(cfg.HTTP),
    gofy.WithNacosRegistration(nx),
    // ...
)
if err != nil {
    return err
}

// 5. 配置变更：进程内热更新（推荐）
if nx.Enabled && nx.Watch {
    go nacos.Watch(ctx, nx, nacos.WithLogger(app.Logger()),
        nacos.WithOnChange(func(ctx context.Context, _ nacos.Result) error {
            next, err := config.Load(ctx, nx.File)
            if err != nil {
                return err
            }
            return app.Reload(ctx, gofy.ReloadSpec{
                Env:             next.Env,
                Log:             &next.Log,
                Database:        &next.DB,
                ModuleFactories: factories,
            })
        }))
}

return app.Execute(ctx)
```

不要在 `stop` / `version` / `script` 等路径调用 `Sync`，否则这些命令也会依赖 Nacos。

## 配置文件

### nacos.yaml

```yaml
nacos:
  host: 127.0.0.1
  port: 8848
  username: ''
  password: ''
  namespace: dev          # 命名空间 ID；public 或空表示默认命名空间
  data_id: gofy-service   # 单文件布局时才放这里
  group: GO_GROUP
  file: config.yaml
```

支持 `addr: http://127.0.0.1:8848`，优先级高于 `host`/`port`。

### application.yaml

```yaml
nacos:
  enabled: true                       # 可选；两文件布局默认启用
  service_config:
    data_id: gofy-service
    group: GO_GROUP
    tenant: ''                        # 仅当 nacos.yaml 未配置 namespace 时生效
  enable_nacos_config_change_restart: true
  config_change_strategy: reload    # reload | restart | exit，见「配置变更策略」
  restart_grace_period: 5          # 秒；restart / exit 前摘流后的固定等待，0 表示不等
  monitor_config_change:
    listener_timeout_ms: 30000
    failed_wait_ms: 3000

  enable_nacos_register: true
  service_register:
    ip: ${POD_IP}
    # port: 9501                     # 默认使用实际 HTTP 监听端口
    service_name: demo-service
    namespace_id: dev
    group_name: swoolefy-group
    cluster_name: DEFAULT
    weight: 1
    heartbeat_interval: 5            # 秒
    metadata:
      version: 1.0.0

  discovery_service_client:
    load_balancer: random            # random | round_robin | weight
    cache_ttl: 60                    # 秒
    stale_ttl: 180                   # 秒
    healthy_only: true
    namespace_id: dev
    group_name: swoolefy-group
    clusters: DEFAULT
```

规则：

- `nacos.Load` 合并两个文件，优先级为 **文件 > 环境变量 > 默认值**；
- `application.yaml` 可选：不存在时保留 `nacos.yaml` 的结果；其他读取/解析错误直接返回；
- 两文件布局默认启用 Nacos；`nacos.enabled: false` 或 `NACOS_ENABLED=0` 可关闭；
- 命名空间优先级：`nacos.yaml` 的 `namespace`（或 `NACOS_NAMESPACE`）→
  `service_config.tenant`（或 `NACOS_TENANT`）→ public；
- `enable_nacos_config_change_restart` 对应 `Config.Watch`，只控制启动后是否继续
  监听；键名沿用 swoolefy，变更后的动作由 `config_change_strategy` 决定；
- `config_change_strategy` 决定变更落盘后的动作，可选 `reload` / `restart` / `exit`，
  默认 `reload`；`restart_grace_period` 是 `restart` / `exit` 动作前摘流后的固定等待
  （单位秒，默认 `0` 即不等）；
- **这两个键环境变量优先于文件**：部署形态属于运行环境而不是镜像内容，
  `NACOS_CONFIG_CHANGE_STRATEGY` / `NACOS_RESTART_GRACE_PERIOD` 可以覆盖镜像里
  写死的取值；其余键仍保持 文件 > 环境变量 > 默认值；
- `service_register.namespace_id` / `group_name` 为空时继承连接命名空间与
  `DEFAULT_GROUP`；
- `discovery_service_client` 的 `namespace_id` / `group_name` 为空时继承
  `service_register` 的值。本包只负责解析，实际发现逻辑在客户端实现；
- `service_register.ephemeral` 不读取，实例固定为临时实例，与 swoolefy 一致。

### 环境变量

| 变量 | 说明 | 默认值 |
| --- | --- | --- |
| `NACOS_ENABLED` | 是否启用 | `false`（两文件布局下默认 `true`） |
| `NACOS_ADDR` | 服务地址，优先于 `NACOS_HOST`/`NACOS_PORT` | `http://127.0.0.1:8848` |
| `NACOS_HOST` / `NACOS_PORT` | 服务地址与端口 | `127.0.0.1` / `8848` |
| `NACOS_USERNAME` / `NACOS_PASSWORD` | 鉴权信息 | 空 |
| `NACOS_NAMESPACE` | 命名空间 ID（不是显示名称） | 空（public） |
| `NACOS_TENANT` | 命名空间兜底值 | 空（public） |
| `NACOS_DATA_ID` / `NACOS_GROUP` | 配置项标识 | — / `DEFAULT_GROUP` |
| `NACOS_FILE` | 本地生成文件 | `config.yaml` |
| `NACOS_CONFIG_CHANGE_STRATEGY` | 变更策略，优先于 application.yaml | `reload` |
| `NACOS_RESTART_GRACE_PERIOD` | restart / exit 前摘流后的等待秒数，优先于 application.yaml | `0` |
| `NACOS_TIMEOUT_MS` | 单次请求超时 | `10000` |
| `NACOS_LISTENER_TIMEOUT_MS` | 长轮询挂起时间 | `30000` |
| `NACOS_LISTENER_FAILED_MS` | 监听/应用失败后的重试间隔 | `3000` |
| `NACOS_SERVICE_REGISTER_HOST` | 注册 IP，优先级最高 | 空 |
| `POD_IP` | K8s Pod IP | 空 |
| `NACOS_SERVICE_REGISTER_PORT` | 注册端口 | 实际监听端口 |
| `NACOS_SERVICE_NAMESPACE_ID` | 注册命名空间 | 连接命名空间 |
| `NACOS_SERVICE_GROUP_NAME` | 注册/发现分组 | `DEFAULT_GROUP` |
| `NACOS_DISCOVERY_LOAD_BALANCER` | 客户端负载均衡策略 | `random` |
| `NACOS_DISCOVERY_CACHE_TTL` | 实例缓存秒数 | `60` |
| `NACOS_DISCOVERY_STALE_TTL` | 陈旧列表可用秒数 | `max(3×cache, 180)` |
| `NACOS_DISCOVERY_HEALTHY_ONLY` | 是否只返回健康实例 | `true` |
| `NACOS_DISCOVERY_CLUSTERS` | 只发现指定集群，逗号分隔 | 空（全部） |

## 配置中心

### Load / FromEnv / LoadConfig

| 函数 | 用途 |
| --- | --- |
| `Load(nacosPath, appPath)` | 两文件布局；`appPath` 可为空或不存在 |
| `LoadConfig(path)` | 单文件（所有键写在同一个 `nacos.yaml`） |
| `FromEnv()` | 只读环境变量；`nacos.yaml` 缺失时的回退 |
| `DefaultConfig()` | `127.0.0.1:8848`、`config.yaml`、默认关闭 |

### Sync

`Sync(ctx, cfg)` / `(*Client).Sync(ctx)`：

1. 拉取远端配置；
2. 校验必须是合法 YAML，否则返回错误且**不修改本地文件**；
3. 内容未变化时不重写；
4. 内容变化时写入同目录临时文件，`chmod 0600`，`fsync` 后 `rename` 原子替换。

返回 `Result{File, Digest, Changed}`，`Digest` 是远端内容的 MD5。

顶层 `Sync(ctx, cfg)` 为这次调用自建客户端，返回前会关闭它的空闲连接；需要反复调用时
用 `NewClient` 复用同一个 `*Client`，并在结束时调用 `Close`。

### Watch

```go
go nacos.Watch(ctx, nx,
    nacos.WithLogger(logger),
    nacos.WithDrain(app.StopAccepting),
    nacos.WithOnChange(func(ctx context.Context, r nacos.Result) error {
        // 文件已经替换完成
        return app.Reload(ctx, spec)
    }))
```

- 启动时先 `Sync` 一次；失败时用本地文件的摘要继续轮询；
- 通过 `configs/listener` 长轮询检查变化，服务端挂起最短 30 秒；`Long-Pulling-Timeout`
  头与客户端实际等待时长一致（`PollTimeout` 低于 30s 时按 30s 发送）；
- 监听失败按 `RetryDelay` 重试；`changed=false` 却在不足挂起时长一半就返回（代理或错误页
  提前截断）时同样按 `RetryDelay` 退避，避免紧循环；远端内容非法时只记录错误，不替换文件；
- `WithOnChange` 在文件写入后执行，返回的错误只记录日志；只在 `reload` 策略下调用；
- `WithDrain` 只在 `restart` / `exit` 策略下、动作执行前调用，用于停止接受新请求；
  错误只记录日志，不取消动作。钩子内不要调用 `App.Shutdown`（会与 watcher 自等待）；
- `WithRestartOnChange(pidFile)` 是 `config_change_strategy: exit` 的兼容写法，
  强制发送 SIGTERM 交给 supervisor 重启；推荐改用配置化策略；
- `Watcher` 自建的客户端归 `Run` 所有：退出时关闭空闲连接。只构建、不启动的
  `Watcher` 用 `Close()` 释放。

**命令边界**：

| 命令 | 启动时 `Sync` | 运行期 `Watch` | 三种变更策略 |
| --- | --- | --- | --- |
| `start` / 无子命令（HTTP） | 一次 | 有 | 生效 |
| `worker --group=<g>` | 一次 | **无** | **不生效** |
| `worker --list` / `--help`、`script`、`stop`、`version` | 无 | 无 | 不生效 |
| `restart`（进程内 stop + Run） | 无 | 无 | 不生效 |

worker 启动时拉一次配置，是为了让新起的进程不拿镜像里那份可能已过期的
`config.yaml` 启动；运行期保持不可变，配置变更由部署层滚动重启生效。查询类命令
完全不访问 Nacos，配置中心不可用时仍要可用。`restart` 不重新执行 `main`，沿用本次
进程启动时加载的配置，因此也不挂 watcher。

### 配置变更策略

这三种策略由 HTTP 进程的 watcher 执行，`config_change_strategy` 决定变更内容写入
本地文件之后做什么，三种取值对应三种部署形态：

| 策略 | 动作 | 适用部署 | 进程 / pid |
| --- | --- | --- | --- |
| `reload`（默认） | 调用 `WithOnChange`，应用侧执行 `App.Reload` 原地重建模块与连接池 | 本地开发 | 不变，不退出 |
| `restart` | 摘流 → 等待 → `syscall.Exec` 重新执行当前二进制，从 `main` 开头再走一遍 | 物理机 / 云主机，无外部监管 | pid 不变，进程映像全新 |
| `exit` | 摘流 → 等待 → 给自己发 SIGTERM，优雅退出后由外部拉起 | k8s、Docker restart policy、systemd | 进程退出，pid 变化 |

配置写在本地 `application.yaml`（不会被 `Sync` 覆盖），也可用
`NACOS_CONFIG_CHANGE_STRATEGY` / `NACOS_RESTART_GRACE_PERIOD` 注入。这两个键是
环境变量优先于文件的键：镜像里写着 `reload`，k8s 清单或 `docker -e` 仍能把它切成
`exit`，或重新设定摘流等待。策略取值非法时 `Load` 直接报错，不静默回落默认值。

#### 摘流与 grace period

`restart` / `exit` 在动作之前执行两步，**`reload` 不执行**、全程继续对外服务：

1. `WithDrain(fn)` 钩子：应用应在这里停止接受新的 HTTP 请求。demo-service 注入的是
   `App.StopAccepting(ctx)` —— 它把 HTTP handler 的闸门关掉（新请求直接 503，
   带 `Connection: close`，健康检查也会变红），并从 Nacos 命名服务注销；listener
   保持打开、`Serve` 继续运行、在途请求仍持有 generation lease 可以跑完。
   **钩子不能调用 `App.Shutdown`**：它运行在 watcher goroutine 里，Shutdown 会停掉
   该 watcher 所属模块并等它退出，形成自等待死锁。钩子报错只记日志，不取消动作。
2. `restart_grace_period`（秒，默认 `0`）：固定等待，给在途请求、worker tick、
   consumer handler 一点收尾时间。等待可被 ctx 取消（例如进程已收到 SIGTERM）。

这是**最小折中**：框架不等待任何具体任务，只是降低把请求、handler 或事务切断的
概率。超过该秒数仍未跑完的工作照样会被 `exec` / SIGTERM 切断；未 ack 的消息仍会
重投，handler 必须幂等。想真正做到「等在途任务结束」，应改用 `exit` + supervisor，
走框架的完整优雅停机链路。

**不要把它当成严格优雅关停**。准确的说法是「摘流 + 固定等待 + 换/退进程」：它不知道
任何具体请求是否结束，只知道已经等了几秒。

#### 部署预算

固定等待只有在部署预算配好时才安全，四项缺一不可：

| 需要保证 | 原因 |
| --- | --- |
| `grace_period` ≥ HTTP handler 与 MQ handler 的 p99（含下游超时） | 小于实际耗时等于按概率切断请求与事务 |
| k8s `terminationGracePeriodSeconds` ≥ `grace_period` + 退出清理预算 | `exit` 先等 grace，再走完整停机链路（`shutdownTimeout` 默认 10s，worker `DrainTimeout` 默认 20s）；总预算不够会被 kubelet 直接 SIGKILL |
| 摘流（闸门 503 + Nacos 注销）严格先于动作 | 顺序反过来的话，等待期间新流量还在进来，等待就失去意义 |
| 不可中断任务自身幂等、可恢复 | 任何固定等待都会切到它们；未 ack 消息靠 broker 重投，副作用靠 `support/claim` 的 owner+attempts fencing |

**动作失败会恢复接客**：`exec` / 发信号失败时，watcher 调用 `WithResume` 钩子（demo
注入的是 `App.ResumeAccepting`）重新打开闸门并重新注册，进程继续对外服务；如果 resume
本身也失败，watcher 会再请求一次终止，把进程交给监管者 —— 而不是停在「活着、已注销、
永久 503」的状态。

边界：

- **三种策略只在 HTTP 进程生效**：`worker` 不挂 watcher，`reload` / `restart` /
  `exit` 对它都不执行；`reload` 还依赖 `App.Reload` 的 generation 换代机制，worker
  进程本来就没有这套东西。worker 的配置变更由部署层滚动重启负责，与「进程不可变、
  配置变更等于发布一个新版本」的云原生惯例一致，也避免同一批实例热更新进度不一致。
- `restart` 只用于**没有外部监管者**的物理机 / 云主机 / 本地环境。有 k8s、
  Docker restart policy、systemd 时应优先用 `exit`，或干脆不监听 Nacos、由部署层
  滚动更新：这样变更有 revision、可回滚、可审计，且每个实例拿到的是同一版配置。
- `reload` 只在应用注入了 `WithOnChange` 时才有意义；另外两种策略不调用回调。
- `restart` 用 `exec` 替换进程映像，**不执行 defer**、监听 fd 因 close-on-exec 释放；
  环境变量被继承，若应用用 `GOFY_ENV` 判断"是否显式设置"，自重启后会误判为显式
  设置。Windows 不支持该策略，返回错误。
- `exit` 只负责退出，不会自己拉起新进程；没有 `--restart=always` / k8s
  `restartPolicy` / systemd 监管时服务会一直停着。新进程启动时 `Sync` 会再拉一次
  配置，因此读到的必然是最新内容。
- `WithRestartOnChange(pidFile)` 是 `exit` 的兼容入口，仍可用但推荐改用配置；
  它同样会走摘流与等待流程。

## 服务注册

`gofy.WithNacosRegistration(cfg)` 把注册交给 `App` 管理：

- HTTP 监听端口开始接受连接后才注册，注册失败会让启动失败；
- 端口优先级：`NACOS_SERVICE_REGISTER_PORT` > `service_register.port` > 实际监听端口；
- IP 优先级：`NACOS_SERVICE_REGISTER_HOST` > `POD_IP` > 配置中的 `ip` >
  到 Nacos 的外联地址 > 第一个非回环 IPv4 > `127.0.0.1`；
- 实例固定为临时实例（ephemeral），按 `heartbeat_interval` 发送心跳；
  Nacos 可在响应中调整间隔（限制 1s–30s），并逐步切换为轻心跳；
- 心跳失败后先切回完整心跳；连续失败两次则重新注册；
- `Shutdown` / `stop` 先注销再关闭监听，注销失败只记录警告；
- 注册对象挂在 `App` 上，`App.Reload` 不会重复注册或注销；
- **启动期摘流会撤销注册**：watcher 模块先于注册启动，若它在注册进行时已触发
  `StopAccepting`，注册完成后会立刻注销（撤销失败视为启动失败）。否则会出现「已经
  在返回 503、却仍挂在命名服务里」的实例，这正是发现服务最该避免的状态。

需要自己控制生命周期时直接用 `Registrar`：

```go
registrar, err := nacos.NewRegistrar(nx, logger)
if err != nil {
    return err
}
if err := registrar.Start(ctx); err != nil {
    return err
}
defer registrar.Stop(ctx)

inst := registrar.Instance() // 实际注册的 IP/端口/分组
```

`NewRegistrar` 要求 `Register.Enabled` 为 true 且 `ServiceName` 非空，端口可用
`RegisterConfig.Port` 或环境变量提供。

## Naming API

不想用 `Registrar` 时可以直接调用底层接口：

```go
client, err := nacos.NewNamingClient(nx) // 只校验连接字段，不要求 DataID/File

err = client.RegisterInstance(ctx, nacos.Instance{
    ServiceName: "demo-service",
    IP:          "10.0.0.5",
    Port:        8080,
    Weight:      1,
    Ephemeral:   true,
    Group:       "swoolefy-group",
    Namespace:   "dev",
    Metadata:    map[string]string{"scheme": "https"},
})

hosts, err := client.ListInstances(ctx, nacos.InstanceQuery{
    ServiceName: "demo-service",
    Group:       "swoolefy-group",
    Namespace:   "dev",
    HealthyOnly: true,
})
healthy := nacos.AvailableInstances(hosts)

beat, err := client.BeatInstance(ctx, inst, true) // true = 轻心跳
err = client.DeregisterInstance(ctx, inst)
```

- 重复注册同一 `ip:port` 是幂等的；
- `BeatInstance` 的 `light=true` 不携带 body；`BeatResult` 返回服务端建议的间隔
  与是否继续使用轻心跳；
- `Instance.Metadata.scheme` 可供客户端选择 `http`/`https`。

## 错误

| 错误 | 含义 |
| --- | --- |
| `ErrDisabled` | `Config.Enabled=false` 时调用 `Sync` / `Watch` / `Get` / `Listen` |
| `ErrConfigNotFound` | Nacos 返回 404，配置项不存在 |

## 测试

```bash
go test ./support/nacos/
go test -race ./support/nacos/
```

单元测试用 `httptest` 模拟 Nacos，覆盖配置合并优先级、YAML 校验与原子写入、
长轮询、鉴权 token 缓存、注册/心跳/注销、实例列表过滤与 IP 探测。
