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
real PostgreSQL for tests; the gRPC Transport; tracing; the Security Starter; and the Presets. That is the whole library
surface `docs/spec.md` locks, and CI keeps the import-leak check on it. The Scaffold has landed
too: `goboot new` writes a project, and [Start a new service](#start-a-new-service) is the command.

## Install

```
go get github.com/squall-chua/go-boot
```

## Ten minutes to a running service

Three stops, in order. Each one is a directory in this repository you can run right now, and every
Go block below is lifted **verbatim** from the file CI compiles. A test fails if a block drifts from
its file, and it fails again if a Go block appears here without one. Nothing on this path goes
stale on you.

### 1. The smallest service — two minutes

One Transport, the default middleware, nothing else. The routes are in
`routes.go` and the one feature is a package under `internal/`, which is the layout the Scaffold
writes — `run` names no feature and never grows.

<!-- from: examples/http-only/main.go -->
```go
func run(ctx context.Context) error {
	app, err := goboot.New(goboot.Config{})
	if err != nil {
		return err
	}
	srv, err := web.New(web.Config{Addr: ":8080"}, app.Log)
	if err != nil {
		return err
	}
	srv.Use(web.DefaultMiddleware(app.Log)...)
	app.Add(srv)

	addRoutes(srv)

	return app.Run(ctx)
}
```

```
go run ./examples/http-only
curl localhost:8080/hello/world
```

`hello` in the same file is an ordinary `http.HandlerFunc`. go-boot has no handler type of its own,
so everything written for `net/http` works here unchanged.

`app.Add` ignores the order you write. Each Component declares its own Tier, and go-boot starts
from the lowest Tier to the highest and stops in reverse. So wiring in the wrong order is not a
mistake you can make.

### 2. Add the Actuator and config — four minutes

The realistic default: the same Transport, plus the operational endpoints and the service's own
config key beside go-boot's.

<!-- from: examples/http-actuator-config/main.go -->
```go
func run(ctx context.Context) error {
	cfg := config{Greeting: "hello"} // the struct pre-fill IS the defaults layer
	if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
		return err
	}
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		return err
	}
	act, err := actuator.New(cfg.Actuator, app)
	if err != nil {
		return err
	}
	srv, err := web.New(cfg.Web, app.Log)
	if err != nil {
		return err
	}
	srv.Use(web.DefaultMiddleware(app.Log)...)
	act.MountOn(srv)
	app.Add(act, srv)

	addRoutes(srv, cfg)

	return app.Run(ctx)
}
```

```
go run ./examples/http-actuator-config
curl localhost:8080/readyz
curl localhost:8080/actuator/metrics
curl -X PUT localhost:8080/actuator/loglevel -d '{"level":"DEBUG"}'
```

**Metrics answer 404 until you name them.** The `actuator.expose` list in
`examples/http-actuator-config/app.yaml` is a whitelist. Drop `metrics` from it and the endpoint is
never registered at all. This is the one thing that surprises people, so it is said here rather
than further down.

### 3. The whole surface — four minutes

HTTP, gRPC, database, Actuator and tracing, wired by one call to a Preset.

<!-- from: examples/full/main.go -->
```go
func run(ctx context.Context) error {
	var cfg config
	if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
		return err
	}
	app, err := traced.Full(cfg.Config, migrations())
	if err != nil {
		return err
	}

	addRoutes(app.Web, app.DB, app.Log, cfg)

	return app.Run(ctx)
}
```

`addRoutes` is `routes.go`, and both forms call it with the same four arguments — so the two forms
differ in wiring and in nothing else. The feature behind it is `internal/greeting/`: the `Repository`
interface beside the Service Layer that uses it, `entity/` for the Entity and its SQL, `rest/` for
HTTP and `rpc/` for gRPC.

This stop needs a PostgreSQL, because it runs migrations and opens a pool. `examples/full/app.yaml`
points `db.dsn` at a throwaway one on `localhost:5432`; send a real password in `ORDERS_DB__DSN`
instead, which is the layer that wins.

```
go run ./examples/full            # the Preset form, main.go
go run ./examples/full explicit   # the same service wired by hand, explicit.go
```

`explicit.go` is exactly what `traced.Full` expands to. Copying that body is the only way to change
what a Preset wires, so the copy ships as compiling code and one test drives both forms.

**`maxOpenConns` defaults to 10.** That is ten pods against a stock PostgreSQL, which allows about
97 connections. Read the pool defaults below before you run more than ten.

### Where to go next

Everything below is reference, read as you need it: the Scaffold, the default middleware, security,
the Actuator, gRPC, tracing, the database and the Preset. Most of those sections quote fragments and single lines
rather than whole wiring, and a fragment has no file it can be lifted from whole, so it is not
checked. Where a block *is* a whole excerpt it carries a `<!-- from: ... -->` comment, and CI
checks it against that file exactly as it does on the path above. The marker is the whole rule:
marked is checked, unmarked is not.

## Start a new service

The three stops above are directories in this repository. To get the same thing in a directory of
your own, run the Scaffold:

```
go run github.com/squall-chua/go-boot/cmd/goboot@latest new github.com/acme/orders
cd orders
go mod tidy
go run . migrate   # needs the PostgreSQL named in app.yaml
go run .
```

It writes thirteen files: a `main.go` with one call to `preset.Full` and the `orders migrate`
subcommand, a `routes.go` listing the features, and `internal/greeting/` — one feature as a domain
package (the `Repository` interface beside the Service Layer that uses it, and a test driving that
Service against a fake Repository) plus one sub-package per adapter: `entity/` for the Entity and
the SQL that loads it, `rest/` for HTTP. Then an `internal/transport/` adapter and its test, an `app.yaml`, one
goose migration, a `README.md` and a `go.mod`. Feature two is a sibling of `internal/greeting/` and
two more lines in `addRoutes`, so `serve()` never grows.

Those three patterns are the **application's**, not go-boot's. go-boot hands back a plain `*sql.DB`
and defines no Repository or Entity ([ADR 0009](docs/adr/0009-no-repository-abstraction.md)), and
its Transport takes a plain `http.Handler` and defines no handler signature
([ADR 0004](docs/adr/0004-http-handler-boundary.md)). Keeping both out of the library is exactly
what lets the generated project put `sqlc`, `ent` or `gorm` behind that interface, and keep every
`net/http` middleware working unchanged. Why a Scaffold may write what the library refuses is
[ADR 0015](docs/adr/0015-the-scaffold-writes-patterns-go-boot-refuses.md). Add
`-grpc` and it writes four more — `buf.yaml`, `buf.gen.yaml`, a sample `.proto` and an `rpc/`
package holding the adapter type and its own mount, a sibling of `rest/` — and then `buf generate`
has to run before the project compiles.

**One flag, and that is on purpose.** A flag exists only where it changes which *files* are
written. Everything else you might want different — tracing, the Actuator whitelist, the database —
is lines in a `main.go` you own from the first second, and deleting lines needs no flag.

**It copies a project, not a template.** The two it copies live in `cmd/goboot/scaffold`, they are
`package main` packages this repository compiles and vets, and CI would fail if either stopped
building. So what you get is code CI builds. See
[`docs/spec.md` 15](docs/spec.md#15-the-scaffold-cli).

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

### Counting and timing HTTP requests

A fourth middleware, from `goboot/web/metrics`, opt-in by import and deliberately **not** in the
default set — nobody links Prometheus who did not ask for it. It answers "how many requests to
`GET /orders/{id}` failed, and how slow are they" from the endpoint that already answers everything
else. It is a separate package because `goboot/web` links **2 modules** — no package here links
fewer — and every HTTP user imports it, so naming Prometheus there would charge all of them for a
counter most never scrape:

```go
srv.Use(append(web.DefaultMiddleware(app.Log), metrics.Middleware)...)
```

If you trace, append to the other default set instead — `trace.DefaultMiddleware` is five entries
and a second `Use` call cannot reorder what the first added:

<!-- from: examples/full/explicit.go -->
```go
srv.Use(append(trace.DefaultMiddleware(app.Log), metrics.Middleware)...)
```

Two metrics, both labelled `route` and `status`, both registered on the Prometheus default registry,
so both appear at `/actuator/metrics` once `metrics` is named in `actuator.expose`:

| Metric                          | Type      | What it answers                       |
| ------------------------------- | --------- | ------------------------------------- |
| `http_requests_total`           | counter   | How many, and how many of them failed |
| `http_request_duration_seconds` | histogram | How slow, at a quantile               |

**`route` is `r.Pattern`, never the path.** `GET /orders/1` and `GET /orders/2` are two requests on
the one series `GET /orders/{id}`. A metric labelled by path is an unbounded label set and a
Prometheus outage. A request that matched no pattern gets an empty `route`, so a scanner walking
`/wp-admin` and `/.env` lands on one series rather than inventing one each. There is no `method`
label: the route already carries the method, and on an unrouted request the method is the one thing
a caller could still make up.

**A panicking handler counts as `500`.** `Use` appends, so this middleware lands inside `Recovery`
and a panic would otherwise unwind straight past the counter. It records in a `defer`, so it is
right whether you append it or splice it in ahead of `Recovery`. If the handler had already written
part of a response before it panicked, the label is the status the client actually got, not 500 —
`Recovery` cannot take a sent status line back, so a 500 there would disagree with the access log
for the same request. `http.ErrAbortHandler` is passed through uncounted, the same as everywhere
else.

**Append it — do not splice it above `Logging`.** `Logging` hands everything below it a new request,
and `ServeMux` fills the route in place on the one it routed, so a middleware above `Logging` reads
an empty `route` and every route lands on the series meant for requests that matched nothing. The
line above is safe because `Use` appends.

**Probe paths ARE counted**, unlike the access log. The log skips them because 17,000 lines a day is
volume; a metric has no volume, a probe is one more series. Exclude them in PromQL if you do not
want them — nothing recovers a measurement that was never taken, and `/readyz` latency is the time
your readiness Checks take. Two things follow: the scrape of `/actuator/metrics` counts itself, and
setting `actuator.addr` moves the Actuator to its own listener that this middleware never wraps, so
there the probe endpoints are not counted at all.

**No Preset wires it.** A Preset takes no options, and `goboot/preset` is forbidden from reaching
this package by the import-leak check, so the line above goes in `main`. A Preset user who wants
these metrics copies the body of `Full`.

Errors on the wire are RFC 7807 documents from `web.WriteProblem`, so a panic and a hand-written
400 come out in the same shape. `web.DecodeJSON` reads a request body with the size cap, unknown
field rejection and readable errors that `json.NewDecoder(r.Body).Decode` leaves to you.

## Security

`goboot/security` is opt-in by import: security headers, CORS, a JWT bearer middleware over a JWKS
key set, and per-route scope checks. A service that does not import it links none of it.

```yaml
security:
  headers:
    hstsMaxAge: 4320h # 180 days. Go durations have no "d" unit
  cors:
    allowedOrigins: [https://app.example.com]
  jwt:
    issuer: https://auth.example.com/
    audience: orders-api
    jwksUrl: https://auth.example.com/.well-known/jwks.json
    # ...or jwksFile: /etc/orders/jwks.json
    # ...or publicKeyFile: /etc/orders/issuer.pem
```

<!-- from: examples/http-secure/main.go -->
```go
	sec, err := security.DefaultMiddleware(cfg.Security)
	if err != nil {
		return err
	}
	// APPEND. Use appends, so the first entry listed ends up outermost: this
	// puts the security middleware inside web.Recovery, where a panic in it
	// still becomes a 500 rather than an EOF.
	srv.Use(append(web.DefaultMiddleware(app.Log), sec...)...)
```

Each feature mounts its own routes, so the wrapper ends up beside the handler it protects. An open
route has none, and needs no token:

<!-- from: examples/http-secure/internal/hello/rest/rest.go -->
```go
func Routes(srv *web.Server, s *hello.Service) {
	srv.Handle("GET /hello/{name}", transport.Handle(bindHello, sayHello(s)))
}
```

A guarded one carries it at the mount, because nothing else can see whether it is missing:

<!-- from: examples/http-secure/internal/orders/rest/rest.go -->
```go
func Routes(srv *web.Server, s *orders.Service) {
	srv.Handle("POST /orders", security.RequireScope("orders:write")(create(s)))
}
```

`DefaultMiddleware` is a slice you can print and edit. `Headers` is always in it; `CORS` joins once
`allowedOrigins` names something, and `Authenticate` once the `jwt` section is filled in. So a
service that wants headers only writes no `security` config at all and still gets them.

A section that is only **half** filled in is a startup error, not a skip — a misspelt key must never
leave a service quietly unauthenticated. Every one of those errors comes from `Headers`, `CORS` or
`Authenticate` rather than from `DefaultMiddleware`, so wiring the three by hand gets you the same
answers.

### Authentication is not a global gate

`Authenticate` verifies a bearer token **when one is there** and puts a `Principal` in the request
context. It does not reject a request that carried no token. Rejecting is `RequireScope`'s job, at
the mount.

That is not a preference. `/livez`, `/readyz` and `/actuator/*` share this listener, so a middleware
demanding a token on every request would either lock Kubernetes out of its own probes or grow a path
allowlist — a security decision written in a config file that no compiler checks. The wrapper goes
next to the handler it protects instead, in Go.

**The trap that leaves is real, and go-boot cannot catch it: a route nobody wrapped is a route with
no authorization.** What go-boot can do is keep the wrapper short enough that its absence shows up
in review.

A token that is present and **bad** is a 401 straight away, even on a route no `RequireScope` wraps,
with `WWW-Authenticate: Bearer error="invalid_token"` and an RFC 7807 body. The reason it failed
goes to the request logger at WARN, carrying the same request ID as the access line; the caller is
told none of it, and the token itself is never logged.

Read the Principal in a handler:

<!-- from: examples/http-secure/internal/orders/rest/rest.go -->
```go
		p, ok := security.PrincipalFrom(r.Context())
		if !ok {
			web.WriteProblem(w, http.StatusUnauthorized, "authentication required")
			return
		}
```

`Principal` carries `Subject`, `Issuer`, `Scopes` and the whole claim map. `Scopes` reads `scope`
(one space-separated string) and `scp` (a string or an array), because issuers disagree. Roles have
no helper, because no claim name for them is standard either: Keycloak writes `realm_access.roles`
and Azure writes `roles`, so `Claims` holds the payload and the three lines are yours.

`RequireScope()` and `RequireAnyScope()` with **no** arguments both mean "just be authenticated".

### Keys, and what is checked

**Three key sources, all asymmetric, and you name exactly one.**

| Key | What it is | Rotation |
| --- | --- | --- |
| `jwksUrl` | your issuer's key set, over `https` | an unknown `kid` re-fetches |
| `jwksFile` | the same JWKS document on disk | an unknown `kid` re-reads the file |
| `publicKeyFile` | one PEM public key, or a certificate carrying one | none — a change needs a restart |

Reach for `jwksUrl` unless you cannot. The other two are for a service that may make no outbound
request, or that was handed a key rather than an endpoint. Both file sources are read **at startup**,
so a wrong path fails the boot instead of every request an hour later.

**There is no `hmacSecret`,** and `publicKeyFile` refuses anything that is not an RSA or ECDSA public
key — including a private key file, which is the one most likely to be pointed at by mistake. A
shared secret is the alg-confusion hole in the shape that keeps being rediscovered.

`jwksUrl` is fetched **lazily, on the first token** — fetching at startup would turn an auth-server
outage into a service that will not boot. The trade that buys is worth knowing: once the key set is
cached, an issuer outage costs you nothing, but a **cold start during one refuses every token**. If
that is unacceptable, use `jwksFile`. Rotation needs no timer and no goroutine: a token with a `kid`
the cache does not hold triggers one refetch, and no more than one every ten seconds, so junk `kid`s
cannot be turned into traffic against your issuer.

`issuer` and `audience` are **required**, and the constructor says which one is missing. The
audience check is the one worth defending: a resource server that skips it accepts every token the
issuer minted, including tokens meant for a different client of the same issuer. **This is stricter
than Spring**, which validates `aud` only if you ask it to — that default is a well-known footgun,
and sane defaults are the point of go-boot.

`audience` is a **list**, and any one entry satisfies the claim, so a service can answer to two
names at once while it is being renamed. `aud` is read as a string or as an array; a token carrying
no `aud`, or an empty array, is refused.

> **If every request is a 401 and the log says `token has invalid audience`,** look at your issuer
> first. Keycloak's default realm does not put a resource-specific value in `aud` until you add an
> audience mapper to the client.

`exp` is required on the token too, and the algorithms are a fixed allowlist — `RS*`, `PS*`, `ES*`,
all asymmetric, with no config key that could add `HS256` or `none`.

**`jwksUrl` must be `https`.** A loopback host is the one exception, so a local issuer and your own
tests still work. Everywhere else plain `http` is refused at startup, and it is worth knowing why it
is refused rather than merely discouraged: the key set *is* what your service trusts, so anyone who
can rewrite that response picks the keys you believe and can mint any identity they like. It is a
total bypass that looks like nothing in a config file.

A token that carries no `kid` is checked against every key in the set, and the signature decides.

### Headers and CORS

Four headers, and `X-Frame-Options` is not one of them: `frame-ancestors 'none'` in the CSP is what
current browsers read, and on a JSON API the older header does nothing.

| Header | Value |
| --- | --- |
| `X-Content-Type-Options` | `nosniff` |
| `Content-Security-Policy` | `default-src 'none'; frame-ancestors 'none'` |
| `Referrer-Policy` | `no-referrer` |
| `Strict-Transport-Security` | `max-age=<n>`, only when `hstsMaxAge` is set |

**HSTS is off until you configure it**, and that default is deliberate: sent over plain HTTP on your
machine, it pins `localhost` to HTTPS in that browser for the whole `max-age`, and undoing it means
a trip into browser internals.

**`X-Request-Id` is always readable cross-origin.** A browser can only read seven response headers
by default, and that is not one of them, so go-boot names it in `Access-Control-Expose-Headers` for
you — otherwise the request id it puts on every response would be invisible to exactly the callers
you turned CORS on for. `exposedHeaders` **adds** to it rather than replacing it.

CORS **refuses the dangerous mistake at startup**: `allowedOrigins: ["*"]` together with
`allowCredentials: true` is an error rather than a note in this file. Origins are matched exactly —
there is no pattern syntax, because a wildcard in the middle of an origin is how
`app.example.com.evil.test` gets matched by a rule meant for `app.example.com`. `Vary: Origin` is on
every response, allowed or not, so a shared cache cannot serve one origin's answer to another.

**What it costs.** One module, `github.com/golang-jwt/jwt/v5`, with no transitive dependencies of its
own. Wiring the whole of `DefaultMiddleware` plus one guarded route into a plain HTTP-only service
takes that binary from 6,807,817 to 7,516,425 bytes stripped: **+708,608 bytes**, nearly all of it
stdlib crypto that stops being dead code once something verifies a signature.

### Guarding the Actuator

`act.MountOn(srv)` registers `/actuator/*` on the shared listener, so on that shape
`PUT /actuator/loglevel` is open to anyone who can reach the port. `actuator.Handler` is a
**one-method interface**, so you can pass something else and guard the endpoints on the way past:

<!-- from: examples/http-secure/main.go -->
```go
type operators struct {
	srv *web.Server
	mw  web.Middleware
}

// Handle guards everything except liveness and readiness. Kubernetes carries
// no bearer token, so guarding those two would fail every probe and the pod
// would never go ready — the same reason authentication is not a global gate.
func (o operators) Handle(pattern string, h http.Handler) {
	if isProbe(pattern) {
		o.srv.Handle(pattern, h)
		return
	}
	o.srv.Handle(pattern, o.mw(h))
}
```

Liveness and readiness stay open, and that is not optional: Kubernetes carries no bearer token, so
guarding those two means the pod never goes ready. `examples/http-secure` is the whole file, and its
test drives exactly that — probes open, `/actuator/loglevel` 401 without a token and 200 with the
scope.

The other answer is `actuator.addr`, which moves the Actuator to its own private listener that
nothing routes to from outside. If you can run one, prefer it: a port nobody can reach needs no
scope check.

**No Preset wires it**, because a Preset takes no options and every field of the `jwt` section is a
value only your service knows. The wiring above goes in `main`, and `examples/http-secure` is that
`main` in full — CI builds it and a test drives it, so these blocks cannot rot.

## The Actuator

**Metrics answer 404 until you name them.** `actuator.expose` is a whitelist and it defaults to
`livez, readyz, info`. An endpoint not on the list is never registered, so a wrong Ingress rule
has nothing to leak. This is the one thing that surprises people, so it is said first.

```go
act, err := actuator.New(cfg.Actuator, app)   // an expose typo is refused here
if err != nil {
	return err
}
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

<!-- from: examples/full/internal/greeting/rpc/rpc.go -->
```go
	srv.Handle(greetv1connect.NewGreetServiceHandler(&server{svc: s}, grpc.DefaultOptions(log)...))
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

<!-- from: examples/full/internal/greeting/rpc/rpc.go -->
```go
type server struct{ svc *greeting.Service }

func (g *server) Greet(ctx context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err // bare: the sanitiser owns what the caller sees
	}
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: out}), nil
}
```

The Service Layer stays free of connect, and both Transports call the same `greeting.Service`.

**Return the error bare.** `connect.NewError(connect.CodeInternal, err)` looks tidier and it is the
leak: it makes `err`'s own text the message your caller receives, and the sanitiser below passes a
`*connect.Error` through untouched by design. To tell a caller something useful, write the words
yourself — `connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))`.

### The default options

```go
grpc.DefaultOptions(app.Log)  // a slice you can edit, like web.DefaultMiddleware
```

Three entries: panic recovery, the error sanitiser, and connect's required protocol header.

**The sanitiser is mandatory, not a nicety.** A bare `error` returned from a connect handler reaches
the caller **verbatim**. Measured: `pq: password authentication failed for user "app" at
10.0.0.5:5432` went out on the wire, host and username and all. The sanitiser replaces anything
that is not already a `*connect.Error` with a bare `CodeUnknown` and logs the real one. An error you
built yourself with `connect.NewError` passes through untouched, which is what lets a handler send
a caller a useful message — and is also why wrapping an error from below in one defeats the whole
thing. The same rule holds on the HTTP side: `WriteProblem`'s `detail` is a string you wrote, never
`err.Error()`.

There is **no logging or request-ID interceptor**, and that is not an omission. Under the shared
listener `web.DefaultMiddleware` has already run, so `goboot.LoggerFrom(ctx)` and the request ID
reach your connect handler free.

One honest asymmetry with `web.Use`: connect options are **per service**, not per server, so you
repeat them at every mount. connect has no global registry and there is no way around it.

### A failed gRPC call in the access log

The gRPC and gRPC-Web status rides in **trailers**, not in the HTTP status line, so the access log
line for a failed RPC still says `"status":200` — that is what went on the wire. It also carries
`"rpcCode":"2"` at level `ERROR`, because `web.Logging` reads the trailer the handler left behind:

```json
{"level":"ERROR","msg":"request","path":"/greet.v1.GreetService/Greet","status":200,"rpcCode":"2","requestId":"9f2c..."}
```

So `rpcCode` is what you grep the access log for. It is the gRPC code as a **number**, because
`goboot/web` links no connect-go and cannot name it.

**The sanitiser's `rpc failed` line is still where the detail lives.** It carries the procedure, the
code by name and the real error, tagged with the same `requestId` as the access line.

**Two cases are missed**, and both are written down in `docs/spec.md` §9. They are the failures
connect puts in the response **body**, where no HTTP middleware can reach them: a gRPC-Web call that
fails *after* its first message, and a Connect-protocol **stream**. Plain gRPC is covered however
late the failure comes, a gRPC-Web unary failure is covered, and a Connect unary failure never
needed this — it maps its code onto the HTTP status line.

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

### Counting and timing RPCs

A third package, `goboot/grpc/metrics`, opt-in by import like the two above. It answers "how many
of my RPCs failed, and how slow are they" from the endpoint that already answers everything else:

```go
srv.Handle(greetv1connect.NewGreetServiceHandler(&server{svc: s},
	append(grpc.DefaultOptions(log), metrics.Options()...)...))
```

Two metrics, both labelled `procedure` and `code`, both registered on the Prometheus default
registry, so both appear at `/actuator/metrics` once `metrics` is named in `actuator.expose`:

| Metric                 | Type      | What it answers                       |
| ---------------------- | --------- | ------------------------------------- |
| `rpc_requests_total`   | counter   | How many, and how many of them failed |
| `rpc_duration_seconds` | histogram | How slow, at a quantile               |

**`code` is the code the caller received**, so `ok`, `not_found`, `unknown` and the rest. A bare
`error` from a handler counts as `unknown`, because that is what the sanitiser sends. A panicking
handler counts as `internal`, and is counted at all only because the interceptor records in a
`defer`: `connect.WithRecover` wraps it, so a panic would otherwise unwind straight past the
counter and the failure nobody wants to miss would be the one failure not recorded.

The exception is `http.ErrAbortHandler`, which is a handler saying "drop this connection quietly".
It is passed through uncounted, because that is what every other layer does with it — the access
log writes no line for it either.

**This is the only metrics pipeline.** Prometheus owns every metric go-boot emits and OTel owns
traces, so an operator asking one question looks in one place. This package needs neither tracing
nor a collector.

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

<!-- from: preset/traced/traced.go -->
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

### RPCs get one span

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

**Metrics do not come from here.** `otelconnect` can emit them, but into the OTel pipeline, where
`/actuator/metrics` — which reads the Prometheus registry — cannot see them. go-boot runs two
pipelines on purpose, Prometheus for metrics and OTel for traces, so this package passes
`otelconnect.WithoutMetrics()` and the RPC counter and histogram live in `goboot/grpc/metrics`
instead, on the registry the Actuator serves. That package needs no tracing and no collector: see
[Counting and timing RPCs](#counting-and-timing-rpcs).

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

The call is [stop 3 of the ten-minute path](#3-the-whole-surface--four-minutes) above, and it is not
repeated here. That block is checked against `examples/full/main.go`; a second copy on this page
would not be, and an unchecked copy of wiring is what this section spends the rest of its words
warning about.

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
in this file. `examples/full/main.go` is the Preset form of stop 3; `examples/full/explicit.go` is
exactly what `traced.Full` expands to. CI builds both, and one test drives both and asserts they
serve the same service.

**`goboot.Load` stays in `main`.** Every real service owns at least one config key of its own, so a
Preset that loaded config for you would break on the first one. Your struct embeds the Preset's
config inline and adds its own keys beside it:

<!-- from: examples/full/main.go -->
```go
type config struct {
	traced.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}
```

**`grpc.DefaultOptions` stays in your own code too, in both forms** — `examples/full` keeps it in
`internal/greeting/rpc`, beside the mount — because the mount names your own generated package and a
Preset can never see it. So **the Preset does not protect you from
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
