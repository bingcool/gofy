# interface-service（项目契约模板）

> **复制本目录到新仓库后，第一步执行 `go mod edit -module <真实 VCS 路径>` 并全量替换 import。**
> `github.com/yourorg/interface-service` 只是不发布模板的占位 module path。

本模块持有项目的业务契约 `appinterface/` 与生成物 `openapi/`；框架无关的共享
基础设施（`contracts`、`client`、`docgen`、`docsui`）来自
`github.com/bingcool/gofy-interface-api`，gofy 适配层在 `gofy-adapter`。

```bash
make check     # fmt/vet/test/test-race/refcheck/golden
make generate  # 重新生成 openapi/*.yaml 与 *.json
```

本模块**不依赖 `github.com/bingcool/gofy` 框架**：`appinterface/` 与生成物只引用
`gofy-interface-api` 的纯契约包。`go.mod` 里唯一的 `replace` 指向本地
`gofy-interface-api` 仓库，仅供联调；发布时删除并固定版本。
