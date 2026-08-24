# Go version floor — evidence for issue #16

Legwork only. This file does not decide the floor; it assembles what a human needs to decide it.

Assembled 2026-08-24. Local toolchain `go1.26.3 linux/amd64`. Every claim carries a source URL or a
command you can re-run. Scratch modules for the experiments were built under a temp directory; all
commands are reproducible from an empty directory.

---

## 0. The three facts that frame everything

1. **The `go` line is a floor set by your *requires*, not by your imports.**
   *"The `go` line must be greater than or equal to the `go` line of all dependencies."*
   — [go.dev/ref/mod#go-mod-file-go](https://go.dev/ref/mod#go-mod-file-go)
   One module ⇒ one `go` line ⇒ the highest dependency anywhere in `go.mod` wins, even for a user
   who imports only the root package. Verified in §5.

2. **Go 1.25 is already out of support.** As of 2026-08-24, `https://go.dev/dl/?mode=json` returns
   exactly two stable releases: **go1.27.0 and go1.26.7**. Go 1.27.0 shipped 2026-08-18 — six days
   ago — which retired 1.25.
   ```
   curl -s "https://go.dev/dl/?mode=json" | python3 -c "import json,sys;print([x['version'] for x in json.load(sys.stdin)])"
   # ['go1.27.0', 'go1.26.7']
   ```

3. **Since Go 1.21, a too-high `go` line is not a wall — it is a download.** Empirically verified in
   §2. This is the single most decision-relevant finding on the ticket, and it substantially
   defuses the "cuts your audience" argument that produced the 1.22 floor.

Go release dates, from the toolchain module `.info` timestamps on `proxy.golang.org`:

| Release | Date | Status today |
|---|---|---|
| 1.22 | 2024-02-02 | EOL since 1.24 (2025-02-10) |
| 1.23 | 2024-08-07 | EOL since 1.25 (2025-08-08) |
| 1.24 | 2025-02-10 | EOL since 1.26 (2026-02-10) |
| 1.25 | 2025-08-08 | **EOL since 1.27 (2026-08-18)** |
| 1.26 | 2026-02-10 | supported |
| 1.27 | 2026-08-18 | supported |

```
curl -s "https://proxy.golang.org/golang.org/toolchain/@v/v0.0.1-go1.25.0.linux-amd64.info"
```

---

## 1. Verified floor table

Every row read from the module proxy on 2026-08-24, at the module's **current latest release**:

```
curl -s https://proxy.golang.org/<module>/@latest                 # version
curl -s https://proxy.golang.org/<module>/@v/<version>.mod        # go directive
```

| Module | Latest version | `go` directive |
|---|---|---|
| `connectrpc.com/connect` | v1.20.0 | **`go 1.25.0`** |
| `google.golang.org/grpc` | v1.83.1 | **`go 1.25.0`** |
| `github.com/grpc-ecosystem/grpc-gateway/v2` | v2.30.0 | **`go 1.25.0`** ⚠️ *not 1.26 — see correction below* |
| `google.golang.org/protobuf` | v1.36.12 | `go 1.23` |
| `go.opentelemetry.io/otel` | v1.45.0 | **`go 1.25.0`** |
| `go.opentelemetry.io/otel/sdk` | v1.45.0 | **`go 1.25.0`** |
| `go.opentelemetry.io/otel/sdk/metric` | v1.45.0 | **`go 1.25.0`** |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` | v1.45.0 | **`go 1.25.0`** |
| `go.opentelemetry.io/otel/exporters/prometheus` | v0.67.0 | **`go 1.25.0`** |
| `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp` | v0.70.0 | **`go 1.25.0`** |
| `github.com/prometheus/client_golang` | v1.24.1 | **`go 1.25.0`** |
| `github.com/pressly/goose/v3` | v3.27.3 | **`go 1.25.7`** ⚠️ *highest floor in the whole stack* |
| `github.com/jackc/pgx/v5` | v5.10.0 | **`go 1.25.0`** |
| `go.yaml.in/yaml/v3` | v3.0.5 | `go 1.16` ⚠️ *not 1.22 — see correction below* |
| `github.com/knadh/koanf/v2` | v2.3.6 | `go 1.23.0` |
| `github.com/spf13/viper` | v1.21.0 | `go 1.23.0` |
| `gofr.dev` | v1.59.0 | `go 1.26.0` |

### Corrections to earlier tickets

- **#5 said grpc-gateway needs Go 1.26. It does not — yet.** The released tag v2.30.0 declares
  `go 1.25.0`
  ([raw go.mod](https://raw.githubusercontent.com/grpc-ecosystem/grpc-gateway/v2.30.0/go.mod)).
  `main` **does** declare `go 1.26.0`
  ([raw go.mod](https://raw.githubusercontent.com/grpc-ecosystem/grpc-gateway/main/go.mod)), so #5
  read an unreleased branch. The next grpc-gateway release will require 1.26. Directionally right,
  factually wrong today. (Moot anyway — #5 chose connect-go.)

- **#4 said `go.yaml.in/yaml/v3` v3.0.3+ declares `go 1.22`. Stale.** v3.0.2 and v3.0.3 did; **v3.0.4
  and v3.0.5 went back down to `go 1.16`**. Verified:
  ```
  curl -s https://proxy.golang.org/go.yaml.in/yaml/v3/@v/v3.0.5.mod   # -> go 1.16
  ```
  The one dependency #4 chose for the base Starter imposes **no floor at all**.

- **#16's comment says "only `goose` has not objected". That is wrong, and backwards.** goose
  v3.27.3 declares **`go 1.25.7`** — a *patch-level* floor, and the single highest requirement in
  the entire non-gRPC stack.
  ([raw go.mod](https://raw.githubusercontent.com/pressly/goose/v3.27.3/go.mod))

- **pgx was never checked.** `github.com/jackc/pgx/v5` v5.10.0 declares `go 1.25.0`. The database
  Starter is at 1.25 independently of goose.

### Newest usable version at each candidate floor

Boundary versions and their release dates, from the proxy `@v/list` + `.info`:

| Dependency | @ floor 1.23 | @ floor 1.24 | @ floor 1.25 |
|---|---|---|---|
| `otel` / `otel/sdk` | v1.38.0 | v1.41.0 (2026-03-02) | v1.45.0 (2026-08-03) |
| `otelhttp` | v0.63.0 | v0.66.0 (2026-03-02) | v0.70.0 |
| `otel/exporters/prometheus` | v0.60.0 | v0.63.0 | v0.67.0 |
| `prometheus/client_golang` | v1.23.2 (2025-09-05) | v1.23.2 (2025-09-05) | v1.24.1 (2026-07-24) |
| `connectrpc.com/connect` | ✗ (v1.18.1, `go 1.21`) | v1.19.2 (2026-04-20) | v1.20.0 (2026-05-20) |
| `google.golang.org/grpc` | ✗ | v1.80.0 (2026-04-01) | v1.83.1 |
| `grpc-gateway/v2` | v2.28.0 | v2.28.0 (2026-02-17) | v2.30.0 |
| `pgx/v5` | v5.7.6 | v5.8.0 (2025-12-26) | v5.10.0 (2026-06-03) |
| `goose/v3` | v3.26.0 (2025-10-03) | v3.26.0 (2025-10-03) | v3.27.3 (`go 1.25.7`) |

Reproduce with the loop in `hist.sh`-style form:
```
for v in $(curl -s https://proxy.golang.org/go.opentelemetry.io/otel/@v/list | sort -V); do
  echo "$v -> $(curl -s https://proxy.golang.org/go.opentelemetry.io/otel/@v/$v.mod | grep '^go ')"
done
```

**Read this table as a decay curve, not a menu.** OTel Go states it tracks *"the current supported
versions of the Go language"*
([README](https://raw.githubusercontent.com/open-telemetry/opentelemetry-go/v1.45.0/README.md),
CI matrix: Go 1.26 + 1.25). grpc-go states *"any one of the **two latest major** releases"*
([README](https://raw.githubusercontent.com/grpc/grpc-go/v1.83.1/README.md)). Both will move to
`go 1.26.0` around Go 1.28 (≈ Feb 2027). **A 1.24 floor buys roughly six months before every row
above needs pinning again.**

---

## 2. What a high `go` directive actually does (the decisive question)

### Documented behaviour

- *"When the `go` or `toolchain` line is newer than the bundled toolchain, the go command runs the
  newer toolchain instead. It first looks in the PATH for a program named `go1.21.9` and otherwise
  **downloads and caches** a copy."* — [go.dev/doc/toolchain](https://go.dev/doc/toolchain)
- *"The default GOTOOLCHAIN setting is `auto`, which enables the toolchain switching described
  earlier."* — same page. `$GOROOT/go.env` ships `GOTOOLCHAIN=auto` in standard distributions.
- *"This automatic toolchain switching can be disabled, but in that case … the go command will
  refuse to run in a main module … in which the `go` line requires a newer version of Go."* — same.
- Russ Cox, [Forward Compatibility and Toolchain Management in Go 1.21](https://go.dev/blog/toolchain):
  *"You'll never have to manually download and install a Go toolchain again. The `go` command will
  take care of it for you."*

### Measured behaviour (all commands re-runnable)

Scratch module `example.com/expa`, one `main.go`, `go` line varied.

| # | Setup | Command | Result |
|---|---|---|---|
| 1 | `go 1.26.0` | `go build` (local go1.26.3, default `auto`) | builds |
| 2 | `go 1.26.0` | `GOTOOLCHAIN=go1.24.13 go build` | **fails**: `go.mod requires go >= 1.26.0 (running go 1.24.13; GOTOOLCHAIN=go1.24.13)` |
| 3 | `go 1.26.0` | `GOTOOLCHAIN=go1.24.13+auto go build` | **`go: downloading go1.26.0 (linux/amd64)`** → builds, **14.5 s wall clock**, once |
| 4 | `go 1.27.0` | `GOTOOLCHAIN=local go build` (local 1.26.3) | **fails**: `requires go >= 1.27.0 (running go 1.26.3; GOTOOLCHAIN=local)` |
| 5 | `go 1.26.6` | `GOPROXY=off GOTOOLCHAIN=go1.24.13+auto go build` | **fails**: `download go1.26.6 for linux/amd64: toolchain not available` |
| 6 | `go 1.26.6` | `GOTOOLCHAIN=go1.20.14 go build` (pre-1.21) | **fails ugly**: `go.mod:3: invalid go version '1.26.6': must match format 1.23` |

`GOTOOLCHAIN=go1.24.13` (test 2) is the exact emulation of what `GOTOOLCHAIN=local` does for a user
whose installed Go *is* 1.24.13.

**The full user journey, measured.** A developer on Go 1.22.12 with stock settings, adding a
dependency that needs 1.25:

```
$ cat go.mod
module example.com/user
go 1.22.0

$ GOTOOLCHAIN=go1.22.12+auto go get connectrpc.com/connect@v1.20.0
go: connectrpc.com/connect@v1.20.0 requires go >= 1.25.0; switching to go1.26.7
go: upgraded go 1.22.0 => 1.25.0
go: added connectrpc.com/connect v1.20.0

$ GOTOOLCHAIN=go1.22.12+auto go build ./...
go: downloading go1.25.0 (linux/amd64)
# builds
```

Three observations from this run, all decision-relevant:

- **It just works.** No manual install, no error, one informative line. The user's Go version was
  four major releases behind the requirement.
- **`go get` silently rewrote the user's own `go` line** from `1.22.0` to `1.25.0`. Their module's
  floor moves too, and with it their GODEBUG defaults (see §4).
- **⚠️ Startup selection downloads the *exact* version on the `go` line, not the latest patch.**
  `go get` switched to the supported **go1.26.7**, but the subsequent `go build` fetched
  **go1.25.0** — an unpatched, out-of-support toolchain. The doc explains the asymmetry: after-startup
  switches *"aim to switch to a supported Go toolchain"*, while startup selection *"runs
  `go<version>`"* from the `go` line verbatim ([go.dev/doc/toolchain](https://go.dev/doc/toolchain)).
  Whatever floor go-boot picks, users who never run `go get go@latest` will build with that exact
  patch release.

### Cost of a switch

- Download ≈ **67 MB** (`go1.26.7.linux-amd64.tar.gz`, from `go.dev/dl/?mode=json`), **≈ 240 MB on
  disk** in `$GOMODCACHE/golang.org/toolchain@v0.0.1-go1.26.0.linux-amd64` (measured `du -sh`).
- ≈ **15 s once**, then cached per machine.

### Where switching genuinely does not save you

1. `GOTOOLCHAIN=local` or `GOTOOLCHAIN=<pinned>` — set by policy in some regulated/air-gapped shops,
   and by some repackaged distributions ( *"repackaged Go toolchains may change this value"* —
   [go.dev/doc/toolchain](https://go.dev/doc/toolchain) ). **Not verified** which specific distro
   packages set it; treat as a real but unquantified population.
2. `GOPROXY=off` / offline or vendor-only CI (test 5).
3. Go < 1.21 (test 6) — and the error is a parse error, not a helpful message. Go 1.20 has been EOL
   since 2024-02.
4. **Not verified:** whether the downloaded `linux-amd64` toolchain runs on musl/Alpine. Worth a
   five-minute check before writing it into docs.

---

## 3. Real-world Go version adoption

### There is no survey data

The **2025 Go Developer Survey** ([go.dev/blog/survey2025](https://go.dev/blog/survey2025), 5,379
respondents, fielded Sept 2025) contains **no question about which Go version respondents use**, and
none about upgrade cadence. Same for
[2024 H2](https://go.dev/blog/survey2024-h2-results). **Do not cite a survey percentage for this —
there isn't one.**

### There is no ecosystem-wide `go` directive dataset

No published statistic exists — from the Go team, `proxy.golang.org`, or `pkg.go.dev` — on the
distribution of `go` directive values across published modules, nor on module downloads broken down
by client Go version. **Marked explicitly as absent rather than estimated.**

### There *is* Go telemetry, and it is the best hard evidence available

[telemetry.go.dev](https://telemetry.go.dev/) publishes daily merged reports of **opt-in** telemetry
including the `GoVersion` of `cmd/go`. Raw JSONL at `https://telemetry.go.dev/data/<YYYY-MM-DD>`.

Aggregating `Program == "cmd/go"` over 2026-08-18, -20, -22 (5,773 records, 9.27 M `go/invocations`):

| Go version | share of reporting installs | share of `go` invocations |
|---|---|---|
| go1.27 | 1.5 % | 9.1 % |
| go1.26 | 47.1 % | 70.3 % |
| go1.25 | 34.7 % | 12.2 % |
| go1.24 | 10.6 % | 5.5 % |
| go1.23 | 6.1 % | 3.0 % |
| ≤ go1.22 | **0 observed** | 0 observed |

Cumulative by installs: **≥1.24 = 93.9 %, ≥1.25 = 83.2 %, ≥1.26 = 48.6 %.**

Reproduce:
```
for d in 2026-08-18 2026-08-20 2026-08-22; do curl -s "https://telemetry.go.dev/data/$d" -o tel-$d.jsonl; done
# then bucket Programs[].GoVersion where Program=="cmd/go"
```

**⚠️ Read this with its bias.** Telemetry is opt-in and the population skews hard toward developers
running `gopls`/VS Code — i.e. exactly the people who upgrade. It is *not* a random sample of
production build fleets. It is nonetheless the only measured data that exists, and the direction is
unambiguous: **not one reporting install was below Go 1.23.** These figures are my aggregation of
public raw data, not a Go-team-published statistic.

### Distro-shipped Go (the actual laggards)

| Distro / channel | Go version |
|---|---|
| **Ubuntu 24.04 LTS (noble)** | **1.22** |
| Ubuntu 25.04 / 25.10 | 1.24 |
| Ubuntu 26.04 LTS (resolute) | 1.26 |
| Debian 12 bookworm (oldstable) | 1.19 (backports: 1.23) |
| **Debian 13 trixie (stable)** | **1.24** (backports: 1.26) |
| Fedora 43 / 44 | 1.25.12 / 1.26.6 |
| Alpine 3.23 / 3.24 / edge | 1.25.10 / 1.26.3 / 1.27.0 |
| RHEL/UBI 9 & 10 `go-toolset` | 1.26.x |

Sources: `https://api.ftp-master.debian.org/madison?package=golang-go&text=1`;
`https://api.launchpad.net/devel/ubuntu/+archive/primary?ws.op=getPublishedSources&source_name=golang-defaults`;
[bodhi.fedoraproject.org](https://bodhi.fedoraproject.org/updates/?packages=golang&status=stable);
Alpine `APKINDEX` under `https://dl-cdn.alpinelinux.org/alpine/v3.24/community/x86_64/`;
[UBI9 go-toolset](https://catalog.redhat.com/en/software/containers/ubi9/go-toolset/61e5c00b4ec9945c18787690).
Cross-check: [repology.org/project/go/versions](https://repology.org/project/go/versions).

**Ubuntu 24.04 LTS shipping Go 1.22 is the strongest single argument for a low floor** — and
toolchain switching neutralises it, because `apt`-installed Go still ships `GOTOOLCHAIN=auto`.

### CI and cloud — all let you pick

- **actions/setup-go**: accepts exact versions, ranges, `stable`/`oldstable`, or reads
  `go-version-file` (go.mod/go.work), preferring the `toolchain` directive —
  [github.com/actions/setup-go](https://github.com/actions/setup-go). The `ubuntu-24.04` runner
  toolcache holds Go 1.24.13 / 1.25.13 / 1.26.6 —
  [Ubuntu2404-Readme](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md).
- **Docker `golang`**: current tags include `1.25.14`, `1.26.7`, `1.27` —
  [hub.docker.com/_/golang](https://hub.docker.com/_/golang).
- **Cloud Run buildpacks**: `go127` (Preview), `go126`/`go125`/`go124` GA —
  [runtime support](https://docs.cloud.google.com/run/docs/runtime-support).
- **App Engine standard**: Go 1.27 Preview (2026-08-19), 1.26 GA —
  [release notes](https://docs.cloud.google.com/appengine/docs/standard/go/release-notes).
- **AWS Lambda**: no managed Go runtime at all — you ship a binary on `provided.al2023`, so any Go
  version you can compile with — [docs](https://docs.aws.amazon.com/lambda/latest/dg/lambda-golang.html).
- **Heroku**: default `go1.25`; version read from `go.mod` —
  [heroku-buildpack-go data.json](https://github.com/heroku/heroku-buildpack-go/blob/main/data.json).

**Answer to "what fraction would a 1.25 floor exclude?"** For anyone on Go ≥ 1.21 with
`GOTOOLCHAIN=auto` and network: **zero** — they get a one-time 67 MB download. The genuinely
excluded set is `GOTOOLCHAIN=local` / offline / pre-1.21 builds, and **no data exists to size it**.
Do not put a number on it.

---

## 4. What each floor buys

Only items a *service framework* would use. Quotes are from the official release notes.

### 1.22 — the current floor
- **`net/http.ServeMux` method + wildcard routing.** *"The patterns used by `net/http.ServeMux` have
  been enhanced to accept methods and wildcards … `"POST /items/create"` … `Request.PathValue`"* —
  [go1.22](https://go.dev/doc/go1.22#enhanced_routing_patterns). Confirmed landed in **1.22, not
  1.21**. This is the whole reason no router dependency is needed.
- Per-iteration loop variables — *"each iteration of the loop creates new variables"* —
  [go1.22#language](https://go.dev/doc/go1.22#language). Removes a class of handler/goroutine bugs.
- `math/rand/v2` — [go1.22](https://go.dev/doc/go1.22#math_rand_v2).

### 1.23 — small, but two real ones
- **`Request.Pattern`** — the ServeMux pattern that matched. Directly useful as the route label for
  Prometheus/OTel metrics without hand-maintained label maps —
  [go1.23#nethttppkgnethttp](https://go.dev/doc/go1.23#nethttppkgnethttp).
- **Timer semantics fixed** — timers GC'd without `Stop`, unbuffered timer channels, no stale sends
  after `Reset`/`Stop`. *"These new behaviors are only enabled when the main Go program is in a
  module with a `go.mod` `go` line using Go 1.23.0 or later."* —
  [go1.23#timer-changes](https://go.dev/doc/go1.23#timer-changes). ⚠️ **This is the application's
  `go` line, not go-boot's** — raising go-boot's floor does not turn it on for users.
- Iterators / `range`-over-func, `iter`, `unique`; `net.KeepAliveConfig`;
  `runtime/debug.SetCrashOutput` — [go1.23](https://go.dev/doc/go1.23).
- `go mod` gains the **`godebug` directive**; `go vet` gains `stdversion`, which flags *"references
  to symbols that are too new for the version of Go in effect in the referring file"* — i.e. from
  1.23 onward vet enforces your declared floor — [go1.23#go-command](https://go.dev/doc/go1.23#go-command).
- Also: koanf and viper become available (`go 1.23.0`) — moot, #4 chose stdlib.

### 1.24 — the dependency-removal release
- **h2c in the stdlib — confirmed, with two caveats.**
  *"When `Server.Protocols` contains `UnencryptedHTTP2`, the server will accept HTTP/2 connections
  on unencrypted ports. The server can accept both HTTP/1 and unencrypted HTTP/2 on the same
  port."* Client side too, via `Transport.Protocols`. But: it is **opt-in** (`Protocols` must be
  set), and *"the deprecated 'Upgrade: h2c' header is not supported"* — prior-knowledge only —
  [go1.24#nethttppkgnethttp](https://go.dev/doc/go1.24#nethttppkgnethttp). Confirms #5's finding and
  removes `golang.org/x/net/http2/h2c`.
- **`tool` directives in `go.mod`** — *"removes the need for the previous workaround of adding tools
  as blank imports to a file conventionally named 'tools.go'"* —
  [go1.24#go-command](https://go.dev/doc/go1.24#go-command). Relevant to the Scaffold's `buf`,
  `sqlc`, `goose` tooling.
- `encoding/json` `omitzero` — *"omits zero-valued `time.Time` values, which is a common source of
  friction"* — [go1.24](https://go.dev/doc/go1.24#encodingjsonpkgencodingjson).
- `os.Root`, `runtime.AddCleanup`, `weak`, `testing.B.Loop`, `T.Context`, `slog.DiscardHandler`,
  FIPS 140-3 via `GOFIPS140` — [go1.24](https://go.dev/doc/go1.24).
- `testing/synctest` exists but requires `GOEXPERIMENT=synctest` and has a *different API* —
  [go1.24#testing-synctest](https://go.dev/doc/go1.24#testing-synctest). Not usable as a floor.

### 1.25 — the release that matters most for *this* framework
- **`testing/synctest` GA.** *"The experiment has now graduated to general availability."* Old API
  *"will be removed in Go 1.26"* — [go1.25](https://go.dev/doc/go1.25#new-testingsynctest-package).
  Deterministic fake-clock concurrency testing, no dependency. go-boot's hardest-to-test surfaces —
  Component lifecycle ordering, graceful shutdown deadlines, retry/backoff — are exactly this.
- **Container-aware `GOMAXPROCS`.** *"the runtime considers the CPU bandwidth limit of the cgroup
  containing the process … In container runtime systems like Kubernetes, cgroup CPU bandwidth limits
  generally correspond to the 'CPU limit' option."* —
  [go1.25](https://go.dev/doc/go1.25#container-aware-gomaxprocs). Deletes the standing reason to
  depend on `uber-go/automaxprocs`. ⚠️ Gated on the **application's** `go` line (GODEBUG
  `containermaxprocs`), not go-boot's.
- **`runtime/trace.FlightRecorder`** — continuous in-memory trace ring buffer with `WriteTo`. A
  ready-made Actuator endpoint — [go1.25](https://go.dev/doc/go1.25#trace-flight-recorder).
- **`net/http.CrossOriginProtection`** — stdlib CSRF, *"uses modern browser Fetch metadata, doesn't
  require tokens or cookies"* — [go1.25](https://go.dev/doc/go1.25#nethttppkgnethttp). Relevant to
  the not-yet-specified Security Starter.
- `slog.GroupAttrs`, `Record.Source`; `sync.WaitGroup.Go`; `GODEBUG=checkfinalizers=1`; DWARF5 —
  [go1.25](https://go.dev/doc/go1.25).
- Not usable: `encoding/json/v2` is `GOEXPERIMENT=jsonv2` only; Green Tea GC is experiment-only here.
- Every library in §1 sits here.

### 1.26 — thin for a framework
- `slog.NewMultiHandler`; `errors.AsType`; `io.ReadAll` *"often about two times faster"*;
  `os/signal.NotifyContext` now cancels with a cause naming the signal (nicer shutdown logs);
  `new(expr)` — [go1.26](https://go.dev/doc/go1.26).
- `httputil.ReverseProxy.Director` **deprecated** in favour of `Rewrite` (*"the `Director` hook is
  fundamentally unsafe"*) — [go1.26](https://go.dev/doc/go1.26#nethttphttputilpkgnethttphttputil).
- `ServeMux` trailing-slash redirects move from 301 to 307 — [go1.26](https://go.dev/doc/go1.26#nethttppkgnethttp).
- Green Tea GC on by default, *"10–40 % reduction in garbage collection overhead"* —
  [go1.26](https://go.dev/doc/go1.26#new-garbage-collector). ⚠️ **A toolchain default, not gated on
  any `go` line** — users get it by upgrading Go, not by go-boot raising its floor.
- `go fix` becomes the home of modernizers — [go1.26#go-command](https://go.dev/doc/go1.26#go-command).

**Nothing in 1.26 justifies excluding half the measured install base.**

### The `go` line and GODEBUG — a library-author trap

*"Only the work module's `go.mod` is consulted for `godebug` directives. Any directives in required
dependency modules are ignored."* — [go.dev/doc/godebug](https://go.dev/doc/godebug).
**go-boot's `go` line sets what go-boot can compile and which language features it may use. It does
not change its users' runtime GODEBUG defaults** — those come from the application's own `go.mod`.
So the 1.23 timer fix and the 1.25 container-aware GOMAXPROCS are things go-boot must *document*
for users, not things it can *deliver* by raising its own floor.

What a *user* changes by moving their own line up ([godebug history](https://go.dev/doc/godebug#history)):
1.23 unbuffered timer channels + `httpservecontentkeepheaders=0` (breaks gzip middleware wrapping
`ServeContent`); 1.24 `rsa1024min=1`, MPTCP on by default, SHA-1 cert verification removed;
1.25 cgroup-aware GOMAXPROCS; 1.26 `urlstrictcolons=1`, `cryptocustomrand=0`. Several 1.22-era
compatibility GODEBUGs (`asynctimerchan`, `tls3des`, `x509keypairleaf` …) are **scheduled for removal
in Go 1.27** ([godebug#go-127](https://go.dev/doc/godebug#go-127)) — staying on an old line only
defers the migration.

### ⚠️ Documented-vs-observed discrepancy worth knowing

The Go 1.26 notes say: *"Running `go mod init` using a toolchain of version 1.N.X will create a
`go.mod` file specifying the Go version `go 1.(N-1).0` … This is intended to encourage the creation
of modules that are compatible with currently supported versions of Go."* —
[go1.26#go-command](https://go.dev/doc/go1.26#go-command).

Measured, in empty directories:

| Toolchain | `go mod init` writes |
|---|---|
| go1.25.14 | `go 1.25.14` |
| **go1.26.0** | **`go 1.25.0`** ✅ matches the note |
| go1.26.1 | `go 1.26.1` |
| go1.26.2 | `go 1.26.2` |
| go1.26.3 | `go 1.26.3` |
| go1.26.7 | `go 1.26.7` |
| go1.27.0 | `go 1.27.0` |

```
d=$(mktemp -d); cd $d; GOTOOLCHAIN=go1.26.0 go mod init t; grep '^go ' go.mod
```
**The behaviour shipped in go1.26.0 and was reverted by go1.26.1.** The *stated intent* — new
modules should target a supported release, not the newest — still reads as Go-team guidance, but do
not cite the mechanism as live. Unresolved: I did not trace the revert to a specific CL or issue.

---

## 5. The multi-module escape — tested

Four scratch modules, `replace`-linked. Base = `example.com/goboot` (`go 1.22.0`, requires only
`go.yaml.in/yaml/v3`, uses 1.22 wildcard routing). Sub = `example.com/goboot/grpc` (`go 1.25.0`,
requires `connectrpc.com/connect v1.20.0`).

**Result 1 — the split does work.** After `go mod tidy` the base stayed at `go 1.22.0`, and a
consumer importing only the base built on a **Go 1.22.12** toolchain with switching disabled:
```
$ GOTOOLCHAIN=go1.22.12 go build ./...       # in consumer-base
go: downloading go1.22.12 (linux/amd64)
# success. consumer go.sum contains exactly one module: go.yaml.in/yaml/v3
```

**Result 2 — the single-module contrast, for the record.** A single module declaring `go 1.22.0`
with a `grpc/` subpackage importing connect:
```
$ go mod tidy        # prints nothing about the go line
$ grep '^go ' go.mod
go 1.25.0            # tidy silently rewrote 1.22.0 -> 1.25.0
```
And a consumer importing **only the root package** of that module:
```
$ GOTOOLCHAIN=go1.22.12 go build ./...
go: module ../goboot requires go >= 1.25.0 (running go 1.22.12; GOTOOLCHAIN=go1.22.12)
```
This is §0 fact 1 demonstrated: **import graph is irrelevant; the `require` list sets the floor.**

**Result 3 — the split is as fragile as #3's hard rule implies.** Adding *one test file* to the base
module that imports `example.com/goboot/grpc`:
```
$ go mod tidy && grep '^go ' go.mod
go 1.25.0            # base module's floor jumped, from a test import alone
$ GOTOOLCHAIN=go1.22.12 go build ./...    # in consumer-base
go: module ../goboot requires go >= 1.25.0 (running go 1.22.12; GOTOOLCHAIN=go1.22.12)
```
#3's rule — *the base package and its tests must never import a Starter subpackage* — is **not
softened by splitting modules; it becomes load-bearing for the floor as well as for download
weight**, and needs the same CI guard either way.

### What the split would actually be worth

Per §1, at current versions the Starters land like this:

| Starter | Dependencies | Floor |
|---|---|---|
| base (config, logging, lifecycle, shutdown) | `go.yaml.in/yaml/v3` (`go 1.16`) | **1.22** (only for `net/http` routing, and even that belongs to the http Starter) |
| http | stdlib | 1.22 / 1.24 for `Protocols` h2c |
| **actuator** | otel v1.45, `client_golang` v1.24.1 | **1.25** |
| **db** | pgx v5.10, goose v3.27.3 | **1.25.7** |
| **grpc** | connect v1.20 | **1.25** |

The map states the **Actuator is in every v1 service**. So the multi-module split buys a 1.22 floor
for a user who takes config + logging + lifecycle + shutdown **and nothing else** — no actuator, no
database, no gRPC. That is a hypothetical user, not the target user.

### Against that, the release cost (documented, not measured)

- **Every submodule needs its own prefixed tag.** *"If a module is defined in a subdirectory within
  the repository … each tag name must be prefixed with the module subdirectory, followed by a
  slash."* — [go.dev/ref/mod#vcs-version](https://go.dev/ref/mod#vcs-version). Five modules ⇒ five
  tags per release (`v1.2.0`, `actuator/v1.2.0`, `http/v1.2.0`, `db/v1.2.0`, `grpc/v1.2.0`).
- **Cross-module version bumps become a lockstep dance**: `goboot/grpc` requires `goboot`, so a base
  release must be tagged and proxy-visible *before* the submodules can require it.
- **`replace` directives during development**, which must be stripped before tagging or they poison
  the published module. (Reproduced above — every one of my scratch submodules needed one.)
- #3 already measured the *download* argument as settled in single-module's favour (1 zip / 2.9 KB
  vs 18 zips / 106 MiB for root-only consumers), and the map accepted it.

**Verdict for the human: the split works technically and is worth exactly one hypothetical user.**

---

## 6. Dropping gRPC from v1

**It changes nothing.** Measured directly — a module importing the actuator, db, and config stacks
with **no gRPC at all**:

```go
import (
    _ "github.com/jackc/pgx/v5/stdlib"
    _ "github.com/pressly/goose/v3"
    _ "github.com/prometheus/client_golang/prometheus/promhttp"
    _ "go.opentelemetry.io/otel"
    _ "go.opentelemetry.io/otel/sdk/trace"
    _ "go.yaml.in/yaml/v3"
)
```
```
$ go mod tidy && grep '^go ' go.mod
go 1.25.7
```

**#16's premise — "gRPC is the only thing forcing 1.25" — is false.** Confirmed by `go list -m`:

| Module | Version | `go` |
|---|---|---|
| `github.com/pressly/goose/v3` | v3.27.3 | **1.25.7** ← binding |
| `github.com/jackc/pgx/v5` | v5.10.0 | 1.25.0 |
| `github.com/prometheus/client_golang` | v1.24.1 | 1.25.0 |
| `go.opentelemetry.io/otel` | v1.45.0 | 1.25.0 |
| `go.opentelemetry.io/otel/sdk` | v1.45.0 | 1.25.0 |

**The Actuator alone forces 1.25 — verified true.** Even a Prometheus-only actuator with no OTel
requires `client_golang` v1.24.1 at `go 1.25.0`; the newest `client_golang` below that is v1.23.2
from 2025-09-05. And the **database Starter independently forces 1.25.7** via goose (or 1.25.0 via
pgx alone).

Dropping the gRPC Starter is therefore a decision about scope and maintenance burden. It is **not a
lever on the Go floor.**

---

## 7. Recommendation

### Floor: `go 1.25.0`

**Why not lower.**
- 1.22 and 1.23 are not reachable at all. With current library versions the floor is 1.25.7 even
  with gRPC deleted (§6). Holding 1.22 means pinning otel to v1.35.x (Nov 2024), `client_golang` to
  v1.22.0, goose to v3.24.1, pgx to v5.7.4 — and shipping no gRPC Starter ever.
- 1.24 is the only genuinely arguable alternative. It is technically viable — connect v1.19.2 is
  only one minor behind, otel v1.41.0 is five months behind. But it costs `client_golang` v1.23.2
  (**11 months stale**, and Prometheus's own docs describe v1.24's collector surface), goose v3.26.0
  (10 months), pgx v5.8.0 (8 months) — and it **decays on a schedule**: OTel and grpc-go both pin to
  Go's two-supported-releases window, so they move to `go 1.26.0` around Go 1.28 (≈ Feb 2027). A
  1.24 floor buys about six months and then forces this whole exercise again.

**Why not higher.** 1.26 excludes ~51 % of measured installs from a zero-friction build (§3) and
buys a framework almost nothing (§4). The Go team's own stated intent for new modules is to target a
*supported* release rather than the newest (§4, `go mod init`).

**Why 1.25 specifically.**
- Every dependency in the recommended stack already sits there — no pinning, nothing stale (§1).
- It buys the one stdlib feature that materially changes how go-boot is built: **`testing/synctest`
  GA**, which is the only sane way to test ordered Component lifecycle, shutdown deadlines and
  retry/backoff deterministically. Plus `runtime/trace.FlightRecorder` (an Actuator endpoint for
  free), container-aware GOMAXPROCS (deletes the `automaxprocs` question), stdlib CSRF, and 1.24's
  h2c removal of `golang.org/x/net`.
- 83.2 % of measured installs are already ≥ 1.25, and **0 % are below 1.23** (§3).
- Toolchain switching makes the remainder a 15-second, 67 MB, one-time download — not an exclusion
  (§2).

**Stay single-module.** The split works (§5) but buys a low floor only for a user who takes neither
the Actuator nor the database — and it adds five prefixed tags per release, lockstep cross-module
bumps, and `replace`-directive hygiene. The map rejected it once on download weight; §5 and §6 give
no reason to reopen it.

**Keep the gRPC Starter or drop it on its own merits.** It is not the constraint (§6).

### What this recommendation costs

1. **Ubuntu 24.04 LTS users who pin `GOTOOLCHAIN=local`** cannot build go-boot with their distro Go.
   Same for Debian trixie (1.24) under a local pin. Unpinned, both auto-upgrade silently.
2. **Air-gapped / `GOPROXY=off` CI** breaks with `toolchain not available` (§2 test 5) until the
   pipeline installs a 1.25+ toolchain. This is the real exclusion set, and **no data exists to size
   it** — do not pretend otherwise.
3. **Pre-Go-1.21 users get a parse error, not a diagnosis** (§2 test 6). Go 1.20 has been EOL for
   2½ years; acceptable, but the README should say the minimum plainly.
4. **The floor is below the supported window.** Go 1.25 went EOL on 2026-08-18. A user whose `go`
   line ends up at `1.25.0` and who never runs `go get go@latest` will build with the *unpatched*
   go1.25.0 (§2 — verified: `go build` downloads the exact go-line version). **Mitigation the docs
   must carry:** tell users to keep a `toolchain` line or run `go get go@latest` in their own module.
5. **The goose wrinkle.** With goose v3.27.3, `go mod tidy` will write **`go 1.25.7`**, not
   `go 1.25.0`. Either accept the patch-level floor (harmless for auto-switching users; excludes a
   locally-pinned 1.25.0–1.25.6) or pin goose to v3.26.0 (`go 1.23.0`, 10 months stale). That is a
   database-Starter decision, not a framework-floor decision — but it must be made, or the declared
   floor and the actual `go.mod` will disagree.
6. **This decision has a shelf life of roughly one year.** When Go 1.28 lands (≈ Feb 2027), OTel,
   grpc-go and connect-go will move to `go 1.26.0` and the floor question returns. Budget for it
   rather than being surprised by it.

### Open items, marked unverified

- Whether a downloaded `linux-amd64` toolchain runs on musl/Alpine.
- Which distro Go packages, if any, ship `GOTOOLCHAIN=local` in `$GOROOT/go.env`.
- The CL/issue behind the `go mod init` revert between go1.26.0 and go1.26.1.
- No public data exists on `go` directive distribution across published modules, or on module
  downloads by client Go version. Not estimated here.

---

## Sources

**Go project (primary).**
[go.dev/doc/toolchain](https://go.dev/doc/toolchain) ·
[go.dev/ref/mod](https://go.dev/ref/mod) ·
[go.dev/doc/devel/release](https://go.dev/doc/devel/release) ·
[go.dev/doc/godebug](https://go.dev/doc/godebug) ·
[go.dev/blog/toolchain](https://go.dev/blog/toolchain) ·
release notes [1.22](https://go.dev/doc/go1.22) [1.23](https://go.dev/doc/go1.23)
[1.24](https://go.dev/doc/go1.24) [1.25](https://go.dev/doc/go1.25) [1.26](https://go.dev/doc/go1.26) ·
[go.dev/dl](https://go.dev/dl/?mode=json) ·
[telemetry.go.dev](https://telemetry.go.dev/) ·
[go.dev/blog/survey2025](https://go.dev/blog/survey2025)

**Module data.** `proxy.golang.org` `@latest`, `@v/list`, `@v/<v>.mod`, `@v/<v>.info` for every
module in §1, fetched 2026-08-24.

**Measurements.** All toolchain-switching, multi-module and `go mod tidy` results measured
2026-08-24 on `go1.26.3 linux/amd64` (WSL2), in scratch modules built from empty directories; the
commands are inline above and re-runnable.

**Related tickets.** [#1](https://github.com/squall-chua/go-boot/issues/1) (map) ·
[#3](https://github.com/squall-chua/go-boot/issues/3) (module layout) ·
[#4](https://github.com/squall-chua/go-boot/issues/4) (config) ·
[#5](https://github.com/squall-chua/go-boot/issues/5) (gRPC) ·
[#6](https://github.com/squall-chua/go-boot/issues/6) (migrations) ·
[#7](https://github.com/squall-chua/go-boot/issues/7) (observability)
