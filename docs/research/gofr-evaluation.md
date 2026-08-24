# GoFr evaluation — build go-boot anyway, or adopt?

Research for [#17](https://github.com/squall-chua/go-boot/issues/17). Investigated 2026-08-24.

[#3](https://github.com/squall-chua/go-boot/issues/3) found GoFr late, said it *"already does most of
this job"*, and was blunt that rejecting it would be **preference, not an unmet need**. This ticket
tests that claim by building real code and measuring, rather than counting README bullets.

Every claim below carries a source URL or the exact command that produced it. All measurements are
on this machine, `go1.26.3 linux/amd64`, against **`gofr.dev v1.59.0`** (the current release —
`curl -s https://proxy.golang.org/gofr.dev/@latest`), in a scratch module outside this repo.
Anything not verified is marked **UNVERIFIED**.

---

## 0. The answer in one page

**Build. The measurement upgrades the objection from preference to engineering — but not by as
much as the map would like, and the honest competitor to go-boot is not GoFr.**

The three headline numbers, for the *same* job (one JSON route, structured logging, health,
metrics, tracing, graceful shutdown):

| | GoFr | go-boot's own settled dep set | pure stdlib |
| --- | --- | --- | --- |
| stripped binary | **39.7 MiB** | **14.8 MiB** | **6.0 MiB** |
| modules compiled into the binary | **96** | **28** | **0** |
| cold `go build` download | **202.6 MiB** / 95 zips | **22.6 MiB** / 28 zips | **0 B** |
| `go mod graph` edges | **1506** | **273** | **2** |

And the ticket's specific open question — *"GCP Pub/Sub and GraphQL in `go.mod` may or may not
reach a minimal binary. Find out which"* — is settled: **they do.** So do Kafka, MQTT, SQLite,
MySQL, Postgres, Dgraph, Redis, and three test libraries (§3.3).

The three capabilities [#3](https://github.com/squall-chua/go-boot/issues/3) found missing from
every framework: **GoFr has none of them.** Ordered Component lifecycle — no. Runtime log-level
endpoint — no (inverted, pull-based). Build info — no, partial at best (§2).

**But** the same measurement cuts against go-boot too. The pure-stdlib column above is a **78-line
`main.go`** that has all three missing capabilities and zero dependencies
(`scratchpad/measure/stdlibsvc/main.go`, §7). That file, not GoFr, is the thing go-boot has to beat.

The one-sentence reason for [#15](https://github.com/squall-chua/go-boot/issues/15) to open with:

> **go-boot gives a Go service the operational surface GoFr gives it — health, readiness, metrics,
> tracing, runtime log level, build info, ordered lifecycle, graceful shutdown — for 28 compiled
> modules and a 15 MiB binary instead of 96 and 40 MiB, and it never appears in your handler
> signature.**

---

## 1. GoFr's real API — the `main.go` a developer writes

Source read: [`pkg/gofr/gofr.go`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/gofr.go),
[`run.go`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/run.go),
[`health.go`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/health.go),
[`examples/http-server/main.go`](https://github.com/gofr-dev/gofr/blob/development/examples/http-server/main.go).
Built and run locally.

This is the whole file. It compiles, runs, and was curled (§2.4):

```go
package main

import (
	"fmt"

	"gofr.dev/pkg/gofr"
)

type greetResponse struct {
	Message string `json:"message"`
}

func main() {
	a := gofr.New()

	a.GET("/greet", func(c *gofr.Context) (any, error) {
		name := c.Param("name")
		if name == "" {
			name = "world"
		}
		c.Logger.Debugf("greeting %s", name)
		return greetResponse{Message: fmt.Sprintf("hello, %s", name)}, nil
	})

	a.OnStart(func(c *gofr.Context) error {
		c.Logger.Info("warming up")
		return nil
	})

	a.Run()
}
```

**31 lines, and it is genuinely impressive.** `gofr.New()` alone gives you: config loading, JSON
structured logging with trace/span IDs on every request line, a request-log middleware, OTel
tracing, a Prometheus metrics server on its own port with pprof, two health endpoints, and graceful
shutdown with a configurable grace period. No wiring. Observed startup log:

```
{"level":"INFO",...,"message":"Loaded config from file: ./configs/.env","gofrVersion":"v1.59.0"}
{"level":"INFO",...,"message":"warming up","gofrVersion":"v1.59.0"}
{"level":"INFO",...,"message":"Registered HTTP server on port: 9111",...}
{"level":"INFO",...,"message":"Starting metrics server on port: 9112",...}
```

**How it is shaped.** The core type is `App`
([`gofr.go:35`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/gofr.go)), which holds a
private `*container.Container`. `Container` holds every datasource GoFr supports as a field —
`SQL`, `Redis`, `Mongo`, `Cassandra`, `Clickhouse`, `PubSub`, … — and reaches your handler as an
embedded field of `*gofr.Context`. So `c.SQL`, `c.Redis` are always in scope whether or not you use
them. That is the design decision that produces the weight in §3: the container is one struct, not
a plugin registry, so every driver is linked unconditionally.

- **Config**: `.env` files only, via
  [`joho/godotenv`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/config/godotenv.go).
  Reads `./configs/.env` then overlays `./configs/.$APP_ENV.env`. No YAML, no JSON, no flags.
- **Logging**: GoFr's own logger interface, **not `log/slog`**
  ([`pkg/gofr/logging`](https://github.com/gofr-dev/gofr/tree/development/pkg/gofr/logging)).
- **HTTP**: `gorilla/mux` under the hood; handler signature is
  `func(*gofr.Context) (any, error)`, with the return value wrapped as `{"data": …}`.
- **gRPC**: `google.golang.org/grpc` directly, plus `grpc-ecosystem/go-grpc-middleware/v2`
  ([`grpc.go`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/grpc.go)). Not
  `connect-go`.
- **Metrics**: Prometheus on `METRICS_PORT` (default 2121) at `/metrics`, plus `/debug/pprof/`.
- **Tracing**: OTel, exporters for OTLP gRPC/HTTP and Zipkin, all compiled in.
- **Migrations**: `app.Migrate(map[int64]migration.Migrate{…})`, run **in-process at startup**.
- **Telemetry (note this)**: `gofr.New()` **POSTs to `https://gofr.dev/api/ping/up` on startup and
  `/api/ping/down` on shutdown, by default.** Verified in
  [`telemetry.go`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/telemetry.go) and
  [`constants.go:11-15`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/constants.go)
  (`defaultTelemetry = "true"`). Disable with `GOFR_TELEMETRY=false`. It is announced in the log and
  it is one env var to turn off — but it is default-on outbound network traffic from a library.

**Does it feel like the framework the map wanted?** Partly. The Actuator convention is exactly
right and better executed than the map's plan. The `main.go` is shorter than anything go-boot will
produce. But the map's core sentence is *"plain Go wiring instead of reflection"* and *"a reader can
copy a Preset's body and edit it"* — GoFr has no such seam. There is no readable wiring function to
copy; `gofr.New()` is a 100-line constructor over a private container. That is a shape difference,
and §5 scores it honestly.

---

## 2. The three gaps, tested against GoFr

### 2.1 Ordered Component lifecycle — **NO**

`App.Run` →
[`startAllServers`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/run.go):

```go
func (a *App) startAllServers(ctx context.Context) {
	wg := sync.WaitGroup{}
	a.startMetricsServer(&wg)
	a.startMCPServer(&wg)
	a.startHTTPServer(&wg)
	a.startGRPCServer(&wg)
	a.startSubscriptionManager(ctx, &wg)
	wg.Wait()
}
```

Every server is launched in its own goroutine with no sequencing and no readiness signal — the
`wg.Wait()` waits for them to *exit*, not to *start*. Ordering is whatever the scheduler does.

Shutdown is the reverse problem: it is ordered, but the order is **hardcoded in the framework** and
not extensible
([`gofr.go:92-131`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/gofr.go)) — HTTP,
gRPC, cron, metrics, MCP, container, logger. There is no way to insert your own component into it.

Against the three sub-questions in the ticket:

| | GoFr |
| --- | --- |
| start ordering | **No.** Framework servers start concurrently; no user-registered Components exist at all — there is no `AddComponent`/`Register` API, only `OnStart` hooks and framework-owned servers. |
| stop ordering | **Fixed, not ordered-by-declaration.** A hardcoded sequence over GoFr's own resources. **No `OnShutdown` hook.** GoFr's own docs say so: *"There is no public `OnShutdown` hook today; `App.Shutdown` is what gets called and it operates on the framework's own resources."* — [graceful-shutdown guide](https://github.com/gofr-dev/gofr/blob/development/docs/guides/graceful-shutdown/page.md). The recommended workaround is to pass a cancellable context into your own goroutines yourself. |
| startup-failure rollback | **No.** `OnStart` hooks *are* ordered and fail-fast with panic recovery ([`runOnStartHooks`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/gofr.go)), and the app refuses to start on error — which is real and good. But hooks that already succeeded are **not** rolled back; the process simply returns from `Run()`. |

**Verdict: no.** GoFr has *startup hooks*, which is a third of a lifecycle. It has no Component
concept, no user-controlled ordering, and no unwind.

### 2.2 Runtime log-level endpoint — **NO (inverted)**

GoFr's remote log level is **pull-based**: you set `REMOTE_LOG_URL` to a service *you* run, and GoFr
polls it every 15 seconds
([docs](https://github.com/gofr-dev/gofr/blob/development/docs/advanced-guide/remote-log-level-change/page.md)).
There is no endpoint on the app to curl.

Measured — every plausible path, against the running service (§2.4):

```
/loglevel               HTTP 404
/log-level              HTTP 404
/.well-known/loglevel   HTTP 404
/actuator/loggers       HTTP 404
/debug/loglevel         HTTP 404
```

…and the same five on the metrics port. All 404. The framework log confirms `"error":"route not
registered"` for each.

**Verdict: no.** Changing a log level in an incident requires standing up and operating a second
HTTP service, and then waiting up to 15s. That is materially worse than
`curl -XPOST localhost:9000/loglevel -d '{"level":"DEBUG"}'`.

### 2.3 Build info at runtime — **NO, partial at best**

- `grep -rn "ReadBuildInfo\|debug.BuildInfo\|vcs.revision" pkg/ --include=*.go` → **zero hits.**
  GoFr never calls `runtime/debug.ReadBuildInfo`. No VCS revision, no dirty flag, no build time, no
  dependency list is available anywhere.
- No `/info` endpoint: `/info`, `/actuator/info`, `/.well-known/info` all **404** (measured).
- The one thing that exists is a Prometheus gauge. Measured from the live service:

  ```
  # HELP app_info Info for app_name, app_version and framework_version.
  app_info{app_name="demo",app_version="dev",framework_version="v1.59.0",...} 1
  ```

  `app_version` is **not** build info — it is
  `conf.GetOrDefault("APP_VERSION", "dev")`
  ([`container.go:103`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/container/container.go)),
  i.e. a config string you have to remember to set correctly in your deploy pipeline. It defaulted
  to `"dev"` in my run, which is exactly the failure mode build info exists to prevent.
- Worse, GoFr recently **removed** the framework version from `/.well-known/health` deliberately,
  as a CVE-enumeration hardening measure
  ([health docs](https://github.com/gofr-dev/gofr/blob/development/docs/advanced-guide/monitoring-service-health/page.md)).
  So the trend is away from exposing build identity, not toward it.

**Verdict: no.** A quarter-credit for `app_info`, which answers "what did the operator type into
config", not "what commit is this binary".

### 2.4 What GoFr *does* answer — measured, for fairness

Run: `./gofrsvc` with `configs/.env` setting `HTTP_PORT=9111 METRICS_PORT=9112`.

```
GET :9111/greet?name=go-boot     200  {"data":{"message":"hello, go-boot"}}
GET :9111/.well-known/alive      200  {"data":{"status":"UP"}}
GET :9111/.well-known/health     200  {"data":{"name":"demo","status":"UP"}}
GET :9112/metrics                200  app_go_routines, app_go_sys, app_go_numGC,
                                      app_sys_memory_alloc, app_sys_total_alloc,
                                      app_http_response, app_info + full Go collector
GET :9112/debug/pprof/           200
```

That is health, readiness, and metrics working with **zero configuration** — three of the five
Actuator pieces, better than anything else surveyed in
[#3](https://github.com/squall-chua/go-boot/issues/3). Credit where due.

The known defect stands and GoFr documents it itself: `/.well-known/health` **returns 200 even when
`DEGRADED`**, so a probe wired to the status code always sees the service as ready. From GoFr's own
docs: *"a `DEGRADED` aggregate does **not** produce a non-2xx response, so a probe wired directly to
the status code will always see the service as ready."*

**Also correct a claim in [#3](https://github.com/squall-chua/go-boot/issues/3):** GoFr's migrations
**do** lock. [`pkg/gofr/migration/sql.go`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/migration/sql.go)
creates a `gofr_migration_locks` table with `lock_key`/`owner_id`/`expires_at`, takes a lease,
refreshes it, and fails with `errSQLLockRefreshFailed` if the lease is lost or stolen. That is a
lease-table lock rather than goose's `pg_advisory_lock`, but it is a real answer to concurrent
replicas, and [#3](https://github.com/squall-chua/go-boot/issues/3) implied otherwise.

### 2.5 Three gaps — summary

| Capability | GoFr | Evidence |
| --- | --- | --- |
| Ordered Component lifecycle | **No** | `startAllServers` is concurrent; no `AddComponent`; no `OnShutdown` (GoFr's own docs) |
| Runtime log-level endpoint | **No** | pull-based only; 10 candidate paths measured 404 |
| Build info at runtime | **No / partial** | no `ReadBuildInfo` in the tree; `app_info` carries a config string |

**0 of 3.** [#3](https://github.com/squall-chua/go-boot/issues/3)'s finding survives contact with
GoFr specifically.

---

## 3. The weight, measured

Scratch modules under
`.../scratchpad/measure/{gofrsvc,gobootlike,stdlibsvc}`. Fresh `GOMODCACHE` per measurement.

### 3.1 What was compared

Three services doing the same job:

1. **`gofrsvc`** — the 31-line GoFr file in §1.
2. **`gobootlike`** — pure stdlib **plus exactly the dependency set the map has already settled on**
   for base + actuator + http: OTel v1.45 traces
   ([#7](https://github.com/squall-chua/go-boot/issues/7)),
   `prometheus/client_golang` v1.24.1 ([#7](https://github.com/squall-chua/go-boot/issues/7)),
   `go.yaml.in/yaml/v3` ([#4](https://github.com/squall-chua/go-boot/issues/4),
   [#1](https://github.com/squall-chua/go-boot/issues/1)). This is the honest proxy for go-boot v1's
   HTTP+Actuator floor — it is not a strawman, it is go-boot's own shopping list.
3. **`stdlibsvc`** — zero dependencies. The absolute floor.

All three include health, runtime log level, and build info where the language permits it (2 and 3
have all three; 1 has none, per §2).

### 3.2 The numbers

| Measure | GoFr | go-boot dep set | stdlib | GoFr ÷ go-boot |
| --- | --- | --- | --- | --- |
| stripped binary (`go build -ldflags="-s -w" -trimpath`) | 41,595,145 B = **39.67 MiB** | 15,520,009 B = **14.80 MiB** | 6,267,145 B = **5.98 MiB** | **2.7×** |
| unstripped binary | 56.36 MiB | — | — | |
| direct requires in *your* `go.mod` | 1 | 5 | 0 | |
| **indirect requires forced into your `go.mod`** | **96** | **23** | 0 | **4.2×** |
| distinct modules in `go.sum` | **157** | 40 | 0 | **3.9×** |
| `go mod graph` edges | **1506** | 273 | 2 | **5.5×** |
| `go list -m all` build list | **319** | — | 1 | |
| **modules actually compiled into the binary** | **96** | **28** | 0 | **3.4×** |
| non-stdlib packages compiled | **579** | 191 | 0 | **3.0×** |
| total packages compiled | 804 | 401 | 193 | |
| cold `go build` — module zips | **95** | 28 | 0 | |
| cold `go build` — bytes downloaded | **212,514,298 B = 202.6 MiB** | 23,788,041 B = **22.6 MiB** | **0 B** | **8.9×** |
| cold `go build` — wall time | **33.9 s** | 8.8 s | 0.4 s | **3.9×** |
| cold extracted `GOMODCACHE` | **1167 MiB** | 152 MiB | 0 | **7.7×** |
| cold `go mod tidy` (worse case: all platforms + test deps) | **142 zips / 224.7 MiB / 85.6 s**, 1.3 GiB cache | — | — | |

Reproduce:

```bash
export GOMODCACHE=$(mktemp -d)
cd <svc> && go build -o /dev/null .
find $GOMODCACHE/cache/download -name '*.zip' | wc -l
find $GOMODCACHE/cache/download -name '*.zip' -printf '%s\n' | awk '{s+=$1} END {print s}'
go build -ldflags="-s -w" -trimpath -o out . && ls -l out
go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./... | sort -u | grep -v '^$' | wc -l
go mod graph | wc -l
awk '{print $1}' go.sum | sort -u | wc -l
```

### 3.3 The ticket's specific question: do Pub/Sub and GraphQL reach a minimal binary?

**Yes. All of them do.** From
`go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./... | sort -u` on the HTTP-only service,
these are **linked into the 39.7 MiB binary**:

| What | Module |
| --- | --- |
| GCP Pub/Sub + the whole GCP auth stack | `cloud.google.com/go/pubsub`, `cloud.google.com/go/pubsub/v2`, `cloud.google.com/go/auth`, `cloud.google.com/go/iam`, `google.golang.org/api`, `github.com/google/s2a-go`, `github.com/googleapis/enterprise-certificate-proxy` |
| GraphQL (two of them) | `github.com/graphql-go/graphql`, `github.com/vektah/gqlparser/v2` |
| Kafka | `github.com/segmentio/kafka-go` |
| MQTT | `github.com/eclipse/paho.mqtt.golang` |
| SQLite (a C-to-Go transpiled libc!) | `modernc.org/sqlite`, `modernc.org/libc`, `modernc.org/memory`, `modernc.org/mathutil` |
| MySQL / Postgres / Dgraph / Redis | `github.com/go-sql-driver/mysql`, `github.com/lib/pq`, `github.com/dgraph-io/dgo/v210`, `github.com/redis/go-redis/v9` (+2 redis extras) |
| gRPC + protobuf + grpc-gateway + gogo/protobuf | `google.golang.org/grpc`, `google.golang.org/protobuf`, `github.com/grpc-ecosystem/grpc-gateway/v2`, `github.com/gogo/protobuf` |
| Zipkin | `github.com/openzipkin/zipkin-go`, `go.opentelemetry.io/otel/exporters/zipkin` |
| JWT, WebSocket, OpenCensus | `github.com/golang-jwt/jwt/v5`, `github.com/gorilla/websocket`, `go.opencensus.io` |
| **Test-only libraries, in the production binary** | **`github.com/stretchr/testify`**, **`github.com/DATA-DOG/go-sqlmock`**, **`go.uber.org/mock`**, `github.com/davecgh/go-spew`, `github.com/pmezard/go-difflib` |

The last row is the one that is not a matter of taste. GoFr declares six test libraries as **direct,
non-test requires** of its runtime module
([`go.mod` v1.59.0](https://github.com/gofr-dev/gofr/blob/development/go.mod)), and three of them are
reachable from `pkg/gofr` and therefore linked into every deployed GoFr binary. That is attack
surface, `govulncheck` noise, and SBOM noise, for code that exists to support GoFr's own tests.

**Module graph pruning does not save you here.** [#3](https://github.com/squall-chua/go-boot/issues/3)
proved pruning works when a consumer imports only a light subpackage. GoFr defeats it by design:
`pkg/gofr` itself imports the container, and the container imports every datasource. There is one
import path and it pulls everything.

Only `github.com/alicebob/miniredis/v2` and `github.com/go-redis/redismock/v9` stayed out.

---

## 4. Go version floor — verified

`gofr.dev v1.59.0` declares **`go 1.26.0`**. Verified two ways:

```bash
grep -E '^(go|toolchain) ' $GOMODCACHE/gofr.dev@v1.59.0/go.mod   # -> go 1.26.0
curl -s https://proxy.golang.org/gofr.dev/@v/v1.59.0.mod | grep '^go '
```

The claim in [#17](https://github.com/squall-chua/go-boot/issues/17) is **correct**. Also 49 direct
requires in the released module (`#3` said 50 from the `development` branch — close enough, and it
moves).

**What matters more for [#16](https://github.com/squall-chua/go-boot/issues/16) is the trajectory.**
Measured across releases (`curl -s https://proxy.golang.org/gofr.dev/@v/$v.mod | grep '^go '`):

| GoFr release | `go` directive |
| --- | --- |
| v1.30.0 | `go 1.22` |
| v1.40.0 | `go 1.24` |
| v1.45.0 | `go 1.25` |
| v1.50.0 | `go 1.25` |
| v1.55.0 | `go 1.25.0` |
| **v1.59.0** | **`go 1.26.0`** |

**Adopting GoFr means your Go floor is set by someone else and rises roughly every ten minor
releases.** For a project whose own floor is
[under active dispute](https://github.com/squall-chua/go-boot/issues/16), that is not a neutral
detail — it hands the decision away permanently. Note this cuts both ways: it also means the "Go
1.26 floor" objection is soft, since a user can pin `gofr.dev v1.55.0` and stay on 1.25 (at the cost
of falling behind on fixes).

---

## 5. GoFr against the map's standing constraints and closed decisions

Constraints from [#1](https://github.com/squall-chua/go-boot/issues/1); closed research decisions
from [#3](https://github.com/squall-chua/go-boot/issues/3),
[#4](https://github.com/squall-chua/go-boot/issues/4),
[#5](https://github.com/squall-chua/go-boot/issues/5),
[#6](https://github.com/squall-chua/go-boot/issues/6),
[#7](https://github.com/squall-chua/go-boot/issues/7).

| # | What the map wants | GoFr | Verdict |
| --- | --- | --- | --- |
| 1 | **Thin composable library**, Starters you opt into | One `App` + one `Container` holding every datasource. No opt-in seam; §3.3 shows what that costs. | **Conflicts** |
| 2 | **No reflection DI** | No reflection DI container — but `grpc.go` uses `reflect` for container injection into gRPC servers ([`grpc.go`](https://github.com/gofr-dev/gofr/blob/development/pkg/gofr/grpc.go), `errNonAddressable`). Not a DI graph; still reflection at a wiring seam. | **Mostly satisfies** |
| 3 | **Presets as plain Go a reader can copy** | No such thing. `gofr.New()` is a private constructor; there is no readable wiring body to fork. | **Conflicts** |
| 4 | **Library rule: stdlib first, one well-known third-party only where stdlib falls short** | 96 modules compiled for an HTTP hello-world, including a transpiled SQLite libc and three test libraries. | **Conflicts, hard** |
| 5 | **Go floor 1.22 (contested, #16)** | `go 1.26.0`, and rising ~every 10 releases. | **Conflicts** — but see §4, pinning is possible |
| 6 | **One Go module, Starters as subpackages** | 32 `go.mod` files (`find . -name go.mod \| wc -l`), and the split does **not** slim the core (§3). | **Conflicts** |
| 7 | **Test imports must not leak deps to root-only users** — [#3](https://github.com/squall-chua/go-boot/issues/3)'s hard rule | GoFr does exactly the thing this rule was written to prevent: testify, go-sqlmock and uber/mock are in every user's binary. | **Conflicts — and is the live counterexample** |
| 8 | **Two independent Transports, no transcoding** | HTTP (gorilla/mux) and gRPC (grpc-go) are independent; no proto transcoding. | **Satisfies** |
| 9 | **gRPC via `connect-go`** ([#5](https://github.com/squall-chua/go-boot/issues/5)) | `google.golang.org/grpc` + `go-grpc-middleware/v2`. | **Conflicts** (preference-grade — grpc-go is a legitimate choice) |
| 10 | **HTTP-only user never installs `buf`/gRPC** | grpc-go and protobuf are compiled into the HTTP-only binary regardless. `buf` itself is not required. | **Conflicts** |
| 11 | **DB: wire the pool, stay neutral so sqlc/ent/gorm all work** | **Proven not neutral.** `container.DB` is an interface lacking `PrepareContext`. Compile-tested against sqlc's generated `DBTX` interface verbatim: `cannot use c.SQL (variable of interface type container.DB) as DBTX value in argument to useSqlc: container.DB does not implement DBTX (missing method PrepareContext)`. gorm's `gorm.ConnPool` and ent's `dialect/sql` need the same method. An **escape hatch does compile** — `if raw, ok := c.SQL.(*gofrSQL.DB); ok { useSqlc(raw.DB) }` — but it is a type assertion on an undocumented concrete type, not a contract. | **Conflicts** |
| 12 | **`database/sql` interface, Postgres tested default** | `database/sql` under the hood, Postgres/MySQL/SQLite supported. | **Satisfies** |
| 13 | **Migrations as a separate command by default; opt-in at startup for local dev** | `app.Migrate(...)` runs **in-process at startup**, always. GoFr's own Kubernetes guide warns: *"Migrations on every replica on startup is racy under HPA."* | **Conflicts** (inverted default) |
| 14 | **Migrations must lock** ([#6](https://github.com/squall-chua/go-boot/issues/6)) | Yes — lease-table lock with owner + expiry + refresh (§2.4). Different mechanism from goose's advisory lock; a real answer. | **Satisfies** |
| 15 | **Actuator: health** | `/.well-known/health`, aggregating datasources. **But 200 on DEGRADED** (GoFr's own docs). | **Partial** |
| 16 | **Actuator: readiness** | `/.well-known/alive`. | **Satisfies** |
| 17 | **Actuator: metrics** | Prometheus on a separate port, sensible defaults, + pprof. Better than the map's plan. | **Satisfies, exceeds** |
| 18 | **Actuator: runtime log level** | **No endpoint** (§2.2). | **Conflicts** |
| 19 | **Actuator: build info** | **No** (§2.3). | **Conflicts** |
| 20 | **Logging on `slog.LevelVar`** ([#7](https://github.com/squall-chua/go-boot/issues/7)) | Own logger interface, not `slog`. | **Conflicts** |
| 21 | **Config: stdlib + `go.yaml.in/yaml/v3`** ([#4](https://github.com/squall-chua/go-boot/issues/4)) | `.env` via godotenv; also carries `gopkg.in/yaml.v3` — the **archived** module [#1](https://github.com/squall-chua/go-boot/issues/1) explicitly rules out. | **Conflicts** |
| 22 | **Observability: OTel v1.45 traces + client_golang metrics, two pipelines** | OTel + Prometheus, two pipelines. Exactly the shape [#7](https://github.com/squall-chua/go-boot/issues/7) chose. | **Satisfies** |
| 23 | **Component lifecycle + graceful shutdown** | Graceful shutdown yes, `SHUTDOWN_GRACE_PERIOD` configurable. Ordered lifecycle no (§2.1). | **Partial** |
| 24 | **Scaffold CLI** | None. `gofr.New()` is short enough not to need one. | **Ignores** (arguably correctly) |
| 25 | *(not a map constraint, but a finding)* | **Default-on outbound telemetry to gofr.dev.** One env var to disable. | **New objection** |

**Tally: 7 satisfies, 3 partial, 14 conflicts, 1 ignores.**

The conflicts are not evenly weighted. Rows 4, 7, 11 and 18–19 are the ones with teeth: the
dependency weight, the test-libraries-in-your-binary rule the map wrote specifically to avoid this,
the proven query-layer lock-in, and the two missing Actuator pieces. The rest (9, 20, 21) are
genuinely preference.

---

## 6. The contribute option

Independently researched. All numbers 2026-08-24 via `gh api` (REST — `gh issue view` fails on this
repo with a Projects-classic GraphQL deprecation error).

**The mechanics are good:**

- **Apache-2.0. No CLA, no DCO, no sign-off.** Verified: `grep -rniE "\bCLA\b|contributor
  license|developer certificate|sign-?off|DCO"` over `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`,
  `README.md`, `SECURITY.md`, `.github` → zero hits. Legal friction is zero.
- **Active.** 48 commits / 90d on `development`, 7 distinct authors, last push 3 days ago.
- **They merge outside work, including big outside work.** Of the last 100 merged PRs, **40 were
  authored by non-org-members**; 99 of 100 were human, 1 dependabot. Median created→merged **22
  hours**; p90 580 h (24 days). Among the last 200 **human** closed PRs, **73.5% merged**.
- Concrete outside merges: `feat(metrics): add push-based OTLP metrics export` (+2282/−56, 33 files,
  91 h); `feat(cloudsql): Cloud SQL datasource with IAM auth + pluggable AddSQLDB` (+2371/−12, 13
  days); `fix(subscriber): emit error on handler panic recover` from a true drive-by (13 h).

**The gates are real:**

- **Assignment before work**: *"Contributors should begin working on an issue only after it has been
  assigned to them."* Issues labelled `triage` are not open for contribution.
- **Two GoFr-developer approvals** per PR; **no PR may decrease coverage** (CI-enforced); tests
  mandatory; strict lint; PRs target `development`, not `main`. Opinionated house rules (no globals,
  no `init()`, abstract every external dep behind an interface).
- **Large unsolicited features do get rejected on design and reimplemented in-house.** The clearest
  case: PR #3044 (`built in SSE support`, +952/−15, outside author) sat 142 days collecting review
  pushback (*"it requires a lot of change for the user… this doesn't look clean"*) and was closed
  with *"This has been done along with the PR #3722"* — #3722 being a +7487/−9 maintainer-authored
  feature. Open PR backlog: 45, all human, p90 age 78 days.
- **Governance is one company.** Maintainers' GitHub profiles put four of five at ZopSmart / zop.dev
  (`gh api users/<login>`). No `GOVERNANCE.md`, no `MAINTAINERS.md`; `docs/team.json` (5 people) is
  the only leadership record. **The README claim is only "Listed in the CNCF Landscape", which is
  accurate and means nothing** — verified in `cncf/landscape` `landscape.yml`: the GoFr entry has no
  `project:` key, unlike e.g. gRPC's `project: incubating`. **GoFr is not a CNCF sandbox project.**
  Anyone citing that is wrong.

**Assessment.** Contributing the two small Actuator pieces upstream is **cheap, plausible, and high
reach**. A runtime log-level handler is ~30 lines; a build-info handler over `debug.ReadBuildInfo` is
~15. Both are additive, testable, coverage-safe, and match the shape of things GoFr already merges
fast. On the evidence, they'd likely land — file issues first and get assigned, per CONTRIBUTING.

**But it does not produce the thing the map wants,** and this is the decisive point:

- The **ordered Component lifecycle** is not an additive patch. It means replacing
  `startAllServers`'s goroutine spray with a sequenced start, adding a public Component interface,
  adding `OnShutdown`, and making the hardcoded `Shutdown` order extensible. That is a core
  architectural change to a project that has already shown (PR #3044) it rejects outside
  architectural change and ships its own instead. Realistic outcome: months of review, then a
  maintainer-authored version.
- **The weight cannot be contributed away at all.** The 96 compiled modules are not an oversight,
  they are the container design (§1, §3.3). Fixing it means splitting `Container` into optional
  registrations across 30+ datasources and changing `*gofr.Context`'s public surface — a v2, not a
  PR. Nobody is going to accept that from a drive-by.

**Verdict: contribute the two endpoints; do not expect contribution to substitute for building.**
The two things go-boot actually wants that GoFr can accept are also the two things go-boot could
write in an afternoon. The two things go-boot wants that would matter — a lifecycle and a small
dependency footprint — are the two GoFr cannot take.

---

## 7. The "smaller" thesis — is it achievable, and is it meaningful?

**Achievable: yes, measured.** `gobootlike` is the map's own settled dependency list and it lands at
**14.80 MiB / 28 compiled modules / 22.6 MiB cold download** versus GoFr's **39.67 MiB / 96 / 202.6
MiB**. That is not a projection — both binaries were built.

**The numbers, as ratios:**

| | GoFr ÷ go-boot |
| --- | --- |
| stripped binary | **2.7×** |
| modules compiled in | **3.4×** |
| modules in `go.sum` | **3.9×** |
| module graph edges | **5.5×** |
| cold download bytes | **8.9×** |

**Is "a tenth of the dependencies" true?** No — that specific phrasing overclaims. The honest claim
is **"a third of the modules, a third of the binary, a ninth of the download"**. `go-boot` cannot
reach a tenth while it keeps OTel and `client_golang`, because those two account for almost all of its 28
compiled modules — **27 of the 28 are transitively OTel or `client_golang`; exactly one is
`go.yaml.in/yaml/v3`**. The stdlib floor (0 modules, 5.98 MiB) is only reachable by dropping OTel and
Prometheus, which [#7](https://github.com/squall-chua/go-boot/issues/7) already decided against.

**Is a 3× difference meaningful?** Judge it against what it buys and costs:

- **Yes for supply chain.** 28 modules vs 96 is 68 fewer things to audit, patch and explain in an
  SBOM, and go-boot's 28 contain **zero test libraries** and zero cloud-vendor SDKs. That is a real,
  defensible engineering difference, not taste.
- **Yes for CI.** 22.6 MiB vs 202.6 MiB cold, 8.8 s vs 33.9 s. On a cold runner, every build.
- **Marginal for runtime.** 39.7 MiB vs 14.8 MiB in a container image is 25 MiB. In 2026 that is
  noticeable but rarely decisive.
- **Honest counterweight: the thing 3× smaller than go-boot is "no framework".** `stdlibsvc` is 78
  lines, has 0 modules, 5.98 MiB, and — unlike GoFr — implements **all three** of the capabilities
  [#3](https://github.com/squall-chua/go-boot/issues/3) says nobody ships:

  ```go
  var lvl = new(slog.LevelVar)     // runtime log level, 1 line
  adm.HandleFunc("POST /loglevel", …)  // ~12 lines
  adm.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
      bi, _ := debug.ReadBuildInfo(); writeJSON(w, bi)   // build info, 2 lines
  })
  ```

  This is the number the spec has to survive. go-boot's whole value proposition is that importing it
  beats copying that file — which is true only if go-boot stays close to that file in weight and
  stays radically easier to keep correct across a fleet of services.

**Conclusion on the thesis:** "same job, a third of the dependencies, stdlib underneath" is
**measured and true**. "A tenth" is not. And the thesis alone does not justify a framework — it
justifies a *small library*, which is exactly what
[#3](https://github.com/squall-chua/go-boot/issues/3) already concluded and what
[#1](https://github.com/squall-chua/go-boot/issues/1) already constrains.

---

## 8. Recommendation

**Build go-boot. Adopt is the wrong answer, but the reason is now measured rather than felt — and it
is narrower than the map may want it to be.**

Three findings carry the decision, in order:

1. **GoFr fails all three of the capabilities the map is actually about** (§2). Not partially —
   there is no Component concept, no log-level endpoint on any of ten probed paths, and no
   `ReadBuildInfo` in the tree. [#3](https://github.com/squall-chua/go-boot/issues/3)'s finding
   holds under direct test.
2. **The weight objection is not preference.** 96 modules compiled into an HTTP hello-world,
   including GCP Pub/Sub, two GraphQL parsers, a transpiled SQLite libc, and **three test libraries
   in the production binary** (§3.3). The map's hard rule from
   [#3](https://github.com/squall-chua/go-boot/issues/3) — *"the base package and its tests must
   never import a Starter subpackage, or every root-only user pays"* — was written to prevent
   exactly what GoFr does. Adopting GoFr means adopting the failure mode the map already legislated
   against.
3. **Contributing cannot fix either.** The two things GoFr would accept are the two things go-boot
   can write in an afternoon; the two things that matter — a lifecycle and a small footprint — are
   architectural and PR #3044 shows how those go (§6).

**Concretely:**

- **Write the spec** ([#15](https://github.com/squall-chua/go-boot/issues/15)), opening with the
  one-sentence reason in §0.
- **Do contribute** the log-level and build-info endpoints to GoFr anyway. File issues, get assigned,
  ~45 lines total, no CLA. It costs a day, reaches 21k stars, and it costs go-boot nothing — go-boot
  needs those endpoints on `slog.LevelVar` regardless.
- **Steal from GoFr, deliberately**: the `/.well-known/*` health path convention, the separate
  metrics port with pprof, the `app_*` default metric set, `SHUTDOWN_GRACE_PERIOD`, and the
  lease-table migration lock as a fallback where advisory locks are unavailable. GoFr's Actuator
  defaults are better than the map's current plan.
- **Do not steal**: the container-holds-everything design. That single choice produces every number
  in §3.
- **Set a hard budget in the spec.** From these measurements the defensible line is: **go-boot v1
  HTTP+Actuator must compile ≤ 30 modules and produce a ≤ 16 MiB stripped binary**, and the base
  Starter alone must compile **≤ 2** (yaml only). Make it a CI check next to the
  [#3](https://github.com/squall-chua/go-boot/issues/3) import guard. If v1 drifts past it, the case
  for go-boot has evaporated and the answer flips.
- **[#16](https://github.com/squall-chua/go-boot/issues/16) note:** GoFr's floor moved 1.22 → 1.26
  across 29 minor releases (§4). Whatever go-boot picks, treat the floor as a published promise, not
  an implementation detail — that discipline is itself a differentiator from GoFr.

---

## 9. The strongest honest case for the opposite choice

If the human wants to adopt GoFr — or to build nothing — here is the case, made as well as the
evidence allows. It is not weak.

**1. The measured gap buys you 45 lines and 25 MiB.** Strip the rhetoric from §2 and the three
missing capabilities are: an ordered start loop (~40 lines), a POST handler over `slog.LevelVar` (12
lines), and `json.NewEncoder(w).Encode(debug.ReadBuildInfo())` (2 lines). Spending months of design
and a permanent maintenance obligation to own those, plus a 25 MiB binary saving, is a poor trade on
any spreadsheet. [#7](https://github.com/squall-chua/go-boot/issues/7) already said the whole
Actuator is ~130 lines and *"a real win, not a moat"* — this ticket confirms it, harder.

**2. GoFr's 31-line `main.go` is better than anything go-boot will ship.** It is shorter than the
78-line stdlib file, it needs no Preset to be readable, and it delivers health, readiness, metrics,
tracing, structured request logs and graceful shutdown with one constructor call. go-boot's north
star ([#2](https://github.com/squall-chua/go-boot/issues/2), still open) has to beat that. **The
prototype the ticket wanted as a yardstick does not exist yet** — so this evaluation is comparing
GoFr against a spec, not against a demonstrated better `main.go`. That is a genuine hole in the
build case and it should be closed before [#15](https://github.com/squall-chua/go-boot/issues/15).

**3. "3× smaller" is a weaker claim than "10× smaller", and it is the honest one.** The map's
plausible reason to exist was *"a fraction of the dependencies"*. A third is a fraction, but 28
modules is not obviously virtuous next to 96 — both are far from zero, and 26 of go-boot's 28 come
from two dependencies it has already committed to. If the real value is a small dependency
footprint, the winning move is the 78-line stdlib file, which beats go-boot by more than go-boot
beats GoFr.

**4. Every weight objection has a workaround.** Go 1.26 floor → pin v1.55.0 and stay on 1.25.
Telemetry → `GOFR_TELEMETRY=false`. Query layer → a one-line type assertion to `*gofrSQL.DB`
compiles and works (§5 row 11, verified). DEGRADED-returns-200 → read the body. None of these are
elegant, but "I have to write one type assertion" is a much smaller cost than "I maintain a
framework."

**5. GoFr is a going concern and go-boot is a hypothesis.** 21k stars, 73.5% human PR merge rate,
median 22 h to merge, 48 commits/90d, no CLA, Apache-2.0. [#3](https://github.com/squall-chua/go-boot/issues/3)'s
own warning applies: the Go actuator graveyard is full of 0–3 star repos. The risk is not building
the wrong thing; it is being the ninth abandoned `go-actuator`.

**6. The map's most damning row is self-inflicted.** Row 4 of §5 says GoFr violates *"stdlib first,
one well-known third-party library only where stdlib clearly falls short."* But go-boot's own
settled decisions already pull OTel (20 modules), `client_golang`, `connect-go`, `goose` and a
Postgres driver. On its own rule, go-boot at v1 will be closer to GoFr than to the stdlib floor. If
the rule is what justifies the project, the rule is already bent.

**The single strongest sentence against building:** *the three capabilities that justify go-boot's
existence total 54 lines of Go, and this evaluation wrote all three of them by accident while
constructing a baseline.*

The counter — and it is why the recommendation is still Build — is that those 54 lines are the
smallest part of the value. The value is having them in one place, correct, on `slog`, with agreed
paths and a management port, so twenty services get them by importing rather than by twenty
copy-pastes drifting apart. That is a library-sized problem, and GoFr solves it at 3× the weight
with two of the five Actuator pieces missing.

---

## 10. Marked UNVERIFIED

- Whether the 2.7× binary difference is representative once go-boot adds `connect-go`, `goose` and a
  Postgres driver — the `gobootlike` baseline covers **base + actuator + http only**, matching the
  measurement GoFr got. A full-surface comparison was not built.
- Whether the type-assertion escape hatch (`c.SQL.(*gofrSQL.DB)`) is stable across GoFr releases, or
  whether GoFr documents it anywhere. It compiles at v1.59.0; nothing in the docs mentions it.
- Whether the maintainers would in fact accept a log-level or build-info endpoint. Inferred from the
  merge record (§6), not asked. Filing a proposal issue is the only way to know.
- Whether GoFr's `app_info` is intended as build info; no doc states its purpose beyond the metric
  HELP string.
- GoFr's reflection usage in the **HTTP** request hot path was not audited (only `grpc.go`'s
  container injection was found). [#3](https://github.com/squall-chua/go-boot/issues/3) left the
  same item open.
- Whether `govulncheck` flags the test libraries that are linked into the GoFr binary — asserted from
  their being reachable in `go list -deps`, not run.
- The lease-table migration lock's behaviour under clock skew between replicas was read, not tested.
- Binary sizes are `linux/amd64` only. No cross-platform check.
- Whether GoFr's default-on telemetry POST is disclosed anywhere other than the startup log line and
  the source; the docs directory was not exhaustively searched for it.

---

## Sources and reproduction

Primary sources only: GoFr's repository at
[`gofr-dev/gofr`](https://github.com/gofr-dev/gofr) (branch `development`, commit `b29f693`, and the
released module `gofr.dev v1.59.0` from `proxy.golang.org`), GoFr's own docs in `docs/` (which is
what renders at gofr.dev), the Go module proxy, the CNCF landscape file, and the GitHub REST API.
No blog posts, no secondary write-ups.

Scratch code (outside this repo, deliberately):
`.../scratchpad/measure/{gofrsvc,gobootlike,stdlibsvc,querylayer}/`. Every measurement command is
given inline in §3.2, §4 and §5. GitHub metrics measured 2026-08-24.
