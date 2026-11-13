# 拆分迁移基线（T0-1）

> **归档提示（2026-11-13）**：旧仓库已删除，下面的路径与哈希仅作 T0-1 历史基线保留，
> 不再可复核。当前生成物基线以 `examples/interface-service/openapi/` 与 `make check-all`
> 的 golden 为准。

- 冻结时间：2026-11-13
- 依据：`docs/interface-api-split-design.md` §7 阶段 0 / §11.1 T0-1

## 旧仓库 `gofy-interface-service`

- 路径：`/Users/huangzengbing/Documents/wwwroot/gofy-interface-service`
- HEAD：`37de1d3`（main，工作区干净）
- `make check`：**EXIT=0**（fmt-check / vet / test-race / refcheck / golden 全绿）
- Go 文件：`75`
- 生成物 sha256：

```
945069aec259aefe578bd00f7fa54e7437e511f9f996e59bea449b269f1bc0c5  openapi/all.json
886f584d8e69e9252b6bf1784df268c54187a2259f5c7d53049880cacad8f8e4  openapi/all.yaml
945069aec259aefe578bd00f7fa54e7437e511f9f996e59bea449b269f1bc0c5  openapi/demoservice.json
886f584d8e69e9252b6bf1784df268c54187a2259f5c7d53049880cacad8f8e4  openapi/demoservice.yaml
db937f7951e9f7447562dd6966bb7feac54f53736505ceaa3f3a7557d17ab51e  openapi/embed.go
```

- 生成结果：`all.yaml/json` 与 `demoservice.yaml/json` 各 7 routes，内容相同（当前只有一个服务）。
- 库侧依赖 `appinterface/` 的测试文件（§4.7、T0-4 待迁移，共 8 个）：
  `apicore/adapter/gofy/{bind,middleware_registry,auth_validator}_test.go`、
  `apicore/client/{client,headers}_test.go`、
  `apicore/client/nacos/{registration,resolver}_test.go`、`docgen/docgen_test.go`。

## 框架仓库 `gofy`

- HEAD：`6c9b083`（main）；本会话仅有 `docs/interface-api-split-design.md` 的修订与本文。
- `make check`：**EXIT=0**（冷缓存首次整条链被 SIGTERM，分项执行 + `GOFLAGS=-p=2` 重跑通过，非代码缺陷）。
- 受影响文件：`grep -rn 'gofy-interface-service' gofy/` 命中 30 个文件（排除方案文档自身），清单见方案 §10.2。

## 复核方式

```bash
cd ../gofy-interface-service && shasum -a 256 openapi/* && make check
cd ../gofy && make check GOFLAGS="-p=2"
```

> 迁移过程中若生成物哈希或测试结果偏离本基线，先定位差异，不要改写基线。
