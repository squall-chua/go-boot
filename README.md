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
middleware set with the response helpers below; the Actuator; the database Starter, with a
real PostgreSQL for tests; the gRPC Transport; tracing; and the Presets. That is the whole library
surface `docs/spec.md` locks. What is left is the CI import-leak check, and the Scaffold, which
that spec defers past v1.

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

### Health and reflection

Two more packages, `goboot/grpc/health` and `goboot/grpc/reflection`. **Both are opt-in by
import**: a service that wants neither links neither, and that is checked by a test reading the
linked module list. Once imported there is nothing to configure.

```go
srv.Handle(health.New(app))                                  // the standard gRPC health service
reflection.MountOn(srv, greetv1connect.GreetServiceName)     // list the service with grpcurl
```

**Health answers the same question `/readyz` answers**, read from the same `App`: the App's
readiness first, then every Component's `Check`. So a database outage that takes `/readyz` to 503
takes gRPC health to `NOT_SERVING` in the same breath, and **drain costs nothing** — readiness turns
false at the first moment of shutdown, before the drain delay, so both answers turn over together.

Only the empty service name is answered. Any other name is `NOT_FOUND`: per-service statuses are
not in v1. The streaming `Watch` RPC is not implemented, because `grpc-health-probe` and Kubernetes
both poll `Check`.

**Reflection mounts both handlers**, the current one and the older `v1alpha`, because `grpcurl`
still asks for `v1alpha`. You pass the generated `...ServiceName` constants, because connect keeps
no registry of what has been mounted. That is also why it is `MountOn` rather than a pair handed to
`Handle` the way `health.New` is: two mounts do not fit in one pair.

The package is called `reflection` and not `reflect`, so a `main` that imports the standard
library's `reflect` never has to alias one of them.

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

## Tracing

Tracing is a Starter of its own, `goboot/trace`, and you get none of it unless you import it. That
split is measured on the real thing, not estimated. The same HTTP service is **3 modules and
6,807,817 bytes** stripped without tracing and **26 modules and 16,498,953 bytes** with it: the
OTLP stack is **+9.69 MB**, the heaviest dependency in the project. Inside the Actuator, every
Actuator user would have paid it.

```yaml
trace:
  endpoint: "http://otel-collector:4317"  # empty means OTEL_EXPORTER_OTLP_ENDPOINT
  serviceName: "orders"                   # empty means OTEL_SERVICE_NAME
  sampleRatio: 0.1                        # zero means OTEL_TRACES_SAMPLER, which keeps everything
```

Every key is optional, and an empty key hands the choice back to the standard `OTEL_*` environment
variables, which is the interface an operator already knows. Note that `sampleRatio: 0` does **not**
mean "keep nothing" — it means "not set". To keep almost nothing, write `0.0001`.

`trace.New(cfg, app.Log)` returns a `TierObserve` Component. It starts first and stops last, and
`Stop` flushes: the batch processor holds spans in memory, and a process that exits without the
flush loses the last batch, which is the batch you were chasing.

### Use `trace.DefaultMiddleware`, not a second `Use` call

```go
srv.Use(trace.DefaultMiddleware(app.Log)...)
```

One word different from `web.DefaultMiddleware`, and it has to be one call rather than two.
`Use` **appends**, so the line anyone would write instead —

```go
srv.Use(web.DefaultMiddleware(app.Log)...)
srv.Use(trace.Middleware())               // wrong: this lands INNERMOST
```

— puts the span inside `Logging`, where the access-log line cannot carry the trace ID, because
`Logging` read the request context before the span existed. The spans still export, so nothing
looks broken; the log lines just never join up with them. The order that works is `RequestID`,
trace, `Logging`, `Recovery`, and `trace.DefaultMiddleware` returns exactly that. A test pins both
halves.

The slice has five entries, not four. The fifth is `trace.RouteSpanName` and it is **innermost**,
because that is the only place it works: `ServeMux` fills `r.Pattern` on the request handed to it,
and every middleware that calls `r.WithContext` — `web.Logging` does — passes down a copy, so
anything further out reads an empty pattern. Without it a span is named `GET`; with it, it is
`GET /hello/{name}`. The path is never used as a name: one span name per customer ID is a
cardinality bill, not a trace.

The access-log line carries `traceId` and `spanId` because `trace.DefaultMiddleware` hands
`web.Logging` a logger wrapped by `trace.WithIDs`. Both that and `trace.IsRPC` are exported so the
slice stays one you can rebuild by hand, not hidden behaviour.

### RPCs get one span, and no metrics

`goboot/trace/rpc` holds the connect instrumentation, opt-in by import the same way:

```go
opts, err := rpc.Options()
if err != nil {
	return err
}
srv.Handle(greetv1connect.NewGreetServiceHandler(svc, append(grpc.DefaultOptions(app.Log), opts...)...))
```

