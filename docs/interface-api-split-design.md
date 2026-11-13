# gofy-interface-api 拆分方案（v1）

> 状态：**待评审**（2026-11-13）
>
> 关联文档：[gofy-interface-service 详细技术方案](./gofy-interface-service-design.md)、[gofy Library 详细设计](./library-design.md)、[模块分层设计](./module-layering-design.md)
>
> 本文**取代** `gofy-interface-service-design.md` 中与仓库归属和模块路径相关的部分：
> §3.1 的“仓库定位”、§3.2 的目录结构、§3.3 的依赖规则表、§4.1 的 module path。
> 契约模型、路由描述符、adapter、client、docgen 的**行为**不在本文范围内，保持不变。
>
> 修订记录（2026-11-13，代码核查后的评审）：补齐库内测试归属（§4.7）、修正 §3.2/§4.3
> 依赖规则、修正 Makefile 的 golden 路径与门禁编排，并统一生成物比对口径与适用边界；
> 状态仍为**待评审**。
>
> 实施记录（2026-11-13）：§11.7 记录 M0 / M1 与 P2 可执行项的落地结果；
> §4.7 / §5.1 按实施结果补充真实契约集成测试的归属。旧契约仓库已删除、
> T2-1 关闭；发布与 CI 仍待确认，状态保持**待评审**。
>
> 解耦记录（2026-11-13）：用户确认 api 库与 gofy 应完全独立，§12 记录
> T2-7 的方案 A 落地结果；§2.5 / §4.3 / §4.4 / §4.6 / §9 R1 / §11.8 的
> 相关旧结论以 §12 为准。

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
11. [优先级任务清单](#11-优先级任务清单)
12. [适配层独立与 gofy 解耦](#12-适配层独立与-gofy-解耦)

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
5. 库侧公开类型的签名不变（`endpoint`、`adapter/gofy`、`client`、`docgen.Build` / `Config`），
   只在 import path 上变化；`docgen.Command` 是新增入口，CLI 的生成能力由库侧迁到项目侧，
   属于**显式迁移**而非隐式行为保持（见 §4.5、§10 映射表）。

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

### 2.5 适用边界与成本

**定位边界**：`gofy-interface-api` 的 `go.mod` 必须 require `gofy`（`adapter/gofy` 与
`client/nacos`），因此它是 **gofy 生态专用的契约库**，不是框架无关的通用契约库。
只 import `contracts/` 或 `client/` 主包的项目不会把 gin/gorm 编进二进制，但 gofy 仍会出现在
module graph 中（Go 1.17+ 图裁剪后通常不写入主模块 `go.mod` 的显式 require）。§1.2 的
「稳定、可跨项目复用」应理解为「跨多个 gofy 项目复用」；面向非 gofy 客户端的通用契约层
需要把依赖 gofy 的部分拆到独立 module，见 §9 R1 与开放问题 5。

**何时该拆**（本文假设这些条件成立，动手前需确认）：

| 判据 | 满足时 |
| --- | --- |
| 有 ≥2 个独立项目 / 项目组各自维护契约 DTO | 需要共享层与项目资产分别归属 |
| 共享实现（docgen、client、refcheck）的发布节奏已被项目 DTO 变更打乱 | 需要两套 semver |
| 共享层修 bug 时不希望强制所有项目升版本 | 需要库级 tag |
| 存在跨项目消费的强类型客户端 | 需要稳定的库 module path |

**何时不拆**：若只有一个项目，契约与服务端实现同仓、同批变更，改一次 DTO 就要跨
「契约仓库 → 业务服务仓库」两次提交、两次 CI，并用 replace 才能本地联调。此时只需做 D2
的命名修正（把仓库改成 `<group>/interface-service`），共享层随仓库一起升级即可，
拆分属于过度工程化。

**成本清单**：3 个 module（gofy / api 库 / 项目契约）+ 2 条本地 `replace` + 4 级发布链
（gofy → api 库 → 项目契约 → 业务服务）+ 版本组合矩阵（R5）+ CI 双仓 checkout。
若判据不成立，建议先执行 §3.4 方案 C 的变体（只重命名），暂缓 D1。

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

> **对外发布的模块**，其 module path 必须等于仓库的 VCS 路径，否则 `go get` 无法解析。
> `examples/interface-service` 是仓库内**不发布**的模板，用 `github.com/yourorg/...` 占位，
> 因此不受该规则约束；真实项目建仓后第一步就是 `go mod edit -module <真实路径>` 并全量替换 import。
> 同理，嵌套子模块的 path 必须是「仓库路径 + 子目录」，见 §3.4 方案 B。

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
3. 项目契约模块的 `appinterface/` **只**依赖 api 库的 `contracts/`、`client/` 和标准库，
   不依赖 gofy、gin、adapter、docgen；`cmd/openapi` 是本规则的例外，允许 import `docgen`
   （生成器入口，见 §5.4），`openapi/` 只用标准库 `embed`。验收按 `./appinterface/...` 检查（§8.2）。
4. 业务服务依赖三者：`gofy`、项目契约模块、api 库（`adapter/gofy`、`client`）。

### 3.3 为什么不是“继续单模块，只改 module path”

只把现有仓库改名为 `<group>/interface-service` 也能解决命名冲突，但共享层仍会随每个项目复制一份：

- 修复 `docgen` 的一个 schema 问题，需要在 N 个项目里重复提交；
- 所有项目无法共享同一份 `endpoint` 语义；
- `refcheck`、`docsui` 等工具在每个项目里各有一份实现。

两模块方案把“共享实现”和“项目资产”分开，代价是多一个仓库和本地 `replace` 联调。

### 3.4 承载位置选项

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| A. 独立仓库 `gofy-interface-api` | 与 gofy 同级的共享库；发布、权限、评审独立；项目按版本依赖 | 本地联调需要并排 checkout；CI 需要多仓 checkout | **推荐** |
| B. gofy 仓库内嵌套模块 `interfaceapi/`，module path 为 `github.com/bingcool/gofy/interfaceapi` | 单仓 checkout；本地 replace 路径短 | 发布要打子目录 tag（`interfaceapi/v1.2.3`）；import path 变成 gofy 的子路径，`go get github.com/bingcool/gofy-interface-api` 不再成立；与 gofy 生命周期耦合 | 备选 |
| C. 保持单模块，仅重命名 | 改动最小 | 共享层重复；发布节奏冲突 | 不采用（但见 §2.5 的单项目场景） |

> 子模块的 module path 必须是「仓库 VCS 路径 + 子目录」，不能沿用独立仓库的名字：
> Go 按 module path 反查仓库，声明 `github.com/bingcool/gofy-interface-api` 却放在
> `github.com/bingcool/gofy/interfaceapi/` 下会让 `go get` 找不到对应 tag。方案 B 的代价
> 比原表述更大，A 与 B 的取舍应按这一前提重新评估。

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
├── internal/
│   └── testcontract/                  库内测试用的最小 descriptor / DTO fixture
└── cmd/
    ├── gofy-openapi/                  -validate / -serve -spec（不 import 任何项目）
    └── refcheck/                      library / contracts 两种模式
```

与旧仓库的差异：

- 去掉 `apicore/` 前缀。这个前缀原本用来区分“共享核心”与 `appinterface/`；拆分后整个仓库都是共享核心，前缀失去意义，且会多一层无用的 import 噪音。
- `docgen`、`docsui` 保持顶层，与旧路径只差 module 前缀。
- 不包含 `appinterface/`、`openapi/`、`cmd/gofy-client/`。
- 新增 `internal/testcontract/`：按 §3.2 硬规则 2，库内任何包（含 `_test.go`）都不能
  import 项目契约模块，旧仓库里对 `appinterface/` 的测试引用改用最小 fixture，详见 §4.7。

### 4.2 包移动矩阵

| 旧位置（`gofy-interface-service`） | 新位置（`gofy-interface-api`） | 说明 |
| --- | --- | --- |
| `apicore/contracts/common/**` | `contracts/common/**` | 纯契约原语，行为不变 |
| `apicore/client/*.go` | `client/*.go` | 泛型 Client、Call、SSE、重试、透传头 |
| `apicore/client/nacos/**` | `client/nacos/**` | 唯一依赖 gofy 的客户端子包 |
| `apicore/adapter/gofy/**` | `adapter/gofy/**` | 唯一依赖 gofy + gin 的服务端适配器 |
| `apicore/cmd/gofy-openapi/**` | `cmd/gofy-openapi/**` | **重写**为无项目依赖的校验/预览工具 |
| `apicore/cmd/refcheck/**` | `cmd/refcheck/**` | **重写**分层前缀与 `-module` 默认值，再新增 `-mode` / `-api`（旧实现的 `layer.prefixes` 硬编码了 `apicore/contracts`、`appinterface`，见 §4.3） |
| `docgen/**` | `docgen/**` | 新增 `Command` 入口，`Build`/`Config` 不变；不引入 `docsui` |
| `docsui/**` | `docsui/**` | 不变 |
| `appinterface/**` | → 项目契约模块 | 见 §5 |
| `openapi/**` | → 项目契约模块 | 生成物与 embed 属于项目 |
| `apicore/adapter/gofy/docs.go` | `adapter/gofy/docs.go` | 挂载 Swagger UI 的 gofy Module 仍在此，因此依赖 `docsui`（§4.3） |
| `apicore/**/*_test.go`、`docgen/*_test.go` 中对 `appinterface/` 的引用 | `internal/testcontract/**` 或 → 项目契约模块 | 库内不得保留任何项目契约 import，归属矩阵见 §4.7 |

### 4.3 依赖规则（库内 refcheck）

> **已被 §12.3 取代**：T2-7 之后库内不再有 `adapter/gofy` 与 `client/nacos`，
> 全库禁止 gofy / gin。下表保留为拆分时的历史规则，不再按此执行。

`cmd/refcheck -mode library` 在库内执行以下规则：

| 目录 | 允许 import | 禁止 import |
| --- | --- | --- |
| `contracts/**` | 标准库、`contracts/**` | 任何第三方（gin、gorm、gofy、zap、kin-openapi 等） |
| `client/**`（不含 `client/nacos`） | 标准库、`contracts/**`、`client/**` | gofy、gin、adapter、docgen |
| `client/nacos/**` | 上述 + `github.com/bingcool/gofy/support/nacos` | gin、adapter、docgen |
| `adapter/gofy/**` | 标准库、`contracts/**`、`docsui/**`、`github.com/bingcool/gofy/**`、`github.com/gin-gonic/gin` | docgen、cmd |
| `docgen/**` | 标准库、`contracts/**`、kin-openapi、yaml | adapter、docsui、cmd |
| `docsui/**` | 标准库、swgui | adapter、docgen |
| `cmd/**` | 上述所有 | — |
| 全库 | — | `func init()`（`contracts/`、`client/` 语义为纯契约；项目侧 `appinterface/` 由 `-mode contracts` 检查） |

> `adapter/gofy` **必须允许** `docsui`：`DocsModule(cfg docsui.Config)` 是公开 API，旧仓库
> `apicore/adapter/gofy/docs.go` 就 import 了 `docsui`。把它列入禁止项会让库内 refcheck
> 直接失败。`docgen.Command` 不提供预览（见 §4.5.2），因此 `docgen/**` 仍不依赖 `docsui`。

> 与旧规则相比，唯一放宽的是 `client/nacos`：它原本就 import `gofy/support/nacos`，旧 README 中“客户端不依赖 gofy”的描述需要同步修正为“客户端主包不依赖 gofy；Nacos Resolver 例外”。

> `-mode library` 的规则表由 `cmd/refcheck` 按上表的**目录前缀**生成，不能沿用旧实现里
> 硬编码的 `apicore/contracts` / `appinterface` 前缀；`-mode contracts` 用 `-module` 识别
> 项目自身 import，用 `-api`（默认本库 module path）识别 api 库允许的前缀。

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

发布时删除 `replace`，固定一个已发布的最小 gofy 版本。`contracts/` 与 `client/` 主包虽然不会把 gin/kin-openapi 编进业务二进制，但 gofy 会出现在 module graph 中（Go 1.17+ 图裁剪后，只 import `contracts/` 的主模块通常不显式 require gofy）；这是“单库多包”的既有形态，不额外拆 module。**因此本库是 gofy 生态专用的契约库，不是框架无关的通用契约库**（§2.5）；若出现非 gofy 的客户端消费方，需要把 `adapter/gofy` 与 `client/nacos` 拆到独立 module，见 §9 R1。

### 4.5 CLI 重新设计

#### 4.5.1 为什么生成器必须移到项目侧

生成 OpenAPI 需要项目的 `appinterface.Routes()`。库的命令行有两种实现方式：

| 方式 | 做法 | 问题 |
| --- | --- | --- |
| 动态加载 | `go/packages` 加载路由包，再用临时 main 执行 `Routes()` | `go/packages` 只能类型检查，不能执行；需要生成临时程序并 `go run`，错误定位、构建缓存、`replace` 解析都很脆弱 |
| 项目本地生成器 | 项目建 `cmd/openapi/main.go`，import 自己的 `appinterface`，把 routes 交给库的 `docgen.Command` | 每个项目多约 15 行代码；但编译期检查、IDE 跳转、错误信息都是普通 Go 行为 |

v1 选择**项目本地生成器**。动态加载留作 P2 可选项，不作为默认路径。

#### 4.5.2 库侧 API

`docgen` 新增一个入口，把 flags、筛选、构建、校验与写盘集中起来（预览留在
`cmd/gofy-openapi`，理由见下文）：

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
| `-prefix key=/path` | 可重复，按 key 覆盖 `defaults.Prefixes`；未提供的 key 沿用默认值 |
| `-title` / `-version` / `-description` | 覆盖文档元数据 |
| `-validate <file>` | 只校验已有规范并退出，忽略传入的 routes |

> `-serve` / `-spec` **不属于 `docgen.Command`**：预览需要 `docsui`，而 §4.3 禁止
> `docgen/**` 依赖 `docsui`。预览统一由 `cmd/gofy-openapi -serve :8090 -spec <file>`
> 提供；项目侧先 `make generate` 再预览已有文件即可。`Command` 只负责 flags、筛选、
> 构建、校验与写盘，保持纯生成逻辑，也避免两套 CLI 各自实现一份 UI 挂载。

`cmd/gofy-openapi` 只保留不依赖项目的能力：

```go
// 只接受已有规范文件，不 import 任何项目契约。
gofy-openapi -validate ./openapi/all.yaml
gofy-openapi -serve :8090 -spec ./openapi/all.yaml
```

这样库的命令行可以随 `go install` 发布，也不会把某个项目的契约编进二进制。
`-serve` 的实现依赖 `docsui`，`cmd/**` 按 §4.3 允许 import 库内全部包；这是预览能力
放在 `cmd` 而不放在 `docgen` 的原因。

### 4.6 版本与发布

| 变更类型 | 版本动作 |
| --- | --- |
| `contracts` 增加字段/类型、docgen 修 bug | minor / patch |
| 修改 `endpoint.Spec` 语义、删除导出类型、client 行为破坏性变更 | api 库 major |
| 新增可选能力（如 `docgen.Command` 新 flag） | minor |
| 依赖 gofy 最低版本提升 | minor，并在 CHANGELOG 标注 |

发布顺序：`gofy` → `gofy-interface-api` → 项目契约模块 → 业务服务。项目在 `go.mod` 中固定 api 库版本；升级是显式动作，不做隐式跟随。

### 4.7 测试归属与 fixture 策略

**问题**：旧仓库的库侧测试广泛 import `appinterface/`——
`apicore/adapter/gofy/{bind,middleware_registry,auth_validator}_test.go`、
`apicore/client/{client,headers}_test.go`、
`apicore/client/nacos/{registration,resolver}_test.go`、`docgen/docgen_test.go`。
按 §3.2 硬规则 2，拆库后 api 库不能出现任何项目契约模块，这些 import 会直接让
阶段 1 的 `go test ./...` / `make check` 编译失败；其中 `docgen_test.go` 依赖完整的
`appinterface.Routes()`，是生成器唯一的完整 golden 覆盖，简单删除会造成回归能力流失。
阶段 1 只做「复制 + 替换前缀」不足以完成拆分，必须显式安排测试归属。

**归属矩阵**：

| 测试类型 | 迁移后位置 | 允许依赖 |
| --- | --- | --- |
| 库内纯逻辑单测（endpoint/errcode/paging、client 请求头与重试、docgen schema/naming） | api 库，`internal/testcontract` 提供最小 descriptor / DTO | `contracts`、`client`、fixture |
| adapter 绑定、中间件别名注册表、认证声明校验 | api 库，改用 fixture | `contracts`、gofy、gin、fixture |
| docgen 完整 golden（依赖全量 `Routes()`） | 项目契约模块，挂在 `cmd/openapi` 的生成测试里 | 项目 `appinterface` + api 库 `docgen` |
| 契约 DTO、强类型客户端测试（`contract_test.go`、`demoserviceclient/*_test.go`） | 项目契约模块 | 项目 `appinterface` + api 库 `client` |
| 真实契约集成测试（原 `adapter/gofy/*_test.go`、`client/*_test.go`、`docgen/docgen_test.go`、nacos 注册 E2E） | 业务服务模块 `examples/demo-service/internal/integration/**`，仅 `_test.go` | 项目 `appinterface` + api 库 + gofy + gin |

**实施补充**：项目契约的 DTO 带枚举、校验 tag 与多层嵌套，机械替换成 fixture 会
变成「第二份契约」。因此库侧按行为分支用 `internal/testcontract` 重写（adapter 绑定 /
中间件顺序 / 认证策略、client 重试 / 超时 / resolver / 请求头、docgen schema），
原 76 个测试函数则**原样落到业务服务模块**（demo-service 本就依赖 gofy/gin）
作为真实契约的端到端回归；两者互补，`refcheck -mode contracts` 只检查 `appinterface/`。

**依赖修正**：集成测试最初放在契约模块，会把 gofy/gin 变成为测试服务的 `require`。
已把 `internal/integration/**` 整体移到 demo-service，契约模块 `go.mod` 只剩
`gofy-interface-api` 一条 require 与一条 replace，`go list -deps -test ./...` 实测
gofy 包数为 0。76 个测试仍全部运行，无覆盖流失。

**硬规则**：

- api 库任何包（含 `_test.go`，也含 `cmd/**` 的测试）不得 import 项目契约模块；
  `internal/testcontract` 只放最小结构，不复制项目 DTO，避免 fixture 演变成第二份契约。
- 项目侧的 docgen 完整 golden 必须覆盖全量路由与全部 deployment prefix，弥补库侧
  生成逻辑失去的端到端回归。
- 两侧门禁都要跑测试：api 库 `make check` 含 `test-race`（§8.1），项目模块
  `make check` 含 `test` / `test-race`（§5.6、§8.2）。
- `examples/demo-service/internal/integration/**` 只放 `_test.go` 与测试 stub；
  这些包由 demo-service `make check` 的 `test` / `test-race` 覆盖，不进入发布产物，
  也不会给契约模块引入 gofy 依赖。

**验收**：阶段 1 / 阶段 2 结束时逐项核对上表，确认测试数量与覆盖范围相对拆分前
没有流失；`go list -deps -test ./...` 在 api 库中不含任何项目契约模块。

**实施核验（T1-2）**：旧仓库 8 个耦合测试共 **76 个 `Test*` 函数**；
`client/nacos/resolver_test.go` 的 11 个零改写移回 api 库，其余 65 个落到
`examples/demo-service/internal/integration/**`（含 1 个 nacos E2E 改写为
黑盒 + 最小 fake server）。**76 → 11 + 65，一个不少**；api 库另有 41 个
fixture / endpoint / refcheck / docsui 测试。四套门禁全绿，见 §11.7。

---

## 5. 项目契约模块设计

### 5.1 目录结构（以 `examples/interface-service` 为例）

```text
examples/interface-service/            module github.com/yourorg/interface-service
├── go.mod
├── go.sum
├── README.md                          项目契约说明（可由模板改写）
├── Makefile
├── appinterface/                      业务契约唯一可编辑目录
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

1. **对外发布的**契约 module，其 module path = 仓库的 VCS 路径，例如 `gitlab.company.com/payment/interface-service`；仓库内不发布的模板用占位路径，见 §3.1。
2. 仓库名建议固定为 `interface-service`；冲突由 group 隔离，而不是靠改仓库名。
3. `examples/interface-service` 使用 `github.com/yourorg/interface-service`，README 第一行必须提示“复制后执行 `go mod edit -module <真实路径>`”。
4. 项目契约模块**不发布为库供其他项目复用**；跨项目共享的只有 api 库。

### 5.3 go.mod 与 replace 策略

`examples/interface-service/go.mod`：

```text
module github.com/yourorg/interface-service

go 1.26.0

require github.com/bingcool/gofy-interface-api v0.0.0

replace github.com/bingcool/gofy-interface-api => ../../../gofy-interface-api
```

- `appinterface` 与 `cmd/openapi` 都不 import gofy；api 库里只有 `adapter/gofy`、`client/nacos`
  依赖 gofy，Go 的惰性模块加载让契约模块无需解析这两个包，实测 `go list -deps -test ./...`
  中 gofy 包数为 0，因此 `go.mod` 里不出现 `github.com/bingcool/gofy`。
- `cmd/openapi` 只调用 `docgen.Command`，不直接使用 `openapi3.T`，因此项目不需要显式 require kin-openapi；是否保留为 indirect 由 `go mod tidy` 决定。
- 本地开发只需 `gofy-interface-api` 并排 checkout；契约模块不需要 gofy 仓库。
- 真实项目发布时删除唯一的 `replace`，改为固定 api 库版本。
- 仓库内的参考模块保留 `replace`，README 明确标注“仅用于仓库内联调”。
- **`replace` 只对主模块生效**：契约模块自己写的 replace 不会被上游业务服务继承；
  业务服务即使通过本模块间接依赖 api 库，也必须在自己的 go.mod 里再写一条
  （规则、实测依据与删除时机见 §6.2）。发布后统一改为固定版本并删除。

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

这里 `gofyadapter` 来自 `github.com/bingcool/gofy-interface-gofy/adapter/gofy`（§12），`openapi` 来自项目模块。库不再提供任何项目的规范。

### 5.6 Makefile

```makefile
GO   ?= go
PKGS := ./...

.PHONY: fmt-check vet test test-race generate validate refcheck golden check

test:
	$(GO) test $(GOFLAGS) -count=1 $(PKGS)

test-race:
	$(GO) test $(GOFLAGS) -race -count=1 $(PKGS)

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
	@untracked="$$(git ls-files --others --exclude-standard -- openapi)"; \
	if [ -n "$$untracked" ]; then echo "openapi has new/untracked files:"; echo "$$untracked"; exit 1; fi

check: fmt-check vet test test-race refcheck golden
```

> `refcheck` 在项目模块内通过依赖版本执行（`go run <pkg>` 使用当前 module 解析到的 api 库版本），不需要项目自己维护检查脚本。

> golden 用**文本 diff** 保证生成确定性，并用 `git status --porcelain` 覆盖新增文件；
> 迁移阶段另做一次「解析后 JSON 语义比对」（§7 阶段 2、§8.2），不把语义断言塞进日常门禁，
> 避免两套口径同时判定同一个结果。`check` 必须包含 `test` / `test-race`：契约 DTO、
> 强类型客户端与 docgen 完整 golden 测试都随 `appinterface/` 迁到了本模块（§4.7），
> 丢掉测试会让这批用例退出所有门禁。

### 5.7 新建一个契约项目的步骤

1. 复制 `examples/interface-service` 到新仓库（不要复制 gofy 仓库）。
2. `go mod edit -module gitlab.company.com/<group>/interface-service`。
3. 全量替换 import：`github.com/yourorg/interface-service` → 新 module path。
4. 删除唯一的 `replace`（或保留本地联调，按团队习惯）。
5. `go mod tidy`，确认只 require 已发布的 `gofy-interface-api` 版本，且不出现 `github.com/bingcool/gofy`。
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
| `github.com/bingcool/gofy-interface-service/apicore/adapter/gofy` | `github.com/bingcool/gofy-interface-gofy/adapter/gofy` | 适配层库（§12） |
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

> **`gofy-interface-api` 这条 replace 不是冗余（2026-11-13 实测）**。Go 只读取
> **主模块** go.mod 的 replace，`examples/interface-service/go.mod` 里那条对
> demo-service 完全无效；「契约模块已 require api 库」只提供版本声明，不提供源码位置。
>
> - demo-service 有 6 个生产文件直接 import api 库的 8 个包（`adapter/gofy`、
>   `client/nacos`、`contracts/common/{endpoint,paging,types,errcode}`、`docgen`），
>   所以 `require` 也必须是直接依赖，`go mod tidy` 会自动加回；
> - `v0.0.0` 是本地占位版本、GitHub 上不存在该 tag。删掉 replace 后
>   `go list -m` 报 `unknown revision v0.0.0`，`go build ./...` 报 5 处
>   `missing go.sum entry`，`go mod tidy -diff` 尝试联网下载失败；
> - 改用 `go.work` 也不成立：workspace 把 gofy 根模块纳入同一 module graph 后触发
>   `google.golang.org/genproto` 的 ambiguous import，build 与 test 均 FAIL。
>
> 唯一删除时机是 §4.6 的发布阶段（T2-2）：api 库打 tag 后把 require 改成
> `github.com/bingcool/gofy-interface-api vX.Y.Z` 并删除对应 replace；契约模块
> 的 replace 同时删除。届时 demo-service 的 replace 块只剩仍未发布的模块。

### 6.3 demo 分层 guard 更新

`examples/demo-service/shell/check-imports.sh` 的两处变化：

```bash
contract_prefix="github.com/yourorg/interface-service"
api_contracts_prefix="github.com/bingcool/gofy-interface-api/contracts"
api_client_prefix="github.com/bingcool/gofy-interface-api/client"
adapter_prefix="github.com/bingcool/gofy-interface-gofy/adapter"
adapter_client_prefix="github.com/bingcool/gofy-interface-gofy/client"
```

分类逻辑按**子目录**区分，不能把整个 api 库归为一类：

```bash
case "$imp" in
  "$module_prefix"*) cat="module" ;;
  "$contract_prefix"* | "$api_contracts_prefix"*) cat="contract" ;;
  "$api_client_prefix"* | "$adapter_client_prefix"*) cat="client" ;;
  "$adapter_prefix"*) cat="adapter" ;;
  ...
esac
```

保持现有规则，并按新类别细化：

- `controller` 可依赖同模块 `service`、契约（项目模块 + api 库 `contracts`）与 `adapter`；
- `service` 不得依赖 `contract`（HTTP 契约描述），但**允许** `client`：跨服务调用发生在
  service 层，强类型客户端是运行时依赖而非契约描述；把整个 api 库归为 contract 会误伤这类调用；
- `repository`、`model` 不得依赖 `contract` 与 `client`；
- `adapter` 只允许出现在 controller 与模块根装配层（`module.go`）；
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

根 `Makefile` 新增 `example-contracts` 目标，但**不把它放进默认 `check`**；
端到端验证用新增的 `check-all`：

```makefile
EXAMPLE_CONTRACTS := examples/interface-service
API_LIBRARY       := ../gofy-interface-api

example-contracts:
	@test -d $(API_LIBRARY) || { echo "gofy-interface-api not found at $(API_LIBRARY); see README"; exit 1; }
	cd $(EXAMPLE_CONTRACTS) && $(GO) vet ./... && $(GO) test $(GOFLAGS) -race -count=1 ./... \
		&& $(GO) run ./cmd/openapi -all -out ./openapi \
		&& $(GO) run ./cmd/openapi -service demoservice -out ./openapi \
		&& git diff --exit-code -- openapi \
		&& test -z "$$(git ls-files --others --exclude-standard -- openapi)"

check: fmt-check vet test-race example guards cgo-free
check-all: check example-contracts
```

- **golden 路径**：`git` 的 pathspec 相对当前工作目录；上面已经 `cd $(EXAMPLE_CONTRACTS)`，
  写成 `-- examples/interface-service/openapi` 会匹配不到任何文件、diff 永远为空，
  生成器漂移会静默通过。用 `-- openapi`（或 `:/examples/interface-service/openapi`），
  并用 `git status --porcelain` 覆盖新增文件（与 §5.6 口径一致）。
- **默认门禁保持自足**：`make check` 不包含 `example-contracts`，只改 gofy 根 module 的
  贡献者不需要并排 checkout 第三个仓库；`make check-all` 才要求 `../gofy-interface-api`
  存在，供发布前与 CI 使用，缺失时目标开头给出明确错误。
- 如果后续启用 CI，跑 `make check-all` 的 job 需要同时 checkout 两个仓库：

```yaml
- uses: actions/checkout@v4
  with: { path: gofy }
- uses: actions/checkout@v4
  with: { repository: bingcool/gofy-interface-api, path: gofy-interface-api }
```

- 只改 gofy 根 module 的工作仍可用 `make fmt-check vet test-race guards cgo-free` 单独验证，
  或直接用默认 `make check`。

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
5. 重写 `cmd/refcheck`：把 `layer.prefixes` 里硬编码的 `apicore/contracts` / `appinterface`
   换成 §4.3 的目录规则，`-module` 默认值改为 api 库 path；再新增 `-mode library|contracts`
   与 `-api` 参数（`contracts` 模式接受 `-app` 与 `-module`，`-api` 默认本库 module path，
   只有镜像仓库时才需要覆盖）。这一步不只是加 flag。
6. 新增 `docgen.Command`，把原 CLI 的筛选/校验/写盘逻辑迁入；`-serve` 预览留在
   `cmd/gofy-openapi -serve -spec`，不进 `docgen`（§4.5.2）。
7. 按 §4.7 迁移测试：新增 `internal/testcontract` fixture，改写所有
   `import appinterface/...` 的库内 `_test.go`，确认 api 库没有任何项目契约 import。
8. `go mod tidy`，确保 `adapter/gofy`、`client/nacos` 是仅有的 gofy 依赖边。
9. 验证：`make check`（fmt/vet/test-race/refcheck），用 `go list -deps -test ./...` 断言
   §4.3 的规则，并逐项核对 §4.7 的测试归属矩阵（库侧单测覆盖与迁移前相当）。

### 阶段 2：在 gofy 内建立 `examples/interface-service`

1. 新建 `examples/interface-service`，`go.mod` 使用 `github.com/yourorg/interface-service`（§5.3）。
2. 迁移 `appinterface/` 与 `openapi/`（含 `embed.go` 与四个生成物）。
3. 替换 import 前缀：`.../gofy-interface-service/appinterface/` → `github.com/yourorg/interface-service/appinterface/`。
4. 新增 `cmd/openapi/main.go`（§5.4）与 Makefile（§5.6）。
5. `go mod tidy`；运行 `make generate`，先做一次**解析后 JSON 的语义比对**（路径、security、
   组件名与拆分前一致，标题变化除外），之后的日常门禁以文本 diff 为准（§5.6、§8.2）。
6. 按 §4.7 接收从库侧迁来的 docgen 完整 golden 测试与契约/强类型客户端测试，确认
   项目 `make check` 的 `test` / `test-race` 覆盖它们，无测试覆盖流失。
7. 验证：`make check`，并确认 `openapi/*.yaml` 无文本 diff（除标题等有意调整）。

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
3. 验证：`make check-all`（含 example、example-contracts 与 guards；需要并排 checkout api 库）。

### 阶段 5：旧仓库下线（已关闭）

旧契约仓库 `gofy-interface-service` 已由所有者删除，不再保留迁移 README 或最终 tag。
实施时以「无任何引用」为下线判据：

1. 所有 `go.mod` 不再引用旧仓库（§8.4 第三条，已核验）。
2. 删除后四套门禁复跑仍全绿，确认没有隐式路径依赖。
3. §10 的“旧路径 → 新路径”映射表保留，供历史链接换算；历史设计文档按 §2.3 原样保留。

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
- `cmd/gofy-openapi -validate` 能校验项目生成的规范，`-serve -spec` 能托管已有规范；
- `cmd/refcheck -mode contracts` 能对示例项目通过；
- `go list -deps -test ./...` 中不含任何项目契约模块；§4.7 的 fixture 单测全部通过，
  库侧 `make check` 的 test-race 有实际用例覆盖。

### 8.2 项目契约模块

```bash
cd gofy/examples/interface-service
make check                 # fmt-check、vet、test、test-race、refcheck、generate + golden
```

- `go list -deps ./appinterface/...` 中不含 gin、gorm、gofy、adapter、docgen
  （`cmd/openapi` 允许 docgen，见 §3.2 硬规则 3）；
- 项目 `make check` 的 `test` / `test-race` 覆盖 §4.7 迁来的契约 DTO、强类型客户端与
  docgen 完整 golden 测试，相对拆分前无覆盖流失；
- 迁移阶段对 `openapi/all.*`、`openapi/demoservice.*` 做一次解析后 JSON 语义断言
  （路径、security、组件名与拆分前一致，标题变化除外）；日常门禁以文本为准，不与语义断言混用；
- `make generate` 后 `git diff --exit-code -- openapi` 为空，且
  `git ls-files --others --exclude-standard -- openapi` 为空（无新增未跟踪文件）。

### 8.3 业务服务与框架

```bash
cd gofy
make check                 # 含 examples/demo-service 的构建、vet、race；不依赖第三个仓库
make check-all             # 追加 examples/interface-service，需并排 checkout api 库
cd examples/demo-service && make check
```

- demo 的 controller 只 import 契约与 adapter；service/repository/model 不 import 契约
  （service 允许 api 库 `client`，见 §6.3）；
- `go run . script fix-order --order-id 1 --dry-run` 正常结束且不写库；
- gofy 根 module 的依赖图中没有 `gofy-interface-api`；
- 默认 `make check` 在没有 `../gofy-interface-api` 时仍能通过，`make check-all` 通过后
  才算覆盖到项目契约模块。

### 8.4 端到端

- 用 `examples/interface-service` 作为模板复制出一个临时项目，改写 module path 后 `make check` 通过；
- 复制出的项目把 `openapi.DemoServiceYAML` 挂到 gofy 服务上，`/docs` 可访问；
- 旧仓库 `gofy-interface-service` 不再被任何 `go.mod` 引用（已达成：仓库已删除，删除后四套门禁复跑全绿）。

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
| R8 | 旧仓库下线后历史链接失效 | 文档、CI、依赖指向旧地址 | **已关闭**：仓库已删除且无任何 `go.mod` 引用；§10 映射表保留路径对照，历史链接按映射表换算 |
| R9 | 库侧测试依赖 `appinterface`，项目侧 `check` 又漏了测试 | 阶段 1 编译失败；迁移后契约/DTO/docgen golden 测试退出所有门禁，回归能力下降 | §4.7 的 fixture 与测试归属矩阵；库/项目两侧 `make check` 都含 test-race；§8.1/§8.2 逐项核对覆盖不流失 |
| R10 | 默认 `make check` 依赖尚未建立的第三仓库 | 只改框架代码的贡献者门禁直接失败，绕过检查成为常态 | `example-contracts` 只进 `check-all`；默认 `check` 自足；CI 双仓 checkout 仅用于 `check-all` |
| R11 | golden 的 git pathspec 写成仓库根相对路径 | diff 永远为空，生成器漂移静默通过门禁 | 在 `cd` 后的目录内用 `-- openapi` 或 `:/` 根前缀，并补 `git status --porcelain` 检查新增文件 |

### 开放问题

1. **`client/nacos` 是否拆独立 module？** 建议 v1 不拆。拆分会增加一个发布单元，收益只是“纯客户端零 gofy”，当前没有非 gofy 的客户端消费者。
2. **示例模块放在 gofy 仓库内是否合适？** 符合用户提出的 `examples/interface-service`，也让三模块联调在一个工作区内；代价是 CI 需要并排 checkout api 库。若团队更希望示例独立成仓库，可直接把阶段 2 的产物移到新仓库，模块路径不变。
3. **api 库是否保留 `apicore/` 前缀？** 建议去掉。拆分后“核心里的一切都是核心”，保留前缀只会让 import 更长，且无法带来兼容性收益（路径本来就要改）。
4. **是否需要 `gofy-client` 生成器？** 当前仓库没有该命令，本文不引入。若后续需要，生成器应读取项目 `appinterface` 的 route 包，同样适合放在项目侧 `cmd/`。
5. **api 库是否应保持 gofy 生态专用？** v1 保持（§2.5、§4.4），即 `go.mod` 直接 require
   gofy。若出现非 gofy 的客户端消费方，再评估把 `adapter/gofy` 与 `client/nacos`
   拆到独立 module，避免通用契约库被框架版本绑定。

---

## 10. 附录：import 映射表

### 10.1 完整映射

| 旧路径前缀 | 新路径前缀 |
| --- | --- |
| `github.com/bingcool/gofy-interface-service/apicore/contracts` | `github.com/bingcool/gofy-interface-api/contracts` |
| `github.com/bingcool/gofy-interface-service/apicore/client` | `github.com/bingcool/gofy-interface-api/client` |
| `github.com/bingcool/gofy-interface-service/apicore/adapter/gofy` | `github.com/bingcool/gofy-interface-gofy/adapter/gofy`（§12） |
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
| `apicore/**/*_test.go`、`docgen/*_test.go` 里的 `appinterface` 引用 | api 库 `internal/testcontract/**` 或项目契约模块，按 §4.7 归属矩阵拆分 |
| `appinterface/**` | `examples/interface-service/appinterface/**`（或真实项目仓库） |
| `openapi/**` | `examples/interface-service/openapi/**`（或真实项目仓库） |
| `Makefile` | 拆成两份：api 库 Makefile 与项目 Makefile |
| `README.md` | 拆成两份：api 库 README 与项目模板 README |
| `AGENTS.md` | 保留在 api 库；项目模板沿用 gofy 仓库的注释约定 |

---

## 11. 优先级任务清单

> 用途：把 §7 的阶段描述拆成**可派工、可逐项验收**的任务。优先级按「是否阻断后续」划分，
> 与 §9 风险编号挂钩；同一优先级内按依赖顺序排列，标「可并行」者除外。
> 每条「完成判据」必须是可执行命令或可核对产物，不写「完成开发」这类状态词。
> **P0 全关前不进入 P1**（api 库编译不过会连锁失败），P2 只在 M1 之后启动。
> 本节覆盖实施任务（T0/T1/T2）；评审修订项已在本次修订稿中关闭，对照见 §11.6。

### 11.1 P0：关键路径（§7 阶段 0 → 阶段 1，目标：api 库可独立发布）

| ID | 任务 | 来源 | 前置 | 完成判据 |
| --- | --- | --- | --- | --- |
| T0-1 | 冻结基线：记录旧仓库 `make check` 结果、`openapi/` 四个生成物哈希、gofy `make check` 基线、§10.2 的 30 个受影响文件 | §7 阶段 0 | — | 基线记录已提交，`sha256sum openapi/*` 可复核 |
| T0-2 | 建 api 库骨架：`go.mod`（§4.4 + `replace ../gofy`）、§4.1 目录，按 §4.2 复制包并全量替换 import 前缀 | §4.1/§4.2 | T0-1 | `go build ./...` 通过；`go list -deps ./contracts/...` 无第三方包 |
| T0-3 | 设计并实现 `internal/testcontract` 最小 fixture（规格见 §11.4） | §4.7 | T0-2 | `go test ./internal/testcontract/...` 通过；`go list -deps -test ./...` 不含项目契约模块 |
| T0-4 | 迁移库侧测试：按 §4.7 矩阵改写 8 个 `_test.go`，docgen 完整 golden 移交 T1-2 | §4.7 | T0-3 | 库内 `grep -rn 'appinterface' --include='*_test.go'` 为空；库 `make check`（含 test-race）通过 |
| T0-5 | 重写 `cmd/refcheck`：`layer.prefixes` → §4.3 目录规则、`-module` 默认值改 api 库 path、新增 `-mode` / `-api` | §4.3、§7 阶段 1-5 | T0-2（可与 T0-3/4 并行） | `-mode library` 与 `-mode contracts` 均通过；故意注入违规 import 能被检出 |
| T0-6 | 实现 `docgen.Command`（§4.5.2 的 flag 集）+ 重写 `cmd/gofy-openapi` 为 `-validate` / `-serve -spec` | §4.5 | T0-2（可与 T0-3/4 并行） | `go list -deps ./docgen` 不含 `docsui`；`-validate`、`-serve -spec` 冒烟通过 |
| T0-7 | 关闭 api 库门禁：`go mod tidy`、依赖面断言、`make check`（fmt/vet/test-race/refcheck） | §8.1 | T0-2 … T0-6 | §8.1 全部条目通过；`adapter/gofy`、`client/nacos` 是仅有的两条 gofy 依赖边 |

**M0 里程碑**：T0-1…T0-7 全关 → api 库具备打第一个 tag 的条件（§4.6 发布链第 2 环）。

### 11.2 P1：主干迁移（§7 阶段 2 → 阶段 4，目标：三模块联调通过）

| ID | 任务 | 来源 | 前置 | 完成判据 |
| --- | --- | --- | --- | --- |
| T1-1 | 建 `examples/interface-service`：`go.mod`（module path + 2 条 replace）、`appinterface/`、`openapi/`（含 embed.go）、`cmd/openapi/main.go`、§5.6 Makefile、README 首行占位提示 | §5.1–§5.6 | M0 | `go mod tidy` 后 `go build ./...` 通过 |
| T1-2 | 接收迁移测试：docgen 完整 golden + 契约 DTO + 强类型客户端测试 | §4.7 | T1-1 | 项目 `make check` 的 `test` / `test-race` 覆盖它们；相对 T0-1 基线无覆盖流失 |
| T1-3 | 生成物对齐：一次解析后 JSON 语义比对，再跑 `make golden` | §7 阶段 2、§8.2 | T1-1 | 语义断言通过；`git diff --exit-code -- openapi` 与 `git ls-files --others --exclude-standard -- openapi` 均为空 |
| T1-4 | 重接 demo-service：§6.1 import 映射、`go.mod` 的 3 条 require + 3 条 replace、§6.3 guard 三类前缀 | §6.1–§6.3 | T1-1 | `cd examples/demo-service && make check` 通过；`go run . script fix-order --order-id 1 --dry-run` 正常退出；`/docs` 能加载项目内嵌规范 |
| T1-5 | gofy 侧收尾：§6.4 追加 `go list -deps` guard、§6.5 加 `example-contracts` + `check-all`（默认 `check` 不含）、§7 阶段 4 的文档与注释更新 | §6.4/§6.5、§7 阶段 4 | T1-1、T1-4 | 无 `../gofy-interface-api` 时 `make check` 通过；并排 checkout 时 `make check-all` 通过；`grep -rn 'gofy-interface-service' gofy/` 只剩历史叙述与 §10 映射表 |

**M1 里程碑**：T1-1…T1-5 全关 → §8.2 / §8.3 验收通过，三模块可联调。

### 11.3 P2：收尾与可选（§7 阶段 5 → 阶段 6）

| ID | 任务 | 来源 | 前置 | 完成判据 |
| --- | --- | --- | --- | --- |
| T2-1 | 旧仓库下线（仓库已删除；无迁移 README / 最终 tag） | §7 阶段 5 | M1 | 目录不存在、所有 `go.mod` 0 引用、删除后四套门禁仍 EXIT=0；§10 映射表保留 |
| T2-2 | 版本发布与兼容矩阵：按 `gofy → api 库 → 项目契约 → 业务服务` 打 tag、更新 CHANGELOG、api 库 README 维护兼容矩阵（R5） | §4.6、§9 R5 | M1 | 两级 semver tag 可被 `go list -m -versions` 查到（私有仓库等价手段） |
| T2-3 | 端到端模板验收：复制模板 → 改写 module path → `make check` → 挂载 `openapi.DemoServiceYAML` 访问 `/docs` | §8.4 | T1-1 | §8.4 三条全部通过 |
| T2-4 | 可选 CI：双仓 checkout + `make check-all`（§6.5 的 YAML）；不启用时 `make check` 仍是唯一门禁 | §6.5 | T1-5 | CI job 绿；或明确记录「不启用 CI」的决策 |
| T2-5 | `gofy new interface-service` 脚手架：复制模板并自动 `go mod edit -module`（R6） | §7 阶段 6、§9 R6 | T2-3 | 生成的项目首条命令即 `make check` 通过，且不含 `yourorg` 残留 |
| T2-6 | 条件性评估：`docgen.Command -routes` 动态加载、`client/nacos` 拆独立 module（R1、开放问题 5） | §7 阶段 6、§9 | 仅在出现非 gofy 消费方时启动 | 有书面评估结论；不启动也需记录原因 |

**M2 里程碑**：T2-1 已关闭（仓库已删除）；T2-2 完成 → 拆分正式收口。

### 11.4 `internal/testcontract` 最小 fixture 规格（T0-3）

只覆盖 docgen 与 adapter 判定所需的**形态**，不复制项目 DTO：

| 形态 | 覆盖点 | 构造方式 |
| --- | --- | --- |
| 单资源读写 | 路径参数、query/body schema、`NoBody` 响应 | `endpoint.Get[Req, Res]("/items/{id}", …)` 与对应的写方法 helper |
| 分页列表 | `paging.ListData[T]` 的 schema 生成 | `endpoint.Get[ListReq, paging.ListData[ItemDTO]]("/items", …)` |
| 认证与限流 | `Spec.Auth` 的 `AuthRequired` / `AuthOptional`、`Spec.RateLimit` 推导的 401 / 429 | 两条 Spec 分别设置 |
| 错误声明 | `Spec.Errors` 与 `errcode` 码段映射 | 一条带业务错、一条不带 |
| 分组与前缀 | `Group.Use` 逻辑别名、部署前缀拼接 | `endpoint.NewRouteGroup("/api/test", func(g *endpoint.Group) { … })` |

约束与判据：

- 只依赖 `contracts/**`（`endpoint`、`envelope`、`errcode`、`paging`、`types`）与标准库；
- 导出单一入口 `func Routes() []endpoint.Descriptor`，供库内全部测试复用；
- DTO 用 `ItemDTO` / `ItemRequest` 一类中性命名，不出现 `Order*` / `Product*`，避免 fixture 演变成第二份契约（§4.7）；
- 注释沿用 gofy `AGENTS.md` 的双语约定（英文在前、中文紧随）；
- helper 函数名以迁移后 `endpoint` 包的实际导出为准；测试只断言结构字段（路径、方法、security、组件名），不依赖业务字段值。

### 11.5 全局完成定义（DoD）

- §8.1–§8.4 全部通过；M0 → M1 → M2 依次关闭；
- 默认 `make check` 自足（不依赖第三个仓库），`make check-all` 覆盖项目契约模块；
- api 库的 `go list -deps -test ./...` 与项目模块的 `./appinterface/...` 满足 §3.2 硬规则；
- `grep -rn 'gofy-interface-service' gofy/` 只剩历史叙述与 §10 映射表；
- 测试覆盖相对 T0-1 基线无流失（§4.7 矩阵逐项核对）；
- R9 / R10 / R11 有对应关闭记录（T0-3、T0-4、T1-2、T1-5、T2-3）。

### 11.6 评审修订项对照（已关闭，不重复派工）

| 优先级 | 修订内容 | 落地章节 |
| --- | --- | --- |
| P0 | 测试归属与 fixture、§4.3 规则表与 `-serve` 归属、golden pathspec | §2.5、§4.1–§4.7、§5.6、§6.5、§7、§8、§9 R9/R11 |
| P1 | 项目 `check` 补 test/test-race、门禁拆 `check-all` | §5.6、§6.5、§7 阶段 4、§8.3、§9 R10 |
| P2 | 方案 B 的 Go 多模块前提、比对口径统一、占位路径措辞、refcheck 改造量、guard 分类粒度、适用边界与定位 | §2.2、§2.5、§3.1、§3.2、§3.4、§4.2–§4.5、§5.2、§6.3、§9、§10.3 |

> 本节只做任务拆分，不改变 §2.1 的决策与 §3 的拓扑；实施时若某个 T 任务的判据无法达成，
> 回到 §9 风险评估，而不是降低验收标准。

### 11.7 实施状态

> 每完成一项即时勾选并附验证结果；与 §11.1–§11.3 的任务表一一对应。
> 标记格式：`[x]` 完成（附验证命令/结果）、`[~]` 进行中、`[ ]` 未开始、`[!]` 阻塞待确认。

**P0 · 关键路径（M0）**

- [x] T0-1 冻结基线 — 旧仓库 `make check` EXIT=0，4 个生成物哈希记入 `docs/migration-baseline.md`；gofy `make check` 基线 EXIT=0
- [x] T0-2 建 api 库骨架 + import 全量替换 — `go mod tidy` / `go build ./...` EXIT=0；CLI 重写为 `-validate` / `-serve -spec`（无项目依赖）
- [x] T0-3 `internal/testcontract` 最小 fixture — 5 类 descriptor 形态、双语注释、`go test ./internal/testcontract/...` 通过
- [x] T0-4 迁移库侧测试 — 阶段 1 曾把 8 个业务契约驱动测试暂存在 `testdata/migrated/`；库侧新增 fixture 测试覆盖 client/docgen，`go test ./...` 仅剩 refcheck 旧实现失败（T0-5 修复）。暂存文件已由 T1-2 全部接收，目录已删除
- [x] T0-5 重写 `cmd/refcheck` — `-mode library|contracts`、§4.3 目录规则、`_test.go` 仅放宽库内自身包；`go test ./...` EXIT=0，`refcheck -mode library` 通过
- [x] T0-6 `docgen.Command` + 重写 CLI — `Command` 支持 `-all/-service/-out/-prefix/-title/-version/-description/-validate`；CLI 复用 `docgen.ValidateFile`；`go test ./...` EXIT=0
- [x] T0-7 关闭 api 库 `make check` — **M0 达成**：`make check` EXIT=0（fmt/vet/test-race/refcheck）；§8.1 断言全通过（contracts 零第三方依赖、仅 `adapter/gofy` 与 `client/nacos` 依赖 gofy、无项目契约泄漏、`-mode contracts` 冒烟通过）

**P1 · 主干迁移（M1）**

- [x] T1-1 建 `examples/interface-service` — 25 个文件 import 替换，`go mod tidy` / `go build ./...` / `go test ./...` EXIT=0
- [x] T1-2 接收迁移测试 — **76 个基线测试函数全部恢复**（库侧 `resolver_test.go` 11 个零改写移回 + `examples/demo-service/internal/integration/**` 65 个），`testdata/migrated/` 暂存目录已删除；库侧新增 `adapter/gofy/fixture_test.go`（8 个）与 `client/fixture_test.go`（9 个）按行为分支覆盖 §4.7 矩阵；项目 `make check` 与库 `make check` 的 `test` / `test-race` 均 EXIT=0，覆盖核验见 §4.7
- [x] T1-3 生成物对齐 — `all.{yaml,json}`、`demoservice.{yaml,json}` 与 T0-1 基线**逐字节一致**（sha256 完全匹配）；`refcheck -mode contracts` 对真实项目通过
- [x] T1-4 重接 demo-service — import/go.mod/三类 guard 前缀全量替换，`go mod tidy`/`build`/`test` EXIT=0；`gofy make check` 中的 example 通过
- [x] T1-5 gofy 侧收尾（guard / check-all / 文档） — 根 Makefile 新增 `example-contracts` + `check-all`（默认 `check` 自足）+ `sync-contract-template`，`shell/check-imports.sh` 加反向依赖守卫、`shell/check-contract-template.sh` 加入 `make guards` 防止内嵌快照与模板漂移；README 与 `support/**`、`examples/demo-service/**` 的源码注释、README 中的历史仓库名已按新拓扑润色（`grep` 残留 0 处），api 库 `client` 的默认 User-Agent 同步改为 `gofy-interface-api/<version>`；**`make check` / `make check-all` / api 库 `make check` / 项目 `make check` 四套门禁均 EXIT=0**。`CHANGELOG.md` 与 `docs/` 设计文档中的旧名属历史记录与 §10 import 映射表，按 §2.3 保留不改。

**P2 · 收尾与可选（M2）**

- [x] T2-1 旧仓库下线 — **用户已删除 `gofy-interface-service` 仓库**；无需迁移 README / 最终 tag。核验：目录不存在、全部 `go.mod` 与活跃代码/脚本 0 处引用、删除后 gofy `make check` / `check-all` / api 库 `make check` / 契约模块 `make check` 四套均 EXIT=0；§10 映射表与历史设计文档按 §2.3 保留
- [!] T2-2 版本发布与兼容矩阵 — 需打两级 semver tag 并改 CHANGELOG，待用户确认
- [x] T2-3 端到端模板验收 — 复制 `examples/interface-service` → 改写为 `gitlab.acme.com/platform/contracts` → `go mod tidy` → **`make check` EXIT=0**；`openapi.DemoServiceYAML` 经 `docsui.Handler` 挂载后 `/docs` 与 `/openapi.json` 均 200 且返回完整规范（探针 EXIT=0）；`yourorg` 残留 0 个文件；§8.4 三条全部通过
- [!] T2-4 可选 CI — 双仓 checkout 属外部动作，待用户决定是否启用；当前决策记录见 §11.8，**不启用 CI**，`make check` 仍是唯一默认门禁
- [x] T2-5 `gofy new interface-service` 脚手架 — `cmd/gofy/interface_service.go` 实现子命令（`--module/--dir/--local-root/--force/--no-git`），`cmd/gofy/contract-template/` 内嵌 43 文件快照（`.tmpl` 避免被 Go 工具链编译），生成时改写 module path 与全部 import、按相对路径重写唯一的 api 库 replace（同时支持 replace 块与单行写法）、执行 `go mod tidy` 并初始化 git 供 golden 使用；`shell/sync-contract-template.sh` + `make sync-contract-template` 保持快照同步；CLI 单测 5 项通过，**生成项目首条命令 `make check` EXIT=0**、`yourorg` 残留 0
- [x] T2-6 条件性评估 — 书面结论见 §11.8：两项均**不启动**并记录触发条件（其中「拆适配层」的触发条件后来成立，已由 T2-7 重启，见 §12）
- [x] T2-7 适配层独立与 gofy 解耦 — **方案 A 落地**：新建 `gofy-interface-gofy` 接管 `adapter/gofy` + `client/nacos`，api 库 `go.mod` 变为 3 个纯第三方 require、零 replace、零 gofy / 零 gin；demo-service 11 个文件改 import、go.mod 加 1 require + 1 replace；gofy 根 `example` 目标与反向依赖守卫同步；五套门禁全绿，详见 §12

**依赖修正（用户确认后追加）**：契约模块的生产与测试代码均不依赖 gofy 框架，但
`internal/integration/**` 曾把 gofy/gin 作为测试依赖带入 `go.mod`。已把这 8 个测试
整体移到 `examples/demo-service`，契约模块 `go.mod` 只剩 `gofy-interface-api`
（1 require + 1 replace），`go list -deps -test ./...` 中 gofy 包数为 0。CLI
`--local-root` 现在只需并排的 api 库，模板快照 51 → 43 文件；四套门禁与 T2-5
生成项目复验均 EXIT=0。

**里程碑**：M0 ✅ 达成（T0 全关，api 库 `make check` 全绿）；M1 ✅ 全关
（四套门禁 EXIT=0，76 个基线测试零流失）；M2 ⏸ 可执行项 T2-3 / T2-5 / T2-6 已关闭，
T2-1 / T2-7 已关闭；正式收口待 T2-2 / T2-4 的外部确认。

### 11.8 T2-6 条件性评估结论（2026-11-13）

> **部分结论已被 §12 取代**：表中「client/nacos 拆独立 module」的触发条件
> 「要求 go.mod 完全不含 gofy」已由用户确认成立，T2-7 据此把
> `adapter/gofy` 与 `client/nacos` 一起迁入独立库。动态 routes 一项仍不启动。

| 待评估项 | 结论 | 依据 | 触发重启的条件 |
| --- | --- | --- | --- |
| `docgen.Command -routes` 动态加载 | **不启动** | §4.5.1 已论证 `go/packages` 只能做类型检查、不能执行；临时 main + `go run` 脆弱。当前 routes 由项目侧 `cmd/openapi` 在编译期显式传入，7 条路由规模无动态需求，编译期检查正是拆分收益 | 出现运行时插件化路由、或单一项目路由数增长到需要按插件分批生成 |
| `client/nacos` 拆独立 module | **不启动** | 库内唯一额外的 gofy 依赖边是该包 → `gofy/support/nacos`，仅 1 个包 + 11 个 resolver 单测；再拆一个 module 会新增 tag、replace 与 4 级发布链成本，`client` 主包已零 gofy 依赖 | 出现**非 gofy 框架**的契约消费方，且要求 `go.mod` 完全不含 gofy |

**消费方现状**：`examples/demo-service` 是唯一业务消费方，契约模块自身是唯一的契约生产方。
§2.5 的「单项目不拆」判据在此成立，但拆分已完成且四套门禁全绿，回退成本高于维持，
故结论为**维持三模块现状、不追加拆分**。复核时点：T2-2 发布后，或出现第二个独立消费方时。

**去 replace 决策（2026-11-13，用户确认）**：用户提出 demo-service 不需要
`replace github.com/bingcool/gofy-interface-api => ../../../gofy-interface-api`，
理由是契约模块已 require api 库。三轮实测（删 replace、go.work、workspace 隔离）
均失败，根因是 replace 不继承加 `v0.0.0` 未发布。结论为**保持现状**，
规则与实测依据写入 §5.3 / §6.2；删除动作并入 T2-2 发布，不新设独立任务。
让契约模块 re-export `adapter/gofy` 以消去业务服务的直接依赖同样被否决：
它会把 gofy 重新带回契约模块，推翻「契约模块零 gofy」的结论。

**CI 决策（T2-4）**：仓库无 CI 配置，`make check` 为唯一默认门禁；`make check-all`
需并排 checkout `gofy-interface-api`。本轮**不启用 CI**，理由是当前只有一个消费方、
门禁链已在本地四套验证中全绿，双仓 CI 的维护成本尚无收益对冲；若启用，按 §6.5 的
YAML 双 checkout 执行，并把 `check-all` 设为发布门禁。此决策需用户最终确认。

---

## 12. 适配层独立与 gofy 解耦

### 12.1 触发与结论

§11.8 把「拆适配层」判为不启动，触发条件写的是「要求 `go.mod` 完全不含 gofy」。
用户确认该条件成立：`gofy-interface-api` 与 `gofy` 按定位应完全独立，
`require github.com/bingcool/gofy v0.0.0` + `replace => ../gofy` 既让库带上框架
耦合，也会在 api 库公开后让外部 `go get` 解析失败的占位版本。评估四个方案后
采用**方案 A：适配层独立 module**，其余方案与否决理由：

| 方案 | 否决/采纳理由 |
| --- | --- |
| A 独立 `gofy-interface-gofy` | **采纳**：代码几乎零改写，api 库与 gofy 互不依赖，可并行独立发布 |
| C 下沉到 demo-service | 不新增仓库，但适配层失去共享，第二个项目要复制约 800 行 |
| E 移入 gofy 仓库 | gofy 会反向依赖 api 库，不满足「互不依赖」 |
| B 接口反转 | `support.Router` 是具体 struct 且 `Group()` 返回具体类型，无法隐式实现接口；需重构约 800 行并长期维护两套抽象 |

> 本节取代 §2.5 的定位边界、§4.3 规则表、§4.4 的 go.mod 基线、§4.6 发布顺序、
> §9 R1 与开放问题 5、§11.8 的相关结论。历史叙述按 §2.3 原样保留，不再按旧章节执行。

### 12.2 新拓扑

```text
gofy  <-require-  gofy-interface-gofy  -require->  gofy-interface-api  <-require-  项目契约模块
  ^                       ^                              ^                            ^
  +-----------------------+------------------------------+----------------------------+
                              业务服务 demo-service
```

- `gofy` 与 `gofy-interface-api` **互不依赖**；
- `gofy-interface-gofy` 单独承担全部框架耦合（`support`、`apperr`、
  `gofy/support/nacos`、gin）；
- 项目契约模块只依赖 api 库，生产与测试依赖图中 gofy 包数仍为 0；
- 业务服务依赖四者：gofy、api 库、适配层库、契约模块。

### 12.3 依赖面与门禁

api 库 `go.mod` 现状（**无 replace**）：

```text
require (
    github.com/getkin/kin-openapi v0.149.0
    github.com/swaggest/swgui v1.8.9
    gopkg.in/yaml.v3 v3.0.1
)
```

`cmd/refcheck -mode library` 改为 contracts / client / docgen / docsui / cmd /
internal 六类目录，并对**全库含 `_test.go`** 拒绝 `github.com/bingcool/gofy**`
与 `github.com/gin-gonic/gin`；原 `adapter/gofy`、`client/nacos` 两条规则删除。
前缀比较带尾部斜杠，避免误伤 `github.com/bingcool/gofy-interface-api`。

`gofy-interface-gofy` 自带 `shell/check-imports.sh`：禁止项目契约模块
`github.com/yourorg/**`、禁止 api 库已迁出的 adapter / nacos 路径、禁止 `func init()`。
其 `go.mod` 保留两条本地 replace（gofy 与 api 库），发布时删除并固定版本。

### 12.4 迁移与消费方

| 内容 | 去向 / 改动 |
| --- | --- |
| `adapter/gofy/{bind,options,docs,errors}.go` + fixture 测试 | `gofy-interface-gofy/adapter/gofy` |
| `client/nacos/{resolver.go,resolver_test.go,README.md}` | `gofy-interface-gofy/client/nacos` |
| `internal/testcontract/fixture.go` | 新库复制一份（Go `internal` 不可跨 module 访问） |
| demo-service 11 个文件的 import 前缀 | 改为新 module path |
| demo-service `go.mod` | 新增 1 条 require + 1 条 replace（共 4 条 replace） |
| demo-service `shell/check-imports.sh` | `adapter` / `client` 分类前缀改指新库 |
| gofy 根 `Makefile` | `example` 目标前置检查两个同级库目录 |
| gofy 根 `shell/check-imports.sh` | 增加对 `gofy-interface-gofy` 的反向依赖守卫 |

契约模块、`cmd/gofy` 脚手架与 43 文件模板快照**未改动**：契约模块不 import
适配层，`gofy new interface-service --local-root` 仍只需并排的 api 库。
T2-5 生成项目首条命令 `make check` 复验仍 EXIT=0。

### 12.5 发布链

`gofy` 与 `gofy-interface-api` 可**并行独立发布**（互不依赖）→
`gofy-interface-gofy` 固定两者版本 → 项目契约模块 → 业务服务。
api 库不再需要 §4.6 的「删除 replace、固定 gofy 最低版本」这一步。

代价是本地联调需要三个并排仓库（gofy / gofy-interface-api /
gofy-interface-gofy），`make check` 的 `example` 目标会对缺失目录给出明确报错；
§9 R10 记录的「默认门禁依赖同级仓库」风险随之从 1 个库扩展到 2 个库。

### 12.6 验收（T2-7 `[x]`）

- api 库 `go.mod`：`github.com/bingcool/gofy ` 出现 0 次、`gin-gonic` 出现 0 次、
  `replace` 条数 0；`go list -deps ./...` 中 gofy 包数 0；
- 五套门禁 EXIT=0：gofy `make check` / `make check-all`、api 库 `make check`、
  适配层 `make check`、契约模块 `make check`；
- 76 个基线测试与适配层 2 个包的测试全部通过，无覆盖流失；
- `refcheck -mode library` 检出故意注入的 gofy / gin import（单测覆盖）。
