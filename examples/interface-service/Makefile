GO   ?= go
PKGS := ./...

.PHONY: fmt-check vet test test-race generate validate refcheck golden check

fmt-check:
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then echo "gofmt required:"; echo "$$files"; exit 1; fi

vet:
	$(GO) vet $(PKGS)

test:
	$(GO) test $(GOFLAGS) -count=1 $(PKGS)

test-race:
	$(GO) test $(GOFLAGS) -race -count=1 $(PKGS)

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
