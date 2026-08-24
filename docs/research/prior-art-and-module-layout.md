# Prior art, and module layout

Research for [#3](https://github.com/squall-chua/go-boot/issues/3). Investigated 2026-08-24.

Every factual claim below carries a source URL. Sources are the projects' own repositories,
their own documentation, the Go reference, and the GitHub API. No blog posts, no secondary
write-ups. Part B is measured on this machine with `go1.26.3 linux/amd64`; the commands and
the numbers they printed are given so they can be re-run.

Anything that could not be verified is marked **UNVERIFIED**.

---

## Part B first — module layout

Part B is put first because it is a settled empirical result, and Part A leans on it.

### B.1 The claim under test

Standing constraint from [#1](https://github.com/squall-chua/go-boot/issues/1): *"one Go module,
capability Starters as subpackages."* That constraint is only safe if this is true:

> A consumer importing only `goboot` does not download or build the dependencies that only
> `goboot/grpc` uses.

### B.2 What the Go reference says it should do

- *"If the main module is at `go 1.17` or higher, the module graph used for minimal version
  selection includes only the **immediate** requirements for each module dependency that
  specifies `go 1.17` or higher in its own `go.mod` file [...] The **transitive** dependencies
  of `go 1.17` dependencies are **pruned out** of the module graph."*
  — [Go Modules Reference, Module graph pruning](https://go.dev/ref/mod#graph-pruning)
- *"since the expanded `go.mod` file needed for module graph pruning includes all of the
  dependencies needed to load the imports of any package in the main module, if the main module
  specifies `go 1.17` or higher the `go` tool no longer reads (or even downloads) `go.mod` files
  for dependencies if they are not needed in order to complete the requested command."*
  — [Go 1.17 release notes, Modules](https://go.dev/doc/go1.17)
- *"At `go 1.17` and above, the `go` command adds an indirect requirement for each module that
  provides any package imported (even indirectly) by a package or test in the main module."*
  — [Go Modules Reference, go.mod file](https://go.dev/ref/mod#go-mod-file-go)

Note the wording carefully. Pruning is about the **module graph**. It says nothing about
package imports. If the root package of a module imports a heavy dependency, pruning will not
save anyone. That distinction turns out to be the whole game — see B.7.

### B.3 Experiment 1 — synthetic, controlled

A `file://` module proxy was built with four synthetic modules, so that "was it downloaded"
could be answered by looking at an isolated, initially empty module cache
(`GOMODCACHE` set per consumer).

| Module | `go` directive | Contents |
| --- | --- | --- |
| `example.com/deep v1.0.0` | 1.22 | one package, no deps |
| `example.com/heavy v1.0.0` | 1.22 | one package, imports `example.com/deep` |
| `example.com/lib v1.0.0` | 1.22 | root pkg `lib` (stdlib only); subpkg `lib/sub` imports `heavy` |
| `example.com/oldlib v1.0.0` | **1.16** | identical shape to `lib`, unpruned control |

Three consumers, each `go 1.22`, each with its own empty module cache, each run through
`go mod tidy` then `go build ./...`.

**Consumer A — imports `example.com/lib` (root package only):**

```
go.mod:   require example.com/lib v1.0.0          <- that is the whole file
go.sum:   example.com/lib v1.0.0 h1:...
          example.com/lib v1.0.0/go.mod h1:...    <- 2 lines. no heavy. no deep.
go list -m all:            example.com/heavy v1.0.0   example.com/lib v1.0.0
go mod download (no args): example.com/lib v1.0.0 zip=yes   <- only entry
module cache after build:  1 zip (lib). deep never fetched at all.
```

**Consumer B — imports `example.com/lib/sub`:**

```
go.mod:   require example.com/lib v1.0.0
          require ( example.com/deep v1.0.0 // indirect
                    example.com/heavy v1.0.0 // indirect )
go.sum:   6 lines (zip + go.mod hash for lib, heavy, deep)
go mod download (no args): lib, deep, heavy — 3 zips
```

**Consumer C — imports `example.com/oldlib` root only (the `go 1.16`, unpruned control):**

```
go.sum:   example.com/deep  v1.0.0/go.mod h1:...   <- present, go.mod hash only
          example.com/heavy v1.0.0/go.mod h1:...   <- present, go.mod hash only
          example.com/oldlib v1.0.0 h1:... + /go.mod
go list -m all: includes example.com/deep   <- deep is in the graph. it is not, for lib.
```

Consumer A vs Consumer C is the pruning effect isolated: same package shape, same imports,
the only difference is the `go` directive in the library's own `go.mod`. `deep` is in
Consumer C's module graph and `go.sum` and absent from Consumer A's.

### B.4 Experiment 2 — the CI case: cold cache, `-mod=readonly`

The realistic question is not what `go mod tidy` does on a developer's warm machine, it is what
a CI job does from a checked-in `go.mod` + `go.sum` and an empty cache.

```
Consumer A (root only), empty GOMODCACHE, GOFLAGS=-mod=readonly, go build ./...
  go: downloading example.com/lib v1.0.0
  build OK
  zips fetched:      example.com/lib
  .mod files fetched: example.com/lib      <- heavy's go.mod not even read
  module cache:      1034 bytes

Consumer B (imports sub), same conditions
  zips fetched:      lib, heavy, deep
  module cache:      2434 bytes
```

**This is the direct answer to the ticket.** With `-mod=readonly` and a cold cache, a root-only
consumer fetches neither the zip nor the `go.mod` of the sibling subpackage's dependency.

### B.5 Experiment 3 — go-boot's exact shape, with a real heavy dependency

Same method, but the module is shaped exactly as go-boot plans, and the heavy dependency is the
real `google.golang.org/grpc v1.71.0` from `proxy.golang.org`:

```
example.com/goboot v1.0.0   (go 1.22, require google.golang.org/grpc v1.71.0)
  goboot.go        package goboot  -> imports net/http only
  grpc/grpc.go     package grpc    -> imports google.golang.org/grpc
```

| | imports `example.com/goboot` | imports `example.com/goboot/grpc` |
| --- | --- | --- |
| `require` lines in consumer `go.mod` | **1** | 1 direct + **6 indirect** |
| `go.sum` lines | **3** | **36** |
| module zips downloaded | **1** | **18** |
| module cache bytes | **2,887** | **111,276,555** (~106 MiB) |

The root-only consumer's entire `go.sum`:

```
example.com/goboot v1.0.0 h1:...
example.com/goboot v1.0.0/go.mod h1:...
google.golang.org/grpc v1.71.0/go.mod h1:...     <- go.mod hash ONLY, no zip hash
```

**106 MiB versus 2.9 KiB. The claim holds.** One Go module with `goboot/grpc` as a subpackage
does not cost an HTTP-only user anything but three lines of `go.sum`.

### B.6 Three caveats that are real, and that the ticket's phrasing does not cover

**Caveat 1 — the module still appears in `go list -m all` and in `go.sum`.**

```
$ go list -m all           # consumer that imports ONLY example.com/goboot
example.com/c/gb_root
example.com/goboot v1.0.0
google.golang.org/grpc v1.71.0        <- listed
```

`grpc` is an *immediate* requirement of `goboot`, so it survives pruning into the module graph
([graph pruning](https://go.dev/ref/mod#graph-pruning) prunes transitive requirements, not
immediate ones). Nothing is downloaded and nothing is built, but tools that read `go list -m all`
or `go.sum` — SBOM generators, some SCA and dependency-update bots — will report `grpc` as a
dependency of a service that has never linked it. `govulncheck` works from the import graph, so
it is not affected; that distinction is **UNVERIFIED** here, not tested.

**Caveat 2 — go-boot's own direct requirements still act as version floors.**

Tested: `lib`'s `go.mod` requires `heavy v1.0.0`. A consumer that imports only `lib`'s root
package **and separately requires `heavy v0.9.0` for its own reasons** resolves to:

```
resolved heavy version: example.com/heavy v1.0.0     <- bumped by MVS
```

So if go-boot's `go.mod` names `google.golang.org/grpc v1.71.0`, a user who imports only
`goboot` but uses gRPC directly in their own code cannot stay below v1.71.0. This is a genuine
cost of the single module and it does not go away.

The *transitive* case does not leak, however. The same consumer requiring `deep v0.9.0` — a
dependency of `heavy`, which is only reachable through the unimported sibling — keeps `v0.9.0`;
the consumer that imports the sibling gets bumped to `v1.0.0`. Pruning is doing exactly what the
reference describes.

**Caveat 3 — `go mod tidy` fetches far more than `go build` does, and *tests* are the leak.**

Measured on `github.com/go-kratos/kratos/v2 v2.9.2`:

```
consumer importing kratos root, go mod tidy: 24 module zips, 176 MB module cache
consumer importing kratos root, cold-cache go build from the resulting go.mod/go.sum:
    go: downloading github.com/go-kratos/kratos/v2 v2.9.2
    go: downloading github.com/google/uuid v1.4.0
    go: downloading golang.org/x/sync v0.10.0
    go: downloading github.com/go-playground/form/v4 v4.2.0
    go: downloading google.golang.org/protobuf v1.33.0
    go: downloading gopkg.in/yaml.v3 v3.0.1
    6 zips, 19,788,008 bytes
```

`google.golang.org/grpc v1.61.1` ends up in that consumer's `go.sum` **with a full zip hash**,
and is downloaded by `tidy`, but is never downloaded or compiled by `go build`. Cause, traced:

```
$ go list -f '{{.ImportPath}} TEST-> {{join .TestImports " "}}' github.com/go-kratos/kratos/v2
github.com/go-kratos/kratos/v2 TEST-> ... github.com/go-kratos/kratos/v2/transport/grpc
```

The root package's **own test file** imports its `transport/grpc` subpackage, and
`go mod tidy` resolves the tests of packages imported by the main module
([`go mod tidy`](https://go.dev/ref/mod#go-mod-tidy)). Consumer A in the synthetic experiment
did not show this only because `example.com/lib` has no tests.

**Actionable rule for go-boot: the root package's test files must not import any Starter
subpackage.** If `goboot`'s tests import `goboot/grpc`, every HTTP-only user pays a ~100 MiB
`go mod tidy`. Keep cross-Starter integration tests in a separate subpackage such as
`goboot/internal/integrationtest`, or in a separate `_test` module.

### B.7 The failure mode that pruning does **not** save you from

`github.com/go-kratos/kratos/v2`'s root package compiles **178 packages** and pulls
`google.golang.org/protobuf` — for a program whose entire body is `import kratos`:

```
$ go list -deps . | wc -l
178
$ go list -deps . | grep -c google.golang.org/protobuf
34
```

That is not a module layout failure. Kratos's root package imports `encoding`, which imports
`encoding/form` and `encoding/proto`, which import protobuf
([kratos/encoding](https://github.com/go-kratos/kratos/tree/main/encoding)). No module split
would fix it, and no single-module layout causes it.

**The lesson for go-boot is about package imports, not modules:** the base Starter's import
graph must stay stdlib-only. If `goboot` itself imports a codec registry that imports protobuf,
the module layout question is moot.

### B.8 How the surveyed projects split their modules, and what it cost them

| Project | Go modules in repo | Cost observed |
| --- | --- | --- |
| **go-zero** | **2** — `go.mod`, `tools/goctl/go.mod` ([tree](https://api.github.com/repos/zeromicro/go-zero/git/trees/master?recursive=1)) | Almost none. No `replace` directives. Both modules properly tagged (`v1.10.3`, `tools/goctl/v1.10.2`, cut 17 minutes apart on 2026-08-01). The price is paid elsewhere: everything the CLI does not need lives in the one runtime module, which is why it has **44 direct requires** including `k8s.io/client-go`. |
| **go-kratos** | **28** ([tree](https://api.github.com/repos/go-kratos/kratos/git/trees/main?recursive=1)) — core, 3 CLI modules, 24 under `contrib/` | Real and visible. **Zero `contrib/*` git tags exist**; the proxy serves them only as pseudo-versions (`contrib/registry/etcd/v3@latest` → `v3.0.0-20260626125723-668db92c2c00`). Every contrib module ships a `replace github.com/go-kratos/kratos/v3 => ../../`. Kratos's own scaffold pins `contrib/otel/v3` to a pseudo-version *older* than the v3.0.0 core beside it ([kratos-layout go.mod](https://github.com/go-kratos/kratos-layout/blob/main/go.mod)). A version bump is a 28-module fan-out — the single most recent commit on `main` is literally "deps: upgrade kratos version to v3.0.0". |
| **uber/fx** | **4**, but only **1 published** — `go.mod`; plus `docs/`, `internal/e2e/`, `tools/` ([tree](https://api.github.com/repos/uber-go/fx/git/trees/master?recursive=1)) | Low, and this is the pattern worth copying. The three satellites exist only to keep `golang.org/x/tools`, mkdocs example code and e2e tests out of `go.uber.org/fx`'s dependency graph, and all are `replace`-pinned to `../`. Cost: every [Makefile](https://raw.githubusercontent.com/uber-go/fx/master/Makefile) target loops over `MODULES = . ./tools ./docs ./internal/e2e`, and the satellites carry stale pinned fx versions (`docs/` requires v1.18.2, `internal/e2e/` v1.19.2) masked by the `replace`. |
| **Encore** | **5**, of which 2 are real: `go.mod` → `encr.dev` (CLI/daemon/parser/compiler) and `runtimes/go/go.mod` → `encore.dev` (the SDK users import); plus 3 test fixtures. Also a 6-crate Rust workspace | High, and instructive. **`encore.dev` is not published from this repo** — the proxy resolves `encore.dev@latest` to `v1.57.13` from a *mirror repo*, `github.com/encoredev/encore.dev`. So the two version lines drift: the monorepo is at v1.58.2 (2026-08-19) while the published SDK is at v1.57.13 (2026-07-29), and the CLI's own `go.mod` pins a placeholder `encore.dev v1.1.0`. |
| **goa** | **2** — `go.mod` → `goa.design/goa/v3`; `jsonrpc/integration_tests/go.mod`, `replace`-pinned and never published ([tree](https://api.github.com/repos/goadesign/goa/git/trees/v3?recursive=1)) | Essentially zero *in-repo*. The real cost is cross-repo: `goa.design/plugins/v3` (4 go.mods) and `goa.design/clue` (4 go.mods) are separate repositories on separate release trains — and clue is where logging and health live. |
| **GoFr** | **32** — core `go.mod` plus one per datasource driver (`pkg/gofr/datasource/{arangodb,cassandra,clickhouse,couchbase,dbresolver,dgraph,…}`) and per example | Splitting the drivers out did **not** slim the core: it still carries **50 direct requires**. Both problems at once — kratos's release fan-out and go-zero's weight. |

The Kratos/go-zero contrast is the whole argument in miniature. Kratos split 28 ways to keep
its core at 11 direct requires, and pays for it in release mechanics that visibly do not work —
untagged contrib modules, stale pseudo-version pins in its own scaffold. go-zero stayed at 2
modules and pays for it with a 44-direct-require runtime module.

Module graph pruning offers a third option neither took: **one module, disjoint package import
graphs.** Experiment 3 shows it delivers Kratos's isolation with go-zero's release simplicity.

fx and goa both show the one split that is genuinely cheap: an **unpublished, `replace`-pinned
satellite module for tests and tooling**, so heavy test-only dependencies never enter the
consumer-facing module graph. goa's `jsonrpc/integration_tests` and fx's `internal/e2e`, `docs`
and `tools` cost nothing because nobody ever `go get`s them. This is the escape hatch for
caveat 3 in B.6 if go-boot's cross-Starter tests ever get expensive.

### B.9 Recommendation — single module

**Ship go-boot as one Go module, `github.com/squall-chua/go-boot`, with Starters as
subpackages.** The standing constraint in #1 is correct and is now measured, not assumed.

Conditions that make it hold, all of which are in go-boot's control:

1. **`goboot` (base) imports stdlib only** — no codec registry, no metrics client, nothing that
   drags a dependency into every user's build. B.7 is the cautionary tale.
2. **No Starter subpackage is imported by the base package**, in source or in tests. B.6
   caveat 3 is the cautionary tale.
3. **`go.mod` declares `go 1.22`** (already the constraint in #1), which is above the 1.17
   pruning threshold.
4. **A CI job that builds a root-only consumer from a cold module cache and asserts the
   download list**, so regression 1 or 2 is caught the day it lands rather than in a bug report.
   The experiment in B.4 is that test; it takes about a second to run.

Split into a second module only if and when a Starter needs a dependency with a *conflicting
version floor* that hurts real users (caveat 2) — that is the one problem a single module cannot
solve. Do not split pre-emptively; Kratos shows the bill.

The Scaffold CLI is the one plausible second module later, on go-zero's `tools/goctl` pattern —
it is the case where the dependency direction is clean (CLI depends on library, never the
reverse) and where users `go install` it rather than importing it. Even that can wait: a CLI in
the same module costs importers nothing, by exactly the mechanism measured above.

---

## Part A — prior art, and whether go-boot should exist

### A.0 Coverage of the v1 Starters, at a glance

Rows are go-boot's v1 Starters from [#1](https://github.com/squall-chua/go-boot/issues/1).
Per-project evidence is in A.1–A.7.

| | kratos v3 | go-zero | uber/fx | Encore | goa v3 | GoFr |
| --- | --- | --- | --- | --- | --- | --- |
| Config | yes | yes | **no** | yes | **no** | yes |
| Logging | yes | yes | framework events only | yes | outsourced to `clue` | yes |
| Component lifecycle (ordered) | **unordered** | **unordered** | **yes** | yes | **no** | partial |
| Actuator | **no** | partial | **no** | partial | **no** (in `clue`) | partial |
| HTTP transport | yes | yes | **no** | yes | yes | yes |
| gRPC transport | yes | yes | **no** | **no** | yes | yes |
| Database | **no** | partial, **no migrations** | **no** | yes, Postgres only | **no** | yes |
| Direct `require`s in main module | **11** | **44** | 6 | n/a (2 modules) | 14 | **50** |
| Reflection in the core path | modest | **heavy** | **total** | compile-time codegen | codegen | **UNVERIFIED** |
| Last release | v3.0.0, 2026-06-26 | v1.10.3, 2026-08-01 | v1.24.0, **2025-05-13** | v1.58.2, 2026-08-19 | v3.30.0, 2026-08-19 | v1.59.0, 2026-08-13 |
| Commits to default branch, last 90d | **6** | 41 | **0** | 51 | 48 | 48 |

**No surveyed project covers all seven.** Three capabilities are missing from *every* one of them,
and two of those three are named in #1 as core:

1. **Ordered Component lifecycle.** Only fx has it, and only through reflection DI, which #1
   rules out of scope.
2. **Runtime log-level endpoint.** Nobody. Not one project.
3. **Build-info endpoint.** Only Encore, and only bundled into its health response.

Database migrations are absent from kratos, go-zero, fx and goa.

### A.1 go-kratos

Now at **v3** — `main` is module `github.com/go-kratos/kratos/v3`, v3.0.0 released 2026-06-26
([go.mod](https://raw.githubusercontent.com/go-kratos/kratos/main/go.mod),
[migration guide](https://github.com/go-kratos/kratos/blob/main/docs/migration/v2-to-v3.md)).
The empirical work in Part B used v2.9.2, the latest v2.

**Removes:** `kratos.New(...)` / `App.Run()` starts every registered `transport.Server` in an
`errgroup`, installs `signal.Notify` for SIGTERM/SIGQUIT/SIGINT, runs
`beforeStart`/`afterStart`/`beforeStop`/`afterStop` hooks and calls `server.Stop(ctx)` under
`kratos.StopTimeout` ([app.go](https://raw.githubusercontent.com/go-kratos/kratos/main/app.go)).
Service registration via `kratos.Registrar` + the `registry` package. Both transports
(`transport/http` over gorilla/mux, `transport/grpc`); the gRPC server auto-registers
`grpc_health_v1` and reflection unless disabled
([transport/grpc/server.go](https://github.com/go-kratos/kratos/blob/main/transport/grpc/server.go)).

**Covers:** config (`Config` with `Scan`/`Value`/`Watch`, file + env sources), logging (v3 is a
thin `log/slog` wrapper — `Level = slog.Level`), HTTP, gRPC. **Does not cover:** actuator (a
repo-wide grep for `healthz` returns **0 hits**; `transport/http/pprof.NewHandler()` exists but
you mount it yourself), database (no `sql`/`orm`/`store` package anywhere in the tree — the
scaffold [kratos-layout](https://github.com/go-kratos/kratos-layout/blob/main/go.mod) gets
migrations from `entgo.io/ent`, which is ent's, not kratos's). Lifecycle is **partial**: hooks
and stop timeout yes, but servers start concurrently in an errgroup with no declared ordering.

**Why reject:** the **error convention is lock-in** — `errors.Error` embeds a protobuf
`Status{Code, Reason, Message, Metadata}` and implements `GRPCStatus()`, and
`protoc-gen-go-errors` generates your error constructors from proto enums
([errors.go](https://raw.githubusercontent.com/go-kratos/kratos/main/errors/errors.go)).
Adopting kratos means adopting its error shape across both transports. The README lists
`protoc` and `protoc-gen-go` as install requirements, and `kratos new` clones a layout that
fixes `api/ cmd/ internal/{biz,conf,data,server,service}` and wires ent + wire + buf. Weight is
*not* a valid objection any more: 11 direct requires in v3, down from 18 in v2.

**Maintenance — the concern.** Latest release v3.0.0 (2026-06-26); most recent commit to `main`
also **2026-06-26**; **6 commits to `main` in the last 90 days**; 17 open issues against **89
open PRs**. The repo's `pushed_at` is recent but that is dependabot and feature branches. Two
months of silence on `main` immediately after a major release, with 89 PRs queued, is a
review-throughput signal.

### A.2 go-zero

**Removes:** the most, per line of user code. `service.ServiceConf.MustSetUp()` does, in order,
`logx.SetUp`, mode switch, `prometheus.StartAgent`, `trace.StartAgent`, `proc.Setup(shutdown)`,
`devserver.StartAgent`, `profiling.Start`
([serviceconf.go](https://raw.githubusercontent.com/zeromicro/go-zero/master/core/service/serviceconf.go)).
Embed `rest.RestConf` and you get all of it. `rest.MiddlewaresConf` defaults *every* one of
Trace/Log/Prometheus/MaxConns/Breaker/Shedding/Timeout/Recover/Metrics/MaxBytes/Gunzip to `true`
([rest/config.go](https://raw.githubusercontent.com/zeromicro/go-zero/master/rest/config.go)).

**This is the closest existing thing to an Actuator in Go.** `internal/devserver` starts a
second HTTP listener on **port 6060** exposing `/healthz` (backed by a real readiness `Probe`
registry with `MarkReady`/`MarkNotReady` and 503 + per-probe verbose output), `/metrics`
(promhttp), `/debug/pprof/*` and a route listing — all defaulted on
([server.go](https://raw.githubusercontent.com/zeromicro/go-zero/master/internal/devserver/server.go),
[health.go](https://github.com/zeromicro/go-zero/blob/master/internal/health/health.go)).
Plus SIGUSR1 → goroutine dump, SIGUSR2 → 60s CPU profile. **Missing: no runtime log-level
endpoint** (`logx.SetLevel` is a Go API only) and **no build-info endpoint**.

**Why reject — the weight is the story.** **44 direct requires and 90 indirect**, including
`k8s.io/{api,apimachinery,client-go,utils}`, `go.etcd.io/etcd`, `go.mongodb.org/mongo-driver/v2`,
`jackc/pgx/v5`, `go-sql-driver/mysql`, `redis/go-redis/v9`, 8 OpenTelemetry modules,
`grafana/pyroscope-go`, `fullstorydev/grpcurl` — **and test libraries as direct requires** of the
runtime module (`testify`, `go-sqlmock`, `miniredis`, `goleak`, `go.uber.org/mock`, `gock`).
`go get go-zero` puts a Kubernetes client in your module graph. Magic is heavy and central:
`core/conf` walks struct types with reflection and drives behaviour off a bespoke tag
mini-language (`json:",default=pro,options=dev|test|rt|pre|pro"`, `,optional`, `,range=[0:1000)`)
— runtime errors, not compile-time. Structure lock-in is hard: `goctl api go` emits a fixed
`config/ etc/ internal/{handler,logic,svc,types}` tree, and the health-probe registry lives in
**`internal/`**, so you cannot register a probe from your own code without going through
go-zero's entry points. Graceful shutdown is a **no-op on Windows**
(`//go:build linux || darwin || freebsd` in
[proc/shutdown.go](https://raw.githubusercontent.com/zeromicro/go-zero/master/core/proc/shutdown.go)).

Database: pool and transactions yes (`Transact(func(Session) error)`, nesting rejected),
**migrations no** — `goctl migrate` migrates go-zero *versions*, not schemas.

**Maintenance:** healthy. v1.10.3 (2026-08-01), last commit 2026-08-15, 41 commits/90d — though
the five most recent are all dependabot merges. 107 open issues, 151 open PRs.

### A.3 uber/fx

**Removes:** wiring, and nothing else. fx is a DI container plus a lifecycle runner over
`go.uber.org/dig`, whose own README says it is *"good for: powering an application framework,
e.g. Fx"* and *"bad for: using in place of an application framework"*
([dig README](https://raw.githubusercontent.com/uber-go/dig/master/README.md)).

**Covers exactly one Starter: lifecycle** — and covers it better than anyone else. `fx.Lifecycle`
`OnStart`/`OnStop`, **stop runs in reverse order**, `fx.StartTimeout`/`fx.StopTimeout`
(`fx.DefaultTimeout` = 15s), signal handling, `fx.Shutdowner`
([lifecycle.md](https://raw.githubusercontent.com/uber-go/fx/master/docs/src/lifecycle.md)).
Config, actuator, HTTP, gRPC, database: **none**. The official tutorial has you hand-write
`NewHTTPServer` returning `*http.Server` and register the hooks yourself
([http-server.md](https://raw.githubusercontent.com/uber-go/fx/master/docs/src/get-started/http-server.md)).

**Why reject:** #1 already rules it out, and fx's own documentation supplies the evidence.
Reflection is structural, and the docs admit where it leaks: *"`fx.Supply` … Fx has to use
runtime reflection to inspect the type of the value, and at that point the Go runtime only tells
it that it's a `*redis.Client`"* — so interfaces need an `fx.Annotate(..., fx.As(...))`
workaround ([faq.md](https://raw.githubusercontent.com/uber-go/fx/master/docs/src/faq.md)).
Failure is at runtime: the remedy the docs offer is a *separate* `fx.ValidateApp(opts)` call, and
the getting-started tutorial literally walks you into a silently non-running app — *"Huh? Did
something go wrong? … The server didn't run."* Value groups are explicitly unordered, and
`fx.Module` hierarchy inverts invoke order.

**Maintenance — effectively stalled.** Latest release **v1.24.0, 2025-05-13** (~15 months ago);
last commit to `master` 2025-12-27; **0 commits in the last 90 days**; 57 open issues. dig is
the same: v1.19.0, 2025-05-13, 0 commits in 90 days.

### A.4 Encore

**Removes: more than everything on the list — by not being a Go library.** *"Encore
automatically generates a `main` function… This means you don't write a `main` function"*
([services.md](https://raw.githubusercontent.com/encoredev/encore/main/docs/go/primitives/services.md)).
APIs are a `//encore:api public method=GET path=/blog/:id` comment on a plain function;
routing, marshalling and validation are derived by a custom parser. Resources are declarations —
`sqldb.NewDatabase`, `pubsub.NewTopic`, `objects.NewBucket`, `cron`, `secrets` — mapping to
local Postgres/NSQ/FS and to RDS/SNS+SQS/S3 or Cloud SQL/Pub-Sub/GCS in the cloud
([README](https://raw.githubusercontent.com/encoredev/encore/main/README.md)).

**Actuator:** `/__encore/healthz` runs a `health.CheckRegistry` and returns 200/500 **plus build
info** (`app_slug`, `env_name`, `app_revision`, `encore_compiler`, `deploy_id`, per-check
results) — the only surveyed project that ships build info on an endpoint
([encore_routes.go](https://raw.githubusercontent.com/encoredev/encore/main/runtimes/go/appruntime/apisdk/api/encore_routes.go)).
No separate readiness route, **no `/metrics` scrape endpoint** (metrics are remote-write push),
no runtime log-level endpoint (level fixed at startup from `ENCORE_LOG`). Graceful shutdown is a
documented 3-phase drain
([shutdown.go](https://raw.githubusercontent.com/encoredev/encore/main/runtimes/go/shutdown/shutdown.go)).
**gRPC: not covered** — the documented path is hand-rolling a Connect service with `buf` and
mounting it on a raw endpoint
([grpc-connect.md](https://raw.githubusercontent.com/encoredev/encore/main/docs/go/how-to/grpc-connect.md)).

**Why reject — this is the decisive one.** *"since Encore requires an extra compilation step, you
must run your tests using `encore test` instead of `go test`"*
([testing.md](https://raw.githubusercontent.com/encoredev/encore/main/docs/go/develop/testing.md)).
The `encore.dev` package you import is only an API contract; the compiler supplies the
implementation. **There is no plain `go build` path.** That is disqualifying against #1's "plain
Go wiring" and "stdlib first" constraints regardless of anything else. Licence is **MPL-2.0**
for the framework/parser/compiler; Encore Cloud is not open source. Pricing:
**$49/member/month + $99 per environment + $2.50 per resource per environment**
([encore.dev/pricing](https://encore.dev/pricing)) — note that per-resource billing interacts
badly with infra-from-code, since adding a Pub/Sub subscription in code adds a billable
resource. Postgres only; MySQL and MongoDB are explicitly unsupported.

**Maintenance:** the healthiest surveyed. v1.58.2 (2026-08-19), last commit 2026-08-21,
51 commits/90d, 63 open issues.

### A.5 goa

**Removes:** the transport layer, from a DSL. Routing, decode/encode, validation, OpenAPI
2.0/3.x, `.proto` + gRPC stubs, typed clients, a CLI
([code-generation.md](https://github.com/goadesign/goa.design/blob/main/content/en/docs/1-goa/code-generation.md)).
Quantified from the maintainers' own 2-method calculator example
([goadesign/examples/basic](https://github.com/goadesign/examples/tree/main/basic)): 2,584 bytes
of design, 467 bytes of actual business logic, and **51,622 bytes across 45 generated files
checked into git**.

**Covers HTTP and gRPC. That is all.** Config: nothing but stdlib `flag` in the scaffolded
`main` (`-host -domain -http-port -grpc-port -secure -debug`). Logging: a 1-method interface;
the generated `main` imports `goa.design/clue/log` from a **different repo**. Lifecycle: the
scaffold hand-rolls `signal.Notify` + error channel + `WaitGroup`. Actuator: nothing in goa —
`goadesign/clue` (76 stars, separate module) has `health.Handler` and
`debug.MountDebugLogEnabler` (`/debug?debug-logs=on|off`). Database: zero database code in the
repo.

**Why reject:** *"`goa gen` … **Recreates the entire `gen/` directory from scratch each time** …
**Run after every design change**"*, and generated code is committed. Worse, `goa gen` is not a
template render — [cmd/goa/gen.go](https://github.com/goadesign/goa/blob/v3/cmd/goa/gen.go)
writes a temporary `main.go` and shells out to **`go get` then `go build`** then executes the
binary, so every regeneration costs a full Go build and **can mutate your `go.mod`**. The DSL
exports 149 functions used via dot-import; your API contract lives in a Go-shaped DSL only goa
reads (OpenAPI out, never in). The escape hatch is stringly-typed `Meta("struct:field:type", …)`
and two of the oldest live bugs are exactly that failing
([#3924](https://github.com/goadesign/goa/issues/3924),
[#3595](https://github.com/goadesign/goa/issues/3595)). A production goa service pulls three
separately-versioned repos: goa, plugins (69 stars), clue (76 stars) — the two things most needed
in production, logging and health, live in the 76-star one.

**Maintenance:** healthy. v3.30.0 (2026-08-19), 48 commits/90d on `v3`, 30 open issues.

### A.6 Is there a Go Actuator? No.

This was the sharpest finding of the survey, and it is the strongest single argument for go-boot.

**Three of six Actuator concerns are already stdlib one-liners:**

| Actuator endpoint | Go answer | Source |
| --- | --- | --- |
| `/threaddump`, `/heapdump` | `import _ "net/http/pprof"` — its `init()` registers `/debug/pprof/*` | [pprof.go](https://github.com/golang/go/blob/master/src/net/http/pprof/pprof.go) |
| `/metrics` (ad hoc) | `import _ "expvar"` — `init()` registers `GET /debug/vars`, publishes `cmdline` + `memstats` | [expvar.go](https://github.com/golang/go/blob/master/src/expvar/expvar.go) |
| `/prometheus` | `promhttp.Handler()` | [promhttp](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp) |
| `/info` | `runtime/debug.ReadBuildInfo()` gives `GoVersion`, `Main`, `Settings` incl. `vcs.revision`, `vcs.time`, `vcs.modified` — **data only, no endpoint** | [pkg.go.dev/runtime/debug](https://pkg.go.dev/runtime/debug#ReadBuildInfo) |
| `/loggers` | `slog.LevelVar` — *"a Level variable, to allow a Handler level to change dynamically"* — **data only, no endpoint**. `zap.AtomicLevel` **is** an `http.Handler` (GET returns `{"level":"info"}`, PUT sets it) — the only true `/loggers` parity in Go | [pkg.go.dev/log/slog#LevelVar](https://pkg.go.dev/log/slog#LevelVar), [zap http_handler.go](https://github.com/uber-go/zap/blob/master/http_handler.go) |
| `/health` | **nothing in stdlib** | — |

Note two traps found in source, not in READMEs: `promhttp`'s default registry auto-registers
only `ProcessCollector` + `GoCollector` (verified in the `init()` of
[prometheus/registry.go](https://github.com/prometheus/client_golang/blob/main/prometheus/registry.go),
undocumented in the README), and `net/http/pprof` carries **no security warning in its package
doc** — `import _ "net/http/pprof"` silently mounting on `DefaultServeMux` is a real footgun.

**gRPC health is the one place Go is ahead of Spring:** a ratified cross-language protocol,
`grpc.health.v1.Health` with `Check` and `Watch` and four statuses
([health-checking.md](https://github.com/grpc/grpc/blob/master/doc/health-checking.md)),
implemented in [`google.golang.org/grpc/health`](https://github.com/grpc/grpc-go/tree/master/health)
(`health.NewServer()` seeds `{"": SERVING}`, plus `SetServingStatus`/`Shutdown`/`Resume`).
Note that [grpc-health-probe's own README](https://github.com/grpc-ecosystem/grpc-health-probe)
now says *"Kubernetes has now built-in gRPC health checking capability as generally available. As
a result, you might no longer need to use this tool."*

**The Actuator-branded Go libraries are a graveyard**, and this matters as a warning as much as
an opportunity:

| Repo | Stars | Last commit | Verdict |
| --- | --- | --- | --- |
| [sinhashubham95/go-actuator](https://github.com/sinhashubham95/go-actuator) | **3** | 2025-03-19 | Only installable one. `/metrics` is `runtime.MemStats` as JSON, **not Prometheus**. **No `/loggers`.** |
| [go-spring starter-actuator](https://github.com/go-spring/go-spring/tree/master/starter/experimental/starter-actuator) | (repo 1,791) | 2026-08-19 | Closest to parity anywhere in Go, but **no module tag — `go get` 404s on the proxy**, lives in `experimental/`, needs a whole IoC framework, and its `/loggers` is **read-only by design**. |
| [heptiolabs/healthcheck](https://github.com/heptiolabs/healthcheck) | — | 2021-11-23 | **Archived**, never released. |
| [InVisionApp/go-health](https://github.com/InVisionApp/go-health) | — | — | **Archived**, "no longer actively supported". |
| wreulicke, malike, tellme, huseyinbabal, megaease, ops-k, dennesshen variants | 0–3 | 2018–2025 | 1–3 commits each. Stillborn. |

Live health libraries do exist: [alexliesenfeld/health](https://github.com/alexliesenfeld/health)
(836 stars, last commit 2026-06-15) and
[hellofresh/health-go](https://github.com/hellofresh/health-go) (590 stars, 2026-04-17).

**OpenTelemetry Go** covers tracing and metrics (Traces and Metrics **Stable** at v1.45.0, Logs
**Beta** at v0.21.0) but leaves the boilerplate: the
[official Getting Started](https://opentelemetry.io/docs/languages/go/getting-started/) has you
hand-write `setupOTelSDK` with a propagator, three providers, a `shutdownFuncs` slice folded with
`errors.Join`, and a `handleErr` for partial cleanup — five helpers before one span emits. It
covers **no** Actuator concern: `healthz` and `readyz` code searches return 0 results, and
`resource.DefaultWithContext` has **no `ReadBuildInfo` detector**, so `service.version` is yours
to populate. Module cost: 28 `go.mod` files in the core repo, 68 in contrib; a
traces+metrics+OTLP+HTTP setup pulls 7–8 independently versioned modules, with `otelhttp`/
`otelgrpc` still on `v0.70.0` while the core is stable.

**Conclusion: a Go developer in 2026 has no off-the-shelf Actuator and assembles one every
time.** But the honest framing is that the assembly is roughly 50 lines. **What is missing is not
capability, it is a convention** — a management port, agreed paths, consistent JSON, and a
health/readiness abstraction. That convention is the actual gap, and it is small.

### A.7 GoFr — not in the brief, and the strongest adopt candidate

[gofr-dev/gofr](https://github.com/gofr-dev/gofr) was not on the ticket's list. It should have
been: **20,972 stars, Apache-2.0, v1.59.0 released 2026-08-13, last commit 2026-08-21, 48
commits/90d.** It self-describes as *"An opinionated GoLang framework for accelerated
microservice development. Built in support for databases and observability."* — which is
go-boot's pitch.

**It ships the convention go-boot wants to ship.** Two health endpoints auto-registered with no
code (`/.well-known/alive`, `/.well-known/health`, aggregating every registered datasource), and
**Prometheus metrics on port 2121 at `/metrics` automatically** with default `app_go_routines`,
`app_go_sys`, `app_sys_memory_alloc`, `app_go_numGC`. Config, logging, DB drivers, migrations,
cron, pub/sub, startup hooks.

**Where it falls short, from its own docs:** `/.well-known/health` returns **200 even when
`DEGRADED`** — a probe wired to the status code always sees the service as ready. No `/info`
build-info endpoint, no `/env`. Runtime log-level change is **inverted**: pull-based, where you
set `REMOTE_LOG_URL` and GoFr polls it every 15s
([docs](https://github.com/gofr-dev/gofr/blob/development/docs/advanced-guide/remote-log-level-change/page.md))
— you must *run a log-level service* rather than curl the app.

**Where it fails #1's constraints, verified from its `go.mod`
([raw](https://raw.githubusercontent.com/gofr-dev/gofr/development/go.mod)):**

- **50 direct requires** in the core module, including `cloud.google.com/go/pubsub`,
  `segmentio/kafka-go`, `eclipse/paho.mqtt.golang`, `graphql-go/graphql`, `dgraph-io/dgo`,
  `go-redis`, `go-sql-driver/mysql`, `lib/pq`, `modernc.org/sqlite`, `golang-jwt`, 11
  OpenTelemetry modules — **and six test libraries as direct requires of the runtime module**
  (`go-sqlmock`, `miniredis`, `redismock`, `testify`, `goleak`, `go.uber.org/mock`).
  `go get gofr.dev` puts GCP Pub/Sub and a GraphQL parser in your module graph.
- **`go 1.26.0` in its `go.mod`** — consumers need a Go 1.26+ toolchain. #1 sets go-boot's floor
  at 1.22.
- It owns your `main` and your handler signature (`*gofr.Context`), which is the opposite of
  #1's "thin composable library, plain Go wiring, a reader can copy a Preset's body".

Note GoFr splits its datasource drivers into separate modules — **32 `go.mod` files** — and
*still* carries 50 direct requires in the core. That is the go-zero weight problem with the
kratos release-mechanics problem, both at once.

### A.8 Build or adopt — the recommendation

**Build. But build much less than a framework, and be honest that the gap is narrow.**

**Why not adopt, in one line each, against #1's own constraints:**

| Project | Disqualifier |
| --- | --- |
| **uber/fx** | Reflection DI is explicitly out of scope in #1 — and it is *stalled*: 0 commits in 90 days, no release since 2025-05-13. Adopting a dependency for your **lifecycle core** that has not shipped in 15 months is the wrong risk. |
| **Encore** | No `go build`, no `go test` — a custom compiler is a hard build-time dependency. Fails "plain Go wiring" outright. Plus MPL-2.0, per-resource cloud pricing, no gRPC. |
| **goa** | Solves 2 of 7. Mandatory full regeneration on every design change, via a `go get` + `go build` cycle that can mutate your `go.mod`. Fails "no codegen burden". |
| **go-zero** | 44 direct requires including `k8s.io/client-go` and six test libraries; a reflection-driven struct-tag mini-language; `goctl`-shaped project layout; the health-probe registry is in `internal/` and unreachable from your code. Fails "stdlib first" and "no magic". |
| **go-kratos** | The best-behaved of the six — 11 direct requires, modest reflection, no structure lock-in in core. But it solves 4 of 7, has **no actuator and no database**, an unordered lifecycle, and a protobuf-shaped error type you must adopt everywhere. And `main` has had 6 commits in 90 days with 89 PRs open. |
| **GoFr** | Ships the convention, and is genuinely active. But 50 direct requires, requires Go 1.26, and owns your `main` and handler signature. |

**Do not soften this part:** if the author would accept GoFr's shape, **GoFr already does most of
this job** and has 21k stars behind it. The reason to reject it is weight and shape — not a
capability gap. That is a legitimate reason given #1's stdlib-first and thin-library
constraints, but it is a *preference-driven* decision, and it should be recorded as one rather
than dressed up as an unmet need.

**What genuinely does not exist, and is worth building:**

1. **An ordered Component lifecycle without reflection.** Start in declared order, stop in
   reverse, with timeouts. fx is the only project that does it, and only via a reflection
   container. kratos uses an unordered errgroup; go-zero's own doc comment says *"the starting
   order of the added services is not guaranteed"*. This is a small amount of code and it is the
   single clearest hole in the Go ecosystem.
2. **An Actuator convention.** Not new capability — three of six pieces are stdlib one-liners
   (A.6). What is missing is a management port, agreed paths, consistent JSON, a
   health/readiness abstraction, and two thin handlers over `slog.LevelVar` and
   `debug.ReadBuildInfo`. **Nobody has shipped a runtime log-level endpoint. Nobody but Encore
   has shipped build info.** Roughly 50 lines, and it is the highest-value thing in v1.
3. **Wiring Presets that a reader can copy.** Every alternative either generates your `main`
   (goa, go-zero, Encore) or builds it reflectively (fx). Nobody offers plain Go you can read
   and fork.

**What to be sceptical about in the v1 scope:**

- **The Scaffold CLI is the weakest part of the case.** kratos, go-zero, Encore and goa all ship
  one, all are better resourced, and #1 already defers its design. Ship the library first and
  let the CLI wait until Presets exist — which #1 already says.
- **The db Starter is where the effort will actually go.** Migrations are missing from kratos,
  go-zero, fx and goa; Encore and GoFr have them and both are opinionated about it. This is the
  one Starter where "thin wiring, neutral on the query layer" is a real design problem, not a
  weekend.
- **Section A.6's table is a warning as well as an opportunity.** The Actuator-branded Go repos
  are 0–3 star projects with one to three commits. Building the right thing is not the risk;
  being the ninth abandoned `go-actuator` is. #1's "built for the author's own use first" is the
  correct hedge — it makes the project useful at one user.

**Net:** the honest answer is not "adopt one of them". No project covers the seven Starters,
three capabilities are missing from all of them, and every plausible adopt candidate breaks a
constraint that #1 already settled deliberately. But the gap is **narrow and mostly convention**,
not capability — so the correct build is a small one: lifecycle, the Actuator convention, and
readable Presets over stdlib. If go-boot v1 grows past a few thousand lines, that is the signal
it has drifted into rebuilding kratos, and the answer flips to adopting kratos.

---

## Sources

Every URL cited inline above. Primary sources only: project repositories and their own
documentation, the Go Modules Reference and Go release notes, `pkg.go.dev`, and the GitHub API.
All GitHub metrics measured 2026-08-24. Part B measured on `go1.26.3 linux/amd64`.

## Marked UNVERIFIED

- Whether `govulncheck` ignores modules that are in the pruned module graph but never imported
  (B.6 caveat 1) — asserted from its import-graph design, not tested here.
- kratos's 28-module split rationale is inferred from the README ("optional integrations") and
  the v2→v3 migration guide's JWT note; no document states the policy as a whole.
- go-zero's and fx's module-split rationales are not documented in prose; both were inferred
  from dependency direction and, for fx, from its `Makefile`.
- fx's error-message quality is **not** discussed as a caveat in fx's own docs — inferred from
  `fx.VisualizeError`'s existence and a v1.24.0 changelog line.
- Whether `encore run` works fully offline / logged out — Encore's docs do not state it either way.
- Whether Encore exposes a Prometheus **scrape** endpoint anywhere; only remote-write push was
  found in primary sources.
- GoFr's reflection usage in its core request path was not audited.
- Whether kratos v2 has a maintained long-lived branch now that `main` is v3.
- Whether go-zero's `/healthz` is meaningfully green-on-ready out of the box: which framework
  components register probes was not traced, and `comboHealthManager.IsReady()` returns **false**
  when zero probes are registered.
- `gh api "search/code?q=actuator+language:Go+filename:go.mod"` returns `total_count: 0`; that
  filter combination appears broken, so in-repo (non-library) actuator implementations may exist
  that this survey did not surface.

