# gofy

`gofy` is a small, opinionated Go application framework built on
[gin](https://github.com/gin-gonic/gin). It provides an application lifecycle,
typed HTTP handlers, request binding/validation, a response envelope, logging,
MySQL (GORM) wiring, maintenance scripts and a CLI — without global state,
`init()` side effects or a fixed config file.

## Install

```bash
go get github.com/bingcool/gofy@latest
```

Requires Go 1.24+.

## Quick start

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/bingcool/gofy"
	"github.com/bingcool/gofy/transport"
)

type helloRequest struct {
	Name string `form:"name" default:"world" validate:"required,max=32" message:"max:name is too long"`
}

type helloResponse struct {
	Message string `json:"message"`
}

func main() {
	ctx, stop := gofy.SignalContext(context.Background())
	defer stop()

	app, err := gofy.New(
		gofy.WithName("hello"),
		gofy.WithAPIPrefix("/api/v1"),
		gofy.WithHealthEndpoints(true),
		gofy.WithModules(helloModule{}),
	)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if err := app.Execute(ctx); err != nil {
		os.Exit(gofy.ExitCode(err))
	}
}

type helloModule struct{}

func (helloModule) Name() string { return "hello" }

func (helloModule) Register(r *transport.Router) error {
	transport.GET(r, "/greet", func(_ context.Context, req *helloRequest) (*helloResponse, error) {
		return &helloResponse{Message: "hello " + req.Name}, nil
	})
	return nil
}
```

```bash
go run .            # same as: go run . start
go run . version
go run . stop       # uses the pid file, if configured
```

Routes are mounted at `/api/v1/hello/greet`:

```bash
curl 'http://127.0.0.1:8080/api/v1/hello/greet?name=gofy'
# {"code":0,"msg":"success","data":{"message":"hello gofy"},"req_id":"..."}
```

## Scaffold

```bash
go install github.com/bingcool/gofy/cmd/gofy@latest

gofy new order-service \
  --module github.com/yourorg/order-service \
  --dir ./order-service \
  --with-db \
  --with-docker
```

Add `--replace /path/to/gofy` to develop against a local checkout. The generated
project does not include secrets or a `go.sum`; run `go mod tidy` and then
`go run . start`.

## Configuration

gofy never reads a config file on its own. Applications own their config and
pass it in through options. The optional loader applies defaults, expands
`${ENV_VAR}` references and validates the result:

```go
type Config struct {
	HTTP gofy.HTTPConfig     `yaml:"http"`
	Log  gofy.LogConfig      `yaml:"log"`
	DB   gofy.DatabaseConfig `yaml:"db"`
}

cfg, err := gofyconfig.Load[Config](ctx, "config.yaml")
```

```yaml
http:
  port: 8080
db:
  dsn: ${ORDER_DB_DSN}
```

Secrets should come from the environment, not from committed YAML.

## CLI

| Command | Behavior |
| --- | --- |
| `./service` / `./service start` | Run in the foreground until SIGINT/SIGTERM |
| `./service stop [--pid-file path] [--timeout 10s] [--force]` | SIGTERM, then optional SIGKILL |
| `./service script <name> [flags]` | Run a maintenance script |
| `./service script --list` | List registered scripts |
| `./service version [--json]` | Build information |
| `./service <name>` | Custom command registered with `WithCommands` |

Exit codes: `0` success, `1` runtime error, `2` usage error.

## Testing

`gofytest` builds an in-process handler with no listener, pid file or real DB:

```go
app := gofytest.New(t, gofy.WithModules(orderModule{}))
rr := app.GET("/api/v1/order/orders/1")
gofytest.Status(t, rr, 200)
```

Run the framework suite:

```bash
make test-race
make example   # builds and tests examples/order-service
```

The MySQL integration test is opt-in:

```bash
GOFY_TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/app?charset=utf8mb4&parseTime=True&loc=Local' \
  go test ./support/mysql -run Integration -v
```

## Repository layout

```text
gofy.go, option.go, run.go, execute.go, stop.go   App, lifecycle, CLI
apperr/        business error model
core/          shared Runtime (leaf package)
transport/     Router, typed handlers, responder, middleware
logging/       zap logger construction and GORM adapter
support/mysql/ GORM MySQL connection management
script/        maintenance script descriptors
command/       custom CLI command descriptors
config/        optional YAML loader
gofytest/      HTTP test helpers
internal/      pid files, process control, version, cobra wiring
cmd/gofy/      scaffold CLI (not imported by the library)
examples/order-service/  complete service (separate Go module)
docs/          design and migration documents
```

Public packages never call `init()`, never keep package-level mutable state and
never call `os.Exit` or `panic`. The app owns its logger, DB and HTTP server and
releases them on shutdown.
