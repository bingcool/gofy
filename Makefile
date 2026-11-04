GO      ?= go
GOFLAGS ?=
PKGS    := ./...
EXAMPLE := examples/demo-service

.PHONY: all fmt fmt-check vet test test-race example guards check build clean

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
	./scripts/check-imports.sh
	@if grep -rn "func init()" --include="*.go" *.go apperr core transport logging support script command config gofytest internal; then \
		echo "public packages must not use init()"; exit 1; \
	fi

check: fmt-check vet test-race example guards

build:
	$(GO) build $(GOFLAGS) $(PKGS)

clean:
	rm -rf $(EXAMPLE)/bin
