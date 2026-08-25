# go-boot v1 — the locked spec

Status: **locked**, 2026-08-25. Written for [#15](https://github.com/squall-chua/go-boot/issues/15),
which is the last ticket on the map ([#1](https://github.com/squall-chua/go-boot/issues/1)).

"Locked" means every design decision here is settled and written down. Building go-boot v1 is
typing, not deciding. Anything still open is listed in
[11. Deferred past v1](#11-deferred-past-v1), and nothing in the rest of this file depends on it.

**How to read this file.** Each part says what the code must do and names the ticket that settled
it. The reasons are not repeated here — they live in the ticket and, where a decision is hard to
reverse, in an ADR under `docs/adr/`. Words with a capital letter (Starter, Preset, Component,
Tier, Check, Transport, Service Layer, Actuator, Scaffold, Profile) are defined in `CONTEXT.md`
and are used here with exactly that meaning.

**Evidence.** Every signature below is either compiled in `prototypes/` or copied from the
resolution comment of the ticket that settled it. `prototypes/` is a throwaway module that matches
this spec's contract; `go build`, `go vet`, `go test`, `gofmt -l` and
`prototypes/scripts/check-imports.sh` are all clean as of 2026-08-25. Where the prototype and a
ticket disagree, this file follows the ticket and says so.

---

## 0. What go-boot is, and who it is not for

go-boot gives a Go service the operational spine a Spring Boot developer expects — an **ordered
Component lifecycle** and a **real Actuator** — at roughly **a third of GoFr's weight**, on
**stdlib foundations**, staying **neutral about the query layer**
([#17](https://github.com/squall-chua/go-boot/issues/17)).

**go-boot is not for a single-endpoint HTTP service with no database and no Actuator.**
It has no lifecycle to order, so there is nothing for go-boot to encode. Use `net/http` directly.
Measured in [#2](https://github.com/squall-chua/go-boot/issues/2): at one Component a Preset made
`main` *longer*, and a 78-line stdlib file beat go-boot outright. This sentence belongs on the
README's first screen.

---

## 1. Ground rules

These hold across every Starter.

**One Go module.** Starters are subpackages, not separate modules. Confirmed by measurement in
[#3](https://github.com/squall-chua/go-boot/issues/3): a consumer of the root package alone
downloads 1 zip and 2.9 KB, against 18 zips and 106 MiB for the multi-module layout.

**Module path: `github.com/squall-chua/go-boot`** ([#20](https://github.com/squall-chua/go-boot/issues/20)).
The library packages sit at the repository root, so the base Starter is
`github.com/squall-chua/go-boot` and binds to the identifier `goboot`, and a Starter is
`github.com/squall-chua/go-boot/web`. For brevity this file writes those short forms — `goboot`,
`goboot/web` — everywhere below; they are package identifiers, not literal import paths. No example
in this spec needs an import alias, checked against every call site in `prototypes/`
(`docs/research/module-path.md`).

**Go version floor: `go 1.25.0`** ([#16](https://github.com/squall-chua/go-boot/issues/16)).
Note that `go mod tidy` will write **`go 1.25.7`** into `go.mod`, because `goose/v3` declares a
patch-level `go` directive and the highest floor in the module wins. So the real floor once the
database Starter exists is 1.25.7, and Go 1.25.0 to 1.25.6 cannot build go-boot at all, even for
a user who never imports `goboot/db`. `GOTOOLCHAIN=local`, `GOPROXY=off` and Go below 1.21 are
unsupported; say so in the README. A stock Go 1.22.12 toolchain auto-switches in about 15 seconds,
so the floor does not cut the audience.

**stdlib first.** One well-known third-party library only where the stdlib clearly falls short.
The full list of what go-boot links, and why each one is there, is
[7. Dependencies](#7-dependencies-and-the-ticket-that-chose-each-one).

**The optional-subpackage rule.** Go links by import: a dependency named in a package is paid for
by everyone who imports that package. So **a dependency only some users need lives in a package
they must import.** That rule alone explains `goboot/trace`, `goboot/grpc/health`,
`goboot/grpc/reflection`, `goboot/trace/rpc`, `goboot/db/dbtest` and `goboot/preset/traced`. It is
stated once in `CONTEXT.md` and once here, and never repeated per package.

**Base imports no Starter, and neither do its tests.** `go mod tidy` leaks through test imports,
so a test import in the root package would make every root-only user pay for gRPC
([#3](https://github.com/squall-chua/go-boot/issues/3)). This is checked in CI — see
[8. CI rules](#8-ci-rules-go-boots-own-repo-must-keep).

**No panics in go-boot's own code paths.** Constructors that can fail return an error. `App.Run`
returns an error. Registering an ambiguous route panics, but that is `net/http` doing it at
registration time, which is what you want.

**Package naming avoids stdlib collisions.** `goboot/web`, not `goboot/http`, because every `main`
imports `net/http` and the alias would appear in every example (ADR `0005`). Same reason
`goboot/grpc/reflection` is not `reflect`.

---

## 2. The Component lifecycle contract

Settled in [#8](https://github.com/squall-chua/go-boot/issues/8). ADR `0001`.

**The core move: start order stops being something `main` can express.** A Component declares its
own Tier. `App` sorts by Tier. The order passed to `Add` is ignored, so wiring four Components in
the wrong order is not a mistake a developer can make.

```go
package goboot

// Tier fixes when a Component starts. Start runs low tier to high; stop runs
// the reverse. A Component declares its own Tier, so the wiring order in
// main cannot be wrong.
type Tier int

const (
	TierObserve   Tier = iota // Actuator, tracing. Starts first, stops last.
	TierResource              // database pool, cache. Starts before Transports.
	TierTransport             // HTTP, gRPC, consumers. Starts last, stops first.
)

type Component interface {
	Name() string
	Tier() Tier
	// Start returns once the Component is ready and must not block. The
	// channel reports a failure that happens after startup; return nil if
	// that cannot happen.
	Start(ctx context.Context) (<-chan error, error)
	Stop(ctx context.Context) error
}

// Drainer is optional: stop taking new work. Runs in START order, before any Stop.
type Drainer interface{ Drain(ctx context.Context) }

// Checker is optional: the Actuator registers it under Name(). Nothing is
// wired in main.
type Checker interface{ Check(ctx context.Context) error }
```

### What `Run` does, in order

1. Stable sort by Tier. Reject duplicate names. Start each Component, with the whole sequence
   inside `StartTimeout`. On a start failure: stop the started ones in reverse, with **no drain and
   no drain delay** — the pod never passed readiness, so no load balancer is sending traffic.
   Return the start error joined with any stop errors.
2. `Ready()` turns true. Watch every death channel.
3. Wait for SIGINT or SIGTERM, or for a death. A death is fatal. Then restore Go's default signal
   handling, so a second signal kills the process outright.
4. Drain phase: `Ready()` turns false, stop watching the death channels, run every `Drainer` in
   **start order**, then sleep `DrainDelay`.
5. Stop in reverse, inside `StopTimeout`, joining errors.

**Drain runs in start order, not reverse.** Reverse order drains the Actuator last, so the 503
lands after the Transports have already let go. Announce first, tear down last. The prototype had
this backwards; it was a latent bug.

**A Component that cannot die after startup returns a nil channel.** A nil channel blocks forever,
which is free and correct.

**One-shot work is not a Component.** Work that runs once belongs inside a `Start`, the way the
database Starter runs migrations. With death being fatal, a one-shot that finished cleanly would
look like a crash. There is no `Job` type and no `goboot.Main` helper.

### Timeouts

| Key | Default | Covers |
|---|---|---|
| `lifecycle.startTimeout` | 30s | the whole start sequence, all Components |
| `lifecycle.drainDelay` | 5s | the pause after Drain, so a load balancer sees the 503 |
| `lifecycle.stopTimeout` | 10s | the whole stop sequence, all Components |

One budget per phase for every Component, not one budget each. 5s plus 10s fits inside Kubernetes'
30s default grace period with room to spare. The 10s stop timeout is also what cuts a long-lived
gRPC stream, because `http.Server.Shutdown` waits for open streams until its context expires.

---

## 3. Config

Settled in [#9](https://github.com/squall-chua/go-boot/issues/9). ADR `0002`.

**Config is not a Starter and not a Component.** It must be loaded before `goboot.New`, so it has
no lifecycle to take part in. It is one plain function in the base package.

```go
package goboot

// Load fills out from embedded defaults, then a file on disk, then
// PREFIX-scoped environment variables. Later sources win.
//
// defaults may be nil. path names the file in both layers: the embedded
// lookup uses its base name, the disk lookup uses it whole.
func Load(defaults fs.FS, path, prefix string, out any) error
```

```go
//go:embed app.yaml
var defaults embed.FS

goboot.Load(defaults, "app.yaml", "ORDERS_", &cfg)
```

### Precedence

Outside beats inside, profile beats base, environment beats everything.

| Layer | Required? |
|---|---|
| struct pre-fill (a Starter's own defaults, written in Go) | — |
| embedded `app.yaml` | must exist when `defaults` is non-nil |
| embedded `app-<profile>.yaml` | optional |
| disk `app.yaml` | required only when `defaults` is nil |
| disk `app-<profile>.yaml` | optional |
| `ORDERS_*` environment variables | — |

The order is fixed. There is no flag layer.

### Binding

One `mapstructure` decode over a merged `map[string]any`, at the end.

| Rule | Behaviour |
|---|---|
| Key matching | **relaxed**: lowercase both sides, drop `-` and `_`. `readHeaderTimeout`, `read-header-timeout` and `READ_HEADER_TIMEOUT` are one key |
| Canonical spelling in docs | camelCase, under the existing `yaml:` tag |
| Unknown key | startup error naming the path: `'lifecycle' has invalid keys: stoptimeuot` |
| Lists | bare commas when the target field is a slice, or YAML flow `[a, b, c]`. A string field keeps its commas |
| Durations | `12s` |
| Slice override | replaces the whole slice, never merged element by element |
| Env nesting | `__` splits sections; a single `_` is part of a name |

Relaxed key matching and type-directed comma lists are both Spring Boot's rules, and go-boot's
audience is Spring developers. **This is a public promise**: once config files rely on it, it
cannot be tightened (ADR `0002`).

### Formats

The file extension picks the parser. `.yaml` and `.yml` go to YAML, `.properties` to a
hand-written parser, anything else is an error naming the extension. One file, one format; there
is no searching and no precedence between formats. The profile file keeps the base file's
extension.

The `.properties` subset that is supported:

| Supported | Not supported |
|---|---|
| `#` and `!` comment lines | `\uXXXX` escapes |
| `=` or `:` separator, whichever comes first | backslash line continuations |
| whitespace trimmed around key and value | escaped separators inside a key |
| `.` splits the key into nested sections | `servers[0]` indexed keys — use YAML, or bare commas |
| typed values, so `10s`, `true` and `8080` all work | |

### Profiles

`<PREFIX>PROFILE` selects **one** profile, not a list. It is removed before the environment merge,
so `profile` is a reserved key. `ORDERS_PROFILE=local` loads `app.yaml` then `app-local.yaml` in
each layer. A missing profile file is fine.

The case profiles are for is **local development** — a committed `app.yaml` beside a git-ignored
`app-local.yaml`. In Kubernetes you ship one image and vary it with a ConfigMap and environment
variables, where profiles buy nothing.

### The prefix

**The prefix belongs to the service, not to go-boot.** Documentation uses `ORDERS_`. `GB_` in the
prototype was a placeholder and must not appear in docs — two go-boot services on one host must
not share a namespace.

**An empty prefix is an error.** The environment layer claims every variable it sees and unknown
keys are a startup error, so with no prefix `PATH` and `HOME` become config keys and nothing ever
boots.

### Deliberately not in v1

Write these in the config documentation as choices, so nobody files them as gaps.

- **Flags.** `flag` needs every key declared up front, so a working flag layer means the user
  hand-writing fifteen `flag.String` lines. If it is ever wanted it arrives as a second entry
  point.
- **A `Validate() error` hook.** The decoder already reports unknown keys and type mismatches.
  Anything semantic — an unreachable DSN, a bound port — belongs in the Starter's `Start`, which
  already returns an error.
- **An effective-config endpoint, and effective-config logging.** Both print the database password
  — one to an ops URL, the other into a log aggregator that keeps it for ninety days.
- **A secret-store hook.** Secrets come from environment variables.
- **Hot reload.** Config is immutable after load. The runtime log level is not a counter-example:
  it mutates a `slog.LevelVar` and never re-reads the file.
- **JSON and TOML.** A second supported format is a second thing to document, for no user who
  asked.

---

## 4. The public API of every v1 Starter

**There are six Starters: base, actuator, web, grpc, db, trace.** Plus four optional subpackages
that exist only to hold a dependency: `goboot/grpc/health`, `goboot/grpc/reflection`,
`goboot/trace/rpc`, `goboot/db/dbtest`.

### 4.1 `goboot` — the base Starter

Config, logging, the Component lifecycle, graceful shutdown, and the request-scoped logger.

```go
package goboot

type LogConfig struct {
	Level  string `yaml:"level"`  // slog level name; default INFO
	Format string `yaml:"format"` // "text" or "json"; default text
}

type LifecycleConfig struct {
	StartTimeout time.Duration `yaml:"startTimeout"` // 30s
	DrainDelay   time.Duration `yaml:"drainDelay"`   // 5s
	StopTimeout  time.Duration `yaml:"stopTimeout"`  // 10s
}

type Config struct {
	Log       LogConfig       `yaml:"log"`
	Lifecycle LifecycleConfig `yaml:"lifecycle"`
}

func New(cfg Config) (*App, error)

func (a *App) Add(c ...Component)
func (a *App) Checks() map[string]Checker // the Actuator pulls these
func (a *App) Ready() bool
func (a *App) Start(ctx context.Context) error
func (a *App) Stop(ctx context.Context) error
func (a *App) Run(ctx context.Context) error // Start, wait, Stop

// App fields
//   Log   *slog.Logger
//   Level *slog.LevelVar   // the Actuator's /actuator/loglevel writes this

func Load(defaults fs.FS, path, prefix string, out any) error

// WithLogger is what the web Starter's Logging middleware calls, and what
// the gRPC interceptor will call.
func WithLogger(ctx context.Context, log *slog.Logger) context.Context

// LoggerFrom returns the request-scoped logger, already carrying the request
// ID. It never returns nil; it falls back to slog.Default().
func LoggerFrom(ctx context.Context) *slog.Logger
```

`App.Start` and `App.Stop` are public and signal-free so a test can call them directly. `Run` is
`Start`, wait for a signal or a death, then `Stop`. There is no `App.Wait` until a test actually
needs to watch a mid-life death.

**`LoggerFrom` lives in base on purpose.** Both Transports then share one context key, so a
Service Layer function that logs behaves the same whichever Transport called it. A context key and
a lookup function import nothing, so the hard rule is not broken.

> **Prototype note.** `prototypes/goboot` still has `New(cfg LogConfig)` with the three timeouts as
> public `App` fields, and `Checks()` returning `map[string]func(context.Context) error`. #8's
> resolution is the authority and this spec follows it: `New(cfg Config)` with a `Lifecycle`
> section, and `Checks()` returning `map[string]Checker`. The prototype stub is behind on this one
> point and everything else matches.

### 4.2 `goboot/actuator`

Settled in [#10](https://github.com/squall-chua/go-boot/issues/10). ADR `0003`.

```go
package actuator

type Config struct {
	// Addr moves the Actuator to a private listener it owns. Empty means it
	// shares the application's port under /actuator.
	Addr string `yaml:"addr"`
	// Expose is a whitelist. Anything not named is never registered and
	// answers 404. Default: livez, readyz, info.
	Expose []string `yaml:"expose"`
	// ShowDetails is "never" (default) or "always". Spring's key name.
	ShowDetails string `yaml:"showDetails"`
}

// Handler is the structural interface MountOn takes. *web.Server satisfies
// it, so the Actuator imports no Starter.
type Handler interface {
	Handle(pattern string, h http.Handler)
}

type Check func(context.Context) error

func New(cfg Config, app *goboot.App) *Actuator
func (a *Actuator) MountOn(h Handler)

func (a *Actuator) Name() string                              // "actuator"
func (a *Actuator) Tier() goboot.Tier                         // TierObserve
func (a *Actuator) Start(ctx) (<-chan error, error)           // pulls app.Checks()
func (a *Actuator) Drain(ctx context.Context)                 // /readyz goes 503
func (a *Actuator) Stop(ctx context.Context) error
```

**The Actuator shares the application's port**, the way Spring Boot does when
`management.server.port` is unset. `actuator.addr` moves it to a private listener that it binds
itself in `Start`. `MountOn` is one line in `main` that is correct in both modes; the config alone
decides where the endpoints live.

**`actuator.expose` is a whitelist and it defaults to `livez,readyz,info`.** An endpoint that is
not named is never registered, so it answers **404, not 403** — there is nothing for a wrong
Ingress rule to leak. The whitelist applies in both port modes. An entry naming an endpoint that
does not exist is a **startup error**.

**Metrics are 404 until you opt in. This belongs on the first line of the Actuator's
documentation, not the last.** It is the one thing that will surprise people.

#### Paths

| Path | Method | Exposed by default |
|---|---|---|
| `/actuator/livez`, and `/livez` | GET | yes |
| `/actuator/readyz`, and `/readyz` | GET | yes |
| `/actuator/info` | GET | yes |
| `/actuator/metrics` | GET | no |
| `/actuator/loglevel` | GET, PUT | no |
| `/actuator/pprof/*` | GET | no |

`/livez` and `/readyz` also answer at the root, because Kubernetes is what reads them and those
are the names its own components use. One entry in `expose` governs both the prefixed path and the
root alias: they are one endpoint with two names.

`/actuator/metrics` rather than `/metrics`, so it cannot collide with a user who already scrapes
their own.

#### Health and readiness

- **`/livez` never runs a Check.** It answers `{"status":"UP"}` unconditionally. A liveness test
  that touches a dependency turns a database outage into a restart storm.
- **`/readyz` runs every Check on each request, synchronously.** No background ticker, no cached
  result, no staleness window. It is 503 unless `app.Ready()` is true *and* every Check passes.
- **The Actuator adds no timeout of its own.** Checks get the request context, which already
  carries the probe's real deadline — the operator's number, not a guess.
- **A Check must respect its context.** One that ignores cancellation blocks a goroutine on every
  probe. This is a contract, and it is stated in `CONTEXT.md` next to the Component definition.
- **The body is bare by default**: `{"status":"UP"}` or `{"status":"DOWN"}` with 503. With
  `showDetails: always` the body carries the full detail, error strings included:

  ```json
  {"status":"DOWN","checks":{"db":"DOWN: dial tcp 10.0.0.5:5432: connection refused"}}
  ```

  Two values, not Spring's three: `when-authorized` needs Spring Security, which go-boot has no
  equivalent of in v1. The documentation must warn that `always` can print a database host.
  Whichever way `showDetails` is set, a failing Check is logged at WARN with full detail.

#### Metrics

**No Prometheus type appears in go-boot's public API.** There is no `Actuator.Registry`. The
Actuator serves `promhttp.Handler()`, which reads `prometheus.DefaultGatherer`. A user adds a
metric with plain `promauto.NewCounter(...)` or `prometheus.MustRegister(...)` — the code every Go
service already writes, mentioning go-boot nowhere. Measured: the default registry already carries
**38 metric families**, including `go_goroutines` and `process_cpu_seconds_total`, with no
registration code.

The cost is a package-level global, so two Apps in one process would share one registry. go-boot
is one service per process, so nobody pays it.

#### Deliberate omissions

Write these in the Actuator's documentation as choices.

- No effective-config endpoint (it holds the database password).
- No discovery index at `/actuator`. go-boot has a fixed list of endpoints in its documentation;
  Spring's index exists to find endpoints auto-configuration registered behind your back.
- No health groups. Two fixed sets, liveness and readiness, with no configuration surface.
- No `/shutdown`. A remote kill switch on a public port is not an ops tool.
- No thread or heap dump endpoints. pprof covers the same ground and is opt-in.

### 4.3 `goboot/web` — the HTTP Transport Starter

Settled in [#11](https://github.com/squall-chua/go-boot/issues/11). ADRs `0004`, `0005`.

```go
package web

type Config struct {
	Addr              string        `yaml:"addr"`              // ":8080"
	ReadHeaderTimeout time.Duration `yaml:"readHeaderTimeout"` // 5s
	IdleTimeout       time.Duration `yaml:"idleTimeout"`       // 120s
	MaxBodyBytes      int64         `yaml:"maxBodyBytes"`      // 1 MiB
	TLS               struct {
		CertFile string `yaml:"certFile"`
		KeyFile  string `yaml:"keyFile"`
	} `yaml:"tls"`
}

type Middleware = func(http.Handler) http.Handler

func New(cfg Config, log *slog.Logger) *Server

// Handle takes the two-value return of a connect-go constructor unchanged.
func (s *Server) Handle(pattern string, h http.Handler)
func (s *Server) HandleFunc(pattern string, h http.HandlerFunc)
// Use appends. The FIRST entry listed ends up outermost.
func (s *Server) Use(mw ...Middleware)
func (s *Server) Addr() string // the bound address, useful after ":0"

func (s *Server) Name() string                       // "web"
func (s *Server) Tier() goboot.Tier                  // TierTransport
func (s *Server) Start(ctx) (<-chan error, error)
func (s *Server) Stop(ctx context.Context) error

// DefaultMiddleware is a slice you can edit, not hidden behaviour.
// Order: RequestID, Logging, Recovery.
func DefaultMiddleware(log *slog.Logger) []Middleware
func RequestID(next http.Handler) http.Handler
func Logging(log *slog.Logger) Middleware
func Recovery(log *slog.Logger) Middleware

// Helpers the user calls. Not a handler signature.
func WriteProblem(w http.ResponseWriter, status int, detail string)
func WriteJSON(w http.ResponseWriter, status int, v any)
func DecodeJSON(r *http.Request, out any) error
```

**Router: stdlib `net/http` only.** Measured on Go 1.26.3, `http.ServeMux` already gives 405 with
an `Allow` header, `HEAD` following `GET`, `{$}` anchoring, a registration-time panic on ambiguous
patterns naming both files and lines, and `r.Pattern`. `r.Pattern` is the important one: it is the
low-cardinality route label that metrics and span names need, and it is free.

**Handlers stay `http.HandlerFunc`** (ADR `0004`). There is no `func(w, r) error`, no go-boot error
type a handler returns, and no request or response wrapper. Everything go-boot offers is a plain
function called from inside an ordinary handler. That is why `otelhttp` and every other middleware
work unchanged, and why a validation library needs no wiring — `validator` was measured at 8
modules and +3.11 MB and refused.

**Middleware order.** `Use` is variadic and **the first entry listed is the outermost**, which is
how the line reads. `Use` appends, so **a later `Use` call lands innermost** — pinned by
`TestUseOrder`. This is not a detail: it is why tracing cannot be added after the fact. See
[4.6 `goboot/trace`](#46-goboottrace).

**Recovery is not optional, and it sits inside Logging.** Measured: a panicking handler on a bare
`net/http` server gives the client `EOF` — no response at all — so the caller sees a network fault
rather than a bug in the service. Recovery inside Logging means the 500 it writes passes back out
through the logging wrapper and is recorded as a 500.

**RequestID** is generated from `crypto/rand`, no dependency. It honours an inbound `X-Request-Id`
**only up to a length and character-set cap** — an unbounded attacker-controlled string flowing
into every log line is a log-injection hole.

**Request logging.** One line per request, on the way out, INFO normally and **ERROR for 5xx** so a
server error is findable by level alone:

```
level=INFO msg=request method=GET path=/users/7 route="GET /users/{id}"
  status=200 bytes=142 duration=1.4ms requestId=9f2c...
```

Both `path` and `route`: `path` is what was asked for, `route` is `r.Pattern`, which is what you
group by.

**Probe paths are not logged.** `/livez`, `/readyz` and `/actuator/*` are skipped. Kubernetes probes
the first two every 10 seconds, which is roughly **17,000 log lines a day** saying nothing. Three
hardcoded paths, not a config key — those paths belong to go-boot. Document it, or someone will
wonder why probe traffic is invisible.

**Errors on the wire: RFC 7807.** `Content-Type: application/problem+json`, with `type`, `title`,
`status` and `detail`. A struct and a content type, no dependency.

**No `instance`.** RFC 7807 makes it optional, and `WriteProblem(w, status, detail)` takes no
request, so there is nothing to build the URI from. Adding one would mean
`WriteProblem(w, r, status, detail)` — a longer line at every call site for a member few readers
look at. The `X-Request-Id` on the response already answers "which occurrence was this". Spring Boot has shipped
`ProblemDetail` since 3.0, and the audience is Spring developers. The recovery middleware uses the
same `WriteProblem`, so a panic and a hand-written 400 come out in the same shape.

**`DecodeJSON` is not a one-liner wrapper.** `json.NewDecoder(r.Body).Decode(&v)` is one line but
it is not the correct one. The correct one wraps the body in `http.MaxBytesReader` so a 4 GB POST
cannot exhaust memory, sets `DisallowUnknownFields`, and turns the decoder's error into something a
caller can act on. That is about fifteen lines, and it is exactly the assembly-and-defaults value
go-boot sells. The cap is `web.maxBodyBytes`, default 1 MiB.

**Timeouts.** `readHeaderTimeout` 5s and `idleTimeout` 120s are on. **`writeTimeout` is off**,
because gRPC streams share this server.

**TLS is two keys**, `certFile` and `keyFile`. No autocert.

**Not in v1**: CORS (the dangerous mistake — `*` with credentials — is the easy one to make; point
at `rs/cors`) and security headers (on a JSON API `X-Frame-Options: DENY` does nothing, and half a
header set now means changing it when the Security Starter arrives).

### 4.4 `goboot/grpc` — the gRPC Transport Starter

Settled in [#12](https://github.com/squall-chua/go-boot/issues/12). ADR `0006`.

**`goboot/grpc` owns no server.** connect-go's generated constructor returns
`(string, http.Handler)`, which is exactly `web.Server.Handle(pattern, h)`. A connect service
mounts on the HTTP Starter's listener with no adapter and no second port.

**There is no `grpc.addr` and no `Config` at all.** It is the first Starter with none, and the
missing key is the first thing a reader will look for, so the documentation must say it outright.
The address belongs to `goboot/web`. Two ports is `web.New` called twice.

```go
package grpc

// DefaultOptions is a slice you can edit, the same shape as
// web.DefaultMiddleware. Three entries:
//   1. connect.WithRecover
//   2. an error-sanitising interceptor
//   3. connect.WithRequireConnectProtocolHeader
func DefaultOptions(log *slog.Logger) []connect.HandlerOption
```

**The error-sanitising interceptor is mandatory, not a nicety.** Measured: a bare `error` returned
from a connect handler reaches the caller **verbatim** —
`pq: password authentication failed for user "app" at 10.0.0.5:5432` went out on the wire. The
interceptor replaces any non-`*connect.Error` with a bare `CodeUnknown`, logs the real error with
the procedure, and sends the caller nothing.

**No logging or request-ID interceptor.** Under the shared listener `web.DefaultMiddleware` has
already run, so `goboot.LoggerFrom(ctx)` and the request ID reach the connect handler free.

**The adapter type is mandatory, not stylistic, and the example must appear early.** The generated
interface wants `Greet(ctx, *connect.Request[...])` and the Service Layer already owns the name
`Greet`, so embedding gives a confusing ambiguity error. The gRPC Transport is a separate thin
type:

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

**The HTTP access log records 200 for a failed gRPC or gRPC-Web call.** Measured: the status rides
in trailers. gRPC and gRPC-Web both show 200, Connect shows 404. The error interceptor's log line
is where the truth lives. Document this.

**Codegen: go-boot requires nothing.** It never runs codegen and never imports generated code — the
user's generated package is passed in as a value. `buf` is the documented path only, and a user
with existing protos changes nothing. There is no `protoc-gen-goboot`. The Scaffold writes
`buf.yaml`, `buf.gen.yaml`, one sample `.proto` and the adapter type **only when gRPC is chosen**,
and **no Makefile** — go-boot does not own the user's build tool. The commands go in a README
comment.

**Streaming is supported**, because it costs nothing: connect does it and go-boot's only relevant
choice was `writeTimeout` off. There are no streaming helpers. The one number to document is #8's
10s stop timeout, which is what cuts a long-lived stream.

**One honest asymmetry with `goboot/web`:** connect options are **per service**, not per server, so
the user repeats `opts...` at every mount. connect has no global registry and there is no way
around it.

#### `goboot/grpc/health` and `goboot/grpc/reflection`

Both opt-in by import. This is Spring's optional-jar auto-configuration translated: Go has no
classpath conditionals, so "optional jar" becomes "a package you must import". Once imported both
work with no config.

- **`goboot/grpc/health`** has one checker reading exactly what `/readyz` reads. `""` is SERVING
  when the App is ready; an unknown service name returns `NOT_FOUND`. Per-service statuses are not
  in v1. **Drain costs nothing**: ADR `0001` flips App readiness before the drain delay, so gRPC
  health goes NOT_SERVING at the same moment `/readyz` goes 503.
- **`goboot/grpc/reflection`** mounts **both** `NewHandlerV1` and `NewHandlerV1Alpha`, because
  `grpcurl` still uses the old one. Named `reflection`, not `reflect`, to avoid the stdlib
  collision ADR `0005` exists to prevent.

### 4.5 `goboot/db` — the database Starter

Settled in [#13](https://github.com/squall-chua/go-boot/issues/13). ADRs `0007`, `0008`, `0009`.

```go
package db

type Config struct {
	DSN             string        `yaml:"dsn"`             // from an environment variable
	Driver          string        `yaml:"driver"`          // "pgx"; the user blank-imports it
	MaxOpenConns    int           `yaml:"maxOpenConns"`    // 10
	MaxIdleConns    int           `yaml:"maxIdleConns"`    // 10
	ConnMaxIdleTime time.Duration `yaml:"connMaxIdleTime"` // 5m
	ConnMaxLifetime time.Duration `yaml:"connMaxLifetime"` // 30m
	MigrateOnStart  bool          `yaml:"migrateOnStart"`  // false; local development only
}

// New returns the plain pool and the Component. migrations may be nil.
func New(cfg Config, log *slog.Logger, migrations fs.FS) (*sql.DB, *Component, error)

// NewProvider is the ONE place WithSessionLocker is wired. Start and the
// user's `myservice migrate` subcommand both call it. driver is the same
// string as Config.Driver; the goose dialect is derived from it by a switch.
func NewProvider(pool *sql.DB, driver string, migrations fs.FS, log *slog.Logger) (*goose.Provider, error)

// WithTx runs fn in a transaction. It does not nest and takes no options.
func WithTx(ctx context.Context, db *sql.DB, fn func(context.Context, *sql.Tx) error) error

func (c *Component) Name() string                     // "db"
func (c *Component) Tier() goboot.Tier                // TierResource
func (c *Component) Start(ctx) (<-chan error, error)  // ping, then migrate or HasPending
func (c *Component) Stop(ctx context.Context) error   // pool.Close()
func (c *Component) Check(ctx context.Context) error  // pool.PingContext(ctx)
// deliberately no Drain
```

> **One conflict resolved here.** #13's resolution comment writes `NewProvider`'s second parameter
> as `dialect goose.Dialect`, but its own prose says the dialect is derived from `driver` by a
> switch, and the switch exists because `goose.Dialect("pgx")` returns `unknown dialect: "pgx"`.
> With a `goose.Dialect` parameter the user's `migrate` subcommand would have to know the mapping,
> which defeats the point. This spec takes `driver string`, which is what `prototypes/goboot/db`
> compiles. Recorded as a comment on #13.

**The Starter hands back the plain `*sql.DB`**, as the first return value. That states the
query-layer neutrality claim in the type rather than in prose. Checked by compiling, not by
reading: `entgo.io/ent`, `gorm.io/gorm` and sqlc's generated `DBTX` all accept a plain `*sql.DB`.

**`goboot/db` imports no driver at all.** Measured: `pgx/v5/stdlib` is **+7.64 MB stripped**, the
second-heaviest dependency in the project after OTel, and a MySQL user would have paid all of it
and still had no driver. A forgotten blank import is not confusing — `sql.Open` returns
`sql: unknown driver "pgx" (forgotten import?)`, which names its own fix and fires in `main` before
`app.Run`. The Scaffold writes the import anyway.

The dialect switch maps `pgx`/`postgres` → postgres, `mysql` → mysql, `sqlite`/`sqlite3` →
sqlite3, and errors on anything else while naming the supported set.

**Pool defaults.** Go's own are wrong for a service: unlimited open connections, 2 idle, and
connections that live forever.

| Key | Default | Why |
|---|---|---|
| `maxOpenConns` | 10 | a stock Postgres allows about 97, so this is **10 pods** before it runs out — say that number out loud in the docs |
| `maxIdleConns` | 10 | matching `maxOpenConns` avoids churn where 8 of 10 connections are closed and reopened on every burst |
| `connMaxIdleTime` | 5m | a scaled-down deployment gives its slots back |
| `connMaxLifetime` | 30m | connections rebalance after a failover or a proxy restart |

**Transactions are an explicit closure** (ADR `0008`). `WithTx` takes the `*sql.Tx` as a parameter.
No context stashing, no nesting, no options. It cannot nest: `*sql.Tx` has no `Begin`, so
`database/sql` cannot nest transactions at all. When two Service Layer methods both want the
transaction, the inner one takes a value that both `*sql.DB` and `*sql.Tx` satisfy — which is
exactly the interface sqlc already generates. **go-boot defines no `DBTX` interface**, because
every query layer already has one.

**No Repository or Entity abstraction** (ADR `0009`). go-boot wires the pool, migrations and
transactions, and then stops.

**No `Drain` on the pool.** Drain runs in *start* order, so a draining pool would stop taking work
before the Transports had finished. `Stop` runs in reverse, so the pool closes last anyway. The
opposite looks obviously right until you notice the direction.

#### Migrations

**There is no `goboot migrate` command, and there could not have been.** Migrations live in the
user's own `embed.FS`, so a generic go-boot binary can never see them. `goboot/db` exports
`NewProvider`, and the Scaffold writes a `migrate` subcommand into the user's `main`. **The command
is `myservice migrate`** and the docs should use that name everywhere. Both `Start` and that
subcommand call `NewProvider`, so `WithSessionLocker` is wired in exactly one place.

Accepted cost: `*goose.Provider` is in go-boot's public API, so a goose major bump is a go-boot
breaking change. Hiding it would mean re-exporting 13 methods to keep `Down`, `UpTo` and
`ApplyVersion` reachable.

**A service refuses to start when migrations are pending** (ADR `0007`). This is an operational
contract go-boot imposes on the people who deploy it, and **it belongs on the first page of the
database Starter's documentation, not in a footnote**:

> If migrations run as a separate Kubernetes Job, **that Job must finish before the rollout
> starts.** If it does not, every new pod crashloops until it does. The Job runs the **same image**
> as the pods, which is what stops code and schema drifting.

`migrateOnStart` is for local development. It is bounded by the 30s `lifecycle.startTimeout`; that
is documented, not engineered around, because exempting migrations from the budget would make them
ignore SIGTERM. Log *before* applying, not only after, so a timeout kill is diagnosable.

**`migrations = nil` is a supported mode**, for a service that does not own its schema. It skips
both the migration and the pending-migration refusal.

#### `goboot/db/dbtest`

```go
package dbtest

// Start brings up a real PostgreSQL for one test. migrations may be nil.
func Start(tb testing.TB, migrations fs.FS) *sql.DB

// LintJPAConventions checks the live schema against docs/jpa-interop.md.
func LintJPAConventions(tb testing.TB, db *sql.DB)
```

Embedded PostgreSQL, not `testcontainers-go`. Measured: **3 linked module roots against 45**, 16
`go.sum` modules against 128, and no Docker daemon. Run, not just built: real PostgreSQL 18.3 up in
**2.77s**, goose applied the migration with the session locker on, `HasPending` returned false;
whole test 2.90s.

It ships as a package rather than a documented recipe because the library's defaults are
parallel-unsafe in two measured ways — two instances collide on `initdb`, and isolating only
`DataPath` still fails on the password file. The recipe that works is: share `BinariesPath`,
isolate `RuntimePath` and `DataPath`, take a free port from `net.Listen(":0")`. Four parallel
instances, all green, 3.1s.

Costs to document: a first run needs network and puts 114 MB on disk; `BinariesPath` lets an
air-gapped CI pre-seed. Importing `testing` from a non-test package costs nothing — measured, zero
flags registered, since Go 1.13 moved them into `testing.Init()`.

`LintJPAConventions` is from [#18](https://github.com/squall-chua/go-boot/issues/18). It checks
three things over `information_schema`: identifiers that are not `lower_snake_case`, `timestamp
without time zone`, and a table with no `version` column. It **skips goose's own bookkeeping
table**, which is load-bearing — `goose_db_version` has `tstamp timestamp` and no `version` column,
so without the skip the lint fails every schema go-boot creates. Known false positive:
`@ManyToMany` join tables have no `@Version`. The full convention is `docs/jpa-interop.md`, whose
load-bearing rule is that **every Go `UPDATE` to a table with a `version` column must write
`version = version + 1`**, or a concurrent Hibernate transaction commits over it and neither side
raises anything.

### 4.6 `goboot/trace`

Settled in [#10](https://github.com/squall-chua/go-boot/issues/10) and
[#12](https://github.com/squall-chua/go-boot/issues/12).

Tracing is a **sixth Starter, not part of the Actuator**. The reason is measured: the OTLP stack is
**+9.4 MB stripped and 19 indirect modules**, the heaviest single dependency in the project. Inside
`goboot/actuator` every Actuator user would pay it.

```go
package trace

type Config struct {
	Endpoint    string  `yaml:"endpoint"`
	ServiceName string  `yaml:"serviceName"`
	SampleRatio float64 `yaml:"sampleRatio"`
}

func New(cfg Config, log *slog.Logger) (*Component, error)

func (c *Component) Name() string                     // "trace"
func (c *Component) Tier() goboot.Tier                // TierObserve
func (c *Component) Start(ctx) (<-chan error, error)  // build the provider
func (c *Component) Stop(ctx context.Context) error   // flush the spans

// DefaultMiddleware is web.DefaultMiddleware with tracing in the ONE position
// that works: RequestID, trace, Logging, Recovery.
func DefaultMiddleware(log *slog.Logger) []web.Middleware

// Middleware is otelhttp with the span name from r.Pattern and RPC requests
// filtered out.
func Middleware() func(http.Handler) http.Handler

// IsRPC is exported so the filter stays editable. The rule is exact, not a
// heuristic.
func IsRPC(r *http.Request) bool
```

`Start` builds the provider from the standard `OTEL_*` environment variables; `Stop` flushes. About
20 lines.

**`trace.DefaultMiddleware` exists because `Use` cannot express the order.** `Use` appends, so the
call anyone would write —

```go
srv.Use(web.DefaultMiddleware(app.Log)...)
srv.Use(trace.Middleware())
```

— gives `RequestID → Logging → Recovery → trace → handler`, with tracing **innermost**. The
access-log line then cannot carry the trace ID, because `Logging` wrapped before the span existed.
The order that works is `RequestID → trace → Logging → Recovery`, and `trace.DefaultMiddleware`
returns exactly that: the same slice-you-can-edit shape, one word different at the call site.
`goboot/trace` importing `goboot/web` costs nothing — `goboot/web` links zero third-party modules.

**`goboot/trace` filters `otelhttp` by default**, whether or not `goboot/trace/rpc` is imported.
Measured: `otelhttp` and `otelconnect` together give **two nested spans per RPC**. The filter rule
is exact, from measured headers: content type starts with `application/grpc`, **or** a
`Connect-Protocol-Version` header is present. A user with no gRPC never sees those headers and pays
nothing.

**`goboot/trace/rpc`** holds `otelconnect`, by the optional-subpackage rule.

**Traces only, no metrics.** `otelconnect` would put RPC metrics into the OTel pipeline, which
`/actuator/metrics` cannot see — [#7](https://github.com/squall-chua/go-boot/issues/7) settled on
two pipelines and #10 removed `Actuator.Registry` for the same reason. The consequence is a real
gap and it is listed in [9. Known gaps](#9-known-gaps-in-v1).

---

## 5. The Presets, and what each wires

Settled in [#14](https://github.com/squall-chua/go-boot/issues/14). ADR `0010`.

**There is one Preset in v1, plus its tracing twin. That is the whole section.**

```go
package preset

type Config struct {
	Log       goboot.LogConfig       `yaml:"log"`
	Lifecycle goboot.LifecycleConfig `yaml:"lifecycle"`
	Web       web.Config             `yaml:"web"`
	DB        db.Config              `yaml:"db"`
	Actuator  actuator.Config        `yaml:"actuator"`
}

// App is what a Preset hands back. The embedded *goboot.App is the escape
// hatch that matters: app.Add(myConsumer) still works.
type App struct {
	*goboot.App              // Run, Log, Level, Add
	Web *web.Server          // route mounting
	DB  *sql.DB              // for the Service Layer
}

// Full wires every v1 Starter except tracing. migrations may be nil.
// Nothing is started yet.
func Full(cfg Config, migrations fs.FS) (*App, error)
```

```go
package traced

type Config struct {
	preset.Config `yaml:",inline"`
	Trace         trace.Config `yaml:"trace"`
}

// Full wires every v1 Starter, tracing included.
func Full(cfg Config, migrations fs.FS) (*preset.App, error)
```

`preset.Full` wires, in this order: `goboot.New`, `db.New`, `actuator.New`, `web.New`,
`srv.Use(web.DefaultMiddleware(app.Log)...)`, `act.MountOn(srv)`, `app.Add(act, database, srv)`.
`traced.Full` is the same with `trace.New` added and `trace.DefaultMiddleware` in place of
`web.DefaultMiddleware`.

**There is no `preset.HTTP` and no `preset.Web`** — #2 Q21 deleted both, because at one Component
a Preset made `main` longer. **There is no gRPC variant either**: `goboot/grpc` has no server, no
Component and no config, so a gRPC service and an HTTP service wire identically. There was never a
second Preset to design.

**A Preset takes no options, ever.** No flags, no negation config keys, no middle setting. The
returned struct lets you *add* — `app.Add`, `app.Web.Handle`, `app.Web.Use` — never remove or
reorder. To do either you copy the body, and **the moment you copy you have chosen to own your
wiring**, exactly as if you had never used a Preset. A Preset is take-it-or-leave-it and the docs
should say so rather than soften it.

**`traced.Full` is a copy of `preset.Full`, not a wrapper.** It cannot wrap: by the time the plain
Preset has returned, the middleware order is already fixed by `Use`'s append semantics. Two
near-identical bodies, deliberately.

**`goboot.Load` stays visible in `main`**, next to the user's own config struct. The prototype's
config-loading entry point `New(path, prefix, migrations)` is deleted, because it breaks the moment
a service adds one key of its own — which is every real service.

### How to sell the Preset, and how not to

**Do not sell it on the line count.** Counted, not quoted from memory:

| variant | Preset wiring | explicit wiring |
|---|---:|---:|
| `http-only` | — | 8 |
| `http-actuator-config` | — | 14 |
| `full` | **9** | **22** |

That is 13 lines saved, 59%. #2 Q22 rejected line count as the yardstick, and under its actual test
— *name a rule the Preset encodes that a developer would otherwise get wrong* — only **two** rules
survive, both one-liners:

| Rule | How it fails |
|---|---|
| `srv.Use(trace.DefaultMiddleware(app.Log)...)` | **Silently.** A panicking handler with no recovery middleware returns *no response at all*. |
| `act.MountOn(srv)` | **Loudly.** No `/readyz`, so the pod never goes ready and the first deploy tells you. |

**The argument that carries the Preset is the upgrade path: wiring held in a Preset gets fixed by
`go get -u`; wiring held in the user's `main` does not.** If go-boot later learns that a fourth
middleware belongs in the default set, every Preset user picks it up by bumping a version. That is
why "let the Scaffold write the 22 lines into `main`" was rejected — it loses nothing today and
everything on upgrade.

**And go-boot should promise *one call*, not *one line*.** That is what the numbers support and
what the call site actually looks like.

### The copy must compile

**For every Preset, one example directory holding both forms, and CI builds both.**
`prototypes/cmd/full/` is the working shape: `main.go` calls `traced.Full`, `explicit.go` is the 22
lines it expands to, and the two are kept honest by the compiler rather than by a doc page. A doc
snippet rots; a build failure does not. Since a Preset has no options, copying the body is the only
escape hatch, which makes the copy load-bearing.

---

## 6. The `main.go` a developer writes

Three variants, all compiling in `prototypes/cmd/`. **`cmd/full` compiles and vets but has never
been run** — it needs a Postgres. That has been true since #2.

### 6.1 `http-only` — no Preset form, and that is the point

8 wiring lines. There is no Preset for this shape, because one came out longer than the body.

```go
func run(ctx context.Context) error {
	app, err := goboot.New(goboot.Config{})
	if err != nil {
		return err
	}
	srv := web.New(web.Config{Addr: ":8080"}, app.Log)
	srv.Use(web.DefaultMiddleware(app.Log)...)
	app.Add(srv)

	srv.Handle("GET /hello/{name}", http.HandlerFunc(hello))

	return app.Run(ctx)
}
```

### 6.2 `http-actuator-config` — the realistic default

14 wiring lines. One Transport, an Actuator, and the service's own config key. No Preset form.

```go
//go:embed app.yaml
var defaultsFS embed.FS

// The service's own config struct: go-boot's keys plus its own.
type config struct {
	Log       goboot.LogConfig       `yaml:"log"`
	Lifecycle goboot.LifecycleConfig `yaml:"lifecycle"`
	Web       web.Config             `yaml:"web"`
	Actuator  actuator.Config        `yaml:"actuator"`
	Greeting  string                 `yaml:"greeting"`
}

func run(ctx context.Context) error {
	cfg := config{Greeting: "hello"} // struct pre-fill IS the defaults layer
	if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
		return err
	}
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		return err
	}
	act := actuator.New(cfg.Actuator, app)
	srv := web.New(cfg.Web, app.Log)
	srv.Use(web.DefaultMiddleware(app.Log)...)
	act.MountOn(srv)
	app.Add(act, srv)

	srv.Handle("GET /hello/{name}", greet(cfg.Greeting))

	return app.Run(ctx)
}
```

### 6.3 `full` — the whole v1 surface, both forms

HTTP, gRPC, database, Actuator and tracing. **9 wiring lines by Preset, 22 explicit.** Both forms
live in the same directory and CI builds both.

Preset form:

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed app.yaml
var defaultsFS embed.FS

type config struct {
	traced.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}

func run(ctx context.Context) error {
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
}
```

Explicit form — exactly what `traced.Full` expands to:

```go
func runExplicit(ctx context.Context) error {
	var cfg config
	if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
		return err
	}
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		return err
	}
	pool, database, err := db.New(cfg.DB, app.Log, migrations())
	if err != nil {
		return err
	}
	tracer, err := trace.New(cfg.Trace, app.Log)
	if err != nil {
		return err
	}
	act := actuator.New(cfg.Actuator, app)
	srv := web.New(cfg.Web, app.Log)
	srv.Use(trace.DefaultMiddleware(app.Log)...) // RequestID, trace, Logging, Recovery
	act.MountOn(srv)
	app.Add(act, tracer, database, srv)

	svc := &greeter{db: pool, greeting: cfg.Greeting}
	srv.Handle("GET /hello/{name}", httpGreet(svc))
	srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))

	return app.Run(ctx)
}
```

Note the two lines that are **identical in both forms**:

- `_ "github.com/jackc/pgx/v5/stdlib"` — the user brings their own driver.
- `grpc.DefaultOptions(app.Log)` — the mount names the user's generated package, so it cannot move
  into a Preset. See [9. Known gaps](#9-known-gaps-in-v1).

The Service Layer is shared by both forms and knows nothing about HTTP or gRPC:

```go
type greeter struct {
	db       *sql.DB
	greeting string
}

func (g *greeter) Greet(ctx context.Context, name string) (string, error)
```

### Measured weight of the three variants

Stripped, `go build -ldflags="-s -w"`, counting linked non-stdlib module roots.

| binary | modules | bytes |
|---|---:|---:|
| `cmd/http-only` | 1 | 6,414,601 |
| `cmd/http-actuator-config` | 10 | 9,363,721 |
| `cmd/full` | 21 | 14,405,897 |

**Caveat when quoting these:** `prototypes/goboot/trace` is signature-only and imports no OTel. The
numbers above measure call-site shape, which is real. The +9.4 MB and 19 modules that put tracing in
its own Starter are **#10's** measurement, not the prototype's.

---

## 7. Dependencies, and the ticket that chose each one

go-boot links these and nothing else. Versions are the ones measured; re-check the proxy before
pinning.

| Module | Version | Used by | Ticket | Why it, and not the alternative |
|---|---|---|---|---|
| `go.yaml.in/yaml/v3` | v3.0.5 | base (config) | [#4](https://github.com/squall-chua/go-boot/issues/4) | stdlib plus a ~80-line loader beat koanf (16 modules) and viper (23 modules, 7.97 MB). `gopkg.in/yaml.v3` is archived, so the fork is used everywhere |
| `github.com/go-viper/mapstructure/v2` | v2.5.0 | base (config) | [#9](https://github.com/squall-chua/go-boot/issues/9) | relaxed key matching and type-directed comma lists need reflection the hand loader would have to write. **Zero transitive dependencies**: 1 `go.sum` module, 1 linked module |
| `github.com/prometheus/client_golang` | v1.24.1 | actuator | [#7](https://github.com/squall-chua/go-boot/issues/7) | `promhttp.Handler()` over the default registry. Two pipelines, not one: Prometheus for metrics, OTel for traces |
| `connectrpc.com/connect` | v1.20.0 | grpc | [#5](https://github.com/squall-chua/go-boot/issues/5) | proven by experiment: a real `grpc-go` client reached it over cleartext with no proxy, and one port served Connect JSON and gRPC-Web too. CNCF sandbox. grpc-gateway's in-process mode is unary-only and kills interceptors; Vanguard is still alpha after three years |
| `google.golang.org/protobuf` | v1.36.11 | grpc (indirect), actuator (indirect) | [#5](https://github.com/squall-chua/go-boot/issues/5) | comes with connect. gRPC costs exactly these two modules and +3.57 MB |
| `github.com/pressly/goose/v3` | v3.27.3 | db | [#6](https://github.com/squall-chua/go-boot/issues/6) | driven through `NewProvider` in-process. ⚠️ **goose does not lock unless told to** — `lock.NewPostgresSessionLocker()` is wired on by default. Atlas ruled out twice (its library never locks, and the real revision store is behind a paid binary); golang-migrate blocks uncancellably and carries the dirty flag |
| `go.opentelemetry.io/otel` and its SDK | v1.45.0 | trace | [#7](https://github.com/squall-chua/go-boot/issues/7), [#10](https://github.com/squall-chua/go-boot/issues/10) | traces only. **+9.4 MB stripped and 19 indirect modules**, which is why it is a separate Starter |
| `.../contrib/instrumentation/net/http/otelhttp` | v0.70.0 | trace | [#7](https://github.com/squall-chua/go-boot/issues/7) | HTTP spans, span name from `r.Pattern` |
| `connectrpc.com/otelconnect` | v0.9.0 | `goboot/trace/rpc` | [#12](https://github.com/squall-chua/go-boot/issues/12) | RPC spans. Separate subpackage, and `goboot/trace` filters `otelhttp` for RPCs or you get two nested spans |
| `github.com/fergusstrange/embedded-postgres` | v1.34.0 | `goboot/db/dbtest` | [#13](https://github.com/squall-chua/go-boot/issues/13) | 3 linked modules against `testcontainers-go`'s 45, and no Docker daemon. Real PostgreSQL 18.3 up in 2.77s |

**No database driver is linked by a go-boot Starter.** The user blank-imports their own.
`pgx/v5/stdlib` is +7.64 MB.

> **Amended by [#26](https://github.com/squall-chua/go-boot/issues/26).** This line first read "no
> database driver is a go-boot dependency", and building `goboot/db/dbtest` proved that too strong:
> `dbtest.Start` returns a `*sql.DB`, so it must link a driver, and `github.com/jackc/pgx/v5` is
> therefore in go-boot's `go.mod`. `github.com/lib/pq` arrives too, as an indirect of
> `embedded-postgres`. Neither reaches a user's binary — `go list -deps ./db` is clean of both, and
> a test asserts it — but both are in every consumer's module graph. The claim that holds is about
> linking, not about `go.mod`.

**Health written in-house**, not taken from a library ([#7](https://github.com/squall-chua/go-boot/issues/7)).
The whole Actuator is about 220 lines of code, plus its comments. The 130 first written here
described the prototype stub, which had no whitelist check, no pprof, no `showDetails` and no
timeout on its private listener ([#25](https://github.com/squall-chua/go-boot/issues/25)). There is
no capability gap in the Go ecosystem here; the gap is assembly and correct defaults.

---

## 8. CI rules go-boot's own repo must keep

These are product requirements, not repo hygiene. Two of them exist because a decision above is
only true if a check keeps it true.

### 8.1 The import-leak check

Four assertions, settled in [#14](https://github.com/squall-chua/go-boot/issues/14). Prototyped as
`prototypes/scripts/check-imports.sh`, all four passing, **and verified to fail**: importing
`goboot/trace` from `goboot/preset` makes assertion 2 report the leak and the script exit 1.

1. **The base package and its *tests* import no Starter.** `go list -deps` alone misses test
   imports, so `.TestImports` and `.XTestImports` must be asked for explicitly. This is #3's hard
   rule, and it is about `go mod tidy`, not about the build.
2. **No short-path package reaches a heavy optional package.** The short paths are `goboot`,
   `goboot/web`, `goboot/db`, `goboot/actuator`, `goboot/grpc` and `goboot/preset`. The heavy
   optional packages are `goboot/trace`, `goboot/grpc/health`, `goboot/grpc/reflection`,
   `goboot/trace/rpc` and `goboot/db/dbtest`.
3. **`goboot/db` links no driver.** Grep the dependency list for `jackc`, `go-sql-driver`, `lib/pq`
   and `mattn/go-sqlite3`.
4. **A pinned module count per package**, in a golden file, regenerated deliberately and never
   silently. This is the one that catches the next leak nobody predicted. It could not be shown
   firing in the prototype, because the trace stub adds no modules; in the real repo that leak moves
   `goboot/preset` from 15 modules to roughly 34.

The check must cover **every package a short path imports, not just `goboot`** — the rule as first
written missed `goboot/preset`, whose Preset dragged the Actuator into an HTTP-only binary: 10
modules and 12.4 MB against 1 module and 9.2 MB.

### 8.2 CI builds both forms of every Preset

One example directory per Preset, holding the Preset form and the explicit form it expands to. CI
builds both. See [5. The Presets](#5-the-presets-and-what-each-wires).

### 8.3 The ordinary gates

`go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` clean.

> **Not yet done.** The real repository has no CI workflow at all. This section states what the
> workflow must assert; wiring it up is build work, not a spec decision.

---

## 9. Known gaps in v1

Written down as gaps, not left out.

- **No RPC metrics.** No count and no latency by procedure. This is the consequence of choosing
  traces-only for RPCs: `otelconnect` would put metrics into the OTel pipeline, which
  `/actuator/metrics` cannot see. RPCs get spans. ([#12](https://github.com/squall-chua/go-boot/issues/12))
- **`grpc.DefaultOptions(app.Log)` stays in `main` in both the Preset and the explicit form**,
  because the mount names the user's generated package. **So the Preset does not protect anyone
  from the missing error-sanitising interceptor**, and #12 measured what that costs: a bare `error`
  reaches the caller verbatim, password and all.
- **The HTTP access log records 200 for a failed gRPC or gRPC-Web call.** The status rides in
  trailers. The error interceptor's log line carries the real code.
- **`ddl-auto=validate` has one hole the JPA lint cannot close.** `varchar` length lives in the
  Java class, so the lint is a supplement to `validate`, never a replacement.
  ([#18](https://github.com/squall-chua/go-boot/issues/18))
- **`maxOpenConns: 10` is 10 pods before a stock Postgres runs out.** Not a defect, but an operator
  must not discover it at scale.
- **Two Apps in one process share one Prometheus registry.** go-boot is one service per process, so
  nobody pays it.
- **Every Actuator user links Prometheus, even with metrics at 404.** Measured in
  [#25](https://github.com/squall-chua/go-boot/issues/25) against the `http-only` example: 6.49 MB
  and 2 linked modules become 11.09 MB and 11. `CONTEXT.md` says a Starter splits a dependency only
  some of its users need, and this one qualifies, because `metrics` is off by default. It is not
  split anyway: a `goboot/actuator/metrics` subpackage could not register its route through the
  whitelist, so `main` would need a second mount line, and `act.MountOn(srv)` being one line that is
  correct in both port modes is the Actuator's best property. Half the weight ADR `0010` split
  `traced.Full` out for, for an API cost ADR `0003` was written to avoid.

---

## 10. What go-boot does not do

Deliberate. Most of these are here because Go's standard library already covers what Spring Boot
had to add for Java.

- **A reflection DI container** (uber/fx style). It fails at runtime instead of compile time and
  fights the compiler. Presets and plain constructors cover it.
- **Classpath-style auto-configuration.** Go has no equivalent and should not grow one. Spring
  Boot's auto-config becomes a plain Preset.
- **A reactive stack** (WebFlux equivalent). Goroutines already are this.
- **Hot-reload dev tooling.** `air` and `wgo` cover it, and it is not a library's job.
- **Batch and Integration equivalents.** Overkill for Go services.
- **Anything the stdlib gives free**: JSON binding, templating, mail, cron scheduling, async, static
  file serving, HTTP client, password hashing, single-binary packaging.
- **Proto transcoding, or one proto driving both Transports.** HTTP and gRPC are independent
  Starters over a shared Service Layer. Ruled against for v1; revisit only if real users ask.
- **A router, a handler signature, a validation library, a `DBTX` interface, a Repository
  abstraction, a Prometheus type in the public API, a gRPC code table.** Each is covered above with
  the measurement that refused it.

---

## 11. Deferred past v1

In scope for go-boot, but not in this spec and not blocking it.

- **Security Starter** — authentication, authorization, JWT, OAuth2 resource server, security
  headers. It owns the ground `goboot/web` deliberately left empty. Too large to phrase sharply
  until it starts.
- **Messaging Starter** — Kafka and RabbitMQ consumers as Components. The lifecycle contract it was
  waiting on is settled: a consumer is `TierTransport` and is the named user of the optional
  `Drainer`. Specifiable now, but not a v1 Starter.
- **Cache / Redis Starter** — likely a thin wiring Starter; unclear whether it earns its place.
- **Scaffold CLI design** — commands, flags, what it writes, how thin the generated `main` stays.
  It already carries three requirements from this spec: write the `myservice migrate` subcommand and
  the driver blank import; write the `buf` files and the gRPC adapter type only when gRPC is chosen;
  and write schemas that use `timestamptz`, identity ids and `lower_snake_case`, with a way to add
  the `version` column and the `ddl-auto=validate` CI job. Only the `version` column is
  JPA-specific — the rest is right for everyone, so most of it should be the default rather than a
  flag.
- **Error handling convention across go-boot** — sentinel errors, wrapping, what a Starter returns
  on misconfiguration, and whether a go-boot error type exists at all. Both #11 and #12 deferred it
  here deliberately: settle it once for HTTP and gRPC together, not twice.
- **Versioning and release policy** — one tag for one module, and what stability v1 promises.
- **Docs and examples strategy** — how a newcomer learns this in ten minutes.

---

## Sources

- The map and its *Decisions so far* index: [#1](https://github.com/squall-chua/go-boot/issues/1)
- Domain vocabulary: `CONTEXT.md`
- Decisions that are hard to reverse: `docs/adr/0001` through `docs/adr/0010`
- Research, one file per research ticket: `docs/research/`
- Measurements from writing the call sites: `docs/prototypes-notes.md` (section 7 is current;
  sections 1–6 are the old shape and are marked superseded)
- Compiling evidence for every signature above: `prototypes/`
- The JPA schema convention: `docs/jpa-interop.md`
