# Observability: what the Actuator needs from OTel, Prometheus and slog

Research for [#7](https://github.com/squall-chua/go-boot/issues/7). Findings dated **2026-08-24**.
Versions move; re-check the module proxy before acting on this months from now.

Sources are primary only: official OpenTelemetry docs and repo source, `prometheus/client_golang`
source and Prometheus docs, the Go standard library source, Kubernetes docs, Spring Boot's own
reference. Every version number below was read from
[`proxy.golang.org`](https://proxy.golang.org/) or from the module's `go.mod` on GitHub, not from
memory. Every code sample was compiled, and the non-trivial ones were tested.

---

## 0. The finding that outranks the rest: Go 1.22 is not reachable

Issue [#1](https://github.com/squall-chua/go-boot/issues/1) sets **Go 1.22 minimum** as a standing
constraint. That constraint is incompatible with a current observability stack.

The `go` directive in `go.mod` is a **minimum requirement**, not a hint. From
[go.dev/doc/toolchain](https://go.dev/doc/toolchain):

> The Go toolchain refuses to load a module or workspace that declares a minimum required Go
> version greater than the toolchain's own version.

and

> A module's `go` line must declare a version greater than or equal to the `go` version declared
> by each of the modules listed in `require` statements.

Read straight from the published `go.mod` files:

| Module | Version | `go` directive |
|---|---|---|
| [`go.opentelemetry.io/otel`](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go/v1.45.0/go.mod) | v1.45.0 | `go 1.25.0` |
| [`go.opentelemetry.io/otel/sdk`](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go/v1.45.0/sdk/go.mod) | v1.45.0 | `go 1.25.0` |
| [`go.opentelemetry.io/otel/sdk/metric`](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go/v1.45.0/sdk/metric/go.mod) | v1.45.0 | `go 1.25.0` |
| [`go.opentelemetry.io/otel/exporters/prometheus`](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go/v1.45.0/exporters/prometheus/go.mod) | v0.67.0 | `go 1.25.0` |
| [`.../contrib/instrumentation/net/http/otelhttp`](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go-contrib/instrumentation/net/http/otelhttp/v0.70.0/instrumentation/net/http/otelhttp/go.mod) | v0.70.0 | `go 1.25.0` |
| [`github.com/prometheus/client_golang`](https://raw.githubusercontent.com/prometheus/client_golang/v1.24.1/go.mod) | v1.24.1 | `go 1.25.0` |

**Even the Prometheus-only path requires Go 1.25.** There is no version of this stack that builds
on Go 1.22.

The last releases that still fit Go 1.22, read from their `go.mod` on GitHub:

- `go.opentelemetry.io/otel` **v1.35.x** (`go 1.22.0`); v1.38.0 moved to `go 1.23.0`, v1.40.0 to
  `go 1.24.0`, v1.42.0 to `go 1.25.0`.
- `github.com/prometheus/client_golang` **v1.22.0** (`go 1.22`); v1.23.0 moved to `go 1.23.0`.

Pinning there means forgoing roughly a year of releases and all security fixes since.

**Consequence for the map:** the Go 1.22 floor and a modern Actuator are mutually exclusive. The
floor has to move to **Go 1.25** (which also keeps `net/http` method+wildcard routing, the reason
1.22 was chosen — that landed in 1.22 and is still there). This should be raised as an amendment to
#1 rather than decided inside this ticket.

---

## 1. OpenTelemetry Go: current state

### Stability

The [OTel Go README](https://github.com/open-telemetry/opentelemetry-go/blob/main/README.md) and
[opentelemetry.io/docs/languages/go](https://opentelemetry.io/docs/languages/go/) give:

| Signal | Status |
|---|---|
| Traces | **Stable** |
| Metrics | **Stable** |
| Logs | **Beta** |

Metrics reaching stable is the notable change — `go.opentelemetry.io/otel/sdk/metric` now ships at
**v1.45.0**, no longer a `v0.x` module. Confirmed against the proxy:
`https://proxy.golang.org/go.opentelemetry.io/otel/sdk/metric/@latest` → `v1.45.0`, tagged
2026-08-03.

Per the project's own
[VERSIONING.md](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go/v1.45.0/VERSIONING.md),
`v0` modules carry the semver warning "Anything MAY change at any time. The public API SHOULD NOT
be considered stable." So the `v0.x` list matters:

**Still pre-1.0 as of today** (versions from the module proxy):

- `go.opentelemetry.io/otel/log` — **v0.21.0**
- `go.opentelemetry.io/otel/sdk/log` — **v0.21.0**
- `go.opentelemetry.io/otel/exporters/prometheus` — **v0.67.0**
- `go.opentelemetry.io/otel/metric/x` — **v0.67.0** (pulled in transitively by `sdk/metric`)
- `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` — **v0.70.0**
- `go.opentelemetry.io/contrib/exporters/autoexport` — **v0.70.0**

Note the sting: **`otelhttp`, the thing you actually wrap your mux in, is still v0.70.0.** The
stable-v1 guarantee covers the API and SDK, not the HTTP instrumentation every service uses.

Stable core, `v1.45.0`, all released together
([proxy](https://proxy.golang.org/go.opentelemetry.io/otel/@latest), 2026-08-03): `otel`,
`otel/trace`, `otel/metric`, `otel/sdk`, `otel/sdk/metric`, and the OTLP trace/metric exporters.

### How much setup code, actually

The [official getting-started guide](https://opentelemetry.io/docs/languages/go/getting-started/)
walks through an `otel.go` of roughly 90 lines plus main-wiring, because it sets up all three
signals with stdout exporters and shutdown plumbing for each.

That is not the floor. I wrote and **compiled** the minimum for a tracing-enabled HTTP service:

```go
// setupTracing configures the global tracer provider and propagator from the
// standard OTEL_* environment variables. It returns a shutdown func.
func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.Default()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}
```

**15 lines of function, 32 lines of file including the import block.** Plus one line at the call
site: `otelhttp.NewHandler(mux, "server")`.

Two things make it this short, both verified in the module cache rather than assumed:

- `resource.Default()` already runs `defaultServiceNameDetector`, `fromEnv` and `telemetrySDK`
  detectors ([`sdk/resource/resource.go:250`](https://github.com/open-telemetry/opentelemetry-go/blob/v1.45.0/sdk/resource/resource.go)),
  so `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` are honoured with no manual resource
  construction.
- `otlptracegrpc.New(ctx)` with no options reads `OTEL_EXPORTER_OTLP_ENDPOINT` /
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, defaulting to `https://localhost:4317`
  ([`otlptracegrpc/doc.go`](https://github.com/open-telemetry/opentelemetry-go/blob/v1.45.0/exporters/otlp/otlptrace/otlptracegrpc/doc.go)).

So the honest answer to "how much setup code" is **~15 lines for tracing**, and the guide's ~90 is
the cost of also doing metrics, logs and stdout exporters in the same file. The premise that OTel
Go setup is a large hand-rolled slab is **weaker than it looks** — the slab in the docs is a
teaching artefact, not a requirement.

### Has a helper appeared? Yes — and it is the wrong trade

`go.opentelemetry.io/contrib/exporters/autoexport` **v0.70.0** exists
([pkg.go.dev](https://pkg.go.dev/go.opentelemetry.io/contrib/exporters/autoexport)). It provides
`NewSpanExporter`, `NewMetricReader` and `NewLogExporter`, driven by the spec's
`OTEL_TRACES_EXPORTER` / `OTEL_METRICS_EXPORTER` / `OTEL_LOGS_EXPORTER` selector variables
(default `otlp`, protocol default `http/protobuf`).

It replaces **one line** (`otlptracegrpc.New(ctx)`) with one line, and in exchange its
[`go.mod`](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go-contrib/exporters/autoexport/v0.70.0/exporters/autoexport/go.mod)
requires every OTLP exporter, every stdout exporter, the Prometheus exporter, the Prometheus
bridge, and the whole `v0.21.0` logs surface. Measured below: **+2.0 MB binary and +32 `go.sum`
lines over the equivalent stack without it**, and it drags `otel/log` (beta) into a build that
otherwise never touches logs.

**Verdict: skip `autoexport`.** It buys env-var exporter selection that a service configures once.

### Which exporter should be the default?

Measured (see §7): **OTLP/gRPC and OTLP/HTTP cost the same** — 22.0 MB vs 21.8 MB, a 0.15 MB
difference. Picking `otlptracehttp` does **not** escape the gRPC dependency tree, because
`go.opentelemetry.io/proto/otlp` pulls `google.golang.org/grpc` in transitively either way.

Since weight is a wash, decide on fit:

- The OTel spec's own default protocol is `http/protobuf` (per the `autoexport` docs above).
- go-boot ships a **gRPC Transport Starter in v1** ([#1](https://github.com/squall-chua/go-boot/issues/1)),
  so `google.golang.org/grpc` is in the tree regardless, and the Collector's canonical receiver is
  gRPC on 4317.

**Recommendation: `otlptracegrpc` as the default**, with the swap to `otlptracehttp` documented as
a one-line, zero-cost change. This is genuinely close to a coin flip; do not over-defend it.

**Stdout should not be the default** — it is a debugging exporter. Offer it as an explicit opt-in.

---

## 2. Metrics: Prometheus or OTel?

### `prometheus/client_golang` is still what Prometheus itself tells you to use

The [official Prometheus Go guide](https://prometheus.io/docs/guides/go-application/) names
`github.com/prometheus/client_golang` and gives this as the whole `/metrics` setup:

```go
reg := prometheus.NewRegistry()
reg.MustRegister(
	collectors.NewGoCollector(),
	collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
)
http.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
```

Current version **v1.24.1**, tagged 2026-07-24
([proxy](https://proxy.golang.org/github.com/prometheus/client_golang/@latest)). Actively released,
`v1.x` stable, and it is the only one of the two that is a stable module end-to-end.

OTel metrics have **not** displaced it. OTel's own
[Prometheus-client-libraries page](https://opentelemetry.io/docs/compatibility/prometheus/client-libraries/)
is explicitly a migration/translation guide, not a recommendation — it opens "This guide is for
developers familiar with the Prometheus client libraries who want to understand equivalent patterns
in the OpenTelemetry metrics API and SDK." It makes no claim that you should switch.

### Can one pipeline serve both? Yes, at a price

Two routes, both first-party:

**(a) OTel SDK → Prometheus exposition**, via
[`go.opentelemetry.io/otel/exporters/prometheus`](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/prometheus).
It "converts OTLP metrics into the Prometheus exposition format and implements
`prometheus.Collector`". Costs:

- The module is **v0.67.0 — pre-1.0**, so your metrics pipeline sits on an unstable API while your
  traces sit on a stable one. Its `WithoutCounterSuffixes` and `WithoutUnits` options are already
  deprecated in favour of `WithTranslationStrategy`, which is the API churning in public.
- It [retracts v0.59.0](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go/v1.45.0/exporters/prometheus/go.mod)
  for producing incorrect metric names — a fair signal of how settled the naming translation is.
- Measured cost: **+1.3 MB** over the native-Prometheus equivalent stack (§7).

**(b) Prometheus server ingesting OTLP natively.** Per
[prometheus.io/docs/guides/opentelemetry](https://prometheus.io/docs/guides/opentelemetry/),
Prometheus accepts OTLP over HTTP at `/api/v1/otlp/v1/metrics` behind the
`--web.enable-otlp-receiver` flag (disabled by default). Delta temporality needs the experimental
`otlp-deltatocumulative` feature flag.

Either way you pay the translation tax, which the
[OTel/Prometheus compatibility spec](https://opentelemetry.io/docs/specs/otel/compatibility/prometheus_and_openmetrics/)
documents as genuinely lossy: `_total` suffix insertion, unit-word suffixes, `otel_scope_name` /
`otel_scope_version` labels added to every point (cardinality), resource attributes displaced into
a separate `target_info` metric that must be dropped on round-trip, exponential histograms with
scale > 8 downscaled or dropped, exemplars on gauges and summaries dropped.

**Recommendation: two pipelines, not one.** OTLP for traces, native `client_golang` + `promhttp`
for metrics. They are independent concerns with independent transports, and unifying them means
adopting a pre-1.0 module plus a lossy translation to buy a tidiness that nobody operating the
service will notice. Measured, the native-Prometheus stack is also the *smaller* one.

Runtime metrics come free from `collectors.NewGoCollector()` and
`collectors.NewProcessCollector()` in the snippet above — no extra dependency needed.

---

## 3. Runtime log level via `slog.LevelVar`

**Confirmed, from the Go source at `$GOROOT/src/log/slog`, not from docs.**

`LevelVar` is a single `atomic.Int64` (`level.go`):

```go
// A LevelVar is a [Level] variable, to allow a [Handler] level to change
// dynamically.
// It implements [Leveler] as well as a Set method,
// and it is safe for use by multiple goroutines.
// The zero LevelVar corresponds to [LevelInfo].
type LevelVar struct {
	val atomic.Int64
}
```

The dynamic behaviour is guaranteed because handlers re-read the level per record. From
`HandlerOptions` in `handler.go`:

> The handler calls `Level.Level` for each record processed; to adjust the minimum level
> dynamically, use a `LevelVar`.

And the implementation confirms it (`handler.go:222`):

```go
func (h *commonHandler) enabled(l Level) bool {
	minLevel := LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return l >= minLevel
}
```

So a `Set` takes effect on the **existing** logger with no rebuild, no lock, no handler swap. The
assumption holds exactly.

Free bonus: `Level` implements `encoding.TextUnmarshaler`, and its `parse` accepts
case-insensitive `DEBUG`/`INFO`/`WARN`/`ERROR` plus a numeric offset like `INFO+2`
(`level.go`). `Level.String()` round-trips the same format. So parsing and rendering the level is
stdlib, not yours.

### Smallest correct wiring

Written, compiled and **tested** (test asserts a live logger's `Debug` goes from suppressed to
emitted across the PUT, with no logger rebuild):

```go
// Handler serves GET (read) and PUT (set) of the level held by lvl.
func Handler(lvl *slog.LevelVar) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"level": lvl.Level().String()})
		case http.MethodPut:
			var body struct{ Level string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var l slog.Level
			if err := l.UnmarshalText([]byte(body.Level)); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			lvl.Set(l)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, PUT")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}
```

**36 lines including imports. Zero dependencies.**

Spring Boot's [`/actuator/loggers`](https://docs.spring.io/spring-boot/reference/actuator/loggers.html)
does more: per-logger-name levels, `GET /actuator/loggers/{name}`, a `configuredLevel` vs
`effectiveLevel` distinction, `POST {"configuredLevel": "DEBUG"}`, and `null` to reset. Per-logger
levels have **no stdlib equivalent** — `slog` has one level per handler, not a logger hierarchy.
Matching Spring here would mean building a name-keyed level tree and a custom `Handler`. That is a
real feature gap, and a deliberate one to decline for v1: one global level covers the operational
case (turn on debug, look, turn it off).

**Do not use OTel logs for this.** `otel/log` and `otel/sdk/log` are **v0.21.0 and Beta**
(§1). `log/slog` is stdlib and stable.

---

## 4. Health and readiness: buy or build?

### Survey of existing Go health libraries

Maintenance data read from the GitHub API on 2026-08-24:

| Library | Stars | Last push | Archived | Notes |
|---|---|---|---|---|
| [alexliesenfeld/health](https://github.com/alexliesenfeld/health) | 836 | 2026-06-15 | no | **v0.8.1**, `go 1.18`, zero runtime deps, 1055 LOC |
| [InVisionApp/go-health](https://github.com/InVisionApp/go-health) | 747 | 2024-12-23 | **yes** | archived |
| [vmware-archive/healthcheck](https://github.com/vmware-archive/healthcheck) (was heptiolabs) | 684 | 2021-11-23 | **yes** | archived |
| [hellofresh/health-go](https://github.com/hellofresh/health-go) | 590 | 2026-04-17 | no | active |
| [AppsFlyer/go-sundheit](https://github.com/AppsFlyer/go-sundheit) | 561 | 2026-07-14 | no | active |
| [dimiro1/health](https://github.com/dimiro1/health) | 450 | 2023-11-18 | no | stale |
| [etherlabsio/healthcheck](https://github.com/etherlabsio/healthcheck) | 274 | 2023-12-13 | no | stale |

The best of them is `alexliesenfeld/health`, and it is genuinely well built: sync `WithCheck` and
cached `WithPeriodicCheck` modes, per-check timeouts, 1-second default cache, 200/503 JSON output
([README](https://github.com/alexliesenfeld/health/blob/main/README.md)). Its
[`go.mod`](https://github.com/alexliesenfeld/health/blob/main/go.mod) requires only `testify`, so
its runtime dependency cost is **zero**.

Against it: it is **v0.8.1 — pre-1.0**, so it carries the same "anything may change" caveat as the
OTel `v0.x` modules, and go-boot would be re-exporting a third party's `Check` struct through its
own public API.

### Building it: measured at 79 lines

I wrote a minimum Registry with `Live()` / `Ready()` / `Draining()`, compiled it, ran `go vet`, and
ran `go test -race -count=5` including a case with 8 concurrent failing checks. **79 lines**,
stdlib only. (The first draft had a real data race on the aggregate `ok` flag that `-race` did not
catch with a single failing check — worth noting that this code is *small*, not *trivial*, and
needs its concurrency test.)

The under-100-lines estimate in the ticket is **correct**.

**Recommendation: build it.** The library saves ~79 lines of stdlib code and costs a pre-1.0 type
in go-boot's public surface. The saving does not clear the bar in
[#1](https://github.com/squall-chua/go-boot/issues/1)'s "one well-known third-party library only
where stdlib clearly falls short". Stdlib does not fall short here.

If periodic/cached checks are wanted later (they matter when a check is slow and the probe period
is 10s), that is the point to revisit `alexliesenfeld/health` — or add caching in another ~15
lines.

---

## 5. Kubernetes probe conventions

From [Configure Liveness, Readiness and Startup Probes](https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/)
and [Pod Probes](https://kubernetes.io/docs/concepts/workloads/pods/probes/):

**Status codes.** For an `httpGet` probe:

> Any code greater than or equal to 200 and less than 400 indicates success. Any other code
> indicates failure.

So the success window is `[200, 400)`. 503 is the natural failure code, and it matches what Spring
Boot returns (§6).

**Defaults** (all seconds except thresholds):

| Field | Default | Meaning |
|---|---|---|
| `initialDelaySeconds` | 0 | wait after container start before first probe |
| `periodSeconds` | 10 | probe interval |
| `timeoutSeconds` | **1** | probe response timeout |
| `successThreshold` | 1 | consecutive successes to pass |
| `failureThreshold` | 3 | consecutive failures before action |

`successThreshold` **must be 1** for liveness and startup probes.

The `timeoutSeconds: 1` default is the one that bites: a readiness handler that synchronously pings
a database on every request will flap under load. This argues for the probe handler being fast and
for slow checks being cached — noted above as the future case for periodic checks.

**Semantics, and what failure does:**

| Probe | Runs | On failure |
|---|---|---|
| **startup** | at startup only; other probes are disabled until it passes | kubelet kills the container, restart policy applies |
| **liveness** | periodically, whole lifecycle | kubelet **restarts the container** after `failureThreshold` |
| **readiness** | periodically, whole lifecycle | Pod IP removed from EndpointSlices; Services stop routing to it. **No restart.** |

The design rule falls straight out of that table: **liveness must never check an external
dependency.** If the database is down and liveness checks it, every replica restarts in a loop and
turns a dependency outage into an outage of your own. Readiness is where dependency checks belong.
The 79-line implementation encodes this — `Live()` returns 200 unconditionally, and the test
asserts liveness stays 200 while readiness is 503.

**Startup probe:** point it at the readiness path with a generous `failureThreshold` (the docs'
example uses `failureThreshold: 30, periodSeconds: 10` for a 5-minute budget). A separate endpoint
is not required.

**Draining:** `Draining()` flips readiness to 503 while liveness stays 200, so on SIGTERM the Pod
leaves the load-balancer rotation without being restarted. This is the piece hand-rolled Go
services most often miss, and it is 3 lines.

---

## 6. Spring Boot's health groups, and how they map

From the [Actuator endpoints reference](https://docs.spring.io/spring-boot/reference/actuator/endpoints.html):

Spring Boot pre-configures two health **groups** with dedicated endpoints:

- `GET /actuator/health/liveness` — backed by `LivenessState` / `LivenessStateHealthIndicator`
- `GET /actuator/health/readiness` — backed by `ReadinessState` / `ReadinessStateHealthIndicator`

Status → HTTP mapping:

| Status | HTTP |
|---|---|
| `UP` | 200 |
| `UNKNOWN` | 200 |
| `DOWN` | 503 |
| `OUT_OF_SERVICE` | 503 |

Lifecycle states:

| Phase | LivenessState | ReadinessState |
|---|---|---|
| Starting | `BROKEN` | `REFUSING_TRAFFIC` |
| Started (context refreshing) | `CORRECT` | `REFUSING_TRAFFIC` |
| Ready | `CORRECT` | `ACCEPTING_TRAFFIC` |

Crucially the docs carry the same warning as the Kubernetes docs:

> Do not add external system checks (databases, APIs) to the liveness probe — only the readiness
> probe should check external dependencies.

By default the readiness group does **not** include external system checks; you opt them in via
`management.endpoint.health.group.readiness.include`.

**Mapping onto go-boot:** the group mechanism is Spring solving a problem go-boot does not have.
Spring needs configurable groups because auto-configuration registers indicators you did not ask
for, so you need a property language to filter them back out. go-boot registers checks explicitly
in plain Go. Two fixed groups — liveness (nothing) and readiness (everything registered) — cover
the same ground with no configuration surface at all.

`/livez` and `/readyz` are better path names than `/actuator/health/liveness`: they match the
convention Kubernetes uses for its own components, and Spring Boot itself now offers
`add-additional-paths: true` to expose exactly `/livez` and `/readyz`.

---

## 7. Measured dependency weight

Built in a scratch module tree (Go 1.26.3, linux/amd64), one `go.mod` per variant,
`go mod tidy` then `go build`. Not in the go-boot repo.

| Variant | `go.mod` requires (direct/indirect) | `go.sum` lines | Binary MB | Stripped MB | Δ stripped vs baseline |
|---|---|---|---|---|---|
| baseline (`net/http` + `log/slog`) | 0 / 0 | 0 | 8.41 | 5.82 | — |
| prom (`client_golang` + `promhttp`) | 1 / 8 | 36 | 14.58 | 10.00 | **+4.18** |
| otel-grpc (traces + `otelhttp`) | 4 / 19 | 65 | 21.99 | 15.24 | +9.41 |
| otel-http (traces + `otelhttp`) | 4 / 19 | 65 | 21.84 | 15.14 | +9.31 |
| otel-prom (OTel metrics → prom exporter) | 3 / 17 | 61 | 17.09 | 11.74 | +5.92 |
| **full** (OTLP gRPC traces + native prom) | **5 / 24** | **83** | **23.07** | **15.97** | **+10.14** |
| full-otelprom (OTLP traces + OTel→prom) | 7 / 25 | 89 | 24.38 | 16.87 | +11.04 |
| otel-autoexport | 4 / 40 | 115 | 25.03 | 17.29 | +11.46 |

Readings:

- **Prometheus alone is cheap**: +4.2 MB stripped, 9 modules.
- **OTLP tracing is where the weight is**: +9.4 MB stripped, and the gRPC-vs-HTTP exporter choice is
  a **0.1 MB wash** — `otlptracehttp` still pulls the gRPC tree via `go.opentelemetry.io/proto/otlp`.
- **Prometheus is nearly free once OTel is in**: `full` is only +0.7 MB stripped over `otel-grpc`
  alone.
- **`full-otelprom` costs +0.9 MB over `full`** *and* puts the pre-1.0 `exporters/prometheus`
  v0.67.0 into the API-stability surface, to buy one pipeline instead of two. Bad trade.
- **`autoexport` is the worst trade**: +1.3 MB and +32 `go.sum` lines over `full`, for env-var
  exporter selection.

Caveat on module counting: `go list -m all` reports far larger numbers (62–97) because it walks the
pruned module graph including test-only dependencies of dependencies. The `go.mod` require count is
the honest "what gets compiled into your binary" figure and is what the table uses.

**Not measured** (flagged as gaps): runtime memory/RSS overhead of the SDK, CGO/static-link
variants, and cold-start latency of the batch span processor.

---

## 8. Does any Go project already ship an Actuator?

Searched, then checked maintenance via the GitHub API:

| Project | Stars | Last push | Assessment |
|---|---|---|---|
| [sinhashubham95/go-actuator](https://github.com/sinhashubham95/go-actuator) | **3** | 2025-03-19 | endpoints `/env /info /health /metrics /ping /shutdown /threadDump`; effectively unused |
| [malike/spring-boot-go-actuator](https://github.com/malike/spring-boot-go-actuator) | **0** | **2018-08-31** | dead, no licence |
| [mikeyGlitz/gohealth](https://gitlab.com/mikeyGlitz/gohealth) ("Goctuator") | n/a | n/a | GitLab, low profile; **unverified** — not maintenance-checked |

Adjacent frameworks bundle *some* of it: [Kratos](https://github.com/go-kratos/kratos) ships
OpenTelemetry tracing and Prometheus metrics middleware, and [go-kit](https://gokit.io/faq/) ships
metrics adapters. Neither presents a coherent operational endpoint group, and both are full
microservice frameworks — adopting either contradicts #1's "thin composable library" shape.

**Nothing here is worth copying or adopting.** The two direct Actuator clones have 3 and 0 stars,
which is the ecosystem's verdict, not mine.

---

## 9. What the stdlib already gives free

Worth recording so the Actuator does not grow dependencies it does not need:

- **Build info** — `runtime/debug.ReadBuildInfo()` returns `GoVersion`, `Path`, `Main` (module path
  and version), `Deps`, and `Settings`. The Go toolchain stamps `vcs.revision`, `vcs.time` and
  `vcs.modified` into `Settings` automatically
  ([`cmd/go/internal/load/pkg.go`](https://github.com/golang/go/blob/master/src/cmd/go/internal/load/pkg.go)).
  That is Spring's `/actuator/info` with **zero dependencies and no build-time ldflags**.
- **Level parsing/formatting** — `slog.Level.UnmarshalText` / `String` (§3).
- **Routing** — `net/http` method + wildcard patterns (Go 1.22+), so no router dependency for the
  Actuator's own paths.
- **Runtime and process metrics** — `collectors.NewGoCollector()` /
  `NewProcessCollector()`, already inside `client_golang`, no extra module.

---

## 10. Recommended default observability stack

| Concern | Choice | Why |
|---|---|---|
| **Tracing** | `go.opentelemetry.io/otel` v1.45.0 + `sdk` + `otlptracegrpc`, wrapped with `otelhttp` | traces are Stable; ~15 lines of setup; env-var configured |
| **Trace exporter default** | **OTLP/gRPC**, one-line swap to OTLP/HTTP | measured cost identical; gRPC is already in go-boot's tree via the gRPC Starter |
| **Metrics** | `github.com/prometheus/client_golang` v1.24.1, `promhttp` + `collectors.*` | what Prometheus itself documents; fully stable module; smaller than the OTel path |
| **One pipeline for both?** | **No** | costs +0.9 MB, a pre-1.0 module, and a documented-lossy translation |
| **Logging** | stdlib `log/slog` + `*slog.LevelVar` | stable stdlib; OTel logs are Beta / v0.21.0 |
| **Log level endpoint** | write it — 36 lines | verified working; one global level, not Spring's per-logger tree |
| **Health / readiness** | **write it — 79 lines**, stdlib only | best library is pre-1.0 and saves less than it costs in public API surface |
| **Probe paths** | `/livez`, `/readyz` | Kubernetes convention; Spring Boot offers the same names |
| **Build info** | `runtime/debug.ReadBuildInfo()` | stdlib, zero deps, VCS stamped automatically |
| **`autoexport`** | **skip** | worst measured trade in the survey |
| **Go minimum** | **raise 1.22 → 1.25** | forced; see §0 |

Total measured cost of this stack over a bare `net/http` service: **+10.1 MB stripped, 5 direct and
24 indirect modules, 83 `go.sum` lines.** Roughly **130 lines** of go-boot code sit on top
(15 tracing + 36 log level + 79 health).

---

## 11. Verdict on the premise

The parent session's claim was: *"Go has a real hole here and everyone hand-rolls it badly."*

**Half of that is wrong, and the wrong half is the load-bearing half.**

**Where the premise fails.** Every primitive an Actuator needs already exists, is mature, and is
well documented:

- OTel Go traces and metrics are **Stable** at v1.45.0, and tracing setup is **15 lines**, not the
  90-line slab the getting-started guide implies.
- `prometheus/client_golang` is stable, current (v1.24.1, July 2026), and Prometheus's own docs
  hand you a 5-line `/metrics`.
- `slog.LevelVar` does exactly what the ticket assumed — confirmed in the stdlib source, tested
  against a live logger.
- Kubernetes probes want nothing more exotic than a 200 or a 503.
- Build info is one stdlib call.

There is **no capability gap**. Anyone claiming Go can't do this has not looked recently.

**Where the premise holds.** The gap is **assembly, not capability**, and it is narrower than
"everyone hand-rolls it badly" — but it is real:

1. **Nothing ships the bundle.** The two direct Actuator clones have 3 and 0 stars and one was last
   touched in 2018 (§8). The health libraries are all pre-1.0 and stop at health.
2. **The pieces come from five places** — `otel`, `otel/sdk`, `otlptracegrpc`, `otelhttp`,
   `client_golang`, `log/slog`, `runtime/debug` — with no single doc telling you which combination
   is correct, or that `otelhttp` is still v0.70.0 while `otel` is v1.45.0.
3. **The defaults that matter are judgement calls, and they are the ones people get wrong.**
   Liveness must not check dependencies. Readiness must flip on SIGTERM without restarting.
   `timeoutSeconds` defaults to 1. Those three mistakes turn a dependency blip into a
   restart storm, and none of them are visible from any single library's README.

**The honest caution.** The go-boot code that closes this gap is about **130 lines**. That is a real
convenience and a real correctness win — item 3 above is worth more than the line count suggests —
but it is **not a deep moat**. The Actuator's value is *curation and correct defaults*, not volume
of code. It should be specified and sold that way, and #1's "headline feature" framing should be
adjusted to match: the Actuator is the feature users will *notice* first, not the feature that is
*hardest* to build.

**And one thing the research changed outright:** the Go 1.22 floor in #1 cannot survive contact with
this stack (§0). That needs an amendment before anything else in this area is specified.

---

## Unverified / open items

- **Goctuator** (`gitlab.com/mikeyGlitz/gohealth`) was not maintenance-checked — GitLab, and the
  GitHub API survey did not reach it. Low expected value given the category.
- **Runtime overhead** of the OTel SDK (RSS, allocation rate, batch-processor latency) was not
  measured — only binary size and module count.
- **`hellofresh/health-go` and `AppsFlyer/go-sundheit` APIs** were not read in detail; they were
  ruled out on the same pre-1.0 / stdlib-suffices reasoning as the leader, not on their merits.
- **OTel logs bridge for slog** (`otelslog`) was not evaluated — it depends on `otel/log` v0.21.0,
  which is Beta, so it is out of scope for v1 regardless.
- **gRPC Transport tracing** (`otelgrpc` interceptors) was not costed; only HTTP was measured.
- Spring Boot doc pages were read at their current published version; the reference URLs are
  unversioned and will drift.
