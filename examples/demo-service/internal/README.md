# internal

本目录只有 `_test.go`，没有业务代码：它是**四个仓库交汇处的跨仓库集成测试**。

一句话分工：**各仓库自己的测试回答「这个包对不对」，这里回答「拼起来对不对」。**

## 为什么放在 demo-service

`demo-service/go.mod` 把三个仓库都 replace 到了本地：

```go
replace (
    github.com/bingcool/gofy-adapter       => ../../../gofy-adapter
    github.com/bingcool/gofy-interface-api => ../../../gofy-interface-api
    github.com/yourorg/interface-service   => ../interface-service
)
```

而被测对象全是**外部仓库的包**：

| 被测包 | 属于 |
| --- | --- |
| `adapter/gofy`（`Bind` / `BindAll` / `BindGroup`） | `gofy-adapter` |
| `client`（`Call` / resolver / retry / SSE） | `gofy-interface-api` |
| `docgen`（OpenAPI 生成） | `gofy-interface-api` |
| `client/nacos`（注册、发现、类型化调用） | `gofy-adapter` |

`gofy-adapter` 不能 import 业务契约，否则 go.mod 里会写下「适配器依赖某个具体业务示例」，库的边界就没了；`gofy-interface-api` 同理。只有 demo-service 天然同时依赖四方，所以集成测试只能落在这里。

**这也是它们不可替代的原因**：`gofy-adapter` 自己用 `fixture_test.go` 里的假契约做单测，契约字段改名时它不会红；这里用的是 `interface-service/appinterface/...` 下的**真实契约**，能抓到「契约改了、adapter 没跟上」这类跨仓库漂移。

## 目录

| 目录 | 验证什么 |
| --- | --- |
| `integration/adapter/` | 契约绑定全流程：`BindAll` 成功与错误映射、校验细节、204、重复 OperationID 拒绝、分组与路由中间件的解析顺序、名字缺失时报错、`BindOptions.Middleware` 覆盖注册表、自定义注册表实现，以及**不隐式安装认证中间件**（认证只由 `Use("auth")` 显式声明） |
| `integration/client/` | 类型化调用：请求 ID、错误包络解析、`204`、503 重试与幂等键、超时与连接超时、resolver 每次重解析、协议错误、响应体上限、SSE 流、文件下载、query 编码、请求头透传与合并顺序 |
| `integration/docgen/` | OpenAPI 生成：stable name、输出确定性与合法性、service 前缀回退与过滤、嵌套/匿名结构拒绝、重复 operation 拒绝、**不生成 security 声明** |
| `integration/nacos/` | 注册 → 发现 → 类型化调用的端到端链路；`helper_test.go` 提供共用的最小假 Nacos HTTP 服务（`/instance/list`、`/instance`、`/instance/beat`） |

## 三条约定

1. **全部是 `package xxx_test`**（`gofyadapter_test`、`client_test`、`docgen_test`、`nacos_test`）。只走公开 API，不碰内部实现——重构不该让它们失败，接口变了才该失败。
2. **不依赖外部服务。** Nacos 用 `httptest` 造假，其余用例用 `httptest.Server` 或纯内存逻辑。不需要启动 MySQL、Redis、Nacos 才能跑。
3. **必须进 `make check`。** `test` 目标是 `go test -race -count=1 ./...`，本目录没有 build tag 隔离，也就没有「只在本地跑」的用例。

## 什么不该放进来

- **单个包内部逻辑的单测** → 放被测包旁边，跟着它一起重构。
- **需要真实中间件的用例**（真实 Nacos 集群、真实 broker、真实 DB） → 单独脚本或独立 CI job，不要塞进 `go test ./...`，否则本地和 CI 都会变慢且不稳定。
- **业务代码或测试辅助库** → 这里只放 `_test.go`；`internal/` 只是命名上的「非交付 API」，不是可复用的测试工具包。

## 挂了先判断是哪一类

| 现象 | 大概率原因 |
| --- | --- |
| 契约字段、路由、OperationID 变了 | `interface-service` 改了契约，adapter/client 需要跟进 |
| 断言里的行为变了但契约没动 | adapter / client 的语义变更，确认是不是有意的 |
| 两边都没动 | 依赖版本或本地 replace 指向不对 |

第二类最值得停下来看：如果新行为是想要的，改断言并写清理由；如果不是，说明库被意外改坏了。
