GO      ?= go
GOFLAGS ?=
PKGS    := ./...
EXAMPLE := examples/demo-service
EXAMPLE_CONTRACTS := examples/interface-service
API_LIBRARY := ../gofy-interface-api
ADAPTER_LIBRARY := ../gofy-adapter

.PHONY: all fmt fmt-check vet test test-race example example-contracts sync-contract-template check-all guards cgo-free cross check build clean

all: fmt-check vet test

fmt:
	gofmt -l -w .

fmt-check:
	@files="$$(gofmt -l . | grep -v '^examples/demo-service/go.sum$$' || true)"; \
	if [ -n "$$files" ]; then echo "gofmt required:"; echo "$$files"; exit 1; fi

vet:
	$(GO) vet $(GOFLAGS) $(PKGS)

test:
	$(GO) test $(GOFLAGS) -count=1 $(PKGS)

test-race:
	$(GO) test $(GOFLAGS) -race -count=1 $(PKGS)

example:
	@test -d $(API_LIBRARY) || { echo "gofy-interface-api not found at $(API_LIBRARY); see README"; exit 1; }
	@test -d $(ADAPTER_LIBRARY) || { echo "gofy-adapter not found at $(ADAPTER_LIBRARY); see README"; exit 1; }
	cd $(EXAMPLE) && $(GO) build ./... && $(GO) vet ./... && $(GO) test $(GOFLAGS) -race -count=1 ./...

example-contracts:
	@test -d $(API_LIBRARY) || { echo "gofy-interface-api not found at $(API_LIBRARY); see README"; exit 1; }
	cd $(EXAMPLE_CONTRACTS) && $(GO) vet ./... && $(GO) test $(GOFLAGS) -race -count=1 ./... \
		&& $(GO) run ./cmd/openapi -all -out ./openapi \
		&& $(GO) run ./cmd/openapi -service demoservice -out ./openapi \
		&& git diff --exit-code -- openapi \
		&& test -z "$$(git ls-files --others --exclude-standard -- openapi)"

check-all: check example-contracts

sync-contract-template:
	./shell/sync-contract-template.sh

guards:
	./shell/check-imports.sh
	$(EXAMPLE)/shell/check-imports.sh
	$(EXAMPLE)/shell/check-io-access.sh
	@if grep -rn "func init()" --include="*.go" *.go apperr core logger support script command config gofytest internal; then \
		echo "public packages must not use init()"; exit 1; \
	fi
	./shell/check-contract-template.sh

# 全仓库禁止依赖 cgo：源码必须能在 CGO_ENABLED=0 下构建，依赖图里不能有
# CgoFiles（SQLite 因此使用 glebarez/sqlite 的纯 Go 实现）。-race 需要 cgo，
# 只在 test-race 里启用。
#
# list 也必须带 CGO_ENABLED=0：默认环境下 go list 会把只在该平台 + cgo 时编译的
# 文件算作 CgoFiles，例如 prometheus/client_golang 的
# process_collector_mem_cgo_darwin.go。那种文件在 CGO_ENABLED=0 的构建里根本不参与
# 编译（该库有纯 Go 回退），是误报；真正的 cgo-only 依赖会在上面的 build 一步失败。
cgo-free:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) $(PKGS)
	@cgo="$$(CGO_ENABLED=0 $(GO) list -deps -test -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' $(PKGS) | grep -v '^$$' || true)"; \
	if [ -n "$$cgo" ]; then echo "cgo is not allowed:"; echo "$$cgo"; exit 1; fi

# Windows 交叉编译守卫：只在 Linux/macOS 开发时，平台相关的符号缺失不会在本地构建里暴露。
# worker_notify.go 曾直接引用只在 detach_unix.go 定义的 process.NotifyEnv / NotifyFD，
# 结果 Windows 整体编译不过而无人察觉。go vet 而不是 go build，因为测试文件里的构建标签
# 同样需要被检查，而 go build 不编译测试。
cross:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) vet $(GOFLAGS) $(PKGS)

check: fmt-check vet test-race example guards cgo-free cross

build:
	$(GO) build $(GOFLAGS) $(PKGS)

clean:
	rm -rf $(EXAMPLE)/bin
