# app/module/order

演示模块：用 `gofy` 的模块机制挂载一组订单 HTTP 路由，同时展示
controller / service / repository 的分层边界。契约来自
`gofy/gofy-interface-service`，本模块只负责实现。

## 目录结构

```
order/
├── module.go                  # 模块装配、配置、路由注册
├── module_test.go             # 基于 gofytest 的接口测试
├── controller/
│   └── order_controller.go    # 适配 demoservice.Service 契约
├── service/
│   └── order_service.go       # 业务用例，返回 DTO
├── repository/
│   └── order_repository.go    # 持久化接口 + GORM 实现
└── model/
    └── order.go               # 持久化模型（orders 表）
```

分层约定：

| 层 | 职责 | 依赖限制 |
| --- | --- | --- |
| controller | 实现契约接口，做错误码映射 | 不导入 `model`，只使用契约类型 |
| service | 业务用例，出入参用普通值 / DTO | 只在 `toDTO` 中接触 `model` |
| repository | 定义 `OrderRepositoryInterface` 与 `ErrNotFound` | 拥有 `model` 和 GORM |
| model | GORM 实体，映射 `orders` 表 | 不感知上层 |

## 路由

模块名 `order` 会作为挂载前缀；`main.go` 里的
`gofy.WithAPIPrefix("/api/v1")` 再套一层，因此完整路径是：

| 方法 | 路径 | OperationID | 说明 | 成功响应 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/order/orders/{id}` | `GetOrder` | 查询订单详情 | `200` + `GetOrderResponse` |
| `GET` | `/api/v1/order/orders` | `ListOrders` | 按用户分页查询 | `200` + `paging.ListData[OrderDTO]` |
| `POST` | `/api/v1/order/orders` | `CreateOrder` | 创建订单 | `200` + `CreateOrderResponse` |
| `DELETE` | `/api/v1/order/orders/{id}` | `CancelOrder` | 取消订单 | `204`，无响应体 |
| `GET` | `/api/v1/order/config` | `GetConfig` | 查询订单模块配置 | `200` + `GetConfigResponse` |

路由描述符定义在 `gofy-interface-service` 的
`appinterface/demoservice/orderroute`，本模块通过
`gofyadapter.BindAll` 一次性绑定，不在业务代码里重复写路径和参数校验。

只使用模块（不带 `/api/v1`）时路径是 `/order/orders...`，测试里就是这种形态。

### 请求示例

创建订单，`user_id` 和 `items` 为必填，`amount` 单位为分：

```bash
curl -X POST http://127.0.0.1:8080/api/v1/order/orders \
  -H 'Content-Type: application/json' \
  -d '{
    "user_id": 100,
    "amount": 500,
    "items": [{"sku_id": 90001, "num": 1}]
  }'
```

分页查询，`user_id` 必填，`page` 默认 1，`page_size` 默认 20、最大 100：

```bash
curl 'http://127.0.0.1:8080/api/v1/order/orders?user_id=100&page=1&page_size=20'
```

> 契约还声明了 `status`、`created_from`、`created_to` 三个查询参数，会参与
> 参数校验，但当前 demo 的 `List` 只按 `user_id` 过滤，这三个条件尚未生效。

## 配置

配置位于 `config.yaml` 的 `order` 段，结构体是 `order.Config`：

```yaml
order:
  payment_timeout: 6s
  # auth_token: 设置后，所有契约路由都要求 Bearer Token
