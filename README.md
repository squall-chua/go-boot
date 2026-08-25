# go-boot

go-boot gives a Go service the operational spine a Spring Boot developer expects: an ordered
Component lifecycle and a real Actuator, built on the standard library, neutral about the query
layer.

**go-boot is not for a single-endpoint HTTP service with no database and no Actuator.** It has no
lifecycle to order, so there is nothing for go-boot to encode. Use `net/http` directly. This was
measured: at one Component go-boot made `main` *longer*, and a 78-line standard-library file beat
it outright.

## Status

Early. What works today is HTTP routes served by a real listener, started and stopped in Tier
order, with a clean shutdown on SIGTERM; config from a file and the environment; the default
middleware set with the response helpers below; the Actuator; and the database Starter, with a
real PostgreSQL for tests. The rest of the v1 surface — gRPC, tracing and the Presets — is being
built ticket by ticket against `docs/spec.md`.

## Install

```
go get github.com/squall-chua/go-boot
```

## Use

```go
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

func main() {
	app, err := goboot.New(goboot.Config{})
	if err != nil {
		log.Fatal(err)
	}

	srv := web.New(web.Config{Addr: ":8080"}, app.Log)
	srv.Use(web.DefaultMiddleware(app.Log)...)
	srv.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		web.WriteJSON(w, http.StatusOK, map[string]string{"hello": "world"})
	})
	app.Add(srv)

	if err := app.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

`examples/http-only` is this same service as a file you can run.

`app.Add` ignores the order you write. Each Component declares its own Tier, and go-boot starts
from the lowest Tier to the highest and stops in reverse. So wiring in the wrong order is not a
mistake you can make.

## The default middleware

`web.DefaultMiddleware` is a slice you can print and edit, not hidden behaviour. It holds three
entries, outermost first:

1. `RequestID` — puts an `X-Request-Id` on the response. It reuses the one the caller sent only if
   it is at most 64 characters of letters, digits, `-`, `_` and `.`; anything else is replaced.
   An unbounded attacker-controlled string flowing into every log line is a log-injection hole.
2. `Logging` — one line per request on the way out, carrying method, path, route, status, bytes,
   duration and request ID. A 5xx goes to ERROR, everything else to INFO, so a server error is
   findable by level alone. It also attaches the request-scoped logger, so
   `goboot.LoggerFrom(r.Context())` inside a handler returns a logger already tagged with the
   request ID.
3. `Recovery` — turns a panicking handler into a 500 as an RFC 7807 document. Without it, a panic
   gives the client `EOF` and no response at all. It sits **inside** `Logging` on purpose, so the
   500 it writes passes back out through the logging wrapper and is recorded as a 500.

**Probe traffic is not logged.** `/livez`, `/readyz` and `/actuator/*` are skipped. Kubernetes hits
the first two every ten seconds, which is roughly 17,000 log lines a day saying nothing. These three
paths are hardcoded, not a config key.

Errors on the wire are RFC 7807 documents from `web.WriteProblem`, so a panic and a hand-written
400 come out in the same shape. `web.DecodeJSON` reads a request body with the size cap, unknown
field rejection and readable errors that `json.NewDecoder(r.Body).Decode` leaves to you.

## The Actuator

**Metrics answer 404 until you name them.** `actuator.expose` is a whitelist and it defaults to
`livez, readyz, info`. An endpoint not on the list is never registered, so a wrong Ingress rule
has nothing to leak. This is the one thing that surprises people, so it is said first.

```go
act := actuator.New(cfg.Actuator, app)
act.MountOn(srv)   // the same line whether actuator.addr is set or not
app.Add(act, srv)
```

| Path | Method | Exposed by default |
|---|---|---|
| `/actuator/livez`, and `/livez` | GET | yes |
| `/actuator/readyz`, and `/readyz` | GET | yes |
| `/actuator/info` | GET | yes |
| `/actuator/metrics` | GET | no |
| `/actuator/loglevel` | GET, PUT | no |
| `/actuator/pprof/*` | GET | no |

`/livez` never runs a readiness Check: a liveness test that touches the database turns an outage
into a restart storm. `/readyz` runs every Check on each request, synchronously, and is 503 unless
the App has finished starting and all of them pass. A Check is not registered by hand — a Component
that offers `Check(ctx) error` is picked up when the Actuator starts. The Check gets the request
context, which already carries the probe's real deadline, so it **must respect cancellation**.

The readiness body is bare, `{"status":"UP"}`. `actuator.showDetails: always` adds the error text
of each failing Check, which can print a database host, so read it as a decision. Either way a
failing Check is logged at WARN with the full detail.

`actuator.addr` moves every endpoint to a private listener the Actuator binds and owns. The
whitelist still applies there.

`examples/http-actuator-config` is a service with the Actuator, the web Starter and its own config
key, as a file you can run.

## gRPC

**There is no gRPC address and no gRPC config.** It is the first thing a reader looks for, so it is
said outright. `goboot/grpc` owns no server, no `Component` and no config at all: the address
belongs to `goboot/web`, and two ports is `web.New` called twice. See
[ADR 0006](docs/adr/0006-grpc-shares-the-http-listener.md).

connect-go's generated constructor returns `(string, http.Handler)`, which is exactly the shape of
`web.Server.Handle`, so a connect service mounts on the HTTP listener with no adapter and no second
port:

```go
srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))
```

One cleartext port answers gRPC, gRPC-Web, Connect JSON and plain REST at once. `web` turns on
HTTP/2 over cleartext for this, which since Go 1.24 costs no extra module and no h2c wrapper.

### Write the gRPC Transport type, always

This is mandatory, not stylistic. The generated interface wants `Greet(ctx, *connect.Request[...])`
and your Service Layer already owns the name `Greet`, so embedding the service does not work. Both
compiler errors were measured:

| what you wrote | what the compiler says |
|---|---|
| embed the Service Layer | `*badA does not implement GreetServiceHandler (wrong type for method Greet)`, then `have` and `want` |
| embed the Service Layer **and** the generated `Unimplemented...` type | `*badB does not implement GreetServiceHandler (ambiguous selector *badB.Greet)` |

The second is the confusing one, and it is the common case, because embedding the generated
`Unimplemented...` type is what you do for forward compatibility. It never mentions the signatures
at all — only that it cannot choose between two `Greet` methods.

A separate thin type — the gRPC Transport, in this repo's language — is the way out, and it is
four lines:

```go
type grpcGreeter struct{ svc *greeter }

func (g *grpcGreeter) Greet(ctx context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: out}), nil
}
```

The Service Layer stays free of connect, and both Transports call the same `greeter`.

### The default options

```go
grpc.DefaultOptions(app.Log)  // a slice you can edit, like web.DefaultMiddleware
```

Three entries: panic recovery, the error sanitiser, and connect's required protocol header.

**The sanitiser is mandatory, not a nicety.** A bare `error` returned from a connect handler reaches
the caller **verbatim**. Measured: `pq: password authentication failed for user "app" at
10.0.0.5:5432` went out on the wire, host and username and all. The sanitiser replaces anything
that is not already a `*connect.Error` with a bare `CodeUnknown` and logs the real one. An error you
built yourself with `connect.NewError` passes through untouched.

There is **no logging or request-ID interceptor**, and that is not an omission. Under the shared
listener `web.DefaultMiddleware` has already run, so `goboot.LoggerFrom(ctx)` and the request ID
reach your connect handler free.

One honest asymmetry with `web.Use`: connect options are **per service**, not per server, so you
repeat them at every mount. connect has no global registry and there is no way around it.

### The access log records 200 for a failed gRPC call

The gRPC and gRPC-Web status rides in **trailers**, not in the HTTP status line, so the access log
line for a failed RPC says `"status":200`. Only the Connect protocol maps errors onto HTTP status
codes. This is not a bug and it is not fixable from go-boot's side.

**The sanitiser's `rpc failed` line is where the truth lives.** It carries the procedure, the code
and the real error, tagged with the same `requestId` as the access line.

### Streaming

Streaming works, and there are no streaming helpers — connect does it, and go-boot's only relevant
choice was leaving `writeTimeout` off so a write deadline cannot cut a stream in half.

**The one number to know is the 10s stop timeout.** That is what cuts a long-lived stream on
shutdown: `Stop` waits for open streams until the timeout expires, then drops them.

### Codegen

go-boot requires nothing. It never runs codegen and never imports your generated code — you pass
the generated package in as a value. `buf` is the documented path only, and a repo with existing
protos changes nothing. There is no `protoc-gen-goboot` and no Makefile: go-boot does not own your
build tool. The two commands go here, in a comment, rather than in a build file:

```sh
# once
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest

# every time the .proto changes
buf lint
buf generate
```

## The database

**Run the migration Job before the rollout, not beside it.** A go-boot service refuses to start
while migrations are pending. If migrations run as a separate Kubernetes Job, that Job must finish
before the rollout starts, or every new pod crashloops until it does. The Job runs the **same
image** as the pods, which is what stops code and schema drifting. This is the operational
contract, so it is said first rather than in a footnote. See
[ADR 0007](docs/adr/0007-migrations-refuse-to-start.md).

`db.migrateOnStart` applies them at startup instead. It is off by default and it is for local
development: every pod races every other pod at rollout, and the whole run is bounded by the 30s
`lifecycle.startTimeout`.

```go
//go:embed migrations/*.sql
var migrations embed.FS

pool, dbc, err := db.New(cfg.DB, app.Log, migrations)  // pool first: a plain *sql.DB
app.Add(dbc, srv)
```

**go-boot links no database driver.** A driver is +7.64 MB stripped, and a MySQL user would have
paid all of it for one they cannot use. Blank-import your own in `main`:

```go
import _ "github.com/jackc/pgx/v5/stdlib"
```

Forgetting that line is not a puzzle. `sql.Open` reports `sql: unknown driver "pgx" (forgotten
import?)`, and `db.New` runs in `main` before `app.Run`.

### Pool defaults

Go's own are wrong for a service: unlimited open connections, two idle, and connections that live
forever.

| Key | Default | Why |
|---|---|---|
| `maxOpenConns` | 10 | a stock PostgreSQL allows about 97 connections, so **this is ten pods** before the database runs out. Scaling past ten means raising the database's limit or putting a pooler in front |
| `maxIdleConns` | 10 | matching `maxOpenConns` avoids churn where 8 of 10 connections are closed and reopened on every burst |
| `connMaxIdleTime` | 5m | a scaled-down deployment gives its slots back |
| `connMaxLifetime` | 30m | connections rebalance after a failover or a proxy restart |

### Transactions

`db.WithTx` commits, rolls back if the closure returns an error, and rolls back and keeps panicking
if it panics. The transaction is a **parameter**, so a reader can see it. It takes no options and it
does not nest — `*sql.Tx` has no `Begin`, so `database/sql` cannot nest transactions at all
([ADR 0008](docs/adr/0008-transactions-are-an-explicit-closure.md)).

```go
err := db.WithTx(ctx, pool, func(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO widget (name) VALUES ($1)", name)
	return err
})
```

There is no Repository interface and no Entity type, and there will not be one
([ADR 0009](docs/adr/0009-no-repository-abstraction.md)). The pool is a plain `*sql.DB`, so `sqlc`,
`ent`, `gorm` and hand-written SQL all take it unchanged.

### Migrations, and the `migrate` subcommand

There is no `goboot migrate` command and there could not be one: migrations live in your own
`embed.FS`, which a generic go-boot binary can never see. `db.NewProvider` returns a
`*goose.Provider`, and your `main` wires a `myservice migrate` subcommand onto it. Both that
subcommand and `Start` call `NewProvider`, so the session lock is wired in exactly one place —
goose leaves locking off unless you ask, and forgetting to ask is the known bug.

**goose ships a session locker for PostgreSQL only.** On MySQL and SQLite there is nothing to wire,
so two pods applying the same migration are not protected from each other. Run the migration as a
Job, which is the documented way in any case.

Passing `nil` migrations is a supported mode for a service that does not own its schema. It skips
both the migration run and the refusal, and leaves you the pool and the readiness Check. See
[`docs/jpa-interop.md`](docs/jpa-interop.md) for sharing one database with a Spring Data JPA
service.

### A real PostgreSQL for tests

`goboot/db/dbtest` starts a real PostgreSQL 18 for one test, with **no Docker daemon**: 3 linked
module roots against `testcontainers-go`'s 45, and up in under three seconds.

```go
func TestWidgets(t *testing.T) {
	pool := dbtest.Start(t, migrations)   // torn down by t.Cleanup
	dbtest.LintJPAConventions(t, pool)    // optional; see docs/jpa-interop.md
}
```

It is safe to call from parallel tests. The first run downloads about 14 MB of PostgreSQL binaries
and extracts them to roughly 71 MB under your user cache directory — measured on linux/amd64 with
PostgreSQL 18.3. Set `GOBOOT_PG_BINARIES` to a pre-seeded directory to run air-gapped. It is a separate package because it is heavy and because it links a driver, which
`goboot/db` refuses to.

## Go version

The floor is **Go 1.25.7**.

It rose from 1.25.0 when the database Starter landed: go-boot now depends on `goose/v3`, which
declares a patch-level `go` directive, and the highest floor in a module wins. Go 1.25.0 through
1.25.6 no longer build go-boot at all — even for a user who never imports the database Starter.

A stock Go 1.22 toolchain handles this on its own: it downloads the newer toolchain in about
fifteen seconds. But that needs the toolchain switch and the module proxy to be working, so
`GOTOOLCHAIN=local`, `GOPROXY=off` and any Go below 1.21 are unsupported.

## Documentation

- `docs/spec.md` — the locked v1 spec, and the design authority for every ticket.
- `CONTEXT.md` — the words go-boot uses, and what each one means.
- `docs/adr/` — the decisions, one file each.

## Licence

See `LICENSE`.
