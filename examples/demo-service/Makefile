APP      := demo-service
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 1.0.0)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_AT ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_AT)

.PHONY: build test guards check run script docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) .

test:
	go test -race -count=1 ./...

guards:
	./shell/check-imports.sh

check: guards test

run:
	go run . start

script:
	go run . script $(ARGS)

docker:
	docker build -t $(APP):$(VERSION) .