```

| YAML 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `payment_timeout` | duration | `3s`（tag 默认值） | 传给 `service.Config`，当前仅记录在启动日志中，尚未实现超时任务 |
| `auth_token` | string | 空 | 非空时启用鉴权；为空时关闭校验，方便本地直接调用 |

默认值来自结构体 tag，只有通过 `gofyconfig.Load` 加载时才会应用；手工构造
`order.Config{}` 时 `PaymentTimeout` 为零值。

## 装配

`main.go` 通过工厂注册模块，启动和热更新共用同一份工厂列表：

```go
gofy.WithModuleFactories(order.NewModuleFactory(cfg.Order))
```

`NewModuleFactory` 在 `Runtime` 就绪后执行：

1. 检查 `rt.DB`，为空直接返回 `order module requires database`；
2. 用 `repository.NewFromHandle(rt.Logger)` 创建 GORM 仓库，查询时从 `mysql.Db` 取连接；
3. 用 `service.New(repo, rt.Logger, ...)` 创建服务；
4. 调用 `NewModule(cfg, svc, rt.Logger)` 包装出模块。

工厂执行时 `app/io` 还没 `Start`，`mysql.Db` 仍是 nil，所以 `NewFromHandle`
把句柄解析推迟到每次查询；`Start` 安装新连接后仓库自动使用新句柄。脚本模式不经过
模块生命周期，直接用 `repository.New(rt.DB, rt.Logger)` 传显式连接。

`NewModule` 负责鉴权：`auth_token` 非空时校验 Bearer Token，为空时放行。
`Register` 创建 controller（同时传入 logger），并用 `gofyadapter.BindAll`
绑定全部契约路由，把鉴权中间件作为 `BindOptions.Auth` 传入。

测试或自定义场景可以跳过工厂，自己构造 service 后调用 `NewModule`：

```go
module := order.NewModule(order.Config{}, myService, zap.NewNop())
```

## 日志

三个包都用构造函数注入 `*zap.Logger`，由工厂统一传 `rt.Logger`，不使用包级
全局 logger，也不在函数里临时 `zap.New`。构造函数在 logger 为 `nil` 时退化
为 `zap.NewNop()`，测试因此可以完全不关心日志。每个构造函数还会附加
`component` 字段（`order.controller` / `order.service` / `order.repository`），
便于按层过滤。

```go
repo := repository.NewFromHandle(rt.Logger) // HTTP 模块：查询时取 mysql.Db
svc := service.New(repo, rt.Logger, service.Config{...})
h := controller.NewOrderController(svc, rt.Logger)
```

分层记录约定是“一个事件只在一个层记录一次”：

| 层 | 级别 | 记录内容 |
| --- | --- | --- |
| controller | `Error` | 只记录最终映射成 500 的意外错误，带 `zap.Error(err)` 和业务键 |
| service | `Info` / `Debug` | `Info`：创建、取消等状态变更；`Debug`：查询、预期内的 not found |
| repository | `Debug` | 数据访问的参数和影响行数；错误只 `fmt.Errorf` 包装后返回，不在这里打 |

具体做法：

- 成功请求不在 controller 里重复记录，访问日志已经有 method/path/status/
  `req_id`；业务日志用 `order_id`、`user_id` 等字段与之关联；
- 错误只在“决定 HTTP 响应”的 controller 里打一次 `Error`，service 和
  repository 只返回带上下文的错误，避免 `error.log` 出现同一条错误的多个副本；
- `ErrOrderNotFound` 属于预期业务结果，service 用 `Debug`，controller 直接
  映射为 404，不进 `error.log`；
- 用 `zap.Uint64`、`zap.Int64`、`zap.Error` 等强类型字段，不要用 `fmt.Sprintf`
  拼消息；字段名保持 snake_case；
- 不记录 token、密码和完整请求体；需要排查时用业务主键回查。

一条创建订单的成功日志和一条失败日志大致如下：

```text
{"level":"info","ts":"...","caller":"service/order_service.go:...","msg":"order created","component":"order.service","order_id":42,"user_id":100,"amount":500}
{"level":"error","ts":"...","caller":"controller/order_controller.go:...","msg":"create order failed","component":"order.controller","user_id":100,"amount":500,"error":"create order: ..."}
```

按执行模式和日期，这两类日志分别落在
`storage/log/http/<YYYYMMDD>/` 或 `storage/log/script/<YYYYMMDD>/` 下。
HTTP 模式是 `info-h<H>.log` / `error.log` / `system_error.log`；脚本模式在
日期目录下再分 `info/` 和 `error/`，并以脚本名命名（`info/fix-order-h<H>.log`、
`error/fix-order.log`、`error/fix-order-system_error.log`），一个脚本对应
一组日志文件，panic 才进 `system_error.log`。

Nacos 热更新时，`main.go` 会重新执行工厂并替换模块；端口不变，新配置里的
`order` 段会生效。已有请求不受影响，新请求使用新模块。

## 鉴权

`app/middleware.Auth` 从 `Authorization: Bearer <token>` 中取 token，
校验失败时返回与框架一致的响应包：

```json
{"code": 40100, "msg": "unauthorized", "data": null, "req_id": "..."}
```

HTTP 状态码为 `401`。`auth_token` 为空时中间件放行，因此示例默认不需要
凭证。

## 响应与错误码

所有非 `204` 响应都由框架统一包装为：

```json
{"code": 0, "msg": "success", "data": {...}, "req_id": "..."}
```

`req_id` 会回写到 `X-Request-ID`；请求带 `X-Request-ID` 时原样透传。

| code | HTTP | 场景 |
| --- | --- | --- |
| `40001` | 400 | 参数校验失败，`details` 按字段给出原因 |
| `40100` | 401 | 鉴权失败（仅在配置了 `auth_token` 时） |
| `40401` | 404 | 订单不存在 |
| `50000` | 500 | 内部错误；具体原因只写日志，不返回给客户端 |

映射方式：

- controller 把 `service.ErrOrderNotFound` 转成
  `gofyadapter.Error(demoservice.ErrOrderNotFound)`；
- 其他错误用 `apperr.Internal(...).WithCause(err)`，响应只保留通用文案；
- panic 由框架恢复为 `50000`，堆栈和内部信息不会出现在响应里。

> 契约里 `CancelOrder` 还声明了 `409 / ErrCodeOrderClosed`，当前 demo 的实现
> 只是把状态更新为 `cancelled`，重复取消仍返回 `204`，不会触发 409。

## 订单状态

状态枚举来自 `appinterface/demoservice/orderenum`：

| 值 | 含义 |
| --- | --- |
| `pending` | 待支付，创建订单时的初始状态 |
| `paid` | 已支付 |
| `cancelled` | 已取消 |

订单 ID、用户 ID 在契约中都是 `types.Uint64String`，JSON 里以字符串传输，
避免 JavaScript 大整数精度丢失；写响应时由 `toDTO` 转换。

## 数据表

模型 `model.Order` 对应 `orders` 表，DDL 在
[`migrations/0001_orders.sql`](../../../migrations/0001_orders.sql)：

```sql
CREATE TABLE IF NOT EXISTS orders (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NOT NULL,
    amount BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    created_at DATETIME(3) NULL,
    updated_at DATETIME(3) NULL,
    PRIMARY KEY (id),
    KEY idx_orders_user_id (user_id),
    KEY idx_orders_status (status)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4;
```

项目不使用 `AutoMigrate`，首次运行前需要手动执行迁移文件。`ListByUser`
按 `id DESC` 排序，并先 `COUNT` 再分页，返回当前用户的总数。
`UpdateStatus` 在 `RowsAffected == 0` 时返回 `ErrNotFound`，因此取消不存在的
订单会得到 `40401`。

## 测试

```bash
cd examples/demo-service
go test ./app/module/order/...
```

测试不需要数据库：`module_test.go` 用 `fakeRepository` 实现
`OrderRepositoryInterface`，再通过 `gofytest.New` 启动一个只挂载 order 模块的
测试应用。覆盖内容包括：

- 成功响应的 envelope、`req_id` 及 `X-Request-ID` 透传；
- 参数校验（非法 id、缺失必填字段、`page_size` 超限）；
- 订单不存在返回 `40401`；
- panic 返回 `50000` 且不泄露内部信息；
- 配置 `auth_token` 后的 `40100` 与带 token 的正常路径；
- 创建订单的初始状态为 `pending`，列表分页结果正确。

新增路由时，先在 `gofy-interface-service` 里补契约描述符，再在
`gofyadapter.BindAll` 的绑定范围内实现 controller 方法即可；路径、校验和
错误声明都不需要在业务模块里重复。