`goboot/trace` filters `otelhttp` for RPC requests whether or not you import this package. Mounted
together without the filter they give **two nested spans per RPC** — a redundant HTTP parent
wrapping the real one — and a test measures that rather than asserting it. The rule is exact, not a
guess at the path: the content type starts with `application/grpc`, **or** a
`Connect-Protocol-Version` header is present. That covers all four protocols connect speaks, and a
service with no gRPC never sees either header.

**v1 ships no RPC metrics**, and this is a known gap rather than an oversight. `otelconnect` can
emit them, but into the OTel pipeline, where `/actuator/metrics` — which reads the Prometheus
registry — cannot see them. go-boot runs two pipelines on purpose, Prometheus for metrics and OTel
for traces, and half a metric surface visible only to whoever runs the collector is worse than
none. So there is no RPC count and no RPC latency by procedure. Spans carry the duration and the
status code, so the data is there per request; the aggregate is what is missing.

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

`dbtest.StartDSN` returns the connection string alongside the pool, for a test that drives a whole
`main` rather than a query — `examples/full` uses it to point the service's own `db.dsn` at a real
database.

It is safe to call from parallel tests. The first run downloads about 14 MB of PostgreSQL binaries
and extracts them to roughly 71 MB under your user cache directory — measured on linux/amd64 with
PostgreSQL 18.3. Set `GOBOOT_PG_BINARIES` to a pre-seeded directory to run air-gapped. It is a separate package because it is heavy and because it links a driver, which
`goboot/db` refuses to.

## The Preset

`preset.Full` wires a whole service in one call: the App, the database pool, the Actuator, the HTTP
Server and the default middleware. `traced.Full` is the same with tracing.

```go
var cfg config
if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
	return err
}
app, err := traced.Full(cfg.Config, migrations())
if err != nil {
	return err
}

svc := &greeter{db: app.DB, greeting: cfg.Greeting}
app.Web.Handle("GET /hello/{name}", httpGreet(svc))
app.Web.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))

return app.Run(ctx)
```

**The reason to use it is the upgrade path, not the size of the diff.** Wiring held in a Preset gets
fixed by `go get -u`; wiring held in your own `main` does not. If go-boot later learns that a fourth
middleware belongs in the default set, every Preset user picks it up by bumping a version and
nobody else does. That is the whole argument, and it is why go-boot promises *one call* rather than
*one line*.

**A Preset takes no options.** No flags, no negation config keys, no middle setting. The returned
struct lets you **add** — `app.Add(consumer)`, `app.Web.Handle`, `app.Web.Use` — because the
`*goboot.App` is embedded. It does not let you remove or reorder. To do either, you copy the body of
`Full` into your own `main` and edit it.

**And copying the body costs you the upgrade path**, which was the only argument for the Preset in
the first place. A user who copies has chosen to own their wiring, exactly as if they had never used
a Preset. That is the trade and it is not softened here.

Because copying is the only escape hatch, the copy ships as compiling code rather than as a snippet
in this file. `examples/full/main.go` is the Preset form above; `examples/full/explicit.go` is
exactly what `traced.Full` expands to. CI builds both, and one test drives both and asserts they
serve the same service.

**`goboot.Load` stays in `main`.** Every real service owns at least one config key of its own, so a
Preset that loaded config for you would break on the first one. Your struct embeds the Preset's
config inline and adds its own keys beside it:

```go
type config struct {
	traced.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}
```

**`grpc.DefaultOptions(app.Log)` stays in `main` too, in both forms**, because the mount names your
own generated package and a Preset can never see it. So **the Preset does not protect you from
forgetting the error-sanitising interceptor**: leave those options off and a bare `error` reaches
the caller verbatim, password and all. The same is true of the `_ "github.com/jackc/pgx/v5/stdlib"`
blank import — you bring your own driver in both forms.

### Two packages, because Go links by import

`traced.Full` lives in `goboot/preset/traced` and is a **copy** of `preset.Full`, not a wrapper
around it. Two reasons, both hard:

- A `preset.WithTracing()` option could not have worked. Naming `goboot/trace` inside
  `goboot/preset` puts OTel in every Preset user's binary whether the option is set or not — +9.69
  MB stripped and +23 modules. A test asserts that a build importing `goboot/preset` links no
  tracing.
- It could not have wrapped either. `Use` appends, so adding the trace middleware after
  `preset.Full` has returned puts the span **inside** `Logging`, where the access-log line cannot
  carry the trace ID. The order that works is RequestID, trace, Logging, Recovery, and only one
  `Use` call produces it.

So there are two near-identical bodies, deliberately.

### There is no other Preset

`preset.Full` is the only one in v1. There is no `preset.HTTP` and no `preset.Web`: at one Component
a Preset came out **longer** than the wiring it replaced, which is what `examples/http-only` and
`examples/http-actuator-config` show. And there is no gRPC variant, because `goboot/grpc` has no
server and no Component of its own — a gRPC service and an HTTP service wire identically.

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
