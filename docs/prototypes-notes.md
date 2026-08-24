# Prototype notes — the day-one `main.go`

Legwork for [#2](https://github.com/squall-chua/go-boot/issues/2). Date: 2026-08-24.
Toolchain: `go1.26.3 linux/amd64`. Code: `prototypes/` (separate module `goboot-prototype`,
throwaway, not the library).

**Nothing here is a decision.** It is what fell out of writing the call sites and running them.

---

## 1. Line counts

Three counts per file, because the naive one flatters the Preset:

- **total** — every line in the file.
- **code** — non-blank, non-comment.
- **wiring** — statements inside `main()` that exist *only* to bootstrap go-boot. Excludes
  imports, the 4-line `explicit` dispatch shim the prototype uses to make both forms runnable,
  route mounting, readiness registration and the Service Layer. This is the number the ticket
  is actually asking about.

| Variant | Form | total | code | **wiring** | wiring + config struct / `embed` boilerplate |
|---|---|---:|---:|---:|---:|
| `cmd/http-only` | Preset | 33 | 25 | **8** | 8 |
| `cmd/http-only` | explicit | 28 | 21 | **10** | 10 |
| `cmd/http-actuator-config` | Preset | 39 | 31 | **12** | 16 |
| `cmd/http-actuator-config` | explicit | 44 | 36 | **15** | 19 |
| `cmd/full` | Preset | 44 | 34 | **8** | 17 |
| `cmd/full` | explicit | 45 | 38 | **21** | 30 |

`cmd/full/service.go` (49 lines, the Service Layer plus the two Transport adapters) is shared by
both forms and counted in neither.

What the Preset actually removes:

| Variant | wiring saved | as a fraction |
|---|---:|---:|
| http-only | **2 lines** | 20% |
| http-actuator-config | **3 lines** | 20% |
| full | **13 lines** | 62% |

### The honest reading

**The Preset is close to worthless at the small end and clearly worth it at the large end.**
Two lines saved on `http-only` does not justify a package, an extra import path, a doc page and
a concept in `CONTEXT.md`. Nobody will thank us for `preset.HTTP`.

The boilerplate that *actually* disappears is not in `main` at all — it is the ~530 code lines of
the stub Starters (base 76, config 67, http 69, actuator 122, grpc 24, db 74, presets 99). That is
the real answer to "did we remove enough boilerplate?": **go-boot moves ~530 lines out of every
service, and `main` shrinks by 2–13.** The library earns its place; the Preset layer barely does
below the full surface.

If the spec keeps Presets, the argument for them has to be *"a Preset is the correct default
ordering and defaults, not a line count"* — Actuator started first and stopped last, the goose
session locker wired on, readiness pre-registered for the database. Those are the things a hand
written `main` gets wrong, and they are invisible in a line count.

---

## 2. Go version floor — what it actually turned out to be

I wrote `go 1.25.0` in `go.mod`. **`go mod tidy` rewrote it to `go 1.25.7`.**

Measured `go` directives of the direct dependencies (from each module's own `go.mod` in the
module cache):

| Module | version | `go` directive |
|---|---|---|
| `go.yaml.in/yaml/v3` | v3.0.5 | `go 1.16` |
| `google.golang.org/protobuf` | v1.36.12 | `go 1.23` |
| `connectrpc.com/connect` | v1.20.0 | `go 1.25.0` |
| `github.com/jackc/pgx/v5` | v5.10.0 | `go 1.25.0` |
| `github.com/prometheus/client_golang` | v1.24.1 | `go 1.25.0` |
| **`github.com/pressly/goose/v3`** | **v3.27.3** | **`go 1.25.7`** |

So, per Starter:

| Starter set | forced floor |
|---|---|
| base + config only | **1.16** |
| + actuator (Prometheus) | **1.25.0** |
| + gRPC (connect-go) | **1.25.0** |
| + db (goose) | **1.25.7** |

**For [#16](https://github.com/squall-chua/go-boot/issues/16): the real floor is not "Go 1.25",
it is Go 1.25.7.** goose declares a *patch-level* `go` directive, which the research files did not
catch (they read the minor version). One Go module means one `go` directive, so a user on Go
1.25.0–1.25.6 cannot build go-boot at all once the db Starter exists — even if they never import
it. Not deciding this, just recording that the number in the amendment should be `1.25.7`, and
that it will keep drifting upward at patch granularity because of one dependency.

Note also that the base + config Starter on its own is happy at **Go 1.16**. Every bit of the
floor comes from the optional Starters. That is an argument *against* single-module packaging
that [#3](https://github.com/squall-chua/go-boot/issues/3) settled on download-size grounds — the
download-size case is strong, the Go-floor case cuts the other way, and nobody has weighed them
against each other.

---

## 3. The thing that surprised me most: the Preset package leaks the Actuator

`cmd/http-only` calls `preset.HTTP(":8080")` — an HTTP-only service, no Actuator, no metrics.
Measured linked non-stdlib modules:

| build | linked non-stdlib modules | binary |
|---|---:|---:|
| `cmd/http-only` (Preset form) | **10** | 12,448,877 B |
| the same code, importing only `goboot` + `goboot/http` | **1** | 9,186,483 B |
| `cmd/full` | 21 | 20,669,076 B |

The extra 9 modules are the whole `prometheus/client_golang` tree. They arrive because
`preset.HTTP` and `preset.Web` live in the same package, and `preset.Web` imports the Actuator.
**+3.26 MB and +9 modules for a service that asked for a bare HTTP server.**

This is exactly the failure mode [#1](https://github.com/squall-chua/go-boot/issues/1)'s hard rule
from #3 exists to prevent — *"the base package and its tests must never import a Starter
subpackage"* — except the rule guards `goboot` and says nothing about `goboot/preset`. The leak
just moved one package to the right, and the planned CI check would not catch it.

Presets cannot all live in one package. In the prototype I ended up with `goboot/preset`
(http + actuator) and `goboot/preset/service` (+ grpc + db), which is already awkward to name and
still leaks at the boundary. Either every Preset gets its own package (five Starters → an
uncomfortable number of one-function packages), or the rule becomes "one Preset package per
dependency set" and someone has to define that set.

---

## 4. What felt wrong at the call site

Ordered by how much I think it matters.

### 4.1 Nobody watches a Component that dies after startup

`Component.Start` returns once the listener is up; `Run` then blocks on the signal context. In my
stub the HTTP server's `Serve` error goes into a buffered channel that is only read in `Stop`.
**If a Transport dies at 3am, the process sits there healthy-looking until someone sends it
SIGTERM.** I wrote that bug without noticing, which is the point — the two-method
`Start`/`Stop` contract invites it.

The spec has to say who watches a running Component and what happens when one fails: does `Run`
return, does the Actuator flip readiness, does the process exit? This is the single most
load-bearing unanswered question the prototype exposed, and it is a lifecycle question, not an
HTTP one.

### 4.2 `Start`/`Stop` is not enough phases — and even three is not enough

I had to add a third optional phase after ten minutes of writing the Actuator:

```go
type Drainer interface{ Drain(ctx context.Context) }
```

Without it there is no correct position for the Actuator in the Component list. Added first, it
is stopped last, so the Transports die while `/readyz` still says UP. Added last, it is stopped
first, so `/readyz` stops answering entirely instead of answering 503. `Drain` runs on every
Component in reverse order before any `Stop`, which fixes the ordering — **but there is still no
grace period between the drain and the stop, so a Kubernetes load balancer never gets a chance to
observe the 503 before the listener closes.** A real `Run` needs a configurable
`drain → sleep → stop` sequence. That is a config key and a default nobody has picked.

### 4.3 A Preset cannot be one line once the service has its own config keys

`preset.Web("app.yaml", "GB_")` loads config itself. That is the one-liner the ticket asks for,
and it locks the user out of adding a single key of their own. The moment `http-actuator-config`
wanted a `greeting:` key, the call site became:

```go
cfg := config{Greeting: "hello"}                        // defaults are struct pre-fill
if err := goboot.Load("app.yaml", "GB_", &cfg); err != nil { panic(err) }
app, err := preset.WebWith(cfg.Config)
```

So every Preset needs two entry points — `Web(path, prefix)` and `WebWith(cfg)` — and the second
is the one real services will use. **The "realistic default" variant does not use the one-line
form.** I suggest the spec stop promising one line and promise *one call*, with the config load
staying visible in `main` where the user can see their own struct.

The embedding trick that makes this work is worth writing down, because it is not obvious:

```go
type config struct {
	preset.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}
```

### 4.4 A Preset returning a struct is right; a Preset returning nothing is wrong

The Preset must hand back the wired parts (`app.HTTP`, `app.Actuator`, `app.DB`) because `main`
still needs to mount routes, register readiness checks and reach the logger. A Preset shaped as
`preset.Run(ctx, handler)` would be shorter and unusable. That is why the "one line" in #2's body
is not achievable without a callback parameter, and a callback is worse to read than three lines
of struct access.

### 4.5 `goboot/http` collides with `net/http` at every call site

Every `main.go` that touches an `http.Handler` — which is all of them — must alias one of the two:

```go
gbhttp "goboot-prototype/goboot/http"
```

There is no way around it while the Starter is named after the protocol. `goboot/web` and
`goboot/transport/http` both dodge it; both are worse names. Small, permanent, and it shows up in
the very first example a newcomer reads.

### 4.6 The Service Layer cannot satisfy connect-go's interface directly

`greeter.Greet(ctx, name string) (string, error)` and the generated
`Greet(ctx, *connect.Request[GreetRequest]) (*connect.Response[GreetResponse], error)` collide on
the method name, so a separate adapter type per Transport is mandatory, not stylistic:

```go
type grpcGreeter struct{ svc *greeter }
```

This is correct — it is what "the Service Layer knows nothing about HTTP or gRPC" *means* — but
users will try embedding first and get a confusing ambiguity error. The docs need this example
early. `app.GRPC.Mount(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}))` reads well
though: connect's two-value return feeds a two-parameter `Mount` unchanged, which is the nicest
call site in the whole prototype.

### 4.7 `app.DB.DB`

`db.DB` embeds `*sql.DB`, so reaching the pool from `main` is `app.DB.DB`. Ugly enough that
someone will fix it wrong. Either the Starter type is not called `DB`, or it exposes `Pool()`.

### 4.8 Env-var binding forces unreadable YAML keys

The loader lowercases each `__`-separated env segment and matches it against the `yaml` tag, so
every tag must be lowercase: `readheadertimeout`, `maxopenconns`, `migrateonstart`. In a config
file a human reads, `readHeaderTimeout` is what you want. Fixing it means a case-insensitive or
kebab-aware key match in the loader — more than the 78 lines in
[#4](https://github.com/squall-chua/go-boot/issues/4)'s research budgeted for. The research
recorded the `__` separator convention but not the key-case convention; they interact.

### 4.9 Two ports, and nobody has decided that

I put the Actuator on its own listener (`:9090`) so `/metrics` is not publicly reachable. The
observability research explicitly notes the opposite is possible — Actuator and Transports on one
listener, since connect-go and `net/http` share a port fine. The choice has consequences the spec
must state: two config keys, two listeners, probes pointing at a different port than traffic,
and a `NetworkPolicy` story. It is a real decision presented here as a default only because a
prototype had to pick one.

---

## 5. Questions the prototype exposed that nobody has asked

1. **Who watches a Component that fails mid-life?** See 4.1. Not asked anywhere in #1.
2. **Where does `goboot migrate` get its goose Provider?** The db Starter builds the Provider
   inside `Start`. The separate migrate command needs the same Provider *without* starting the
   app. Either the Starter exposes it, or it is constructed twice with two chances to forget
   `WithSessionLocker`, or the Scaffold writes a second `main`. #6 decided the library; it did not
   decide where the command lives.
3. **`HasPending` failing startup is a deploy-ordering contract.** My stub refuses to start when
   migrations are pending and `migrateonstart` is off — the safe default #6 hinted at. That means
   a deployment running migrations as a separate Job *must* order the Job before the rollout, or
   every pod crashloops until it finishes. That is a documented operational contract go-boot is
   imposing on its users, and it has never been written down as one.
4. **Does `Add` order or a dependency graph decide start order?** I used add order, and it works,
   but it means the Preset's `Add(actuator, db, http, grpc)` line is load-bearing and silent. A
   user who calls `app.Add` themselves can get it wrong with no error.
5. **What is a Component's start timeout?** There is a shutdown timeout in my stub and no startup
   timeout. A `db.Start` that hangs on `PingContext` hangs the process before the Actuator can
   report anything — except the Actuator started first, so `/livez` answers. That accident is
   doing real work; the spec should make it deliberate.
6. **Does the Actuator own the Prometheus registry, and how does a user register their own
   metrics?** I exposed `Actuator.Registry`. That makes `prometheus.Registry` part of go-boot's
   public API surface, which the observability research's "two pipelines" decision implies but
   never states.
7. **Is there a `goboot.Fatal`/`Main` helper?** Every `main` ends in the same four lines
   (`if err := app.Run(ctx); err != nil { log; exit }`). Four of the 8 "wiring" lines in the
   Preset form are that idiom. Collapsing it to `goboot.Main(app)` would halve the small-variant
   numbers — and would hide the error handling, which is exactly the kind of magic #1 rules out.
   Worth an explicit yes or no rather than drifting into it.

---

## 6. What I did not build, and why it matters to the numbers

- **No OTel tracing.** The three variants in #2 do not name it, so it is out. Adding it costs
  roughly one line at the call site (`srv.Use(otelhttp.NewHandler)`) plus a ~15-line setup
  function per #7, and would not move the floor (otel v1.45 is `go 1.25.0`, already below goose's
  `1.25.7`). The line counts above understate the full v1 surface by about 3 lines of `main`.
- **No middleware, TLS, profile overlay, or config flag layer.** All would add wiring lines to
  both forms roughly equally, so the Preset-versus-explicit *delta* should hold.
- **`cmd/full` was never run** — it needs Postgres. It compiles and vets clean, and that is all
  that is claimed for it.
