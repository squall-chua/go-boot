# go-boot v1 — the locked spec

Status: **locked**, 2026-08-25. Written for [#15](https://github.com/squall-chua/go-boot/issues/15),
which is the last ticket on the map ([#1](https://github.com/squall-chua/go-boot/issues/1)).

"Locked" means every design decision here is settled and written down. Building go-boot v1 is
typing, not deciding. Anything still open is listed in
[11. Deferred past v1](#11-deferred-past-v1), and no part of *building* v1 depends on it. One
part of *tagging* it does: [12. Versioning and release policy](#12-versioning-and-release-policy)
gates `v1.0.0` on the error-handling convention, and says why.

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
`goboot/grpc/metrics`, `goboot/grpc/reflection`, `goboot/trace/rpc`, `goboot/db/dbtest` and
`goboot/preset/traced`. It is stated once in `CONTEXT.md` and once here, and never repeated per
package.

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

**There are six Starters: base, actuator, web, grpc, db, trace.** Plus five optional subpackages
that exist only to hold a dependency: `goboot/grpc/health`, `goboot/grpc/metrics`,
`goboot/grpc/reflection`, `goboot/trace/rpc`, `goboot/db/dbtest`.

### 4.0 The error convention every Starter follows

Settled in [#38](https://github.com/squall-chua/go-boot/issues/38). ADR `0011`. Both
[#11](https://github.com/squall-chua/go-boot/issues/11) and
[#12](https://github.com/squall-chua/go-boot/issues/12) deferred error handling here on purpose, so
that HTTP and gRPC got one answer rather than two that disagree. This is the section
[12. Versioning and release policy](#12-versioning-and-release-policy) named as the gate on
`v1.0.0`.

#### There is no go-boot error type, and no go-boot sentinel

A Starter returns a plain `error`, built with `errors.New` or `fmt.Errorf`. There is no
`goboot.Error`, no `goboot.ErrConfig`, and no exported sentinel in any of the fifteen packages.

A caller who needs to branch matches on the sentinel belonging to whoever produced the fault —
`sql.ErrNoRows`, `fs.ErrNotExist`, `http.MaxBytesError` — with `errors.Is` or `errors.As`. It never
matches on a go-boot identifier, because there is none, and it never matches on a message string.

This is the same refusal ADR `0004` already made one level down: `goboot/web` has no
`func(w, r) error` and no error type a handler returns. An error type here would exist for `main`
alone, and `main` has one error path with one thing to do at the end of it — print the error and
exit non-zero. A type nothing reads is a type nothing needs.

The argument that settles it is the same one ADR `0010` used against Preset options: **adding a
sentinel later is additive, removing one is breaking.** Shipping none is the choice that keeps the
option open. If a real service ever needs `main` to tell a config typo from a database being down,
`goboot.ErrConfig` can arrive in a `v1.x` without breaking anyone.

#### Every error opens with the thing to go and look at

`fmt.Errorf` with `%w`, never `%v`, so the cause survives all the way to `main`. The message opens
with a locator, and which locator depends on what is at fault:

| What is at fault | Locator | Example |
| --- | --- | --- |
| A config key | the key path, as written in YAML | `web.tls: needs both certFile and keyFile, or neither` |
| A config file | `config` and the file name | `config app-local.yaml: line 7: no = or : separator` |
| A Component | the Component name, and the phase when there is one | `start web: listen tcp :8080: address already in use`, `duplicate component name "web"` |
| Nothing narrower | the package identifier, written the short way [1. Ground rules](#1-ground-rules) writes it | `db: migrations are pending; run ...`, `trace/rpc: ...` |

The key path is the one that matters, because it is the only locator that tells an operator which
line of which YAML file to edit. `actuator.expose: no endpoint named "metric"` was already written
this way and is the model the rest were brought to.

There is no bare message with no locator in the fourteen packages a service links. The fifteenth,
`goboot/db/dbtest`, is exempt and is the only one: every one of its exports takes a `testing.TB`
and calls `Fatal` on it, so none of its text ever reaches a `main` — its messages name the check
that failed rather than a config key, and that is right for a test helper.

**One carve-out, and it is a different audience rather than an exception.** The rule above is about
errors a Starter hands back to `main`, for an operator to read. An error written for the **API
caller** carries no locator, because a Go package name has no meaning to whoever called the
service. Two in v1:

- `web.DecodeJSON` — `body is empty`, `field "age" must be a number`. A handler passes these
  straight to `WriteProblem` as the RFC 7807 `detail`, so a `web:` prefix would put a Go package
  name in an HTTP response body.
- `goboot/grpc/health` — `unknown service "orders.v1.Orders"`, inside a `*connect.Error` the
  package built itself.

Both are the same slot as [Text reaching a caller is text the handler chose](#text-reaching-a-caller-is-text-the-handler-chose)
below: go-boot is the handler here, and it wrote those words.

#### Misconfiguration comes back from the constructor, not from Start

**A constructor validates its own config and returns `(T, error)`. `Start` reports only what needs
the world.**

All fifteen public packages are below, so the rule can be checked rather than believed. Every one
that has config to validate follows it; the rest are listed with the reason they have nothing to
validate, because "not applicable" and "overlooked" look identical in a shorter table:

| Package | The constructor rejects | `Start` reports |
| --- | --- | --- |
| `goboot` | a `log.level` that is not a level | a Component that failed to start or died |
| `goboot/actuator` | an `actuator.expose` entry naming no endpoint | binding the private listener |
| `goboot/web` | half a `web.tls` pair | binding the listener |
| `goboot/web/metrics` | nothing: no config, no Component, no constructor at all. `metrics.Middleware` IS the middleware and registers at package init, so there is nothing to fail | — |
| `goboot/security` | a `security.jwt` section missing `issuer` or `audience`; naming none or more than one of `jwksUrl`, `jwksFile` and `publicKeyFile`; a `jwksUrl` on plain `http` outside loopback; a key file that is missing, unreadable or not an RSA/ECDSA public key; and `security.cors` allowing `*` with credentials, or setting `allowCredentials` with no origins | — no Component. The two file sources are read here; `jwksUrl` is fetched lazily, on the first token |
| `goboot/db` | a `db.driver` with no goose dialect, and `sql.Open` | reaching the database, pending migrations |
| `goboot/trace` | a `trace.sampleRatio` outside 0..1 | building the exporter |
| `goboot/preset`, `goboot/preset/traced` | nothing of their own — they return the first error the constructors above give them | — |
| `goboot/trace/rpc` | `rpc.Options` returns what `otelconnect` refuses | — |
| `goboot/grpc`, `goboot/grpc/health`, `goboot/grpc/metrics`, `goboot/grpc/reflection` | nothing: no config, no Component, no constructor that can fail. `metrics.Options` registers at package init, so it has no error to give | — |
| `goboot/db/dbtest` | — a test helper: it takes `*testing.T` and fails the test | — |

`goboot/trace` is the one that was already right before #38 and is worth reading as the model —
"New checks the config and holds it. Nothing is built here: the exporter belongs to Start."

Before #38 the rule was half kept. `goboot.New` and `db.New` returned an error; `web.New` and
`actuator.New` did not, and their two config faults surfaced from `Start` a few lines later. Both
faults are pure validation that touches nothing outside the `Config` struct, so both moved into the
constructor, and `web.New` and `actuator.New` grew an `error` return.

**That signature change is the whole reason #38 gated `v1.0.0`.** It is the only breaking change on
the deferred list, it lands in a `v0`, and after it the surface can be frozen.

It costs `main` six lines in the explicit form of [6.3](#63-full--the-whole-v1-surface-both-forms),
and that cost is real rather than something to talk away. What is bought is one rule a reader can
hold: **if a constructor returned no error, nothing about your config can be wrong yet.** The
alternative — a reader checking, per Starter, whether this one validates early or late — is the
thing the convention exists to delete.

#### No error text a Starter did not write reaches a caller

One rule, both Transports. The mechanisms differ because the protocols do — an HTTP error is a body
a handler writes, a gRPC error is a value a handler returns — but the rule does not.

- **HTTP.** In `goboot/web`, `WriteProblem` is the only writer of an error body, RFC 7807 in
  shape, and the Recovery middleware calls the same function, so a panic and a hand-written 400
  leave in one form. A handler that writes an error body itself has opted out, and go-boot cannot
  stop it.
- **The Actuator is the one other place go-boot answers a caller with an error**, the two 400s on
  `PUT /actuator/loglevel`. They stay plain text rather than RFC 7807, because every other Actuator
  body is plain JSON and `goboot/actuator` deliberately does not import `goboot/web` (ADR `0003`).
  What they do follow is the rule below: the words are the Actuator's own.
- **gRPC.** The sanitising interceptor in `grpc.DefaultOptions` replaces any non-`*connect.Error`
  with a bare `CodeUnknown` and logs the real one against the procedure. #12 measured what its
  absence costs: `pq: password authentication failed for user "app" at 10.0.0.5:5432` went out on
  the wire verbatim.

#### Text reaching a caller is text the handler chose

The rule above is not satisfied by wiring the sanitiser. Measured, with every option in
`grpc.DefaultOptions` correctly on: the adapter this spec used to print,

```go
if err != nil {
	return nil, connect.NewError(connect.CodeInternal, err)
}
```

put `internal: pq: password authentication failed for user "app" at 10.0.0.5:5432` on the wire.
`connect.NewError(code, err)` makes `err`'s own text the message the caller receives, and the
sanitiser passes a `*connect.Error` through **untouched on purpose** — constructing one is the only
way a handler can say "this text is safe to send". Wrapping an error from below in one is a handler
claiming that about text it never read.

So the rule is one sentence and it is the same sentence on both Transports:

> **Any text a caller receives is text the handler wrote for that caller.** Never `err.Error()`
> from below, on either Transport.

- **gRPC.** Return the Service Layer's error **bare** and let the sanitiser own the wire: it logs
  the real error against the procedure and sends a bare `CodeUnknown`. To tell the caller something
  useful, write the text: `connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))`.
- **HTTP.** `WriteProblem(w, status, detail)` takes a `detail` string, and it is a string the
  handler wrote. `web.WriteProblem(w, 400, err.Error())` is the same mistake in the same shape, and
  the type system will not stop it either.

The parallel is exact, and that is the point of settling both at once: `detail` and a constructed
`*connect.Error` are the same slot — the one place a handler is trusted, because it is the one
place a handler has read the words.

Three tests in `goboot/grpc` hold this down: the wrapping adapter still leaks and is pinned as
leaking, the documented adapter does not, and a handler's own `*connect.Error` still arrives with
its text and its code.

#### What was looked at and left alone

**A `grpc.Handle` that applied `DefaultOptions` for you.** The idea was to make the sanitised mount
the short one, since [9. Known gaps](#9-known-gaps-in-v1) records that no Preset can carry a line
naming the user's generated package. It does not survive Go's type inference. The generated
constructor is `func(SomeServiceHandler, ...connect.HandlerOption) (string, http.Handler)`, so a
helper taking it as a value infers its type parameter as the **interface**, and the user's adapter
— which merely *implements* that interface — then fails to unify. The three shapes that compile:

- an explicit type argument, `grpc.Handle[greetv1connect.GreetServiceHandler](...)` — longer than
  the line it replaces, and it makes the user name a generated identifier they otherwise never
  type;
- a closure per mount — three lines where there was one;
- `svc any` with a type assertion inside — short, but it turns a compile error into a startup
  panic, which is the thing [10. What go-boot does not do](#10-what-go-boot-does-not-do) rejects a
  DI container for.

None of them is shorter *and* compile-checked, and a mount helper that is longer than the raw mount
is a helper nobody reaches for. So `goboot/grpc` keeps exactly one exported function,
`DefaultOptions`, and the gap in §9 stays a gap. The leak measured above was the larger half of it
and that one is closed.

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

func New(cfg Config, app *goboot.App) (*Actuator, error)
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

**Prometheus owns every metric go-boot emits. OTel owns traces and nothing else.** Settled by
[#41](https://github.com/squall-chua/go-boot/issues/41), which had to choose a pipeline before it
could add a single RPC counter. ADR `0012`, with the measurements that refused the alternatives.
The rule is one sentence so it can be applied without re-deciding:
**a metric go-boot ships is registered on `prometheus.DefaultRegisterer` and is readable at
`/actuator/metrics`.** An operator asking "how many of my RPCs failed" has one place to look, and
that stays true when HTTP request metrics are added later.

The rule is what keeps `otelconnect.WithoutMetrics()` in `goboot/trace/rpc` — see
[4.6](#46-goboottrace). Turning otelconnect's metrics on would put half the metric surface in the
OTel pipeline, visible only to whoever runs a collector. The other route to one endpoint, an OTel
`MeterProvider` bridged into the Prometheus registry by
`go.opentelemetry.io/otel/exporters/prometheus`, was refused for two reasons: it adds the OTel
metric SDK and its exporter as modules, and it makes RPC metrics conditional on tracing being
imported *and* enabled, so a service that wants a counter and no collector cannot have one.

**HTTP metrics spend that rule, and are now shipped.** `/actuator/metrics` carries a count and a
latency **by route** from `goboot/web/metrics` ([4.3](#43-gobootweb--the-http-transport-starter)),
registered on the same default registry and served by the same endpoint.
[#45](https://github.com/squall-chua/go-boot/issues/45) had nothing left to decide about the
pipeline, which is the point of writing the rule down once: what it had left to settle was where
the middleware goes and how to keep the route label bounded.

**No metric go-boot ships is registered from a package a user links by default.** `goboot/actuator`
serves the registry, it does not fill it. Anything that registers is an opt-in subpackage, which is
what keeps [9](#9-known-gaps-in-v1)'s "every Actuator user links Prometheus" from growing a second
half — the import-leak check's assertions 2, 4 and 5 are what say so on every push.

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

func New(cfg Config, log *slog.Logger) (*Server, error)

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

> **Amended by [#28](https://github.com/squall-chua/go-boot/issues/28).** This section was silent on
> a protocol setting every user of `goboot/web` now gets: the server sets `http.Server.Protocols`
> with **HTTP/1, HTTP/2 over TLS, and unencrypted HTTP/2** all on. Go's default leaves HTTP/2 to
> TLS, and ADR `0006` needs cleartext HTTP/2 or a plain gRPC client cannot reach the shared
> listener at all — measured, `TestCleartextHTTP2IsOn` fails without it. Two consequences an
> HTTP-only user should read: net/http tells the two apart by the **client preface**, so an
> ordinary HTTP/1 client is untouched (`TestHTTP1StillWorks`); and it is **not a config key**,
> because the gRPC Starter rests on it. See the amendment in
> [4.4](#44-gobootgrpc--the-grpc-transport-starter).

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

**A failed RPC is named on the access line.** Settled in
[#43](https://github.com/squall-chua/go-boot/issues/43). gRPC and gRPC-Web put their status in
**trailers**, so the HTTP status line stays 200 and the access line would otherwise read as a
success. `Logging` reads the `Grpc-Status` the handler left on the response once the handler has
returned, and when it is there and is not `0` it adds `rpcCode` and raises the level to ERROR:

```
level=ERROR msg=request method=POST path=/greet.v1.GreetService/Greet
  route=/greet.v1.GreetService/ status=200 rpcCode=2 bytes=0 duration=0.4ms requestId=9f2c...
```

Three choices in that line, each deliberate:

- **`status` still says 200**, because 200 is what went on the wire. The access log reports the
  response, it does not translate it.
- **`rpcCode` is the gRPC code as a number**, not a name. `goboot/web` may not import connect-go —
  assertion 2 of [8.1](#81-the-import-leak-check) — and a seventeen-entry copy of connect's table
  in `goboot/web` is a copy that drifts. The interceptor's `rpc failed` line spells the name.
- **ERROR is the level the error interceptor already uses** for its own line, so one `requestId`
  finds both at the same level.

There is no `rpcCode` on a call that succeeded, so `rpcCode` present is the whole test. Plain gRPC
always carries it. gRPC-Web carries it only when the response is **trailers-only** — nothing written
to the body and no response header set — which is the ordinary unary failure. What is left out is
written down in [9. Known gaps](#9-known-gaps-in-v1).

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

> **Both arrived, in [4.7](#47-gobootsecurity--the-security-starter)**
> ([#34](https://github.com/squall-chua/go-boot/issues/34)), and the two sentences above are what
> they were built to. The dangerous CORS pair is a **startup error** rather than a documentation
> note, and the header set is whole rather than half — `X-Frame-Options` is still left out, for the
> reason written here. Neither is in `goboot/web`: they are in a package a user opts into, so an
> HTTP user who wants neither links neither.

#### `goboot/web/metrics`

Opt-in by import, the same shape [`goboot/grpc/metrics`](#gobootgrpcmetrics) has. Settled by
[#45](https://github.com/squall-chua/go-boot/issues/45). Under the pipeline rule in
[4.2](#42-gobootactuator): the metrics are registered on `prometheus.DefaultRegisterer`, so they
are served by `/actuator/metrics` and by nothing else.

**It is a subpackage because `goboot/web` links 2 modules** — as few as the base package itself,
and no package in the repo links fewer — and it is the one package every HTTP user imports. Naming `client_golang` there would charge all of them for
Prometheus to get a counter most never scrape — the leak assertion 2 of
[8.1](#81-the-import-leak-check) exists to catch. Measured: `goboot/web/metrics` links **9
modules**, `goboot/web` stays at **2**, and `go.mod` gains nothing, because `client_golang` is the
dependency the Actuator has linked since #7.

And measured again from the other side, which is the number that matters: **every one of those 9
modules is one `goboot/actuator` already links**, so for a service that exposes
`/actuator/metrics` — the only service that can see these metrics at all — this package costs
**zero** extra modules. The subpackage is not buying weight back from its own user; it is keeping
the Prometheus dependency off the HTTP user who never asked for it.

```go
package metrics

// Middleware counts and times one request. It is a web.Middleware, and it
// needs no constructor: it holds no logger and has no options to build.
func Middleware(next http.Handler) http.Handler
```

```go
srv.Use(append(web.DefaultMiddleware(app.Log), metrics.Middleware)...)
```

**A traced service appends to the other default set**, for the reason
[4.6](#46-goboottrace) already gives — `trace.DefaultMiddleware` is five entries, not `web`'s three,
and a second `Use` call cannot reorder what the first one added:

```go
srv.Use(append(trace.DefaultMiddleware(app.Log), metrics.Middleware)...)
```

**Both lines are compiled, not printed only here.** The plain one is built by
`web/metrics/metrics_test.go`; the traced one is built by `examples/full/explicit.go`, which
[#46](https://github.com/squall-chua/go-boot/issues/46) wired for that reason. A snippet nothing
builds rots, and this one has a trap in it — copying the plain line into a traced service silently
drops tracing — so the compiler holds it, not a reviewer's eye.

Two metrics, both labelled `route` and `status`:

| Metric                          | Type      | What it answers                       |
| ------------------------------- | --------- | ------------------------------------- |
| `http_requests_total`           | counter   | How many, and how many of them failed |
| `http_request_duration_seconds` | histogram | How slow, at a quantile               |

**`route` is `r.Pattern`, never `r.URL.Path`.** Patterns are registered at startup so the set is
fixed at compile time; paths are whatever a caller sends. This is the same line `Logging` draws
above — *path is what was asked for, route is the low-cardinality label to group by* — except that
getting it wrong here costs a Prometheus outage rather than a wide log line.
`TestAPathWithAnIdInItDoesNotCreateANewLabelValue` pins it, and fails if the label is switched to
the path.

**A request that routed nowhere carries an empty `route`.** Measured on Go 1.26.3: `http.ServeMux`
leaves `r.Pattern` empty for both a 404 and a 405, so every scan of `/wp-admin`, `/.env` and the
rest lands on one series rather than inventing one each.

**There is deliberately no `method` label.** `r.Pattern` already carries the method for a
method-bound pattern — the route label reads `GET /users/{id}` — and on the one path where it does
not, the unrouted request above, the method is an arbitrary token the caller chose. It would be the
only unbounded label left, and it would be unbounded exactly where the scanner traffic is.

**It records in a `defer`, so the status label is right on either side of `Recovery`.** `Use`
appends, so the line above lands this middleware **inside** `Recovery`, and a panic unwinds past
anything written after `next()` — the same lesson `goboot/grpc/metrics` learned, and a test fails
without the `defer`. A recovered panic is labelled with **the status the caller actually got**, and
is then re-panicked so
`Recovery` still logs it and still writes what it writes. Spliced *outside* `Recovery` instead, no
panic reaches this middleware and the recorder already holds the same answer.
`TestItCountsAPanicOnEitherSideOfRecovery` pins both placements.

**But it must stay BELOW `web.Logging`, and that is a hard rule, not a preference.** `Logging`
calls `r.WithContext`, which is a new `*http.Request`, and `http.ServeMux` fills `r.Pattern` **in
place** on the request it routed — so a middleware spliced *above* `Logging` holds a stale copy and
reads an empty `route`. Every route then collapses onto the series meant for requests that matched
nothing, which is a dashboard that is wrong rather than coarse. This is the same rule
[4.6](#46-goboottrace) already states for `trace.RouteSpanName`, which is innermost for it. The
documented line appends, so it obeys the rule by construction;
`TestTheRouteLabelNeedsThisMiddlewareBelowLogging` holds it for a user who edits the slice.
([#47](https://github.com/squall-chua/go-boot/issues/47))

**A panic after a partial write keeps the status already on the wire**, and that is the same
condition `Recovery` itself branches on: a response that has been written cannot be taken back, so
`Recovery` logs the panic and writes no 500, and the client keeps the 200 it was already sent. A
metric that called that request a 500 would be the only place in go-boot claiming a status nobody
received — it would disagree with the access line for the same `requestId`. So `500` is the label
only when nothing had been written yet, which is exactly when `Recovery` writes one.
`TestAPanicAfterAPartialWriteKeepsTheStatusTheClientGot` pins it.

**`http.ErrAbortHandler` is passed through uncounted**, because every other layer already treats it
as a deliberate abort rather than a failure: `web.Recovery` re-panics it rather than writing a 500,
`Logging` writes no access line for it at all, and `goboot/grpc/metrics` does not count it either.

**Probe paths ARE counted, and this is the one place the answer differs from `Logging`.** The log
skips `/livez`, `/readyz` and `/actuator/*` because of **volume** — roughly 17,000 lines a day
saying nothing. A metric has no volume: a probe adds one series, not 17,000 of anything. An
operator who does not want probes in a graph excludes them in PromQL, and nothing recovers a
measurement that was never taken, so the latency of `/readyz` — which runs every readiness Check on
each request — stays available. `TestProbePathsAreCounted` pins it, so the difference is a decision
rather than an oversight.

Two consequences of that to read as choices rather than discover. **The scrape counts itself**: on
the shared port `/actuator/metrics` is an ordinary route, so `http_requests_total{route="GET
/actuator/metrics"}` climbs once per scrape. And **`actuator.addr` turns all of this off for the
Actuator's endpoints**: that key moves them to a private listener the Actuator binds and owns
([4.2](#42-gobootactuator)), which this middleware never wraps, so on that shape `/livez`,
`/readyz` and `/actuator/*` are not counted at all and the `/readyz` latency argument above does
not apply. Neither is a defect of the middleware — both follow from where it is mounted — but a
dashboard built on the shared port and moved to a private one loses those series.

**No Preset wires it**, and that is forced twice over: a Preset takes no options, and assertion 2
of [8.1](#81-the-import-leak-check) forbids `goboot/preset` from reaching this package at all, or
every Preset user pays for Prometheus to get a counter they did not ask for. So the one line above
goes in `main`. A Preset user who wants these metrics copies the body of `Full`, which is the
documented escape hatch of [5](#5-the-presets-and-what-each-wires) and not a fallback.
`examples/full` is that worked example: the explicit form wires this package and the Preset form
cannot, and the difference between the two files is what copying the body buys.

### 4.4 `goboot/grpc` — the gRPC Transport Starter

Settled in [#12](https://github.com/squall-chua/go-boot/issues/12). ADR `0006`.

**`goboot/grpc` owns no server.** connect-go's generated constructor returns
`(string, http.Handler)`, which is exactly `web.Server.Handle(pattern, h)`. A connect service
mounts on the HTTP Starter's listener with no adapter and no second port.

> **Amended by [#28](https://github.com/squall-chua/go-boot/issues/28).** "Mounts on the HTTP
> Starter's listener" needs one line in `goboot/web` that neither this section nor ADR `0006` asked
> for: **`http.Server.Protocols` must have `SetUnencryptedHTTP2(true)`**. Go's default leaves
> HTTP/2 to TLS, so without it a plain gRPC client gets `http2: frame too large, note that the
> frame header looked like an HTTP/1.1 header` and the access log records a 400 for `method=PRI`.
> Measured — a test in `goboot/grpc` fails without the line. It is not a config key, because the
> whole Starter rests on it. Since Go 1.24 this costs no `golang.org/x/net/http2` and no h2c
> wrapper.

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

**`DefaultOptions` is the only exported function in this package**, and
[4.0](#40-the-error-convention-every-starter-follows) records the mount helper that was tried and
refused: Go's type inference cannot give one that is both shorter than the raw mount and
compile-checked.

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

> **Amended by [#28](https://github.com/squall-chua/go-boot/issues/28).** The ambiguity error needs
> one more condition than this line gives it. Measured, both shapes: embedding the Service Layer
> alone gives `wrong type for method Greet` with a readable `have`/`want` pair; the confusing
> `ambiguous selector *badB.Greet` appears only when the generated `Unimplemented...` type is
> embedded **as well**, which is the common case, since that is what a user embeds for forward
> compatibility. The conclusion stands — write the adapter type — but the documentation should show
> both errors rather than promise the second one.

```go
type grpcGreeter struct{ svc *greeter }

func (g *grpcGreeter) Greet(ctx context.Context, req *connect.Request[greetv1.GreetRequest]) (*connect.Response[greetv1.GreetResponse], error) {
	out, err := g.svc.Greet(ctx, req.Msg.GetName())
	if err != nil {
		return nil, err // bare: the sanitiser owns what the caller sees
	}
	return connect.NewResponse(&greetv1.GreetResponse{Greeting: out}), nil
}
```

**The error is returned bare, and that is load-bearing.** This section printed
`connect.NewError(connect.CodeInternal, err)` until
[#38](https://github.com/squall-chua/go-boot/issues/38) measured what it does: it makes `err`'s own
text the message the caller receives, and the sanitiser passes a `*connect.Error` through untouched
by design, so the password went out with every option correctly wired. To tell a caller something
useful, write the text —
`connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))`. See
[4.0](#40-the-error-convention-every-starter-follows).

**A failed gRPC or gRPC-Web call is named on the HTTP access line.** Measured: the status rides in
trailers, so the status line itself shows 200 — where a failed Connect **unary** call maps its code
onto the status line instead (`connectCodeToHTTP`: `CodeUnknown` is 500, `CodeNotFound` is 404).
`web.Logging` reads the trailer rather than the status line and adds `rpcCode` — see
[4.3](#43-gobootweb--the-http-transport-starter). The error interceptor's
`rpc failed` line still carries the code by name, the procedure and the real error, and is the only
one that carries them for the two streaming shapes [9](#9-known-gaps-in-v1) records.

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

#### `goboot/grpc/metrics`

Opt-in by import, the same as the two above. Settled by
[#41](https://github.com/squall-chua/go-boot/issues/41). ADR `0012`. Under the pipeline rule in
[4.2](#42-gobootactuator): the metrics are registered on `prometheus.DefaultRegisterer`, so they
are served by `/actuator/metrics` and by nothing else.

```go
package metrics

// Options returns the connect handler options that count and time an RPC.
// They append to the ones every service already passes, exactly as
// trace/rpc.Options does. There is no error to return.
func Options() []connect.HandlerOption
```

```go
opts := metrics.Options()
srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc},
	append(grpc.DefaultOptions(app.Log), opts...)...))
```

Two metrics, both labelled `procedure` and `code`:

| Metric                 | Type      | What it answers                       |
| ---------------------- | --------- | ------------------------------------- |
| `rpc_requests_total`   | counter   | How many, and how many of them failed |
| `rpc_duration_seconds` | histogram | How slow, at a quantile               |

**`code` is `connect.CodeOf(err)`, not the handler's raw error.** It is the code the caller
receives, so `ok`, `not_found`, `unknown` and the rest — a bare `error` from a handler is `unknown`
here because that is what the sanitiser sends. Both labels are bounded: procedures are fixed at
compile time and there are seventeen connect codes, so this cannot become a cardinality problem.

**Registration happens at package init, not in `Options`.** connect options are per service, so
`Options` is called once per mount, and a `MustRegister` inside it would panic on the second
service. It also means `Options` cannot fail, which is the one place this package's signature
differs from `trace/rpc.Options`.

**A streaming RPC is counted once, when the stream ends**, and its duration is the whole stream's
lifetime rather than a per-message figure. Anything finer needs a metric the handler owns.

**A panicking handler is counted, and `http.ErrAbortHandler` is not.** The interceptor records in a
`defer`, because `connect.WithRecover` is itself an interceptor and `grpc.DefaultOptions` puts it
outermost: a panic unwinds past anything written after `next()`, so the one failure an operator
most wants to see would be the one failure not recorded. It is labelled `internal`, which is what
WithRecover sends the caller. `http.ErrAbortHandler` is the exception and goes back untouched and
uncounted, because every other layer already treats it as a deliberate abort rather than a failure
— connect re-panics it, `web.Recovery` re-panics it rather than writing a 500, and `web.Logging`
writes no access line for it at all.

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

> **Amended by [#26](https://github.com/squall-chua/go-boot/issues/26).** `NewProvider` is the only
> place the session lock is wired, but goose ships a session locker for **PostgreSQL only** —
> `lock/postgres.go` is the whole package. On `mysql` and `sqlite3` there is nothing to wire, so two
> pods applying the same migration are not protected from each other. The dialect switch still
> accepts them; the lock does not follow. Said out loud in the database Starter's documentation.

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

// StartDSN is Start plus the connection string it opened the pool on.
func StartDSN(tb testing.TB, migrations fs.FS) (*sql.DB, string)

// LintJPAConventions checks the live schema against docs/jpa-interop.md.
func LintJPAConventions(tb testing.TB, db *sql.DB)
```

> **`StartDSN` added by [#31](https://github.com/squall-chua/go-boot/issues/31).** The Presets came
> with the rule that one test drives both forms of `examples/full`, and a test that drives a whole
> `main` cannot use the pool: `main` opens its own from `db.dsn`, so what the test needs to hand it
> is a DSN. Without this the test would have had to repeat the parallel-safe recipe this package
> exists to hold, which is the duplication `db/db_pg_test.go` already shows the cost of. `Start` is
> unchanged and is now a two-line wrapper over `StartDSN`.

Embedded PostgreSQL, not `testcontainers-go`. Measured: **3 linked module roots against 45**, 16
`go.sum` modules against 128, and no Docker daemon. Run, not just built: real PostgreSQL 18.3 up in
**2.77s**, goose applied the migration with the session locker on, `HasPending` returned false;
whole test 2.90s.

It ships as a package rather than a documented recipe because the library's defaults are
parallel-unsafe in two measured ways — two instances collide on `initdb`, and isolating only
`DataPath` still fails on the password file. The recipe that works is: share `BinariesPath`,
isolate `RuntimePath` and `DataPath`, take a free port from `net.Listen(":0")`. Four parallel
instances, all green, 3.1s.

Costs to document: a first run needs network and puts about **71 MB** on disk; `BinariesPath` lets
an air-gapped CI pre-seed, and `dbtest` reads the environment variable **`GOBOOT_PG_BINARIES`** to
point it there. Importing `testing` from a non-test package costs nothing — measured, zero flags
registered, since Go 1.13 moved them into `testing.Init()`.

> **Amended by [#26](https://github.com/squall-chua/go-boot/issues/26).** This line first said
> **114 MB**. Re-measured from a cold cache on linux/amd64 with PostgreSQL 18.3: the download is a
> **14.3 MB** `.txz`, and it extracts to **56.4 MB**, so **70.7 MB** in total across the two paths.
> The 114 MB figure is not reproducible here. It may hold on another platform, so re-measure before
> quoting it again. The same amendment names `GOBOOT_PG_BINARIES`, which the spec asked for in
> effect — "`BinariesPath` lets an air-gapped CI pre-seed" — without naming a mechanism a user
> could reach.

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

> **Amended by [#30](https://github.com/squall-chua/go-boot/issues/30).** The signatures above were
> written against a stub that imported no OTel, and building the real one added two exported names
> and a fifth entry to the slice. Both were measured, not preferred.
>
> `DefaultMiddleware` returns **five** entries: `RequestID`, trace, `Logging`, `Recovery`,
> `RouteSpanName`. The four-name order is unchanged and still the point. The fifth is what names
> the span after the route, and it has to be innermost: `ServeMux` fills `r.Pattern` in place on
> the request handed to it, and `web.Logging` calls `r.WithContext`, so everything above it —
> including `otelhttp`'s own rename, which does exactly this — reads an empty pattern and leaves
> the span named `GET`. Measured: drop the entry and `TestSpanNamesComeFromTheRouteTemplate` reads
> `"GET"` instead of `"GET /hello/{name}"`.
>
> `WithIDs(log) *slog.Logger` is the second name. Nothing in `goboot/web` can read a span —
> `goboot/web` links no third-party module and that is load-bearing — so the trace ID reaches the
> access-log line through the logger `DefaultMiddleware` hands to `web.Logging`: a `slog.Handler`
> wrapper that copies the trace and span IDs off the context. Both names are exported for the
> reason `IsRPC` is: the slice is one you can rebuild by hand.
>
> `RouteSpanName` names in a **`defer`**, which is not a detail. `web.Recovery` sits outside it, so
> a panicking handler unwinds past a plain post-call rename and the 500's span keeps the name
> `GET` — the one span anybody is chasing, worst named. Measured: `TestAPanickingHandlerStillGetsItsRouteName`.
>
> `Start` also sets the W3C propagator, which the sketch did not mention. Without it the spans are
> real but every service starts a new trace, which is the failure that looks like success. It is the
> one thing here NOT left to the environment: `OTEL_PROPAGATORS` is a specification variable
> `opentelemetry-go` does not read, so there is nothing to defer to without importing contrib's
> `autoprop` and the four vendor formats behind it.
>
> `Config.SampleRatio` zero means **not set**, not "keep nothing", and `New` returns an error for a
> ratio outside 0..1 — which is the job the spec's `error` return had none of otherwise.
>
> `goboot/trace/rpc` exports `Options() ([]connect.HandlerOption, error)`, appending to
> `grpc.DefaultOptions`, with `otelconnect.WithoutMetrics()` set. Measured module counts:
> `goboot/trace` 26 modules, `goboot/trace/rpc` 11, and neither `goboot`, `goboot/web`,
> `goboot/actuator`, `goboot/db` nor `goboot/grpc` links any of them.
>
> **The weight this Starter exists for, now measured on the real code rather than the stub.** One
> HTTP service, `go build -ldflags="-s -w"`, counting linked non-stdlib module roots: **3 modules
> and 6,807,817 bytes** without tracing, **26 modules and 16,498,953 bytes** with it. That is
> **+9.69 MB and +23 modules**, and it confirms #10's prototype figure of +9.4 MB and 19 indirect
> modules rather than revising it.

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
two pipelines and #10 removed `Actuator.Registry` for the same reason. This was a gap in
[9](#9-known-gaps-in-v1) until [#41](https://github.com/squall-chua/go-boot/issues/41) closed it
from the other side: RPC count and latency by procedure come from `goboot/grpc/metrics`
([4.4](#44-gobootgrpc--the-grpc-transport-starter)), on the Prometheus registry, needing neither
this package nor a collector. `WithoutMetrics()` stays.

### 4.7 `goboot/security` — the Security Starter

Settled in [#34](https://github.com/squall-chua/go-boot/issues/34). ADR `0013`. It owns the ground
[4.3](#43-gobootweb--the-http-transport-starter) deliberately left empty, and it is where the two
things [10](#10-what-go-boot-does-not-do) refused for v1 — CORS and security headers — come back,
as something a user opts into rather than as a default every user links.

```go
package security

type Config struct {
	Headers HeadersConfig `yaml:"headers"`
	CORS    CORSConfig    `yaml:"cors"`
	JWT     JWTConfig     `yaml:"jwt"`
}

type HeadersConfig struct {
	HSTSMaxAge time.Duration `yaml:"hstsMaxAge"` // 0, which is off
}

type CORSConfig struct {
	AllowedOrigins   []string      `yaml:"allowedOrigins"`   // exact origins, or the single entry "*"
	AllowedMethods   []string      `yaml:"allowedMethods"`   // GET, POST, PUT, PATCH, DELETE
	AllowedHeaders   []string      `yaml:"allowedHeaders"`   // Authorization, Content-Type
	ExposedHeaders   []string      `yaml:"exposedHeaders"`   // added to X-Request-Id, always exposed
	AllowCredentials bool          `yaml:"allowCredentials"` // false
	MaxAge           time.Duration `yaml:"maxAge"`           // 10m
}

type JWTConfig struct {
	Issuer   string   `yaml:"issuer"`   // required
	Audience []string `yaml:"audience"` // required, at least one; any of them satisfies aud

	// Exactly one of these three.
	JWKSURL       string `yaml:"jwksUrl"`       // an issuer's published key set, over https
	JWKSFile      string `yaml:"jwksFile"`      // the same document, on disk
	PublicKeyFile string `yaml:"publicKeyFile"` // one PEM public key or certificate

	Leeway time.Duration `yaml:"leeway"` // 30s
}

// Principal is what a verified token became. Claims holds the whole payload,
// so a claim go-boot does not name is still reachable.
type Principal struct {
	Subject string
	Issuer  string
	Scopes  []string
	Claims  map[string]any
}

func PrincipalFrom(ctx context.Context) (*Principal, bool)

// DefaultMiddleware is Headers, CORS, Authenticate, outermost first. It is a
// slice you can edit, the same shape web.DefaultMiddleware has.
func DefaultMiddleware(cfg Config) ([]web.Middleware, error)
func Headers(cfg HeadersConfig) web.Middleware
func CORS(cfg CORSConfig) (web.Middleware, error)
func Authenticate(cfg JWTConfig) (web.Middleware, error)

// Authorization is per route. Both return a web.Middleware, so they wrap a
// handler where it is mounted and nothing else in the service changes.
func RequireScope(scope ...string) web.Middleware    // every one of them
func RequireAnyScope(scope ...string) web.Middleware // at least one of them
```

```go
srv.Use(append(web.DefaultMiddleware(app.Log), sec...)...)
srv.Handle("POST /orders", security.RequireScope("orders:write")(orders))
```

**`DefaultMiddleware` assembles what the config asked for, and the omissions are rules rather than
conveniences.** `Headers` is always in the slice. `CORS` joins it once `cors.allowedOrigins` names
something, and `Authenticate` once the `jwt` section is filled in — so a service that wants headers
only writes no `security` config at all and still gets them.

A section that is **half** filled in is a startup error rather than a skip, which is the rule
`web.New` already applies to half a `web.tls` pair and for the same reason: a misspelt key must
never leave a service quietly unauthenticated. There are four such errors, and all four come from a
constructor:

| The config | The error |
| --- | --- |
| `jwt` with any of `issuer`, `audience`, `jwksUrl` missing | `security.jwt.<key>: required…` |
| `jwt.jwksUrl` on plain `http`, outside loopback | `security.jwt.jwksUrl: … is plain http…` |
| `cors.allowedOrigins: ["*"]` with `cors.allowCredentials` | `security.cors: … cannot be used with allowCredentials` |
| `cors.allowCredentials` with no `cors.allowedOrigins` | `security.cors: allowCredentials is on but allowedOrigins is empty` |

Every one of them lives in `Headers`, `CORS` or `Authenticate` rather than in `DefaultMiddleware`,
so a service that wires the three by hand gets the same answers.

**It is one package, and the ticket that opened it predicted two.** #34 was written expecting a
heavy subpackage for assertion 2 of [8.1](#81-the-import-leak-check) to name. Measured before any
code, on Go 1.26.3 against a bare `net/http` `main` of 8,365,513 bytes:

| candidate | modules | binary delta |
| --- | ---: | ---: |
| `github.com/golang-jwt/jwt/v5` v5.3.1 | **1** | **+36 KB** |
| `github.com/go-jose/go-jose/v4` v4.1.4 | 1 | +495 KB |
| `github.com/coreos/go-oidc/v3` v3.20.0 | 3 | +1.17 MB |

The repo split `goboot/web/metrics` out at 9 modules and `goboot/grpc/metrics` at 10. One module is
below every line this repo has ever drawn, so **there is no subpackage and no new entry on the
heavy list**. `goboot/security` links **3 modules**, which is `goboot`'s two plus `golang-jwt`, and
`goboot/web` stays at 2. The prediction is recorded here rather than quietly dropped, because the
reason it was wrong is the reason the rule is a measurement and not a habit.

**The 36 KB is the dependency, not the Starter, and the difference is worth a second number.**
Wiring this Starter into `examples/http-only` — the whole of `DefaultMiddleware` plus one
`RequireScope` route — takes that binary from **6,807,817 to 7,516,425 bytes stripped**, which is
**+708,608 bytes and +1 module**. Nearly all of the extra is stdlib that was already *compiled in*
and was being dead-code eliminated: `goboot/web` on its own calls nothing that verifies an RSA or
an ECDSA signature, so `crypto/rsa`, `crypto/ecdsa`, `crypto/elliptic` and `math/big` fall out of
the link. Verifying a token calls them, so they stay. Only **two packages** are new in the whole
dependency list, `goboot/security` and `golang-jwt` — the rest of the 0.66 MB is code that becomes
reachable, which is a cost `go list` cannot show and only a binary can.

That number is the one to quote to a user, and it is still the smallest of the three candidates:
`go-oidc` would have added its 1.17 MB **on top** of the same 0.66 MB, because it verifies with the
same stdlib.

What the choice costs is written down too: `golang-jwt/jwt/v5` ships no JWKS client, so the key set
below is about eighty lines of `crypto/rsa`, `crypto/ecdsa` and `encoding/json` that go-boot owns.
`go-jose` would have deleted them for 459 KB and a lower-level call site.

**`goboot/security` joins the short paths of assertion 2.** It is a package a user imports
directly, so the same rule that keeps Prometheus out of `goboot/web` keeps it out of here. The list
in [8.1](#81-the-import-leak-check) is seven names now, not six.

**Authentication is not a global gate, and that is the whole shape of the package.** `Authenticate`
verifies a bearer token **when one is there** and puts the `Principal` in the request context. It
does not reject a request that carried no token. Rejecting is `RequireScope`'s job, at the mount,
one route at a time.

The alternative — a middleware that demands a token on every request — cannot work on this server:
`/livez`, `/readyz` and `/actuator/*` share the listener (ADR `0003`, ADR `0006`), so a global gate
either locks Kubernetes out of its own probes or grows a path allowlist, and a path allowlist is a
security decision written in a config file that no compiler checks. Per-route wrapping is the
opposite: the wrapper is at the mount, in Go, next to the handler it protects, and it is an
ordinary `web.Middleware` so nothing about `Server.Handle` changes.

**The trap that shape leaves is real, and it is named rather than designed away.** A route nobody
wrapped is a route with no authorization. Go's type system cannot catch it, and neither can
go-boot. What go-boot can do is make the wrapper short enough that leaving it off is visible in
review, which is why `RequireScope` takes strings and not a builder.

**`Principal.Scopes` reads two claim names and two shapes.** RFC 8693 writes `scope` as one
space-separated string; Azure AD writes `scp`, sometimes as an array. Both names and both shapes are
read, because a service that guessed wrong would fail every authorization check with no clue why.
Roles get no helper at all — no claim name for them is standard, so `Principal.Claims` holds the
whole payload and the three lines are the caller's.

**Both no-argument forms mean "authenticated only".** `RequireScope()` and `RequireAnyScope()` name
no scope, so there is none to fail. Read literally, "at least one of nothing" would be false and the
second would refuse every caller — a footgun with no use behind it, since the only reason to write
it is to demand a token.

**A token that is present and bad is a 401 straight away**, with
`WWW-Authenticate: Bearer error="invalid_token"` and an RFC 7807 body from `web.WriteProblem`, so a
rejected token leaves in the same shape as every other error the service writes
([4.0](#40-the-error-convention-every-starter-follows)). Carrying a broken token forward as "no
Principal" would turn an expired token on a public route into a silent 200 and the same token on a
guarded route into a 401 that says the wrong thing.

The 401's `detail` is `invalid token` and nothing else. The reason the token failed — expired,
wrong audience, unknown `kid` — goes to the **request logger** at WARN, `goboot.LoggerFrom`, which
means it carries the same `requestId` as the access line `web.Logging` writes. The token itself is
never logged: it is a bearer credential, and a log file is not where one belongs.

**There are three key sources, all asymmetric, and exactly one may be named.** Two sources would
mean go-boot choosing which wins, and the only right answer is "the one you meant", so naming two is
a startup error and so is naming none.

| Key | What it is | Rotation | Reach for it when |
| --- | --- | --- | --- |
| `jwksUrl` | the issuer's published key set, over `https` | an unknown `kid` re-fetches | almost always |
| `jwksFile` | the same JWKS document on disk | an unknown `kid` re-reads the file | the service may make no outbound request, or the key set arrives as a mounted ConfigMap |
| `publicKeyFile` | one PEM public key, or a certificate carrying one | none — a change needs a restart | there is no issuer endpoint at all, only a key someone handed you |

**No shared secret, on any of the three.** There is no `hmacSecret`, and `publicKeyFile` refuses
anything that is not an RSA or ECDSA public key — a private key file, the one most likely to be
pointed at by mistake, is refused by name. A symmetric secret is the alg-confusion hole in the shape
that keeps being rediscovered, and admitting one here would be the same hole arriving through a
different key.

**`jwksUrl` is fetched lazily, on the first token**, not at startup: fetching at startup would turn
an auth-server outage into a service that will not boot, which is the same mistake as a liveness
probe that touches a dependency. What that costs instead is **measured, not argued** — a warm cache
survives an issuer outage indefinitely, because a known `kid` is answered from cache before any
refetch is considered; but a **cold start during an outage refuses every token**. Both halves are
pinned by `TestWhatAnIssuerOutageCosts`. A service that cannot accept the second half is exactly the
service `jwksFile` is for, and the two file sources are read **at construction**, so a wrong path is
a startup error rather than a 401 an hour later.

> **The three sources arrived after the section was first written.** #34 shipped `jwksUrl` alone,
> on the argument that every issuer worth pointing at publishes one. Review found two holes in that:
> the cold-start behaviour above, which no test had measured, and the plain fact that some services
> are not allowed to make an outbound request at all. Spring's `NimbusJwtDecoder` — the thing this
> library is modelled on — offers four sources, `withJwkSetUri`, `withIssuerLocation`,
> `withPublicKey` and `withSecretKey`. go-boot now offers three of those four. The one still
> refused is `withSecretKey`, and that refusal is the part of the original call that survived.

**Rotation is handled by the unknown `kid`, not by a timer.** A token whose `kid` is not in the
cache triggers one refetch, rate-limited to **one every ten seconds** so a stream of junk `kid`s
cannot be turned into a stream of requests to the issuer. Ten and not sixty: at six requests a
minute the flood costs the issuer nothing, and a real rotation heals within ten seconds rather than
within one. A fetch that FAILS starts the same floor, so an issuer that is down is not asked again
per request. There is no background refresh goroutine: an issuer that rotates keys publishes the new
one before it signs with it, so the first token carrying it is the only signal needed, and a
goroutine that polls an endpoint nothing has asked for is a Component's worth of lifecycle for no
gain.

**A token with no `kid` is verified against every key in the set**, and the signature decides. A
`kid` is not a unique identifier — a set may publish several keys without one, and an issuer part
way through a rotation may briefly publish two under one name — so the cache holds a *list* per
`kid`. Picking one key out of a map would be picking whichever parse order won, and a token signed
by any of the others would then fail for no visible reason. Trying several never widens what
verifies: the token still has to be signed by a key this issuer published.

**`jwksUrl` must be `https`, and the constructor refuses anything else.** The one exception is a
loopback host, which is what makes a local issuer and go-boot's own tests possible — the same
carve-out RFC 8252 makes for native-app redirect URIs, and for the same reason: there is no path for
an attacker to sit on. Everywhere else, plain `http` is a **total authentication bypass** rather
than a weakness, because the key set *is* the root of trust: anyone who can rewrite that response
chooses which keys this service believes, and so can mint any identity they like. It is the one
misconfiguration in this section that looks like nothing at all in a YAML file.

**Issuer and audience are both required, and `Authenticate` refuses a config without them** — as
does `DefaultMiddleware`, which calls it. There is no `New` in this package: it starts nothing and
holds nothing, so there is no Component to build. A resource server that does not check `aud`
accepts every token the issuer minted, including the one meant for a different client of the same
issuer — which is not a subtle failure, it is the whole reason the claim exists. `exp` is required
on the token as well. This follows
[4.0](#40-the-error-convention-every-starter-follows): the constructor validates its own config,
and `Authenticate` reaches nothing outside the `JWTConfig` struct, so every one of these faults is
a startup error rather than a 401 in production.

**This is stricter than Spring, deliberately.** Spring Security validates `iss`, `exp` and `nbf` by
default and **not** `aud` — a developer opts in with
`spring.security.oauth2.resourceserver.jwt.audiences` or a custom validator. That default is a
well-known footgun, and "sane defaults" is what go-boot is for, so here the key is mandatory. The
concept is not alien to the audience: Spring has a first-class key for it, and Spring's is **plural**,
which is why go-boot's is a list too. Any one of the entries satisfies `aud`, so a service can
answer to two names at once while an identifier is being renamed.

**`aud` is read in every shape it legally takes.** RFC 7519 allows a string or an array of strings;
both are accepted, and a token whose `aud` array contains any configured audience passes. A token
carrying **no** `aud`, or an empty array — the shape a misconfigured mapper emits — is refused.
`TestTheAudienceClaimInEveryShape` pins all four.

> **Read this if every request is a 401 and the log says `token has invalid audience`.** The
> commonest cause is the issuer, not this config: Keycloak's default realm does not put a
> resource-specific value in `aud` until an audience mapper is added to the client. go-boot cannot
> fix that from its side, and will not accept the token without it.

**The algorithms are an allowlist** — `RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`,
`ES256`, `ES384` and `ES512` — and it is not a config key. Every entry is asymmetric, so `alg: none`
and the HMAC-verified-with-the-RSA-public-key trick are both outside it, and neither is a thing a
user should be able to switch on from a YAML file.

It is the **second** lock rather than the first, and that is written here because the difference is
measurable. The first lock is the key type: the key set hands back an `*rsa.PublicKey` or an
`*ecdsa.PublicKey`, and `golang-jwt` refuses to verify an `HS256` or a `none` token with either.
Deleting `WithValidMethods` from the parser leaves **every rejection in the test suite still
passing**, which is the honest result and is why no end-to-end test claims to cover it. What the
list adds is that the set this service accepts is the set written above, rather than whatever
`golang-jwt` grows support for next; an internal test pins the nine names.

**CORS refuses the dangerous mistake in the constructor**, which is the measurement
[10](#10-what-go-boot-does-not-do) asked for. `allowedOrigins: ["*"]` together with
`allowCredentials: true` is a startup error — the browser would refuse the pair anyway, so the
configuration that looks like "allow everyone to log in" in fact allows nobody, and finding that
out at boot beats finding it out from a support ticket. Origins are matched **exactly**; there is
no pattern syntax, because a wildcard in the middle of an origin is how `evil-example.com` gets
matched by a rule meant for `app.example.com`. `Vary: Origin` is set on every response, allowed or
not, so a shared cache cannot serve one origin's answer to another.

**`X-Request-Id` is always exposed, and that is this section paying a debt
[4.3](#43-gobootweb--the-http-transport-starter) ran up.** That section refuses RFC 7807's
`instance` member on the grounds that "the `X-Request-Id` on the response already answers *which
occurrence was this*". Only seven response headers are readable cross-origin without being named in
`Access-Control-Expose-Headers`, and that is not one of them — so until this key existed the
sentence was false for exactly the callers CORS is for. `exposedHeaders` is a **union** with it
rather than a replacement, unlike `allowedMethods` and `allowedHeaders`: a service naming
`X-Total-Count` means "also expose this" and never "and hide the request id", and hiding it would
buy nothing anyway, since the value is on the wire and visible in any developer console. The header
is go-boot's, the same way the probe paths of `web.Logging` are.

**The header set is four headers, and `X-Frame-Options` is not one of them.**

| Header | Value | Why |
| --- | --- | --- |
| `X-Content-Type-Options` | `nosniff` | The one that matters on a JSON API: it stops a browser deciding a response is HTML. |
| `Content-Security-Policy` | `default-src 'none'; frame-ancestors 'none'` | Nothing an API response contains should ever load or be framed. |
| `Referrer-Policy` | `no-referrer` | An API URL routinely carries an id. |
| `Strict-Transport-Security` | `max-age=<n>` | Only when `hstsMaxAge` is set. |

`X-Frame-Options: DENY` is left out because `frame-ancestors 'none'` is what every current browser
reads, and [4.3](#43-gobootweb--the-http-transport-starter) already said the header does nothing on
a JSON API. HSTS is **off unless configured**, and that default is deliberate: sent over plain HTTP
on a developer's machine it pins `localhost` to HTTPS in that browser for its whole `max-age`, and
undoing it means a trip into browser internals.

**The Actuator's own endpoints are guardable, and nothing in go-boot had to change for it.**
`act.MountOn(srv)` puts `/actuator/*` on the shared listener, so on that shape
`PUT /actuator/loglevel` is reachable by anyone who can reach the port. `actuator.Handler` is a
**one-method interface** (ADR `0003` made it one so the Actuator need not import `goboot/web`), so a
service passes its own `Handle` and wraps each route on the way past. `examples/http-secure` does
exactly that.

**Liveness and readiness must stay open in any such wrapper**, and that is the same rule as the
global gate above rather than a second one: Kubernetes carries no bearer token, so a guard on
`/livez` or `/readyz` means the pod never goes ready. The four patterns to skip are `GET /livez`,
`GET /readyz`, `GET /actuator/livez` and `GET /actuator/readyz`. The other answer is `actuator.addr`
([4.2](#42-gobootactuator)), which moves the Actuator to a private listener nothing routes to from
outside — preferable where it can be run, because a port nobody can reach needs no scope check.

**`examples/http-secure` is the compiled form of all of this**, and it exists for the reason
[4.3](#43-gobootweb--the-http-transport-starter) gives for `examples/full/explicit.go`: a snippet
nothing builds rots, and these have traps in them. Two, specifically — the middleware line must
**append** to `web.DefaultMiddleware` or the security middleware lands outside `web.Recovery`, and
the Actuator must be mounted through the wrapper or `/actuator/loglevel` is world-writable. Its test
drives both: the probes answer with no token, `/actuator/loglevel` is 401 without one and 200 with
the scope. `README.md` quotes that file under `<!-- from: ... -->` markers, so drift fails the build
([8.4](#84-the-readmes-go-samples-are-compiled)).

**No Preset wires it**, and the reason is [ADR 0010](docs/adr/0010-presets-have-no-options.md)
rather than the import rule: a Preset takes no options, and every field of `JWTConfig` is a value
only the service knows. A Preset that wired security would have to invent an issuer. So the wiring
is the two lines above, in `main`, and a Preset user who wants them copies the body of `Full` —
which is the documented escape hatch of [5](#5-the-presets-and-what-each-wires), not a fallback.

**What #34 asked for and this does not do**, so the next ticket starts from a list rather than a
guess: OIDC discovery (`/.well-known/openid-configuration`), so `jwksUrl` is written out by hand;
token introspection for opaque tokens; roles, because no claim name for them is standard —
Keycloak's `realm_access.roles` and Azure's `roles` disagree — and `Principal.Claims` holds the
payload for anyone who wants to write the three lines; and mTLS or API keys.

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

`preset.Full` wires, in this order: `goboot.New`, `db.New`, `actuator.New`, `web.New` — each
checked for an error, per [4.0](#40-the-error-convention-every-starter-follows) —
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

**Do not sell it on the line count.**

> **Amended by [#31](https://github.com/squall-chua/go-boot/issues/31).** A table of wiring lines
> per variant stood here — 8 for `http-only`, 14 for `http-actuator-config`, and 9 against 22 for
> `full`, "13 lines saved, 59%". Counted again on the real `examples/`, under the one rule that
> still reproduces `http-only` at 8, `http-actuator-config` comes out at **13** and `full` at **10**
> against **21**. Every other row is out by one, and not in the same direction, so no single
> counting rule fits all three. The table is deleted rather than corrected, and it costs nothing to
> lose: this section already forbids selling the Preset on the number, and #31 made that a rule the
> documentation has to keep.

#2 Q22 rejected line count as the yardstick, and under its actual test
— *name a rule the Preset encodes that a developer would otherwise get wrong* — only **two** rules
survive, both one-liners:

| Rule | How it fails |
|---|---|
| `srv.Use(trace.DefaultMiddleware(app.Log)...)` | **Silently.** A panicking handler with no recovery middleware returns *no response at all*. |
| `act.MountOn(srv)` | **Loudly.** No `/readyz`, so the pod never goes ready and the first deploy tells you. |

**The argument that carries the Preset is the upgrade path: wiring held in a Preset gets fixed by
`go get -u`; wiring held in the user's `main` does not.** If go-boot later learns that a fourth
middleware belongs in the default set, every Preset user picks it up by bumping a version. That is
why "let the Scaffold write the explicit lines into `main`" was rejected — it loses nothing today
and everything on upgrade.

**And go-boot should promise *one call*, not *one line*.** That is what the call site actually
looks like.

### The copy must compile

**For every Preset, one example directory holding both forms, and CI builds both.**
`examples/full/` is the shape: `main.go` calls `traced.Full`, `explicit.go` is what it expands to,
and the two are kept honest by the compiler rather than by a doc page. A doc snippet rots; a build
failure does not. Since a Preset has no options, copying the body is the only escape hatch, which
makes the copy load-bearing.

> **Amended by [#31](https://github.com/squall-chua/go-boot/issues/31).** This named
> `prototypes/cmd/full/` until the real one existed. `examples/full/` goes further than the
> compiler: one test drives BOTH forms against a real PostgreSQL and asserts they serve the same
> service, so a copy that still builds but no longer matches the Preset fails too.

---

## 6. The `main.go` a developer writes

Three variants, all compiling in `examples/`.

> **Amended by [#31](https://github.com/squall-chua/go-boot/issues/31).** This read "all compiling
> in `prototypes/cmd/`", and **"`cmd/full` compiles and vets but has never been run — it needs a
> Postgres. That has been true since #2."** It is no longer true. `examples/full` is run, in both
> forms, by `TestBothFormsServeTheSameService`, against a real PostgreSQL from `goboot/db/dbtest` —
> which is the package that removed the reason it had never been run.

### 6.1 `http-only` — no Preset form, and that is the point

There is no Preset for this shape, because one came out longer than the body.

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

	srv.Handle("GET /hello/{name}", http.HandlerFunc(hello))

	return app.Run(ctx)
}
```

### 6.2 `http-actuator-config` — the realistic default

One Transport, an Actuator, and the service's own config key. No Preset form.

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

	srv.Handle("GET /hello/{name}", greet(cfg.Greeting))

	return app.Run(ctx)
}
```

### 6.3 `full` — the whole v1 surface, both forms

HTTP, gRPC, database, Actuator and tracing. The Preset form is shorter, and #31 deleted the count
that used to be quoted here — see [5. The Presets](#5-the-presets-and-what-each-wires). Both forms
live in the same directory, CI builds both, and one test drives both.

**The two forms are not identical, and that is the point.** The explicit form wires
`goboot/web/metrics` ([4.3](#43-gobootweb--the-http-transport-starter)) and the Preset form does
not, because assertion 2 of [8.1](#81-the-import-leak-check) forbids `goboot/preset` from reaching
that package. That one line is what copying the body buys, and it is the only difference.

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

Explicit form — what `traced.Full` expands to, plus the one line no Preset can wire:

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
	act, err := actuator.New(cfg.Actuator, app)
	if err != nil {
		return err
	}
	srv, err := web.New(cfg.Web, app.Log)
	if err != nil {
		return err
	}
	// Five entries, not web's three; see 4.6. metrics.Middleware is the line
	// no Preset can wire, because assertion 2 of 8.1 keeps goboot/preset off
	// that package. See 4.3.
	srv.Use(append(trace.DefaultMiddleware(app.Log), metrics.Middleware)...)
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

Stripped, `go build -ldflags="-s -w"`, counting linked non-stdlib module roots and not counting
go-boot itself.

| binary | modules | bytes | stripped |
|---|---:|---:|---:|
| `examples/http-only` | 2 | 6,807,817 | 6.49 MB |
| `examples/http-actuator-config` | 11 | 11,628,809 | 11.09 MB |
| `examples/full` | 41 | 22,896,905 | 21.84 MB |

> **A fourth binary, added by [#34](https://github.com/squall-chua/go-boot/issues/34).**
> `examples/http-secure` is not a variant of the `main.go` this section is about — it is
> `http-actuator-config` plus the Security Starter — so it gets no row above. Measured the same way:
> **12 modules and 12,218,633 bytes**, against `http-actuator-config`'s 11 and 11,628,809. That is
> **+1 module and +589,824 bytes** for security headers, CORS, JWT verification and a guarded
> Actuator. See [4.7](#47-gobootsecurity--the-security-starter), which carries the same measurement
> taken from the lighter end.

> **Re-measured by [#46](https://github.com/squall-chua/go-boot/issues/46).** The `examples/full`
> row moved for two reasons at once, and the release note should name both, because the row is one
> number.
>
> - **+61,440 bytes, and no modules**, for wiring `goboot/web/metrics` onto `trace.DefaultMiddleware`
>   in `examples/full/explicit.go`, so that the traced line
>   [4.3](#43-gobootweb--the-http-transport-starter) and `README.md` print is compiled by CI rather
>   than checked by eye. 22,835,465 became 22,896,905. The module count stays at **41** because
>   every module that package links is one the Actuator already links, and
>   `.github/module-counts.txt` does not move at all, since it excludes `examples`.
> - **+4,096 bytes that are older than this change.** The pinned 22,831,369 already measured
>   22,835,465 on Go 1.26.3 before a line was touched, so the row was stale by one page. This
>   re-measure closes that too.
>
> The two lighter rows are unchanged, to the byte.

> **Re-measured by [#31](https://github.com/squall-chua/go-boot/issues/31).** These were the
> prototype's: 1 module and 6,414,601 bytes, 10 and 9,363,721, 21 and 14,405,897. They came with a
> caveat that `prototypes/goboot/trace` was signature-only and imported no OTel, so the `cmd/full`
> row measured call-site shape rather than weight. With the Presets landed there is a real
> `examples/full` to build, and the row it was warning about grew hard: **21 modules became 41, and
> 13.74 MB became 21.77 MB.** That gap is [#30](https://github.com/squall-chua/go-boot/issues/30)'s
> tracing Starter arriving for real — it measured +9.69 MB and +23 modules — plus the driver and
> the generated gRPC code the prototype also only stubbed. See [4.6](#46-goboottrace).
>
> The two lighter rows are unchanged from
> [#25](https://github.com/squall-chua/go-boot/issues/25), to the byte: it measured `http-only` at
> 6.49 MB and 2 modules, and the Actuator taking it to 11.09 MB and 11. Two independent
> measurements agreeing is what fixes the counting rule stated above, which the prototype's
> `http-only = 1` did not follow.

---

## 7. Dependencies, and the ticket that chose each one

go-boot links these and nothing else. Versions are the ones measured; re-check the proxy before
pinning.

| Module | Version | Used by | Ticket | Why it, and not the alternative |
|---|---|---|---|---|
| `go.yaml.in/yaml/v3` | v3.0.5 | base (config) | [#4](https://github.com/squall-chua/go-boot/issues/4) | stdlib plus a ~80-line loader beat koanf (16 modules) and viper (23 modules, 7.97 MB). `gopkg.in/yaml.v3` is archived, so the fork is used everywhere |
| `github.com/go-viper/mapstructure/v2` | v2.5.0 | base (config) | [#9](https://github.com/squall-chua/go-boot/issues/9) | relaxed key matching and type-directed comma lists need reflection the hand loader would have to write. **Zero transitive dependencies**: 1 `go.sum` module, 1 linked module |
| `github.com/prometheus/client_golang` | v1.24.1 | actuator, `goboot/grpc/metrics` | [#7](https://github.com/squall-chua/go-boot/issues/7), [#41](https://github.com/squall-chua/go-boot/issues/41) | `promhttp.Handler()` over the default registry. Two pipelines, not one: Prometheus for metrics, OTel for traces. #41 made that split a rule and gave it its first writer — **no new module**, because the counter and the histogram come from the package the Actuator already links |
| `connectrpc.com/connect` | v1.20.0 | grpc | [#5](https://github.com/squall-chua/go-boot/issues/5) | proven by experiment: a real `grpc-go` client reached it over cleartext with no proxy, and one port served Connect JSON and gRPC-Web too. CNCF sandbox. grpc-gateway's in-process mode is unary-only and kills interceptors; Vanguard is still alpha after three years |
| `google.golang.org/protobuf` | v1.36.11 | grpc, actuator (indirect) | [#5](https://github.com/squall-chua/go-boot/issues/5) | comes with connect. gRPC costs exactly these two modules and +3.57 MB. **Amended by [#28](https://github.com/squall-chua/go-boot/issues/28):** direct, not indirect — go-boot's own gRPC tests mount a generated service, and the generated code under `internal/gen` imports protobuf by name. Nothing a user links changes |
| `github.com/pressly/goose/v3` | v3.27.3 | db | [#6](https://github.com/squall-chua/go-boot/issues/6) | driven through `NewProvider` in-process. ⚠️ **goose does not lock unless told to** — `lock.NewPostgresSessionLocker()` is wired on by default. Atlas ruled out twice (its library never locks, and the real revision store is behind a paid binary); golang-migrate blocks uncancellably and carries the dirty flag |
| `go.opentelemetry.io/otel` and its SDK | v1.45.0 | trace | [#7](https://github.com/squall-chua/go-boot/issues/7), [#10](https://github.com/squall-chua/go-boot/issues/10) | traces only. **Amended by [#30](https://github.com/squall-chua/go-boot/issues/30):** measured on the real Starter rather than the stub, it is **+9.69 MB stripped and +23 modules**, which is why it is a separate Starter. #10's estimate was +9.4 MB and 19 indirect modules |
| `.../exporters/otlp/otlptrace/otlptracegrpc` | v1.45.0 | trace | [#7](https://github.com/squall-chua/go-boot/issues/7) | the OTLP/gRPC exporter, reading `OTEL_EXPORTER_OTLP_ENDPOINT` with no options. #7 measured OTLP/gRPC and OTLP/HTTP as a 0.15 MB wash and chose gRPC for fit; the swap is one line. **Added by [#30](https://github.com/squall-chua/go-boot/issues/30):** the row was missing, though #7 always named it |
| `google.golang.org/grpc` | v1.83.0 | trace (indirect) | [#7](https://github.com/squall-chua/go-boot/issues/7), [#30](https://github.com/squall-chua/go-boot/issues/30) | **Nobody chose this one; it arrives.** `go.opentelemetry.io/proto/otlp` pulls grpc-go in whichever OTLP protocol you pick, which is why #7 found the two exporters weigh the same. It is a dependency of the trace Starter only — the gRPC Transport still writes `connectrpc.com/connect` and nothing else (ADR `0005`), and a service that does not trace links no grpc-go |
| `.../contrib/instrumentation/net/http/otelhttp` | v0.70.0 | trace | [#7](https://github.com/squall-chua/go-boot/issues/7) | HTTP spans, span name from `r.Pattern` |
| `connectrpc.com/grpchealth` | v1.5.0 | `goboot/grpc/health` | [#29](https://github.com/squall-chua/go-boot/issues/29) | the health proto and its handler, wire compatible with `grpc-health-probe`. Writing it in-house would mean generating `health.proto` here. **One extra linked module**: it needs only connect and protobuf, which `goboot/grpc` already has |
| `connectrpc.com/grpcreflect` | v1.3.0 | `goboot/grpc/reflection` | [#29](https://github.com/squall-chua/go-boot/issues/29) | the reflection protos, both v1 and v1alpha, and the static reflector. **One extra linked module**, same reason |
| `connectrpc.com/otelconnect` | v0.9.0 | `goboot/trace/rpc` | [#12](https://github.com/squall-chua/go-boot/issues/12) | RPC spans. Separate subpackage, and `goboot/trace` filters `otelhttp` for RPCs or you get two nested spans |
| `github.com/fergusstrange/embedded-postgres` | v1.34.0 | `goboot/db/dbtest` | [#13](https://github.com/squall-chua/go-boot/issues/13) | 3 linked modules against `testcontainers-go`'s 45, and no Docker daemon. Real PostgreSQL 18.3 up in 2.77s |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | `goboot/security` | [#34](https://github.com/squall-chua/go-boot/issues/34) | **1 linked module with zero transitive dependencies**, +36 KB on its own — measured against `go-jose/v4` at 1 module and +495 KB and `coreos/go-oidc/v3` at 3 modules and +1.17 MB. Wiring the Starter costs **+708,608 bytes**, mostly stdlib crypto that stops being dead code once something verifies a signature; [4.7](#47-gobootsecurity--the-security-starter) has both numbers and why they differ. It ships no JWKS client, so `goboot/security` writes one over `crypto/rsa` and `crypto/ecdsa` |

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

**Five assertions.** The first four were settled in
[#14](https://github.com/squall-chua/go-boot/issues/14) and prototyped as
`prototypes/scripts/check-imports.sh`, all four passing, **and verified to fail**: importing
`goboot/trace` from `goboot/preset` makes assertion 2 report the leak and the script exit 1. The
fifth was added by [#33](https://github.com/squall-chua/go-boot/issues/33), and is listed with them
below.

1. **The base package and its *tests* import no Starter.** `go list -deps` alone misses test
   imports, so `.TestImports` and `.XTestImports` must be asked for explicitly. This is #3's hard
   rule, and it is about `go mod tidy`, not about the build.
2. **No short-path package reaches a heavy optional package.** The short paths are `goboot`,
   `goboot/web`, `goboot/db`, `goboot/actuator`, `goboot/grpc`, `goboot/preset` and
   `goboot/security`, the last added by
   [#34](https://github.com/squall-chua/go-boot/issues/34). The heavy
   optional packages are `goboot/trace`, `goboot/grpc/health`, `goboot/grpc/metrics`,
   `goboot/grpc/reflection`, `goboot/trace/rpc`, `goboot/db/dbtest` and `goboot/web/metrics`.
   `goboot/grpc/metrics` was added by [#41](https://github.com/squall-chua/go-boot/issues/41), and
   it is the rule doing its plainest job: the package registers on the Prometheus default registry,
   so listing it is what stops `goboot/grpc` growing the dependency
   [9](#9-known-gaps-in-v1) says an HTTP-only user must not pay. `goboot/web/metrics` was added by
   [#45](https://github.com/squall-chua/go-boot/issues/45) for the same reason on the HTTP side, and
   it is the entry that does the second job named in [4.3](#43-gobootweb--the-http-transport-starter):
   it is what makes "no Preset wires it" a rule the toolchain checks rather than a promise in prose,
   because `goboot/preset` is a short path and so may never reach it.
3. **`goboot/db` links no driver.** Grep the dependency list for `jackc`, `go-sql-driver`, `lib/pq`
   and `mattn/go-sqlite3`.
4. **A pinned module count per package**, in a golden file, regenerated deliberately and never
   silently. This is the one that catches the next leak nobody predicted. It could not be shown
   firing in the prototype, because the trace stub adds no modules; in the real repo that leak moves
   `goboot/preset` from 15 modules to roughly 34.
5. **A pinned count of the modules a package's *tests* link**, in the same golden file, one row per
   package, regenerated deliberately and never silently. `go list -deps` excludes tests by design,
   so assertion 4 counts what a **user** links and goes on counting exactly that. But `go mod tidy`
   walks test imports, so a heavy dependency added to a test lands in every consumer's module graph
   even though it reaches no consumer's binary — #3's hard rule, arriving through a door the four
   assertions above do not name.

The check must cover **every package a short path imports, not just `goboot`** — the rule as first
written missed `goboot/preset`, whose Preset dragged the Actuator into an HTTP-only binary: 10
modules and 12.4 MB against 1 module and 9.2 MB.

> **Built by [#32](https://github.com/squall-chua/go-boot/issues/32).** The check is
> `.github/check-imports.sh`, run by CI on every push, with the golden counts of assertion 4 in
> `.github/module-counts.txt` and regenerated only by `.github/check-imports.sh --update`. All four
> assertions were **proven to fail** against a real violation before they were trusted to pass: a
> base test importing `goboot/web` fails 1, `goboot/preset` importing `goboot/trace` fails 2,
> `goboot/db` blank-importing `pgx/v5/stdlib` fails 3, and `goboot/web` importing
> `prometheus/client_golang` fails 4 **and nothing else** — which is assertion 4 doing the job the
> other three cannot. The four injections and their output are recorded on #32.
>
> **A seventh package joins assertion 2.** `goboot/preset/traced` is not a short path, but it is the
> one package allowed to reach `goboot/trace`, so it is checked against the other six heavy
> packages. (The count is written out here, so it moves whenever the list does: it read "four" until
> [#45](https://github.com/squall-chua/go-boot/issues/45), which is one behind what #41 had already
> left — the script reads the list rather than the number, so only this sentence was ever wrong.) Without that it is the obvious place for the next heavy dependency to hide: a twin that
> may reach one heavy package reads, to the next person, as a twin the rule does not apply to.
>
> **Two numbers moved.** The prediction above was that the trace leak takes `goboot/preset` from 15
> modules to roughly 34; measured, it is **16 to 36**. And the package list for assertion 4 is read
> from `go list ./...` rather than written out, so a **new package is a golden-file change too**,
> which is the same leak arriving by a different door.
>
> **Two Go tests keep the rules they already owned**, named in the script so the pair cannot drift:
> `db/db_test.go`'s `TestStarterLinksNoDriver` is assertion 3 inside the package it guards, and
> `trace/trace_test.go`'s `TestABuildWithNoTracingLinksNoTracing` is the tracing third of assertion
> 2 and goes further than the script — it covers the examples, and asserts `goboot/trace` does not
> link `otelconnect`, which is a rule about a heavy package rather than about a short path.

> **Assertion 5 was added by [#33](https://github.com/squall-chua/go-boot/issues/33).** The gap was
> found while building #32 and left alone there, because quietly making four assertions five is not
> a build decision. Measured on `main` at `95b5156`, `go list -deps ./db` reports 7 modules and
> `go list -deps -test ./db` reports 24: seventeen modules that no user links, and that nothing
> checked.
>
> **The rule is a count, not a list of allowed modules.** An allowlist would name 32 module paths
> today, nearly all of them transitive ones nobody chose, and it would churn on every upstream
> version bump. A count is the same shape assertion 4 already proved, and it reuses the one golden
> file, the one `--update` path and the one diff.
>
> **Both numbers share `.github/module-counts.txt`**, as `<package> <user links> <tests link>`, with
> a header line naming the columns. The two are compared column by column, so the report says which
> assertion moved rather than leaving the reader to guess. Four rows carry a second number today:
> `goboot/db` 7 → 24, `goboot/trace/rpc` 10 → 27, `goboot/grpc/health` 5 → 13 and
> `goboot/grpc/reflection` 3 → 5. The other eight packages pull nothing extra in their tests.
>
> **Proven to fail**, the way the other four were: a `goboot/web` test importing
> `prometheus/client_golang` moves that row from `2 2` to `2 11` and fails 5 **and nothing else**.
> The same import in non-test code fails 4 and 5 together, which is the column split doing its job
> in both directions. Editing the header line fails 4 and 5 together as well, because the header is
> the only thing telling a reader which column is which. Both injections and their output are
> recorded on #33.
>
> **One door is left open, on purpose.** Assertion 5 inherits assertion 4's exclusion of `examples`
> and `internal`, so a heavy dependency added to an **example's** test still reaches every
> consumer's module graph unchecked. Those binaries import the heavy packages by design, so their
> counts move whenever an example is edited, and a number that moves for ordinary work pins
> nothing. Named here so nobody has to rediscover it.

### 8.2 CI builds both forms of every Preset

One example directory per Preset, holding the Preset form and the explicit form it expands to. CI
builds both. See [5. The Presets](#5-the-presets-and-what-each-wires).

### 8.3 The ordinary gates

`go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l .` clean.

> **Done.** The workflow is `.github/workflows/ci.yml`, on push and on pull request. It arrived a
> gate at a time rather than all at once — the four gates above with the first route served, then
> the query-layer neutrality build of ADR `0009` with [#26](https://github.com/squall-chua/go-boot/issues/26),
> then both forms of the Preset for 8.2 with [#31](https://github.com/squall-chua/go-boot/issues/31),
> which `go build ./...` and `go test ./...` cover because `examples/full` holds both. **Section 8
> closes with [#32](https://github.com/squall-chua/go-boot/issues/32)**, which replaced the one
> inline assertion with the whole check of 8.1.

### 8.4 The README's Go samples are compiled

`docs_test.go` asserts three things. Two cover the README's ten-minute path: every Go block on it
carries a marker naming an example file, and every marked block is still in that file verbatim.
The third covers the reference sections below **Where to go next**: a Go block that is byte for
byte inside this module's compiled code must carry a marker as well. The files those markers name
are built by `go build ./...`, so a marked block is compiled code and drift fails the build. See
[13. Docs and examples](#13-docs-and-examples).

> **Built by [#40](https://github.com/squall-chua/go-boot/issues/40)**, which found the README's
> `## Use` block had already drifted from `examples/http-only`. It runs under the ordinary
> `go test ./...` of 8.3 and needs no new CI step.

> **Extended by [#48](https://github.com/squall-chua/go-boot/issues/48)**, which added the third
> assertion, `TestVerbatimBlocksAreMarked`, and with it the rule that a reference block which *can*
> be lifted whole has to be. It masks out comments before it looks, so a block found only inside a
> doc comment earns no marker: nothing compiles it. A marker naming `prototypes/` fails, because
> that is a separate module CI does not build.

---

## 9. Known gaps in v1

Written down as gaps, not left out.

- **The gRPC mount stays in `main` in both the Preset and the explicit form**, because it names
  the user's generated package. **So no Preset can protect anyone from the missing error-sanitising
  interceptor**, and #12 measured what that costs: a bare `error` reaches the caller verbatim,
  password and all. [#38](https://github.com/squall-chua/go-boot/issues/38) tried to close it with
  a `grpc.Handle` that carried the options, and refused every shape of it: none is both shorter
  than the raw mount and compile-checked, and one that is longer is one nobody reaches for.
  [#42](https://github.com/squall-chua/go-boot/issues/42) closed as `wontfix` on that record: the
  gap stands until connect gains server-level handler options, or Go's inference accepts an
  argument assignable to a type parameter an earlier argument already bound. Both were re-checked
  against connect `v1.20.0` and Go 1.26 and neither holds. What #38 did close is the larger half
  nobody had measured — the adapter this spec printed leaked the same string **with the interceptor
  correctly wired**, because it wrapped the raw error in a `*connect.Error`.
  ([4.0](#40-the-error-convention-every-starter-follows))
- **A failure connect writes into the response BODY is still logged as a plain 200.** The access
  log reads the gRPC status out of the response trailers, so it cannot see a failure that never
  reaches a header. Two shapes do that, both measured on the real server in
  [#43](https://github.com/squall-chua/go-boot/issues/43): a **gRPC-Web** call that fails after its
  first message, because connect moves gRPC-Web trailers into the body once anything has been
  written; and a **Connect-protocol stream**, which puts its error in the end-of-stream envelope
  after the 200 has already gone. Plain gRPC is covered however late the failure comes, a Connect
  unary failure shows its real status, and every gRPC-Web unary failure is trailers-only and so is
  covered too. The error interceptor's log line carries the real code in all four cases.
  `TestTheAccessLogMissesAFailureInTheBody` pins both shapes, so this entry cannot go stale in the
  direction that flatters go-boot.
- **`ddl-auto=validate` has one hole the JPA lint cannot close.** `varchar` length lives in the
  Java class, so the lint is a supplement to `validate`, never a replacement.
  ([#18](https://github.com/squall-chua/go-boot/issues/18))
- **`maxOpenConns: 10` is 10 pods before a stock Postgres runs out.** Not a defect, but an operator
  must not discover it at scale.
- **Two Apps in one process share one Prometheus registry.** go-boot is one service per process, so
  nobody pays it.
- **And they share one tracer provider, for the same reason.** `trace.Component.Start` calls
  `otel.SetTracerProvider` and `otel.SetTextMapPropagator`, which are process-wide slots, so a
  second App's `Start` replaces the first App's exporter rather than adding to it. Identical in
  shape to the registry above and it costs nobody anything for the same reason, but it is the
  answer to "can I run two go-boot Apps side by side", and that answer is no.
  ([#30](https://github.com/squall-chua/go-boot/issues/30))
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

In scope for go-boot, but not in this spec and not blocking the *building* of v1.

**The error-handling convention has left this list.** It was the one item that gated the `v1.0.0`
tag, and it is settled in
[4.0. The error convention every Starter follows](#40-the-error-convention-every-starter-follows)
([#38](https://github.com/squall-chua/go-boot/issues/38)). Nothing left below gates a tag: both
remaining entries are additive. (It read "four" until
[#35](https://github.com/squall-chua/go-boot/issues/35) — one *ahead* of the list, because #34
removed a bullet without touching the number.)

**The Security Starter has left this list.** It is settled in
[4.7. `goboot/security`](#47-gobootsecurity--the-security-starter)
([#34](https://github.com/squall-chua/go-boot/issues/34)): security headers, CORS, a JWT bearer
middleware over a JWKS key set, and per-route scope checks. Two things it named are **not** in that
section and are not on this list either, because neither is a Starter — OIDC discovery and opaque-token
introspection are additions to `goboot/security`, listed at the end of 4.7.

**The docs and examples strategy has left this list too.** It is settled in
[13. Docs and examples](#13-docs-and-examples)
([#40](https://github.com/squall-chua/go-boot/issues/40)), which fixes the ten-minute path and the
rule that keeps it from rotting.

**The Messaging Starter has left this list, without joining v1.** It is specified in
[14. The Messaging Starter](#14-the-messaging-starter-specified-and-post-v1)
([#35](https://github.com/squall-chua/go-boot/issues/35)): two Starters rather than one, `goboot/kafka`
over franz-go and `goboot/rabbit` over `amqp091-go`. It is the first entry to leave for a section
outside [4](#4-the-public-api-of-every-v1-starter), and the distinction is the point — the design is
settled, the fifteen packages are unchanged, and it ships after `v1.0.0`. Specifying it also added
a condition this bullet did not have. A consumer is still the named user of `Drainer`, but its
`Drain` **cannot be the thing that waits for in-flight work**: `Run` hands every `Drain` a context
with no deadline that nothing can cancel, so a `Drain` that waits hangs the whole shutdown with
nothing able to interrupt it. The waiting moves to `Stop`, which has a real budget.
[14](#14-the-messaging-starter-specified-and-post-v1) has the code references and the consequences.

- **Cache / Redis Starter** — likely a thin wiring Starter; unclear whether it earns its place.
- **Scaffold CLI design** — commands, flags, what it writes, how thin the generated `main` stays.
  It already carries three requirements from this spec: write the `myservice migrate` subcommand and
  the driver blank import; write the `buf` files and the gRPC adapter type only when gRPC is chosen;
  and write schemas that use `timestamptz`, identity ids and `lower_snake_case`, with a way to add
  the `version` column and the `ddl-auto=validate` CI job. Only the `version` column is
  JPA-specific — the rest is right for everyone, so most of it should be the default rather than a
  flag.

---

## 12. Versioning and release policy

Settled by [#39](https://github.com/squall-chua/go-boot/issues/39). This section is the whole
policy: follow it and a tag can be cut with nothing left to decide.

### One tag covers every Starter

go-boot is **one Go module** ([1. Ground rules](#1-ground-rules)), so there is **one tag**, and it
covers all fifteen packages at once. A fix in `goboot/db` bumps the version number a root-only user
sees, even though nothing they import changed. That is the price of the one-module layout, and
[#3](https://github.com/squall-chua/go-boot/issues/3) measured it as small: a root-only consumer
downloads 1 zip and 2.9 KB, so the upgrade they did not need is an upgrade they barely pay for.

The reverse also holds, and it is the part to say out loud: **a user cannot pin one Starter to an
older version than another.** There is one version number for the whole library. Anyone who needs
otherwise is asking for the multi-module layout #3 refused.

### The public surface is these fifteen packages

`goboot`, `goboot/actuator`, `goboot/web`, `goboot/web/metrics`, `goboot/grpc`,
`goboot/grpc/health`, `goboot/grpc/metrics`, `goboot/grpc/reflection`, `goboot/db`,
`goboot/db/dbtest`, `goboot/trace`, `goboot/trace/rpc`, `goboot/preset`, `goboot/preset/traced`
and `goboot/security`, the last added by
[#34](https://github.com/squall-chua/go-boot/issues/34).
Every exported identifier in them is covered by the promise below. `goboot/db/dbtest` is on the
list and not an afterthought: go-boot's own tests use it, so it is shipped code a user may
reasonably build on.

Three things in the repository are **not** surface, and each is excluded by a mechanism rather than
by a sentence, so it cannot drift. `internal/` is excluded by the Go compiler. `examples/` are
`package main` and cannot be imported. `prototypes/` carries its own `go.mod`, so it is a separate
module and ships to nobody.

Surface is not only Go identifiers. **The config keys of [3. Config](#3-config) and their default
values, and the Actuator's endpoint paths and response bodies, are surface too** — an operator's
dashboard breaks on a renamed JSON field exactly as a compile breaks on a renamed function.

A new package cannot appear unnoticed: `.github/check-imports.sh` reads the package list from
`go list ./...` and pins one row per package in `.github/module-counts.txt`, regenerated only by
`--update`, so **a new package fails CI until someone updates the golden file**
([8.1](#81-the-import-leak-check)). Nothing ties that file to the fifteen names written above,
though, so **whoever updates it updates this list in the same commit.** #41 is the first change to
test that rule, and it held: `goboot/grpc/metrics` went into the list and the golden file
together. [#45](https://github.com/squall-chua/go-boot/issues/45) is the second, and it held the
same way: `goboot/web/metrics` and the row `goboot/web/metrics 9 11`, one commit.

### The line before v1 is `v0.x`

**Cut `v0.1.0` from `main` now.** Go treats a `v0` major as unstable by rule and this policy does
not pretend otherwise:

- **A `v0` minor bump may break anything, and is also how anything is added.** Every break is
  named in the release note, with the line a user has to change.
- **A `v0` patch bump neither breaks nor adds.** Bug fixes only.

The `v0` line exists for one reason, named in [the gate below](#the-one-thing-that-gates-v100), and
it ends as soon as that reason does.

### What a `v1.x` tag promises

Once `v1.0.0` is cut, for the whole life of `v1`:

- **No exported identifier in the fifteen packages is removed or renamed, and no function or method
  signature changes.**
- **No config key is removed or renamed, and no default value changes.** `maxOpenConns` is still
  `10` on the last `v1` release. A silently improved default moves a production knob its owner
  never touched, which is worse than a bad default they can see. A better default is a `v2` change,
  or a new key.
- **A minor release may add**: exported identifiers, whole packages, and config keys whose default
  preserves today's behaviour.
- **A patch release fixes bugs and nothing else.**
- **If a break ever becomes necessary it is `github.com/squall-chua/go-boot/v2`** — a different
  import path, so no user is broken by an upgrade they did not type.

**Preset bodies are the one deliberate exception, and it is the product, not a loophole.**
`preset.Full` and `traced.Full` keep their signatures for the life of `v1`, but **what they wire
may grow in a minor release** — a fourth default middleware, a new Component. ADR `0010` and the
README already promise exactly this: wiring held in a Preset gets fixed by `go get -u`, and that is
the only argument the Preset survives on. The signature is the promise; the body is not. A user who
needs the body frozen copies it into their own `main`, which ADR `0010` already names as the
supported escape hatch. Every change to a Preset body is named in the release note.

### What the promise does not cover

Named here so nobody has to infer them from silence.

- **The wording of log messages.** The access log's fields are stable; its prose is not.
- **The Go version floor.** It may rise in a minor release, the way Go's own modules raise theirs.
  Today it is the `go 1.25.7` in `go.mod`, not the `1.25.0` of §1's bold rule — see the note under
  it in [1. Ground rules](#1-ground-rules). A move is named in the release note.
- **Dependency versions.** Any dependency in [7. Dependencies](#7-dependencies-and-the-ticket-that-chose-each-one)
  may be bumped in a minor release. A *new* dependency still obeys the optional-subpackage rule, and
  shows up as a `.github/module-counts.txt` change either way, so it cannot arrive quietly.
- **Six of the eight gaps of [9. Known gaps in v1](#9-known-gaps-in-v1).** All eight ship **with**
  `v1` rather than blocking it. Closing one is a minor release **only where the fix adds something
  or repairs a bug** — for several of those the gap *is* the current behaviour, so the fix changes
  what an operator sees and belongs in the release note. #43 is the worked example: the access log
  used to record a plain 200 for every failed gRPC call, and closing that added an `rpcCode` field
  to a line operators already grep.
  **Two of the eight cannot be closed inside `v1` at all**, because the rules above forbid it:
  `maxOpenConns: 10` is a default value, and splitting the Actuator's Prometheus weight into
  `goboot/actuator/metrics` would need a second mount line in the user's own `main`. Both wait
  for `v2`. They are gaps go-boot has decided to live with for the life of `v1`, and the release
  note says that rather than implying a fix is coming.

### The one thing that gates `v1.0.0`

Every item in [11. Deferred past v1](#11-deferred-past-v1) is **additive**: the Security, Messaging
and Cache Starters are new packages and the Scaffold is a separate binary. A `v1` minor release
can carry any of them.

Exactly one item was ever able to change the surface of an existing Starter: the error-handling
convention ([#38](https://github.com/squall-chua/go-boot/issues/38)). It decided what every Starter
returns on misconfiguration and whether a go-boot error type exists at all — a signature question
across all fifteen packages. Freezing the surface before it landed would have frozen it wrong, and
the only way out of that is a `v2` on a library that has barely shipped.

So the gate was one sentence, and it was checkable rather than a judgement call:

> **`v1.0.0` is cut from the first `v0` release that ships the settled error-handling convention
> of [#38](https://github.com/squall-chua/go-boot/issues/38)**, with the checklist below green.

**Settled** means the same here as everywhere else in this file, so it is a fact to look up rather
than a judgement to make: **[#38](https://github.com/squall-chua/go-boot/issues/38) is closed**,
and this spec has a section stating what it settled.
[4.0. The error convention every Starter follows](#40-the-error-convention-every-starter-follows)
is that section, so **with #38 closed against it the gate is open**. It changed two signatures,
`web.New` and `actuator.New`, and that change must ship in a `v0` release **before** the `v1.0.0`
tag rather than in the tag itself.

### The release checklist

The same five steps for every tag, `v0` and `v1` alike.

1. **CI green on `main`**: the four ordinary gates of [8.3](#83-the-ordinary-gates), the import-leak
   check of [8.1](#81-the-import-leak-check), both Preset forms of [8.2](#82-ci-builds-both-forms-of-every-preset),
   and the query-layer neutrality build of ADR `0009`.
2. **`.github/module-counts.txt` is unchanged**, or its change is deliberate and named in the
   release note.
3. **The release note is written** as the body of a GitHub Release —
   `gh release create vX.Y.Z --notes-file ...` — carrying everything in the list below. There is
   no `CHANGELOG.md`: the tag and its notes are one object, and the issue tracker is already on
   GitHub.
4. **Tag `main` directly, annotated**: `git tag -a vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z`.
   Annotated, because a lightweight tag records no tagger and no date, which is most of what a
   release tag is for. There is no release branch — go-boot commits to `main`, and one module with
   one tag has nothing to branch for.
5. **Confirm the tag resolves.** While the repository is public, ask the proxy:
   `GOPROXY=https://proxy.golang.org go list -m github.com/squall-chua/go-boot@vX.Y.Z`.
   **While it is private the proxy answers 404, and that is not a failed release** — it cannot read
   the repository at all. Check the tag itself instead, the way a user with access would:
   `GOPRIVATE=github.com/squall-chua/* GOPROXY=direct go list -m github.com/squall-chua/go-boot@vX.Y.Z`.
   Note what that means while it lasts: **a tag alone does not make go-boot dependable by an outside
   user — the repository has to be public too.** That is a visibility decision, not a versioning one,
   and this policy does not make it.

### What every release note carries

The first five are per-release. The last is on **every** note, unchanged, because these are the two
numbers an operator otherwise discovers at scale.

- **Every break**, with the line a user has to change. On a `v1` release this list is empty by
  definition; if it is not, the tag is wrong.
- **Every package added to the public surface**, with the one line saying when to reach for it. A
  package arriving breaks nothing, so the bullet above never names it, and a user who reads only the
  note learns that a version shipped and not that a Starter did.
- **Every Preset body change**, because a Preset user's wiring changed without their `main` changing.
- **Every gap of §9 that was closed**, plus the §9 list as it still stands. The remaining gaps ship
  with the release; they belong in the note, not in a file the operator finds afterwards.
- **The Go version floor, if it moved.**
- **These two numbers, every time**: `/actuator/metrics` answers **404 until `metrics` is named in
  `actuator.expose`**, and **`maxOpenConns: 10` is ten pods** against a stock PostgreSQL, which
  allows about 97 connections.

> **The second bullet was added after [#34](https://github.com/squall-chua/go-boot/issues/34).** The
> gap was found by asking which of the five original bullets obliged a note to mention
> `goboot/security`, and finding that none did: the list was five kinds of **change to something
> that already exists**, and had no entry for something arriving. It is the same shape as the hole
> [#44](https://github.com/squall-chua/go-boot/issues/44) exists for, and it is recorded there too.
>
> **The mechanism of [8.1](#81-the-import-leak-check) does not reach this far, and that is the
> point.** A new package fails CI until `.github/module-counts.txt` is regenerated, and the rule
> above says whoever regenerates it updates the fifteen names in this section too. Both of those end
> inside this repository. Nothing carried the fact into a release note until this bullet did.
>
> The count in the sentence above this list is written out, so it moves whenever the list does — the
> same trap [8.1](#81-the-import-leak-check) names about its own.

---

## 13. Docs and examples

Settled by [#40](https://github.com/squall-chua/go-boot/issues/40). This section is the whole
strategy, and it replaces the *Docs and examples strategy* entry that stood in
[11. Deferred past v1](#11-deferred-past-v1).

### The rule the whole section comes from

**A doc snippet rots; a build failure does not.** Every Go sample a newcomer is shown is code this
repository compiles, not prose sitting next to it. That one rule decides everything below.

It had already been paid for once. The `## Use` block in `README.md` said it was the same service
as `examples/http-only`, and it was not: the block used `log.Fatal` and `HandleFunc` where the
example used a `run(ctx) error` shape and `Handle`. The two drifted apart and nothing failed,
because nothing was checking.

### The ten-minute path

One path, three stops, in this order. It sits at the top of `README.md` under **Ten minutes to a
running service**, above every reference section, so a newcomer meets it before anything else.

| Stop | What it adds | Where the reader ends up running |
|---|---|---|
| 1. The smallest service | one Transport and the default middleware | `go run ./examples/http-only`, then `curl localhost:8080/hello/world` |
| 2. The Actuator and config | the operational endpoints and the service's own config key | `go run ./examples/http-actuator-config`, then `curl localhost:8080/readyz` and `/actuator/metrics` |
| 3. The whole surface | HTTP, gRPC, database and tracing, wired by one call to a Preset | `go run ./examples/full`, and `go run ./examples/full explicit` for the same service wired by hand |

> **Amended by [#48](https://github.com/squall-chua/go-boot/issues/48).** Stops 1 and 2 carried
> "eight wiring lines" and "fourteen wiring lines". Under the one counting rule that reproduces
> `http-only` at 8, stop 2 is **13**, not 14 — the same mismatch that had already made
> [#31](https://github.com/squall-chua/go-boot/issues/31) delete the wiring-line table from
> [5](#5-the-presets-and-what-each-wires) rather than correct it. Deleted here for the same
> reason, and removed from the two example doc comments still repeating them. A number no rule
> reproduces is exactly the unverifiable prose this section keeps off the path.

The three stops are the three example directories [#19](https://github.com/squall-chua/go-boot/issues/19)
already scoped, in the order [6. The `main.go` a developer writes](#6-the-maingo-a-developer-writes)
already puts them. **No fourth directory and no tutorial living outside them**: a second place to
learn go-boot is a second place to go stale.

**Every stop ends in a command, not a paragraph.** The path is measured by where the reader gets
to, so a stop that does not finish with something running is not a stop.

**Stop 3 needs a PostgreSQL and says so.** `examples/full/app.yaml` points `db.dsn` at a throwaway
local one, so the reader is not asked to invent a DSN before anything runs.

The two numbers of [What every release note carries](#what-every-release-note-carries) are said
on the path as well, each at the stop where it bites: the metrics whitelist at stop 2, and
`maxOpenConns: 10` at stop 3.

### Every sample on the path is compiled

A code block on the path is **lifted verbatim** out of an example file, and an HTML comment above
it names that file:

    <!-- from: examples/http-only/main.go -->
    ```go
    func run(ctx context.Context) error {
    	app, err := goboot.New(goboot.Config{})
    	...
    }
    ```

`docs_test.go` checks this in two halves, and **both are needed**. It asserts that every marked
block is still inside the file its marker names, and separately that every Go block *between the
path's heading and the next one* carries a marker at all. The example files are compiled by
`go build ./...` and driven by `go test ./...`, so a marked block is compiled code and drift fails
the build. This is [8.4](#84-the-readmes-go-samples-are-compiled).

**The second half is the one worth spelling out.** A check that only compares marked blocks makes
marking optional: dropping one marker, or pasting in a fresh block that never had one, buys silence
rather than a failure. So the path's Go blocks are found by position and the marker is required of
each, which is the difference between a rule and a habit. Renaming the path's heading fails too,
because an empty range would otherwise pass with nothing checked.

**Verbatim means byte for byte, tabs included.** There is no fuzzy match and no whitespace
tolerance: a check that forgives small differences is a check that lets a wrong snippet through,
and the rot it was written to catch starts small.

The shell blocks holding the `go run` and `curl` lines carry no marker and are **not** checked.
They are commands, not samples lifted from a file, so there is nothing to lift them from. This is
the one gap on the path, and it is named here rather than left for a reader to notice.

### What is not on the path, and why that is right

Everything below **Where to go next** in `README.md` is reference: the default middleware, the
Actuator, gRPC, tracing, the database and the Preset. Most of those sections quote single lines and
fragments to make a point, and **a fragment has no file it can be lifted from whole**. Those carry
no marker and no guarantee. Marking a fragment would mean reshaping the prose around what the
checker can verify, which is the wrong way round.

> **Amended by [#48](https://github.com/squall-chua/go-boot/issues/48).** This section used to say
> the reference blocks carry no marker at all. Five of them are lifted whole out of code CI
> compiles, so that sentence was giving up rot protection which cost one line each to have.

**A reference block that can be lifted whole must carry a marker.** Not *may*: **must**. The path
still gets the guarantee because the path is what a newcomer copies, but a block that already
qualifies is free to check, and declining to check it buys the reader nothing. `docs_test.go`
checks a marked block wherever it sits, so this needed no new checking of blocks — only a rule
saying which blocks have to be marked.

**And the rule is enforced, because a hand-marked block is only a habit.** The path can demand a
marker *by position*: every Go block between its heading and the next one needs one. The reference
sections cannot be checked that way, because their fragments legitimately have no marker. So the
demand is made the other way round — `TestVerbatimBlocksAreMarked` finds every Go block in
`README.md` that is byte for byte inside a file in this module, and fails unless it carries a
marker. This is the same rule-not-habit argument that
[the second half of the path check](#every-sample-on-the-path-is-compiled) rests on, applied where
position cannot reach.

**Being inside the file is not enough; it has to be inside the code.** #48 opened by listing seven
blocks as already verbatim. Two of them — the `metrics.Middleware` line and the `pgx/v5/stdlib`
blank import — sit in `web/metrics/metrics.go` and `db/db.go` only inside a **doc comment**, and
nothing in this module compiles them: they are lines the *reader* writes, not lines go-boot runs.
Marking those two would have synced one piece of prose to another, so renaming the symbol they both
name would break neither and the marker would promise what it cannot keep. **They stay unmarked**,
and the check knows the difference: it masks out every comment first, and asks whether the match
lands on a byte the compiler actually reads. A block that merely *carries* a comment still counts,
because the code around it is compiled.

**`prototypes/` does not count.** It is a separate Go module and CI does not build it, so a marker
naming a file in there would promise a guarantee that nothing keeps. The check walks this module
only, in lexical order, so a block sitting in more than one file always names the same one.

**So the marker is the whole rule, and `README.md` says it out loud**: a marked block is checked
and an unmarked one is not. The reader tells them apart by looking, rather than by knowing where
the path ended.

### The other three documents

None is on the ten-minute path, and this section does not change any of them.

- `docs/spec.md` — the design authority. Read by whoever changes go-boot, not by a newcomer.
- `CONTEXT.md` — the vocabulary. A newcomer meets Starter, Preset, Component and Tier in the
  README as they go; this file is where each definition lives.
- `docs/adr/` — one decision per file, read when someone asks why go-boot is like this.

---

## 14. The Messaging Starter, specified and post-v1

Specified by [#35](https://github.com/squall-chua/go-boot/issues/35). It has its own section rather
than a `4.x` one because [4. The public API of every v1 Starter](#4-the-public-api-of-every-v1-starter)
is tied to the fifteen packages
[12. Versioning and release policy](#12-versioning-and-release-policy) promises, and messaging is
not one of them. [11. Deferred past v1](#11-deferred-past-v1) called it "specifiable now, but not a
v1 Starter", and both halves of that sentence are still true: the design below is settled, and it
ships after `v1.0.0`.

### It is two Starters, not one with two backends

**`goboot/kafka` and `goboot/rabbit`, side by side with `goboot/web` and `goboot/db`.** There is no
`goboot/messaging` parent and no shared `Consumer` interface.

`CONTEXT.md`'s rule is that Go links by import, so a Kafka user must not link RabbitMQ. Two
packages satisfy that on their own. The question the ticket asked was whether they should hang off
a parent, the way `goboot/grpc/health` hangs off `goboot/grpc`, and the answer is no, for two
reasons:

- **A parent would have nothing in it.** What the two consumers share is `goboot.Component`,
  `goboot.Drainer` and `goboot.Checker`, which already live in the base package. A parent holding
  only a `Handler` type is a package that exists to be a namespace.
- **The message types genuinely differ, and flattening them loses information.** Kafka acknowledges
  by committing a partition offset; AMQP acknowledges one delivery tag and chooses whether to
  requeue. A shared `Message` would carry `Partition`, `Offset`, `DeliveryTag` and `Redelivered`
  with half of them zero on any given broker, and the first user to branch on which half is
  populated is back to writing broker-specific code through an abstraction that promised they would
  not have to.

The rejected shape is recorded because it is the one a reader will propose again: an empty
`goboot/messaging` directory is also the obvious place for the next shared dependency to hide,
which is the argument [8.1](#81-the-import-leak-check) already made about `goboot/preset/traced`.

**One Component consumes one topic or one queue.** Two topics is `New` called twice and `Add`
called twice, which is how `goboot/web` already handles two ports. That removes the routing table,
the per-topic handler map and the question of what happens when one topic's handler panics and the
others are mid-flight. `Name()` carries the topic, so the two Components do not collide on the
duplicate-name check.

### The dependency each one links

Measured on go1.26.3, linux/amd64, with `go build -ldflags="-s -w"` against a `fmt.Println`
baseline of 1,589,410 bytes. "Linked" counts non-stdlib module roots the way
`.github/check-imports.sh` counts them.

| Candidate | Version | Linked | Stripped delta | Verdict |
|---|---|---|---|---|
| `github.com/twmb/franz-go` | v1.21.6 | 4 | **+7,352,423 B (+7.35 MB)** | **chosen for Kafka** |
| `github.com/segmentio/kafka-go` | v0.4.51 | 3 | +4,845,671 B (+4.85 MB) | smallest, but still `v0` |
| `github.com/IBM/sarama` | v1.60.2 | 15 | +6,385,767 B (+6.39 MB) | drags Kerberos and `go-spew` |
| `github.com/confluentinc/confluent-kafka-go/v2` | v2.15.0 | 1 | +10,692,622 B, **cgo build only** | **cannot build without cgo** |
| `github.com/rabbitmq/amqp091-go` | v1.14.0 | 1 | **+3,825,767 B (+3.83 MB)** | **chosen for RabbitMQ** |

**`confluent-kafka-go` is excluded by a compile, not by a preference.** With `CGO_ENABLED=0` it
does not build at all — measured, the errors are `undefined: kafka.NewConsumer` and
`undefined: kafka.ConfigMap`, because every symbol in the package is behind a cgo build tag. Its
row in the table is therefore the only one not measured under the shared build: 10.69 MB is what
it costs with `CGO_ENABLED=1`, which is the only way it compiles. Every other go-boot dependency is
pure Go, and `CGO_ENABLED=0` is what puts a service in a distroless or scratch image. A Starter
that silently takes that away is not a Starter.

**franz-go is the second-largest thing in the table, and that is the one number this section
spends rather than saves.** Only cgo-only confluent is bigger, and sarama is 966,656 bytes
*smaller*. go-boot's usual tie-break is size, so choosing the heavier client needs a reason, and it
is the other column: **4 linked modules against 15.** Module count is what
`.github/module-counts.txt` pins, what a `go mod tidy` propagates into every consumer's graph, and
what turns into upgrade work forever; 0.97 MB is paid once at link time.

**And those fifteen modules are the argument, not just the count.** `jcmturner/gokrb5/v8` and its
four supporting modules are a Kerberos implementation, `golang.org/x/crypto` and
`golang.org/x/net` come with it, and
`github.com/davecgh/go-spew` — a test-output formatter — is a non-test dependency of a consumer
that only wanted to read a topic.

**`segmentio/kafka-go` is the close call, and it loses on version, not on size.** It is 1 module
and 2.51 MB lighter than franz-go, which under go-boot's usual rule would win. It is still
`v0.4.51`, tagged 2026-04-23, and it has been on the `v0.4.x` line for years.
[12](#12-versioning-and-release-policy) says a `v0` may break anything on a minor bump; putting one
under a package covered by go-boot's own `v1.x` promise means inheriting that risk permanently.
franz-go is `v1.21.6`, tagged 2026-08-12.

> **One claim here is not measured, and it is the one to check before writing code.** The prose
> above rests on version lines and module counts, all of which were measured. It does **not** rest
> on a comparison of the two clients' consumer-group correctness — cooperative rebalancing,
> partition revocation during a commit, behaviour on a coordinator move — because none of that can
> be measured without a real broker, and none was run. If the human reviewing this section believes
> segmentio's consumer group is good enough, 2.51 MB and one module are a real argument for it, and
> nothing else in this section changes: the API below is written against neither client's types.
> The test that settles it is a three-broker cluster, one consumer group of two members, a rolling
> restart, and a count of duplicated and dropped messages on each client.

**RabbitMQ has no close call.** `amqp091-go` is the RabbitMQ team's own continuation of
`streadway/amqp`, it is `v1`, and it links **one module with zero transitive dependencies**. The
3.83 MB is almost entirely `crypto/tls`, which stops being dead code the moment a connection is
encrypted — the same effect [4.7](#47-gobootsecurity--the-security-starter) recorded for
`goboot/security`.

### The API

```go
package kafka

type Config struct {
	Brokers []string `yaml:"brokers"` // required, at least one
	Topic   string   `yaml:"topic"`   // required
	Group   string   `yaml:"group"`   // required, the consumer group id

	TLS  bool       `yaml:"tls"`  // false
	SASL SASLConfig `yaml:"sasl"` // off when Mechanism is empty
}

type SASLConfig struct {
	Mechanism string `yaml:"mechanism"` // "plain", "scram-sha-256", "scram-sha-512"
	Username  string `yaml:"username"`
	Password  string `yaml:"password"` // from an environment variable
}

// Message is what the handler is given. It is Kafka's shape, not a shared one.
type Message struct {
	Key, Value []byte
	Topic      string
	Partition  int32
	Offset     int64
	Headers    map[string][]byte
	Time       time.Time
}

// Handler processes one message. Returning an error means the offset is NOT
// committed, so the message is redelivered. There is no dead-letter policy in
// this Starter: a handler that wants one writes it.
//
// ctx is NOT Start's ctx, which goboot.Component documents as unsafe to keep.
// It descends from a context the Component makes for itself in Start and
// cancels in Stop, so a handler still running when the stop budget expires
// sees its ctx cancelled and must return.
type Handler func(ctx context.Context, m Message) error

func New(cfg Config, log *slog.Logger, h Handler) (*Component, error)

func (c *Component) Name() string                     // "kafka:" + cfg.Topic
func (c *Component) Tier() goboot.Tier                // TierTransport
func (c *Component) Start(ctx) (<-chan error, error)  // dial, join the group, start the loop
func (c *Component) Drain(ctx context.Context)        // stop fetching; returns at once
func (c *Component) Stop(ctx context.Context) error   // wait out in-flight handlers within
                                                      // ctx, then cancel them, leave the
                                                      // group and close the client
func (c *Component) Check(ctx context.Context) error  // the loop's stored fatal error, or nil
```

**There is no `workers` key, and the two `Config`s do not have the same concurrency knobs.** A
handler runs one message at a time per partition or per queue. For Kafka that is not a limitation
being papered over — partitions *are* Kafka's unit of parallelism, and processing one partition
sequentially is what preserves the per-partition ordering the broker guarantees. A service that
wants more throughput adds partitions and pods, which is the same answer Kafka itself gives. So
Kafka gets no knob at all, and RabbitMQ gets `prefetch`, because AMQP has no partitions and its QoS
window is the native way to say the same thing. They differ because the brokers differ, which is
this section's whole thesis; a shared `workers` key would have hidden that behind a number that
means two different things.

> `workers` is the first key to add if a real service measures one-at-a-time as too slow, and
> adding it is additive. Shipping it now would mean specifying how N concurrent handlers interact
> with the `Stop` wait and with offset commit ordering, for no user who has asked.

`goboot/rabbit` is the same six methods and the same `Handler` shape over its own `Message` and
`Config`:

```go
package rabbit

type Config struct {
	URL      string `yaml:"url"`      // required, amqp:// or amqps://; from an environment variable
	Queue    string `yaml:"queue"`    // required
	Prefetch int    `yaml:"prefetch"` // 1; the AMQP QoS window
	RequeueOnError bool `yaml:"requeueOnError"` // true; Nack(requeue) when the handler errors
}

type Message struct {
	Body        []byte
	Exchange    string
	RoutingKey  string
	Headers     map[string]any
	Redelivered bool
}

func New(cfg Config, log *slog.Logger, h Handler) (*Component, error)
// Name() is "rabbit:" + cfg.Queue. Everything else reads as above.
```

**A dropped broker connection is not a death, and not a failed `Check`.** Both clients reconnect,
so a coordinator moving or a broker restarting is ordinary and the fetch loop rides it out. The
`<-chan error` from `Start` carries only what the loop cannot recover from — credentials rejected,
the topic or queue gone, the group fenced — and a death is fatal, so that restarts the pod.
`Check` reports that same stored error and nothing else. In particular **it does not fail on an
idle topic and it never touches the network**: `CONTEXT.md` requires a Check to respect its
context and run inside the probe's deadline, and a Check that dialled the broker would turn a
broker blip into an unready pod, which is the failure mode
[2](#2-the-component-lifecycle-contract) keeps liveness away from for the same reason.

**Neither package declares a queue, a topic, an exchange or a binding.** Consuming from something
that does not exist is a startup error naming it, not a silent `QueueDeclare`. Topology belongs to
whatever owns the broker, and a Starter that creates it on the way past turns a typo in
`rabbit.queue` into a new empty queue nothing ever publishes to.

**Errors follow [4.0](#40-the-error-convention-every-starter-follows) with no new shape.** The
locator is the config key for a bad key — `kafka.brokers: required, at least one` — and the
Component name for a phase failure, which `Run` already prefixes: `start kafka:orders: ...`.

### Drain, which is the part this ticket exists to settle

[11](#11-deferred-past-v1) called a consumer "the named user of the optional `Drainer`", and
specifying one surfaces a hole in that interface that nothing has stood in yet.

**`Drain` has no timeout, and cannot be given one.** `App.Run` calls
`a.Stop(context.WithoutCancel(ctx))`, and `App.drain` passes that context straight to every
`Drainer`. So the context a consumer's `Drain` receives has no deadline and can never be cancelled.
`Drainer.Drain` also returns nothing, and the Drainers run one after another in start order, so a
`Drain` that blocks blocks the whole shutdown with nothing able to interrupt it.

**This has never mattered, because go-boot ships exactly one `Drainer`.** Grep the repository: the
only `Drain` outside tests and `prototypes/` is `actuator/actuator.go`, and its whole body is
`a.draining.Store(true)`. `goboot/web` and `goboot/grpc` do not implement `Drainer` at all — they
let go of connections in `Stop`, through `http.Server.Shutdown`, which has `stopTimeout` behind it.
So a consumer is not merely the first Drainer outside the Transports, as
[11](#11-deferred-past-v1) put it. It is the **first `Drain` in go-boot with anything to do**, and
the interface has no budget to do it in.

**So the consumer's `Drain` stops fetching and returns, and the waiting moves to `Stop`.** `Drain`
tells the fetch loop to take no more messages, which is exactly what the interface's name promises
and all it can safely do. `Stop` is where in-flight handlers are waited on, because `stopStarted`
wraps `Stop`'s context in `context.WithTimeout(ctx, a.life.StopTimeout)` — a real deadline, which
is the thing `Drain` does not have. When it expires, `Stop` cancels the handler context and closes
the client anyway, so shutdown cannot hang.

This needs no change to `goboot`: no `Drain` signature change, no error return, no new timeout key.
It reuses the budgets that exist, and it puts the waiting in the only phase that has one.

The three consequences, each of which must be in the package documentation:

- **`lifecycle.stopTimeout` becomes the handler's real budget**, and its 10s default was sized for
  cutting a gRPC stream, not for finishing a message. A service whose handlers run longer raises
  it. The `lifecycle.drainDelay` that runs in between is a head start rather than the budget: it is
  there so a load balancer sees the 503, and a consumer gets its 5s free. `drainDelay` plus
  `stopTimeout` must still fit the orchestrator's grace period — Kubernetes' default is 30s, which
  [2](#2-the-component-lifecycle-contract) already sizes the defaults against, and 5s plus 10s
  leaves room.
- **Delivery is at-least-once, and shutdown is where that gets exercised.** A handler still running
  when `stopTimeout` expires has its context cancelled with its offset uncommitted, so the message
  is redelivered to whoever picks up the partition. A handler must be idempotent. This is a
  property of every consumer ever written, but it is the sentence users skip, so it goes in the
  package doc comment rather than only here.
- **Drain order is start order, and for a consumer that is the right way round.** The Actuator is
  `TierObserve` and drains first, so `/readyz` is already answering 503 before the consumer stops
  fetching. `TierTransport` also means the consumer stops before the database pool, so a handler
  finishing during `drainDelay` still has its pool.

**The drain-order test the ticket asks for** records into one slice from each Component's `Drain`
and `Stop`, then asserts the sequence: actuator drains, consumer drains, consumer stops, actuator
stops. It is a `goboot`-level test using fakes. Each consumer package then needs three of its own,
against a fake fetch loop rather than a broker: `Drain` returns while a handler is deliberately
still running; `Stop` blocks until that handler returns; and `Stop` gives up, cancels the handler's
context and returns anyway once its own context expires. The third is the one that would catch a
hung shutdown, so it is the one not to skip. None needs a broker.

### What changes in this document, and in CI, when the code lands

Nothing below is edited now. The packages do not exist, and
[12](#12-versioning-and-release-policy) is a promise about what ships. Written out here so whoever
builds this has the list rather than rediscovering it:

- **[7](#7-dependencies-and-the-ticket-that-chose-each-one) gains two rows**, for
  `github.com/twmb/franz-go` and `github.com/rabbitmq/amqp091-go`, each citing #35 in the Ticket column and carrying the
  measured numbers from the table above. That section opens "go-boot links these and nothing else",
  so it is the one this design most directly invalidates, and re-measuring at the pinned versions is
  part of writing the rows — [7](#7-dependencies-and-the-ticket-that-chose-each-one) says to
  re-check the proxy before pinning.
- **[8.1](#81-the-import-leak-check) assertion 2 gains two heavy packages**, `goboot/kafka` and
  `goboot/rabbit`, and two lines using the pattern `goboot/preset/traced` already established —
  each checked against the heavy list minus itself:

  ```sh
  reaches "$M/kafka"  $(printf '%s\n' $heavy | grep -vxF "$M/kafka")
  reaches "$M/rabbit" $(printf '%s\n' $heavy | grep -vxF "$M/rabbit")
  ```

  That is what makes "a Kafka user links no RabbitMQ, and the reverse" a rule the toolchain checks.
  It needs no sixth assertion: assertion 2 already takes an arbitrary package and an arbitrary
  list, and the existing helper does the work.
- **[8.1](#81-the-import-leak-check)'s own written-out count goes stale, and must move in the same
  commit.** Its note reads that `goboot/preset/traced` "is checked against the other **six** heavy
  packages"; with `goboot/kafka` and `goboot/rabbit` on the list that becomes eight. That note
  already warns the number "moves whenever the list does" and has been wrong twice — this is the
  third time, and the first where it was seen coming.
- **`.github/module-counts.txt` gains two rows**, and a new package fails CI until it does.
  Predicted, from `goboot`'s own 2 plus each client's linked count: `goboot/kafka 6 6` with
  franz-go — 5 with segmentio — and `goboot/rabbit 3 3`. Predictions, not measurements: the real
  numbers are whatever `--update` writes, and [8.1](#81-the-import-leak-check) has a standing habit
  of these moving by one or two.
- **[12](#12-versioning-and-release-policy)'s package list goes from fifteen to seventeen**, in the
  same commit as the golden file, which is the rule that section states and #41 and #45 have each
  already held to once.
- **No Preset wires either one, and no Preset can.** A consumer needs a topic and a handler
  function, and ADR `0010` says a Preset takes no options, so there is nowhere for either to come
  from. `goboot/preset` is a short path, so assertion 2 turns that from a statement into a check.
- **Neither package is on the ten-minute path** of [13](#13-docs-and-examples). It gains one
  compiled example directory of its own, under the same rule as the rest.

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
