# gofy-interface-api 拆分方案（v1）

> 状态：**待评审**（2026-11-13）
>
> 关联文档：[gofy-interface-service 详细技术方案](./gofy-interface-service-design.md)、[gofy Library 详细设计](./library-design.md)、[模块分层设计](./module-layering-design.md)
>
> 本文**取代** `gofy-interface-service-design.md` 中与仓库归属和模块路径相关的部分：
> §3.1 的“仓库定位”、§3.2 的目录结构、§3.3 的依赖规则表、§4.1 的 module path。
> 契约模型、路由描述符、adapter、client、docgen 的**行为**不在本文范围内，保持不变。

---

## 目录

1. [背景与问题](#1-背景与问题)
2. [结论与范围](#2-结论与范围)
3. [目标拓扑](#3-目标拓扑)
4. [gofy-interface-api 库设计](#4-gofy-interface-api-库设计)
5. [项目契约模块设计](#5-项目契约模块设计)
6. [业务服务如何消费](#6-业务服务如何消费)
7. [迁移步骤](#7-迁移步骤)
8. [验收标准](#8-验收标准)
9. [风险与开放问题](#9-风险与开放问题)
10. [附录：import 映射表](#10-附录import-映射表)

---

## 1. 背景与问题

### 1.1 现状

当前契约仓库是单模块：

```text
gofy-interface-service/                 module github.com/bingcool/gofy-interface-service
├── apicore/
│   ├── contracts/                     公共契约原语（只依赖标准库）
│   ├── client/                        泛型 HTTP 客户端、SSE、Resolver
│   │   └── nacos/                     依赖 github.com/bingcool/gofy/support/nacos
│   ├── adapter/gofy/                  唯一依赖 gofy + gin 的服务端适配器
│   └── cmd/
│       ├── gofy-openapi/              硬 import appinterface 生成 OpenAPI
│       └── refcheck/                  分层引用检查
├── docgen/                            OpenAPI 3.0.3 生成器
├── docsui/                            内嵌 Swagger UI
├── appinterface/                      业务契约（唯一可编辑目录）
└── openapi/                           生成物 + //go:embed
```

这个结构区分了“共享层”和“业务层”，但**两者仍在同一个 module 里**，因此有两个无法回避的问题。

### 1.2 问题一：module path 是项目级的，不是共享库级的

`module github.com/bingcool/gofy-interface-service` 表达的是“bingcool 的共享仓库”，但里面的 `appinterface/` 是**某个项目的**业务契约。

- 公司内多个项目组各自建契约仓库时，GitLab 上会出现多个同名项目 `gofy-interface-service`，只是 group 不同；
- `appinterface/demoservice` 的 import path 会变成 `github.com/bingcool/gofy-interface-service/appinterface/demoservice`，把“共享库作者”错误地写进了每个项目的业务类型里；
- 项目 A 修改自己的契约，只能 fork 整个共享仓库，或者把 `appinterface/` 拷贝出去，两种做法都会导致共享层出现多份副本。

### 1.3 问题二：共享代码的发布节奏被项目契约绑架

`apicore/`、`docgen/`、`docsui/` 是**随框架升级**的共享实现；`appinterface/`、`openapi/` 是**随项目需求**变化的业务资产。放在同一个 module 里：

- 项目新增一个 DTO，也要打一个新 tag，所有消费方跟着升级；
- 共享层修复一个 bug，需要所有项目重新对齐同一个 tag；
- 破坏性变更无法分级：改 `endpoint.Spec` 是共享层 major，删一个 DTO 是项目 major，但两者都只能表现为同一个 module 的版本号。

### 1.4 问题三：生成器与项目路由强耦合

`apicore/cmd/gofy-openapi/main.go` 直接 `import "github.com/bingcool/gofy-interface-service/appinterface"`。这意味着：

- 它不能作为共享工具发布，因为离开这个仓库就找不到 `appinterface`；
- 项目不能在生成器里传入自己的 `appinterface.Routes()`；
- 新增一个契约项目只能复制整个仓库。

---

## 2. 结论与范围

### 2.1 核心决策

| # | 决策 | 结果 |
| --- | --- | --- |
| D1 | 共享基础设施的位置 | 新库 `github.com/bingcool/gofy-interface-api`，只含契约原语、客户端、adapter、docgen、docsui、命令行工具 |
| D2 | 项目契约的位置 | 每个项目一个独立 module，如 `gitlab.company.com/payment/interface-service`；本文示例用 `github.com/yourorg/interface-service` |
| D3 | 参考实现的位置 | gofy 仓库内新增 `examples/interface-service`，模块路径就是 `github.com/yourorg/interface-service`，作为新项目模板 |
| D4 | 分层方式 | 删除库内的 `apicore/` 前缀：共享/可编辑的边界改由**仓库边界**表达，库根目录直接是 `contracts/`、`client/`、`adapter/gofy/` 等 |
| D5 | gofy 依赖面 | `adapter/gofy` 与 `client/nacos` 是库内仅有的两个依赖 gofy 的包；其余包由 refcheck 保证不依赖 gofy |
| D6 | OpenAPI 生成入口 | 生成改为**项目本地** `cmd/openapi`（提供 routes，调用库的 `docgen.Command`）；库的 `gofy-openapi` 只保留“校验已有规范”和“托管 Swagger UI” |
| D7 | 生成物归属 | `openapi/*.yaml`、`openapi/*.json` 与 `openapi/embed.go` 留在项目模块，库不内嵌任何项目规范 |
| D8 | 引用检查 | `refcheck` 移入库并提供 `library` / `contracts` 两种模式；项目通过 `go run github.com/bingcool/gofy-interface-api/cmd/refcheck` 检查自己的 `appinterface/`（也可用 `@<version>` 形式） |
| D9 | 版本策略 | 库与项目各自打 semver tag；库内契约原语只做加法，破坏性变更升库 major；项目 DTO 的破坏性变更升项目 major |
| D10 | 依赖方向 | `gofy` 不依赖 `gofy-interface-api`，也不依赖任何项目契约模块；依赖只能单向 `项目 -> api 库 -> gofy`（仅 adapter/nacos 两条边） |

### 2.2 目标

1. 让**共享契约基础设施**有一个稳定、可跨项目复用的 module path。
2. 让**每个项目**拥有自己的契约 module，命名与 VCS 路径一致，互不冲突。
3. 业务服务（demo-service）只通过两个明确入口消费契约：项目契约模块（DTO/路由）与 api 库（adapter/client/docgen）。
4. 生成 OpenAPI 不再依赖“库硬编码项目包”的写法。
5. 保持现有对外 API 不变：`endpoint`、`adapter/gofy`、`client`、`docgen` 的签名和行为只在 import path 上变化。

### 2.3 非目标

- 不重新设计契约模型、路由描述符和响应信封。
- 不实现 `go/packages` 动态加载项目路由（v1 明确拒绝，理由见 §4.5）。
- 不拆分 `client/nacos` 到独立 module（保留为已记录例外，见 §9 R1）。
- 不在本期实现 `gofy-client` 代码生成器（当前仓库也没有该命令）。
- 不把 `examples/interface-service` 作为发布物；它是模板，真实项目复制后改写 module path。

### 2.4 与现有设计文档的关系

| 现有内容 | 处理 |
| --- | --- |
| `gofy-interface-service-design.md` §3.1 仓库定位 | 由本文 D1/D2 取代：共享库与项目契约分离 |
| `gofy-interface-service-design.md` §3.2 目录结构 | 由本文 §4.1 与 §5.1 取代 |
| `gofy-interface-service-design.md` §3.3 依赖方向 | 由本文 §4.3 取代，规则内容保留但按新路径重写 |
| `gofy-interface-service-design.md` §4.1 module path | 由本文 D1/D2 取代 |
| 契约模型、adapter 绑定、client、docgen 行为 | **不变**，仅 import path 变化 |
| `docs/library-design.md`、`docs/module-layering-design.md` 中的契约仓库表述 | 需要按本文更新引用名与路径 |

---

## 3. 目标拓扑

### 3.1 仓库与模块清单

| 仓库 | module path | 角色 | 发布节奏 | 本地位置（建议） |
| --- | --- | --- | --- | --- |
| `gofy` | `github.com/bingcool/gofy` | 框架库 | 独立 semver | `~/Documents/wwwroot/gofy` |
| `gofy-interface-api` | `github.com/bingcool/gofy-interface-api` | 契约基础设施库 | 独立 semver，跟随 gofy 最低版本 | `~/Documents/wwwroot/gofy-interface-api` |
| `examples/interface-service`（在 gofy 仓库内） | `github.com/yourorg/interface-service` | 项目契约模板/示例 | 不发布；模板 tag 可选 | `gofy/examples/interface-service` |
| 真实项目契约仓库 | `gitlab.company.com/<group>/interface-service`（示例名） | 某个项目的契约 | 项目自行 semver | 由项目决定 |
| `demo-service`（在 gofy 仓库内） | `github.com/yourorg/demo-service` | 业务服务示例 | 不发布 | `gofy/examples/demo-service` |

> module path 必须等于仓库的 VCS 路径，否则 `go get` 无法解析。`github.com/yourorg/...` 只是示例占位，真实项目建仓后第一步就是 `go mod edit -module <真实路径>` 并全量替换 import。

### 3.2 依赖方向

```text
                     ┌──────────────────────────────┐
                     │ github.com/bingcool/gofy     │
                     │ （不感知契约与项目）          │
                     └──────────────▲───────────────┘
                                    │ 仅 adapter/gofy、client/nacos
                     ┌──────────────┴───────────────┐
                     │ gofy-interface-api           │
                     │ contracts/client/docgen/...  │
                     └──────────────▲───────────────┘
                                    │ contracts、client（可选）
                     ┌──────────────┴───────────────┐
                     │ <project>/interface-service   │
                     │ appinterface + openapi        │
                     └──────────────▲───────────────┘
                                    │ 业务服务实现契约
                     ┌──────────────┴───────────────┐
                     │ demo-service / 真实业务服务   │
                     │ gofy + 契约 + adapter          │
                     └──────────────────────────────┘
```

硬规则：

1. `gofy` 的 `go.mod` 不出现 `gofy-interface-api`（root guard 检查）。
2. `gofy-interface-api` 不出现任何项目契约模块。
3. 项目契约模块**只**依赖 api 库的 `contracts/`、`client/` 和标准库；它的 `appinterface/` 不依赖 gofy、gin、adapter、docgen。
4. 业务服务依赖三者：`gofy`、项目契约模块、api 库（`adapter/gofy`、`client`）。

### 3.3 为什么不是“继续单模块，只改 module path”

只把现有仓库改名为 `<group>/interface-service` 也能解决命名冲突，但共享层仍会随每个项目复制一份：

- 修复 `docgen` 的一个 schema 问题，需要在 N 个项目里重复提交；
- 所有项目无法共享同一份 `endpoint` 语义；
- `refcheck`、`docsui` 等工具在每个项目里各有一份实现。

两模块方案把“共享实现”和“项目资产”分开，代价是多一个仓库和两条 `replace`（本地联调）。

### 3.4 承载位置选项

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| A. 独立仓库 `gofy-interface-api` | 与 gofy 同级的共享库；发布、权限、评审独立；项目按版本依赖 | 本地联调需要并排 checkout；CI 需要多仓 checkout | **推荐** |
| B. gofy 仓库内嵌套模块 `interfaceapi/`，module path 仍为 `github.com/bingcool/gofy-interface-api` | 单仓 checkout；本地 replace 路径短 | 发布要打子目录 tag（`interfaceapi/v1.2.3`）；与 gofy 生命周期耦合 | 备选 |
| C. 保持单模块，仅重命名 | 改动最小 | 共享层重复；发布节奏冲突 | 不采用 |

方案 A 与现有 gofy ↔ demo-service 的协作方式一致（demo 通过 `replace ../../` 使用 gofy），不引入新的机制。

---

## 4. gofy-interface-api 库设计

### 4.1 目录结构

```text
gofy-interface-api/                    module github.com/bingcool/gofy-interface-api
├── go.mod
├── go.sum
├── README.md
├── AGENTS.md                          继承 gofy 的双语注释约定
├── Makefile
├── contracts/                         公共契约原语（只依赖标准库与自身子树）
│   └── common/
│       ├── endpoint/                  Spec、Route[Req,Res]、Descriptor、Group
│       ├── envelope/                  Envelope[T]、ErrorEnvelope
│       ├── errcode/                   Code、Fault、码段
│       ├── paging/                    PageRequest、ListData[T]、PageData[T]
│       ├── types/                     Uint64String、Date、Decimal
│       └── enum/                      枚举辅助
├── client/                            泛型客户端（只依赖标准库与 contracts）
│   └── nacos/                         Nacos Resolver（依赖 gofy/support/nacos）
├── adapter/
│   └── gofy/                          BindGroup/BindAll、错误映射、DocsModule
├── docgen/                            OpenAPI 生成器 + Command（项目本地 CLI 逻辑）
├── docsui/                            net/http Swagger UI handler
└── cmd/
    ├── gofy-openapi/                  只做 -validate / -serve（不 import 任何项目）
    └── refcheck/                      library / contracts 两种模式
```

与旧仓库的差异：

- 去掉 `apicore/` 前缀。这个前缀原本用来区分“共享核心”与 `appinterface/`；拆分后整个仓库都是共享核心，前缀失去意义，且会多一层无用的 import 噪音。
- `docgen`、`docsui` 保持顶层，与旧路径只差 module 前缀。
- 不包含 `appinterface/`、`openapi/`、`cmd/gofy-client/`。

### 4.2 包移动矩阵

| 旧位置（`gofy-interface-service`） | 新位置（`gofy-interface-api`） | 说明 |
| --- | --- | --- |
| `apicore/contracts/common/**` | `contracts/common/**` | 纯契约原语，行为不变 |
| `apicore/client/*.go` | `client/*.go` | 泛型 Client、Call、SSE、重试、透传头 |
| `apicore/client/nacos/**` | `client/nacos/**` | 唯一依赖 gofy 的客户端子包 |
| `apicore/adapter/gofy/**` | `adapter/gofy/**` | 唯一依赖 gofy + gin 的服务端适配器 |
| `apicore/cmd/gofy-openapi/**` | `cmd/gofy-openapi/**` | **重写**为无项目依赖的校验/预览工具 |
| `apicore/cmd/refcheck/**` | `cmd/refcheck/**` | 增加 `-mode` 与 `-api` 参数 |
| `docgen/**` | `docgen/**` | 新增 `Command` 入口，`Build`/`Config` 不变 |
| `docsui/**` | `docsui/**` | 不变 |
| `appinterface/**` | → 项目契约模块 | 见 §5 |
| `openapi/**` | → 项目契约模块 | 生成物与 embed 属于项目 |
| `apicore/adapter/gofy/docs.go` | `adapter/gofy/docs.go` | 挂载 Swagger UI 的 gofy Module 仍在此 |

### 4.3 依赖规则（库内 refcheck）

`cmd/refcheck -mode library` 在库内执行以下规则：

| 目录 | 允许 import | 禁止 import |
| --- | --- | --- |
| `contracts/**` | 标准库、`contracts/**` | 任何第三方（gin、gorm、gofy、zap、kin-openapi 等） |
| `client/**`（不含 `client/nacos`） | 标准库、`contracts/**`、`client/**` | gofy、gin、adapter、docgen |
| `client/nacos/**` | 上述 + `github.com/bingcool/gofy/support/nacos` | gin、adapter、docgen |
| `adapter/gofy/**` | 标准库、`contracts/**`、`github.com/bingcool/gofy/**`、`github.com/gin-gonic/gin` | docgen、docsui、cmd |
| `docgen/**` | 标准库、`contracts/**`、kin-openapi、yaml | adapter、docsui、cmd |
| `docsui/**` | 标准库、swgui | adapter、docgen |
| `cmd/**` | 上述所有 | — |
| 全库 | — | `func init()`（`contracts/`、`client/`、`appinterface` 语义为纯契约） |

> 与旧规则相比，唯一放宽的是 `client/nacos`：它原本就 import `gofy/support/nacos`，旧 README 中“客户端不依赖 gofy”的描述需要同步修正为“客户端主包不依赖 gofy；Nacos Resolver 例外”。

### 4.4 go.mod 基线

```text
module github.com/bingcool/gofy-interface-api

go 1.26.0

require (
    github.com/bingcool/gofy v0.x.y        // adapter/gofy、client/nacos
    github.com/getkin/kin-openapi v0.149.0 // docgen、cmd/gofy-openapi
    github.com/gin-gonic/gin v1.12.0       // adapter/gofy
    github.com/swaggest/swgui v1.8.9       // docsui
    gopkg.in/yaml.v3 v3.0.1                // docgen
)
```

本地联调：

```text
replace github.com/bingcool/gofy => ../gofy
```

发布时删除 `replace`，固定一个已发布的最小 gofy 版本。`contracts/` 与 `client/` 主包虽然不会把 gin/kin-openapi 编进业务二进制，但会出现在 module graph 中；这是“单库多包”的既有形态，不额外拆 module。

### 4.5 CLI 重新设计

#### 4.5.1 为什么生成器必须移到项目侧

生成 OpenAPI 需要项目的 `appinterface.Routes()`。库的命令行有两种实现方式：

| 方式 | 做法 | 问题 |
| --- | --- | --- |
| 动态加载 | `go/packages` 加载路由包，再用临时 main 执行 `Routes()` | `go/packages` 只能类型检查，不能执行；需要生成临时程序并 `go run`，错误定位、构建缓存、`replace` 解析都很脆弱 |
| 项目本地生成器 | 项目建 `cmd/openapi/main.go`，import 自己的 `appinterface`，把 routes 交给库的 `docgen.Command` | 每个项目多约 15 行代码；但编译期检查、IDE 跳转、错误信息都是普通 Go 行为 |

v1 选择**项目本地生成器**。动态加载留作 P2 可选项，不作为默认路径。

#### 4.5.2 库侧 API

`docgen` 新增一个入口，把 flags、筛选、构建、校验、写盘、预览集中起来：

```go
// Command runs the project-side OpenAPI generator. Routes come from the
// caller's appinterface package; defaults supply the document metadata and
// deployment prefixes, and flags override them.
// Command 运行项目侧的 OpenAPI 生成器。路由由调用方的 appinterface 提供，
// defaults 提供文档元数据与部署前缀，命令行参数可覆盖。
func Command(routes []endpoint.Descriptor, defaults Config) error
```

`Command` 支持的 flags 与现有 CLI 保持一致：

| flag | 作用 |
| --- | --- |
| `-all` | 生成全部契约 |
| `-service <name>` | 只生成一个服务（`FilterByService`） |
| `-out <dir>` | 输出目录，默认 `./openapi` |
| `-prefix key=/path` | 可重复，覆盖 `Config.Prefixes` |
| `-title` / `-version` / `-description` | 覆盖文档元数据 |
| `-validate <file>` | 只校验已有规范 |
| `-serve <addr>` | 预览 Swagger UI；配合 `-all`/`-service` 使用生成结果，配合 `-spec` 使用已有文件 |
| `-spec <file>` | 与 `-serve` 搭配时直接托管已有规范，不读取 routes |

`cmd/gofy-openapi` 只保留不依赖项目的能力：

```go
// 只接受已有规范文件，不 import 任何项目契约。
gofy-openapi -validate ./openapi/all.yaml
gofy-openapi -serve :8090 -spec ./openapi/all.yaml
```

这样库的命令行可以随 `go install` 发布，也不会把某个项目的契约编进二进制。

### 4.6 版本与发布

| 变更类型 | 版本动作 |
| --- | --- |
| `contracts` 增加字段/类型、docgen 修 bug | minor / patch |
| 修改 `endpoint.Spec` 语义、删除导出类型、client 行为破坏性变更 | api 库 major |
| 新增可选能力（如 `docgen.Command` 新 flag） | minor |
| 依赖 gofy 最低版本提升 | minor，并在 CHANGELOG 标注 |

发布顺序：`gofy` → `gofy-interface-api` → 项目契约模块 → 业务服务。项目在 `go.mod` 中固定 api 库版本；升级是显式动作，不做隐式跟随。

---

## 5. 项目契约模块设计

### 5.1 目录结构（以 `examples/interface-service` 为例）

```text
examples/interface-service/            module github.com/yourorg/interface-service
├── go.mod
├── go.sum
├── README.md                          项目契约说明（可由模板改写）
├── Makefile
├── appinterface/                      唯一可编辑目录
│   ├── routes.go                      显式聚合所有服务 Routes()
│   └── demoservice/
│       ├── routes.go
│       ├── service.go                 OrdersService / ConfigService / ProductsService
│       ├── errors.go
│       ├── nacos.go                   ServiceName
│       ├── base_url.go                BaseURL
│       ├── doc.go
│       ├── demoserviceclient/         强类型客户端
│       ├── orderroute/  ordererror/  orderconst/  orderdto/  orderenum/
│       ├── orderrequest/  orderresponse/
│       ├── productroute/  producterror/  productdto/
│       └── productrequest/  productresponse/
├── cmd/
│   └── openapi/
│       └── main.go                    项目本地生成器（约 15 行）
└── openapi/
    ├── embed.go                       //go:embed all.* / demoservice.*
    ├── all.yaml  all.json
    └── demoservice.yaml  demoservice.json
```

`appinterface/` 下的子包划分、命名与旧仓库完全一致（`orderdto`、`orderroute` 等带领域前缀），不做改动。

### 5.2 module path 命名规则

1. module path = 仓库的 VCS 路径，例如 `gitlab.company.com/payment/interface-service`。
2. 仓库名建议固定为 `interface-service`；冲突由 group 隔离，而不是靠改仓库名。
3. `examples/interface-service` 使用 `github.com/yourorg/interface-service`，README 第一行必须提示“复制后执行 `go mod edit -module <真实路径>`”。
4. 项目契约模块**不发布为库供其他项目复用**；跨项目共享的只有 api 库。

### 5.3 go.mod 与 replace 策略

`examples/interface-service/go.mod`：

```text
module github.com/yourorg/interface-service

go 1.26.0

require (
    github.com/bingcool/gofy-interface-api v0.0.0
    // github.com/bingcool/gofy v0.0.0 // indirect，由 go mod tidy 根据 api 库补齐
)

replace (
    github.com/bingcool/gofy => ../..
    github.com/bingcool/gofy-interface-api => ../../../gofy-interface-api
)
```

- `appinterface` 不 import gofy，但 api 库的 `adapter/gofy`、`client/nacos` 依赖 gofy，因此 gofy 会以 indirect 形式进入 module graph；本地 `replace` 只对**主模块**生效，所以项目根必须提供这条 replace（api 库自己的 replace 不会被继承）。
- `cmd/openapi` 只调用 `docgen.Command`，不直接使用 `openapi3.T`，因此项目不需要显式 require kin-openapi；是否保留为 indirect 由 `go mod tidy` 决定。
- 本地开发要求 `gofy` 与 `gofy-interface-api` 并排 checkout；这与当前 demo-service 依赖 gofy 的方式一致。
- 真实项目发布时删除两条 `replace`，改为固定版本。
- 仓库内的参考模块保留 `replace`，README 明确标注“仅用于仓库内联调”。

### 5.4 项目本地生成器

`cmd/openapi/main.go`：

```go
// Command openapi generates this project's OpenAPI documents.
// 命令 openapi 生成本项目的 OpenAPI 文档。
package main

import (
    "fmt"
    "os"

    "github.com/bingcool/gofy-interface-api/docgen"
    "github.com/yourorg/interface-service/appinterface"
)

func main() {
    err := docgen.Command(appinterface.Routes(), docgen.Config{
        Title:   "Demo Service API",
        Version: "1.0.0",
        Prefixes: map[string]string{
            "demoservice":         "/api/v1/order",
            "demoservice.product": "/api/v1/product",
        },
    })
    if err != nil {
        fmt.Fprintln(os.Stderr, "openapi:", err)
        os.Exit(1)
    }
}
```

- `Prefixes` 是项目事实，放在这里作为默认值；`-prefix` 仍然可以在命令行覆盖，便于临时验证。
- 生成的组件名、路径、security 与旧仓库保持一致，golden 文件可直接比对。
- 项目不再需要 `gofy-openapi` 的 `-all/-service` 分支逻辑，也不需要在 Makefile 里重复维护 `PREFIX` 变量。

### 5.5 openapi 生成物与文档挂载

`openapi/embed.go` 留在项目：

```go
// Package openapi embeds this project's generated specifications.
// 包 openapi 内嵌本项目生成好的规范文档。
package openapi

import _ "embed"

var (
    //go:embed all.yaml
    AllYAML []byte
    //go:embed all.json
    AllJSON []byte
    //go:embed demoservice.yaml
    DemoServiceYAML []byte
    //go:embed demoservice.json
    DemoServiceJSON []byte
)
```

业务服务挂载：

```go
gofy.WithModules(
    gofyadapter.DocsModule(docsui.Config{
        Title: "Demo Service API",
        Spec:  openapi.DemoServiceYAML,
    }),
)
```

这里 `gofyadapter` 来自 `github.com/bingcool/gofy-interface-api/adapter/gofy`，`openapi` 来自项目模块。库不再提供任何项目的规范。

### 5.6 Makefile

```makefile
GO   ?= go
PKGS := ./...

.PHONY: fmt-check vet generate validate refcheck golden check

fmt-check:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then echo "gofmt required:"; echo "$$files"; exit 1; fi

vet:
	$(GO) vet $(PKGS)

# 生成器在项目内，前缀默认值也在项目内；需要临时覆盖时追加
# ARGS="-prefix demoservice=/other/path"。
generate:
	$(GO) run ./cmd/openapi -all -out ./openapi $(ARGS)
	$(GO) run ./cmd/openapi -service demoservice -out ./openapi $(ARGS)

validate:
	$(GO) run ./cmd/openapi -validate ./openapi/all.yaml
	$(GO) run ./cmd/openapi -validate ./openapi/demoservice.yaml

refcheck:
	$(GO) run github.com/bingcool/gofy-interface-api/cmd/refcheck \
		-mode contracts -app ./appinterface -module github.com/yourorg/interface-service

golden: generate
	git diff --exit-code -- openapi

check: fmt-check vet refcheck golden
```

> `refcheck` 在项目模块内通过依赖版本执行（`go run <pkg>` 使用当前 module 解析到的 api 库版本），不需要项目自己维护检查脚本。

### 5.7 新建一个契约项目的步骤

1. 复制 `examples/interface-service` 到新仓库（不要复制 gofy 仓库）。
2. `go mod edit -module gitlab.company.com/<group>/interface-service`。
3. 全量替换 import：`github.com/yourorg/interface-service` → 新 module path。
4. 删除 `replace`（或保留本地联调的两条，按团队习惯）。
5. `go mod tidy`，确认 requires 为已发布的 `gofy` 与 `gofy-interface-api` 版本。
6. 在 `appinterface/` 下按服务建目录，参照 `demoservice` 的分包规则。
7. 更新 `cmd/openapi/main.go` 的 `Title` 与 `Prefixes`。
8. 运行 `make check`，然后把 `openapi/` 生成物提交。

新增服务的步骤与旧 README 一致：建载荷子包 → 建 `{domain}route` → 在 `{domain}service/routes.go` 聚合 → 在 `appinterface/routes.go` 追加 → `make generate`。

---

## 6. 业务服务如何消费

### 6.1 import 重新映射

| 旧 import | 新 import | 归属 |
| --- | --- | --- |
| `github.com/bingcool/gofy-interface-service/apicore/contracts/...` | `github.com/bingcool/gofy-interface-api/contracts/...` | api 库 |
| `github.com/bingcool/gofy-interface-service/apicore/client/...` | `github.com/bingcool/gofy-interface-api/client/...` | api 库 |
| `github.com/bingcool/gofy-interface-service/apicore/adapter/gofy` | `github.com/bingcool/gofy-interface-api/adapter/gofy` | api 库 |
| `github.com/bingcool/gofy-interface-service/docgen` | `github.com/bingcool/gofy-interface-api/docgen` | api 库 |
| `github.com/bingcool/gofy-interface-service/docsui` | `github.com/bingcool/gofy-interface-api/docsui` | api 库 |
| `github.com/bingcool/gofy-interface-service/appinterface/...` | `github.com/yourorg/interface-service/appinterface/...` | 项目 |
| `github.com/bingcool/gofy-interface-service/openapi` | `github.com/yourorg/interface-service/openapi` | 项目 |

demo-service 当前同时使用两类 import：

- `app/module/*/controller`、`app/module/*/module.go`：契约 DTO、路由分组、`gofyadapter`；
- `app/middleware/rate_limit.go`：`contracts/common/endpoint`；
- `app/container`：`gofyadapter.BindOptions`。

拆分后 controller 继续只依赖“契约 + adapter”，只是路径变化；分层规则不变。

### 6.2 go.mod

`examples/demo-service/go.mod`：

```text
require (
    github.com/bingcool/gofy v0.0.0
    github.com/bingcool/gofy-interface-api v0.0.0
    github.com/yourorg/interface-service v0.0.0
    ...
)

replace (
    github.com/bingcool/gofy => ../..
    github.com/bingcool/gofy-interface-api => ../../../gofy-interface-api
    github.com/yourorg/interface-service => ../interface-service
)
```

### 6.3 demo 分层 guard 更新

`examples/demo-service/shell/check-imports.sh` 的两处变化：

```bash
contract_prefix="github.com/yourorg/interface-service"
api_prefix="github.com/bingcool/gofy-interface-api"
```

在分类逻辑中，两者都归为 `contract`：

```bash
case "$imp" in
  "$module_prefix"*) cat="module" ;;
  "$contract_prefix"* | "$api_prefix"*) cat="contract" ;;
  ...
esac
```

保持现有规则：

- `controller` 可依赖同模块 `service` 与契约/adapter；
- `service`、`repository`、`model` 不得依赖 HTTP 契约（包括 api 库的 `contracts`）；
- `innerapi` 只允许标准库与 gofy 的纯类型 support 包。

### 6.4 gofy 仓库自身的隔离

`shell/check-imports.sh` 追加一条，保证框架库不反向依赖契约库：

```bash
if go list -deps ./... | grep -q 'github.com/bingcool/gofy-interface-api'; then
  fail "gofy must not depend on gofy-interface-api"
fi
```

`go list ./...` 在根 module 内不会进入 `examples/*`（它们是独立 module），因此这条检查只覆盖框架包本身。

### 6.5 根 Makefile 与 CI

根 `Makefile` 新增一个目标，并把示例契约纳入 `check`：

```makefile
EXAMPLE_CONTRACTS := examples/interface-service

example-contracts:
	cd $(EXAMPLE_CONTRACTS) && $(GO) vet ./... && $(GO) test $(GOFLAGS) -race -count=1 ./... \
		&& $(GO) run ./cmd/openapi -all -out ./openapi \
		&& $(GO) run ./cmd/openapi -service demoservice -out ./openapi \
		&& git diff --exit-code -- $(EXAMPLE_CONTRACTS)/openapi

check: fmt-check vet test-race example example-contracts guards cgo-free
```

- 该目标要求 `../gofy-interface-api` 存在（示例模块的 `replace`）；Makefile 在目标开头用 `test -d` 检查并给出明确错误。
- 如果后续启用 CI，需要同时 checkout 两个仓库：

```yaml
- uses: actions/checkout@v4
  with: { path: gofy }
- uses: actions/checkout@v4
  with: { repository: bingcool/gofy-interface-api, path: gofy-interface-api }
```

- 只改 gofy 根 module 的工作（不含示例）仍可用 `make fmt-check vet test-race guards cgo-free` 单独验证。

---

## 7. 迁移步骤

每个阶段都以可执行的验证结束；未通过不进入下一阶段。

### 阶段 0：冻结与基线

1. 在旧仓库 `gofy-interface-service` 记录当前 `make check` 结果与 `openapi/` 生成物哈希。
2. 在 gofy 仓库记录 `make check` 与 demo `make check` 基线。
3. 确认所有消费方：`grep -rn 'gofy-interface-service' gofy/`（当前 30 个文件，见附录 §10.2）。

### 阶段 1：建立 `gofy-interface-api`

1. 新建仓库目录 `gofy-interface-api`，初始化 `go.mod`（§4.4）。
2. 复制并改写：`apicore/contracts` → `contracts`，`apicore/client` → `client`，`apicore/adapter/gofy` → `adapter/gofy`，`docgen`、`docsui` → 顶层。
3. 全量替换库内 import 前缀：
   `github.com/bingcool/gofy-interface-service/apicore/` → `github.com/bingcool/gofy-interface-api/`；
   `github.com/bingcool/gofy-interface-service/docgen` / `docsui` → 新 module 下的同名包。
4. 重写 `cmd/gofy-openapi`（只保留 `-validate` / `-serve -spec`）。
5. 扩展 `cmd/refcheck`：`-mode library|contracts`；`contracts` 模式接受 `-app` 与 `-module`，`-api` 默认本库 module path，只有镜像仓库时才需要覆盖。
6. 新增 `docgen.Command`，把原 CLI 的筛选/校验/写盘/预览逻辑迁入。
7. `go mod tidy`，确保 `adapter/gofy`、`client/nacos` 是仅有的 gofy 依赖边。
8. 验证：`make check`（fmt/vet/test-race/refcheck），并用 `go list -deps` 断言 §4.3 的规则。

### 阶段 2：在 gofy 内建立 `examples/interface-service`

1. 新建 `examples/interface-service`，`go.mod` 使用 `github.com/yourorg/interface-service`（§5.3）。
2. 迁移 `appinterface/` 与 `openapi/`（含 `embed.go` 与四个生成物）。
3. 替换 import 前缀：`.../gofy-interface-service/appinterface/` → `github.com/yourorg/interface-service/appinterface/`。
4. 新增 `cmd/openapi/main.go`（§5.4）与 Makefile（§5.6）。
5. `go mod tidy`；运行 `make generate`，对比生成物与旧仓库是否逐字节一致（路径、security、组件名都应与拆分前相同）。
6. 验证：`make check`，并确认 `openapi/*.yaml` 无 diff（除标题等有意调整）。

### 阶段 3：重接 demo-service

1. 改写 import（§6.1），先 `go build ./...` 收集所有编译错误，避免用 grep 漏改。
2. 更新 `go.mod` 的两条 `replace` 与三条 `require`（§6.2）。
3. 更新 `examples/demo-service/shell/check-imports.sh`（§6.3）。
4. 验证：`cd examples/demo-service && make check`；
   `go run . script fix-order --order-id 1 --dry-run`（不写库）；
   HTTP 模式下 `/docs` 能加载项目内嵌的规范（若启用）。

### 阶段 4：gofy 仓库收尾

1. 更新 `shell/check-imports.sh`（§6.4）。
2. 更新文档与 changelog：
   - `docs/library-design.md`：依赖图与“契约仓库”表述；
   - `docs/module-layering-design.md`：契约前缀与 controller 允许依赖；
   - `docs/repository-composition-design.md`、`docs/gofy-architecture-optimization-plan.md` 中的仓库名；
   - `README.md`、`CHANGELOG.md`（Unreleased: Changed/Added）；
   - demo 的 `order/README.md`、`product/README.md`、`header_propagation_test.go` 等注释中的模块名。
3. 验证：`make check`（含 example 与 guards）。

### 阶段 5：旧仓库下线

1. 旧仓库 README 顶部加迁移说明，指向 `gofy-interface-api` 与各项目契约模块。
2. 打一个最终 tag（或归档），不再接受业务契约变更。
3. 迁移文档中保留一份“旧路径 → 新路径”对照表（即本文附录）。

### 阶段 6（可选）：工具与模板

- 在 `cmd/gofy` 脚手架里增加 `gofy new interface-service` 子命令，直接复制 `examples/interface-service` 模板并改写 module path。
- 在 api 库增加 `docgen.Command` 的 `-routes` 动态加载模式，作为 P2 能力；不作为默认路径。

### 批量替换注意事项

- 先提交/备份，再执行替换；替换后用 `go build ./...` 而不是 grep 判断完成度。
- 只替换 import path，不替换文档中的历史叙述；历史叙述在阶段 4 人工更新。
- `openapi/*.yaml` 里没有 import path，不需要替换；但生成器标题变化会产生 diff，先确认是否符合预期。
- 替换顺序：库 → 项目模块 → demo → gofy 文档；反过来会因为找不到新包而编译失败。

---

## 8. 验收标准

### 8.1 api 库

```bash
cd gofy-interface-api
make check                 # fmt-check、vet、test-race、refcheck
go build ./...
```

- `go list -deps ./contracts/...` 中没有任何第三方包；
- `go list -deps ./client` 中不含 `github.com/bingcool/gofy`；
- `go list -deps ./adapter/gofy` 与 `./client/nacos` 中含 gofy，且仅此两处；
- `cmd/gofy-openapi -validate` 能校验项目生成的规范；
- `cmd/refcheck -mode contracts` 能对示例项目通过。

### 8.2 项目契约模块

```bash
cd gofy/examples/interface-service
make check                 # fmt-check、vet、refcheck、generate + golden
```

- `go list -deps ./appinterface/...` 中不含 gin、gorm、gofy、adapter、docgen；
- `openapi/all.*` 与 `openapi/demoservice.*` 与拆分前语义一致（对解析后的 JSON 做断言，不比较文本）；
- `make generate` 后 `git diff --exit-code -- openapi` 为空。

### 8.3 业务服务与框架

```bash
cd gofy
make check                 # 含 examples/demo-service 的构建、vet、race
cd examples/demo-service && make check
```

- demo 的 controller 只 import 契约与 adapter；service/repository/model 不 import 契约；
- `go run . script fix-order --order-id 1 --dry-run` 正常结束且不写库；
- gofy 根 module 的依赖图中没有 `gofy-interface-api`。

### 8.4 端到端

- 用 `examples/interface-service` 作为模板复制出一个临时项目，改写 module path 后 `make check` 通过；
- 复制出的项目把 `openapi.DemoServiceYAML` 挂到 gofy 服务上，`/docs` 可访问；
- 旧仓库 `gofy-interface-service` 不再被任何 `go.mod` 引用。

---

## 9. 风险与开放问题

| # | 风险 | 影响 | 缓解 |
| --- | --- | --- | --- |
| R1 | api 库依赖 gofy（adapter、client/nacos） | 使用 api 库的项目会把 gofy 带进 module graph | refcheck 限制依赖面；若未来要求“纯客户端零 gofy”，把 `client/nacos` 拆为独立 module `gofy-interface-api-nacos` |
| R2 | 示例模块依赖并排 checkout | gofy 仓库内 `make example` 需要 `../gofy-interface-api` | README 与 Makefile 明确前置条件；CI 同时 checkout 两个仓库；或采用备选方案 B（嵌套模块） |
| R3 | import path 全量变化 | 大量编译错误、漏改注释 | 阶段 3 以 `go build ./...` 为门禁；文档在阶段 4 统一更新 |
| R4 | 生成器移到项目侧 | 每个项目多一个 `cmd/openapi` | 模板 + `docgen.Command` 把逻辑压到约 15 行；golden 检查防止漂移 |
| R5 | 两套版本号 | 升级组合增多 | 发布顺序固定；项目显式固定 api 库版本；api 库 README 维护兼容矩阵 |
| R6 | `yourorg` 示例路径被直接用于生产 | `go get` 失败或误用共享路径 | 模板 README 首行提示；阶段 6 提供脚手架自动改写 module path |
| R7 | `Prefixes` 从 Makefile 移到代码 | 部署前缀与服务器分组可能不一致 | 以项目 `cmd/openapi` 为唯一默认值；临时覆盖仍走 `-prefix`；adapter 测试继续校验分组前缀 |
| R8 | 旧仓库下线后历史链接失效 | 文档、CI、依赖指向旧地址 | 旧仓库保留迁移 README；本文附录保留映射表 |

### 开放问题

1. **`client/nacos` 是否拆独立 module？** 建议 v1 不拆。拆分会增加一个发布单元，收益只是“纯客户端零 gofy”，当前没有非 gofy 的客户端消费者。
2. **示例模块放在 gofy 仓库内是否合适？** 符合用户提出的 `examples/interface-service`，也让三模块联调在一个工作区内；代价是 CI 需要并排 checkout api 库。若团队更希望示例独立成仓库，可直接把阶段 2 的产物移到新仓库，模块路径不变。
3. **api 库是否保留 `apicore/` 前缀？** 建议去掉。拆分后“核心里的一切都是核心”，保留前缀只会让 import 更长，且无法带来兼容性收益（路径本来就要改）。
4. **是否需要 `gofy-client` 生成器？** 当前仓库没有该命令，本文不引入。若后续需要，生成器应读取项目 `appinterface` 的 route 包，同样适合放在项目侧 `cmd/`。

---

## 10. 附录：import 映射表

### 10.1 完整映射

| 旧路径前缀 | 新路径前缀 |
| --- | --- |
| `github.com/bingcool/gofy-interface-service/apicore/contracts` | `github.com/bingcool/gofy-interface-api/contracts` |
| `github.com/bingcool/gofy-interface-service/apicore/client` | `github.com/bingcool/gofy-interface-api/client` |
| `github.com/bingcool/gofy-interface-service/apicore/adapter/gofy` | `github.com/bingcool/gofy-interface-api/adapter/gofy` |
| `github.com/bingcool/gofy-interface-service/apicore/cmd` | `github.com/bingcool/gofy-interface-api/cmd` |
| `github.com/bingcool/gofy-interface-service/docgen` | `github.com/bingcool/gofy-interface-api/docgen` |
| `github.com/bingcool/gofy-interface-service/docsui` | `github.com/bingcool/gofy-interface-api/docsui` |
| `github.com/bingcool/gofy-interface-service/appinterface` | `github.com/yourorg/interface-service/appinterface` |
| `github.com/bingcool/gofy-interface-service/openapi` | `github.com/yourorg/interface-service/openapi` |

### 10.2 gofy 仓库内受影响的文件（当前快照）

代码（import 需要改写）：

- `examples/demo-service/app/middleware/rate_limit.go`、`rate_limit_test.go`（`contracts/common/endpoint`）
- `examples/demo-service/app/module/order/controller/{order_controller,order_config_controller}.go` 及其测试
- `examples/demo-service/app/module/order/{module.go,module_test.go}`
- `examples/demo-service/app/module/product/controller/product_controller.go`、`module.go`、`module_test.go`
- `examples/demo-service/app/container/container.go`、`container_wiring_test.go`
- `examples/demo-service/header_propagation_test.go`
- `examples/demo-service/go.mod`
- `examples/demo-service/shell/check-imports.sh`
- `shell/check-imports.sh`

文档与注释（人工更新，不做机械替换）：

- `docs/gofy-interface-service-design.md`（加迁移横幅，标注被本文取代的章节）
- `docs/library-design.md`、`docs/module-layering-design.md`、`docs/repository-composition-design.md`
- `docs/gofy-architecture-optimization-plan.md`
- `README.md`、`CHANGELOG.md`
- `examples/demo-service/app/module/order/README.md`、`examples/demo-service/app/module/product/README.md`
- `support/middleware/{registry.go,README.md}`、`support/middleware/req_trace_id.go`、`support/headerpropagation/headerpropagation.go`、`support/nacos/README.md` 中作为示例出现的契约路径

### 10.3 旧仓库文件去向

| 旧文件/目录 | 去向 |
| --- | --- |
| `apicore/contracts/**` | api 库 `contracts/**` |
| `apicore/client/**` | api 库 `client/**` |
| `apicore/adapter/**` | api 库 `adapter/**` |
| `apicore/cmd/gofy-openapi` | api 库 `cmd/gofy-openapi`（重写） |
| `apicore/cmd/refcheck` | api 库 `cmd/refcheck`（扩展） |
| `docgen/**` | api 库 `docgen/**`（新增 `Command`） |
| `docsui/**` | api 库 `docsui/**` |
| `appinterface/**` | `examples/interface-service/appinterface/**`（或真实项目仓库） |
| `openapi/**` | `examples/interface-service/openapi/**`（或真实项目仓库） |
| `Makefile` | 拆成两份：api 库 Makefile 与项目 Makefile |
| `README.md` | 拆成两份：api 库 README 与项目模板 README |
| `AGENTS.md` | 保留在 api 库；项目模板沿用 gofy 仓库的注释约定 |
