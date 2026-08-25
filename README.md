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
middleware set with the response helpers below; and the Actuator. The rest of the v1 surface —
gRPC, the database Starter, tracing and the Presets — is being built ticket by ticket against
`docs/spec.md`.

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

## Go version

The floor is **Go 1.25.0**.

It will rise. Once the database Starter lands, go-boot depends on `goose/v3`, which declares a
patch-level `go` directive. The highest floor in a module wins, so `go mod tidy` will write
**`go 1.25.7`** into `go.mod`, and Go 1.25.0 through 1.25.6 will no longer build go-boot at all —
even for a user who never imports the database Starter.

A stock Go 1.22 toolchain handles this on its own: it downloads the newer toolchain in about
fifteen seconds. But that needs the toolchain switch and the module proxy to be working, so
`GOTOOLCHAIN=local`, `GOPROXY=off` and any Go below 1.21 are unsupported.

## Documentation

- `docs/spec.md` — the locked v1 spec, and the design authority for every ticket.
- `CONTEXT.md` — the words go-boot uses, and what each one means.
- `docs/adr/` — the decisions, one file each.

## Licence

See `LICENSE`.
