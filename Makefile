GO      ?= go
GOFLAGS ?=
PKGS    := ./...
EXAMPLE := examples/demo-service

.PHONY: all fmt fmt-check vet test test-race example guards cgo-free check build clean

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
	cd $(EXAMPLE) && $(GO) build ./... && $(GO) test $(GOFLAGS) -race -count=1 ./...

guards:
	./shell/check-imports.sh
	$(EXAMPLE)/shell/check-imports.sh
	@if grep -rn "func init()" --include="*.go" *.go apperr core logger support script command config gofytest internal; then \
		echo "public packages must not use init()"; exit 1; \
	fi

# 全仓库禁止依赖 cgo：源码必须能在 CGO_ENABLED=0 下构建，依赖图里不能有
# CgoFiles（SQLite 因此使用 glebarez/sqlite 的纯 Go 实现）。-race 需要 cgo，
# 只在 test-race 里启用。
cgo-free:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) $(PKGS)
	@cgo="$$($(GO) list -deps -test -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' $(PKGS) | grep -v '^$$' || true)"; \
	if [ -n "$$cgo" ]; then echo "cgo is not allowed:"; echo "$$cgo"; exit 1; fi

check: fmt-check vet test-race example guards cgo-free

build:
	$(GO) build $(GOFLAGS) $(PKGS)

clean:
	rm -rf $(EXAMPLE)/bin
