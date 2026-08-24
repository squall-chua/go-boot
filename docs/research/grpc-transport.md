# gRPC Transport Starter: connect-go vs grpc-go + grpc-gateway

Research for [#5](https://github.com/squall-chua/go-boot/issues/5). Resolved 2026-08-24.

Every claim below cites a primary source: official docs, repository source, release notes, or
a measurement taken on this machine. Blog posts were not used as evidence.

**Recommendation: `connectrpc.com/connect` (connect-go).** Jump to
[Recommendation](#recommendation).

---

## 1. connect-go: current state

| Fact | Value | Source |
|---|---|---|
| Module | `connectrpc.com/connect` | [go.mod](https://github.com/connectrpc/connect-go/blob/main/go.mod) |
| Latest release | **v1.20.0**, 2026-05-20 | [releases](https://github.com/connectrpc/connect-go/releases) |
| Last push to `main` | 2026-08-19 | GitHub API `repos/connectrpc/connect-go` |
| Stars / forks | 4,045 / 154 | GitHub API `repos/connectrpc/connect-go` |
| Known importers | **5,924** | [pkg.go.dev importedby](https://pkg.go.dev/connectrpc.com/connect?tab=importedby) |
| Licence | Apache-2.0 | GitHub API |
| Governance | **CNCF sandbox project** | [FAQ](https://connectrpc.com/docs/faq/) — "We are a Cloud Native Computing Foundation sandbox project" |

**Maintenance.** Commits landed in every month of the last twelve except November 2025 and
February 2026 (GitHub API `repos/connectrpc/connect-go/commits?since=2025-08-24`: 54 commits
across 2025-08 → 2026-08). Cadence is steady rather than busy — the shape of a mature library,
not an abandoned one. Contributor spread is healthy: `akshayjshah` (507), `emcfarlane` (68),
`bufdev` (56), `jhump` (53), `pkwarren` (30) plus others (GitHub API `/contributors`).

**Stability promise.** From the
[README](https://github.com/connectrpc/connect-go/blob/main/README.md#status-stable):

> ## Status: Stable
> This module is stable. It supports:
> * The two most recent major releases of Go […]
> * APIv2 of Protocol Buffers in Go (`google.golang.org/protobuf`).
>
> Within those parameters, `connect` follows semantic versioning. We will _not_ make breaking
> changes in the 1.x series of releases.

**Named production users.** The [introduction](https://connectrpc.com/docs/introduction/)
says Connect's Go implementation "is stable and used by several companies (including Buf) in
production". **Buf is the only organisation named in primary sources.** No public adopters
list exists in the `connectrpc` org. Treat "several companies" as unverifiable; the 5,924
known importers is the harder number.

**Ecosystem health.** All 26 repos in the `connectrpc` org are unarchived. The pieces go-boot
would actually need are all pushed within the last 12 months (GitHub API `orgs/connectrpc/repos`):

| Repo | Purpose | Last push | Latest release |
|---|---|---|---|
| `grpcreflect-go` | gRPC server reflection for `net/http` | 2026-08-13 | v1.3.0 (2025-01-17) |
| `grpchealth-go` | gRPC health checks | 2026-08-13 | — |
| `otelconnect-go` | OTel tracing/metrics | 2026-08-13 | v0.9.0 (2026-01-05) |
| `validate-go` | Request validation | 2026-08-10 | — |
| `cors-go`, `authn-go` | CORS, authentication | 2026-07-22 | — |
| `conformance` | Cross-impl conformance suite | 2026-08-24 | — |

Note the satellite repos release slowly (grpcreflect-go's last tag is 19 months old) even
though they are still maintained. For an Actuator-style gRPC health/reflection story this is
adequate but not vibrant.

---

## 2. Does cleartext gRPC still need the `h2c` wrapper?

**No — not since Go 1.24. The `golang.org/x/net/http2/h2c` dependency is gone.**

Go 1.24 moved unencrypted HTTP/2 into the standard library
([Go 1.24 release notes](https://go.dev/doc/go1.24)):

> The new `Server.Protocols` and `Transport.Protocols` fields provide a simple way to configure
> what HTTP protocols a server or client use.
>
> When `Server.Protocols` contains UnencryptedHTTP2, the server will accept HTTP/2 connections
> on unencrypted ports. **The server can accept both HTTP/1 and unencrypted HTTP/2 on the same
> port.**
>
> Unencrypted HTTP/2 support uses "HTTP/2 with Prior Knowledge" (RFC 9113, section 3.3). The
> deprecated "Upgrade: h2c" header is not supported.

Connect's own docs have been rewritten around this. The
[deployment & h2c page](https://connectrpc.com/docs/go/deployment/) now says:

> You can add h2c support to any `http.Server` and client `http.Transport` using
> [`http.Protocols`](https://pkg.go.dev/net/http#Protocols).

and the [getting-started guide](https://connectrpc.com/docs/go/getting-started/) uses
`p.SetUnencryptedHTTP2(true)` — "Use h2c so we can serve HTTP/2 without TLS."

**Verified by measurement.** The scratch connect-go server built for section 6 serves cleartext
HTTP/2 using only `srv.Protocols.SetUnencryptedHTTP2(true)`. Its complete `go.sum` is two
modules, and `golang.org/x/net` is not among them:

```
connectrpc.com/connect v1.20.0
google.golang.org/protobuf v1.36.12
```

**Caveat worth knowing.** Prior-knowledge only; `Upgrade: h2c` is not supported (Go 1.24 notes,
above). Real gRPC clients use prior knowledge, so this is a non-issue in practice. On the
*client* side there is a sharp edge — per the deployment page, "if HTTP/1 is enabled, H2C will
not be used". Server side is unaffected.

---

## 3. Does connect-go speak a wire protocol real gRPC clients accept, with no proxy?

**Yes. Verified empirically, not just from docs.**

The [gRPC compatibility page](https://connectrpc.com/docs/go/grpc-compatibility/) claims:

> Handlers support the gRPC protocol by default: they work with `grpc-go`, `grpcurl`, and any
> other gRPC client using TLS without any special configuration.

and the [introduction](https://connectrpc.com/docs/introduction/):

> Connect servers and clients support three protocols: gRPC, gRPC-Web, and Connect's own
> protocol. […] Any gRPC client, in any language, can call a Connect server, and Connect clients
> can call any gRPC server.

**Measurement (2026-08-24, this machine, Go 1.26.3).** A scratch connect-go server registered a
single unary handler at `/svc.V1/Ping` on one `http.Server` with
`SetHTTP1(true)` + `SetUnencryptedHTTP2(true)`. A separate, *unmodified* `google.golang.org/grpc`
v1.83.1 client dialled it with `insecure.NewCredentials()` — cleartext, no TLS, no Envoy, no
proxy — and called `cc.Invoke(ctx, "/svc.V1/Ping", …)`:

```
GRPC-OVER-CLEARTEXT: OK (grpc-go client -> connect-go server, no proxy)
```

The same port, at the same time, also answered:

```
$ curl -i -X POST http://127.0.0.1:8080/svc.V1/Ping -H 'Content-Type: application/json' -d '{}'
HTTP/1.1 200 OK
Content-Type: application/json
{}

$ curl -X POST http://127.0.0.1:8080/svc.V1/Ping -H 'Content-Type: application/grpc-web+proto' …
http_version=1.1 code=200
```

So gRPC (HTTP/2), Connect JSON (HTTP/1.1) and gRPC-Web (HTTP/1.1) were all served by one plain
`net/http` server on one cleartext port, with a two-module dependency tree.

**Known gaps** (from the gRPC compatibility page):

- Server reflection is a separate module, `connectrpc.com/grpcreflect`, and **two handlers must
  be mounted** — "there are two versions of the gRPC server reflection API, and many tools
  (including `grpcurl`) still use the older one — most services should mount handlers from both
  `grpcreflect.NewHandlerV1` and `grpcreflect.NewHandlerV1Alpha`."
- Health checks are a separate module, `connectrpc.com/grpchealth`.
- gRPC-Web **text mode** is not supported: "if you're using `protoc-gen-grpc-web`, you must use
  `mode=grpcweb` when generating code."
- *Unverified:* connect-go's behaviour under gRPC-specific client features that go-boot does not
  need — client-side load balancing policies, xDS, custom name resolvers. These live in grpc-go's
  client, not the wire protocol, so a grpc-go client keeps them; but this was not tested.

---

## 4. Vanguard: the transcoding question

**Verdict: still Alpha, self-declared, on the current `main` branch. Not production-ready.
Not abandoned either — it is alive but slow.**

This was the most uncertain point in the ticket, so here is the full evidence.

**The project says so itself.** The last section of the
[README on `main`](https://github.com/connectrpc/vanguard-go#status-alpha) (fetched 2026-08-24 via
GitHub API `repos/connectrpc/vanguard-go/readme`) reads, verbatim and in full:

> ## Status: Alpha
>
> Vanguard is undergoing initial development and is not yet stable.

That text is still present nearly three years after the v0.1.0 release
([2023-10-25](https://github.com/connectrpc/vanguard-go/releases/tag/v0.1.0)).

**Release history** ([releases](https://github.com/connectrpc/vanguard-go/releases)):

| Tag | Date | Gap |
|---|---|---|
| v0.1.0 | 2023-10-25 | — (marked prerelease) |
| v0.2.0 | 2024-05-17 | 7 months |
| v0.3.0 | 2024-08-26 | 3 months |
| **v0.4.0** | **2026-03-04** | **18 months** |

The 18-month gap between v0.3.0 and v0.4.0 is the headline risk. v0.4.0's notes are dominated by
bug fixes — a panic on `WriteHeader` with unset Content-Type, an arm32 build failure, broken
trailer propagation for gRPC errors, an invalid envelope constructor, errors in writer loops.
Those are the kind of defects a library still finding its footing ships, and they were only found
after 18 months in the field.

**It is not dead.** Commits are ongoing but thin — roughly 22 commits across the last 20 months
(GitHub API `repos/connectrpc/vanguard-go/commits?since=2025-01-01`), several of them dependabot.
Real fixes since v0.4.0 include "Ensure operation is cancelled on handler return" (#191,
2026-06-30), "Fix multi-segment bounded path variables with %2F" (#190, 2026-06-11) and "Use
mime.ParseMediaType for REST Content-Type validation" (#189, 2026-05-11). Last push 2026-08-13.
26 issues are open. Stars: 417.

**It is still the officially suggested route.** The Connect
[FAQ](https://connectrpc.com/docs/faq/), answering "Is there a way to generate REST paths with
Connect?", says: "one option would be to use gRPC Transcoding… You could use vanguard-go or the
gRPC-Gateway to serve it." So Connect has not disowned it — but it also has not promoted it out
of Alpha.

**Summary:** alive, officially blessed, actively but slowly maintained, and by its maintainers'
own words not stable. Anyone depending on it is depending on a v0.x with a demonstrated
multi-year quiet period. For go-boot v1 that is a dependency to avoid.

---

## 5. grpc-go + grpc-gateway: current state

### grpc-go

| Fact | Value | Source |
|---|---|---|
| Latest release | **v1.83.1**, 2026-08-19 | [releases](https://github.com/grpc/grpc-go/releases) |
| Stars | 23,031 | GitHub API |
| Known importers | **270,134** | [pkg.go.dev](https://pkg.go.dev/google.golang.org/grpc?tab=importedby) |
| Retraction | `[v1.74.0, v1.74.1]` — "published prematurely with known issues" | [go.mod](https://github.com/grpc/grpc-go/blob/master/go.mod) |

Adoption is two orders of magnitude ahead of connect-go and is not in question. Release cadence
is ~6 weeks.

### grpc-gateway

| Fact | Value | Source |
|---|---|---|
| Latest release | **v2.30.0**, 2026-08-05 | [releases](https://github.com/grpc-ecosystem/grpc-gateway/releases) |
| Stars | 19,985 | GitHub API |
| Known importers (`/runtime`) | **12,080** | [pkg.go.dev](https://pkg.go.dev/github.com/grpc-ecosystem/grpc-gateway/v2/runtime?tab=importedby) |
| Open issues | 151 | GitHub API |

Both are healthy and far more widely deployed than connect-go.

### How many codegen plugins does a working setup need?

Counting binaries you must install and wire into `buf.gen.yaml` / `protoc`:

| Plugin | Needed for | Source |
|---|---|---|
| `protoc-gen-go` | message structs | [tutorial](https://grpc-ecosystem.github.io/grpc-gateway/docs/tutorials/introduction/) |
| `protoc-gen-go-grpc` | gRPC server/client stubs | same |
| `protoc-gen-grpc-gateway` | the reverse-proxy handlers | same |
| `protoc-gen-openapiv2` *(optional)* | Swagger 2.0 spec | [repo](https://github.com/grpc-ecosystem/grpc-gateway/tree/main/protoc-gen-openapiv2) |
| `protoc-gen-openapiv3` *(optional, new)* | OpenAPI 3 spec | [v2.30.0 notes](https://github.com/grpc-ecosystem/grpc-gateway/releases/tag/v2.30.0) |

**Three mandatory, up to five with OpenAPI.** Compare connect-go, which needs **two**:
`protoc-gen-go` and `protoc-gen-connect-go`
([getting started](https://connectrpc.com/docs/go/getting-started/)). Beyond the plugins,
grpc-gateway also requires importing `google/api/annotations.proto` and annotating every method
with `google.api.http` options.

This matters against the map's standing constraint that "an HTTP-only user never installs `buf`"
— the gRPC Starter's codegen burden is a real part of go-boot's onboarding cost.

### In-process gateway mode (`RegisterXxxHandlerServer`): worse than "lacks client streaming"

The ticket asks whether in-process mode still lacks client streaming. **It lacks *all* streaming,
and it also breaks interceptors.** From the generator's own template on `main`
([`protoc-gen-grpc-gateway/internal/gengateway/template.go`](https://github.com/grpc-ecosystem/grpc-gateway/blob/main/protoc-gen-grpc-gateway/internal/gengateway/template.go),
`localTrailerTemplate`, ~line 771), verbatim:

```
// Register{{ $svc.GetName }}{{ $.RegisterFuncSuffix }}Server registers the http handlers for service {{ $svc.GetName }} to "mux".
// UnaryRPC     :call {{ $svc.GetName }}Server directly.
// StreamingRPC :currently unsupported pending https://github.com/grpc/grpc-go/issues/906.
// Note that using this registration option will cause many gRPC library features to stop working. Consider using Register{{ ... }}FromEndpoint instead.
// GRPC interceptors will not work for this type of registration. To use interceptors, you must use the "runtime.WithMiddlewares" option in the "runtime.NewServeMux" call.
```

For any streaming method the generated code emits a stub that always fails:

```go
err := status.Error(codes.Unimplemented, "streaming calls are not yet supported in the in-process transport")
```

And the template that generates the local request functions is empty for all three streaming
shapes — only the unary branch produces code (`localHandlerTemplate`, ~line 591):

```
{{ if and .Method.GetClientStreaming .Method.GetServerStreaming }}
{{ else if .Method.GetClientStreaming }}
{{ else if .Method.GetServerStreaming }}
{{ else}}
{{ template "local-client-rpc-request-func" . }}
{{ end }}
```

The blocking upstream issue,
[grpc/grpc-go#906](https://github.com/grpc/grpc-go/issues/906) ("Add support for custom transport
(in-process, wasm)"), is **still open**. Filed 2016-09-22, last updated 2025-01-21, 57 comments
(GitHub API). Ten years open. Do not plan around it closing.

**Consequence:** the only fully-featured grpc-gateway deployment is the out-of-process one —
the gateway dials your own gRPC server over a real TCP connection (`RegisterXxxHandlerFromEndpoint`).
That means a loopback network hop and a second listener inside one process.

### OpenAPI generation story

- `protoc-gen-openapiv2` is the mature path and emits **Swagger 2.0**, not OpenAPI 3. In v2.30.0
  it was "moved to the protoc toolchain"
  ([#6988](https://github.com/grpc-ecosystem/grpc-gateway/pull/6988)).
- `protoc-gen-openapiv3` is **brand new in v2.30.0** (2026-08-05) and described by the release
  notes as a "Brand new **minimal** OpenAPI v3 generator"
  ([#6623](https://github.com/grpc-ecosystem/grpc-gateway/pull/6623)). Emphasis added — it is
  three weeks old at time of writing and should be treated as immature.
- A separate `openapiv3-merge` tool ships for merging documents
  ([#6771](https://github.com/grpc-ecosystem/grpc-gateway/pull/6771)).

**Connect has no OpenAPI story at all**, by design. The
[FAQ](https://connectrpc.com/docs/faq/) states: "OpenAPI is meant for REST/HTTP endpoints and
doesn't apply to RPC systems like Connect." Third-party generators exist outside the
`connectrpc` org; *unverified* — not evaluated here, and not primary-source-backed.

**This is grpc-gateway's one clear win.** If go-boot ever wanted generated API documentation from
protos, grpc-gateway supplies it and Connect does not.

---

## 6. Dependency weight, measured

Built 2026-08-24 on Linux/amd64 with **Go 1.26.3**, in scratch modules under
`/tmp/.../scratchpad/`. Each module is a minimal but *real* server: it constructs a handler,
registers it, and serves — so the linker cannot dead-code-eliminate the library away.

| Module | go.sum modules | Modules linked into build | Packages in build | Binary | Over baseline |
|---|---:|---:|---:|---:|---:|
| stdlib `net/http` baseline | 0 | 1 | — | **8.42 MB** | — |
| **connect-go** | **2** | **3** | 226 | **12.19 MB** | **+3.76 MB** |
| grpc-go | 6 | 7 | 312 | 15.21 MB | +6.79 MB |
| grpc-go + grpc-gateway | 21 | 9 | 321 | 18.15 MB | +9.73 MB |

Exact byte counts: baseline 8,424,339; connect 12,188,325; grpc 15,212,100; gateway 18,151,396.

**Modules linked in, by name** (`go list -deps -f '{{.Module.Path}}'`):

- **connect-go** — `connectrpc.com/connect`, `google.golang.org/protobuf`. That is the whole list.
- **grpc-go** — `google.golang.org/grpc`, `google.golang.org/protobuf`,
  `google.golang.org/genproto/googleapis/rpc`, `golang.org/x/net`, `golang.org/x/sys`,
  `golang.org/x/text`.
- **grpc-gateway** — the six above plus `github.com/grpc-ecosystem/grpc-gateway/v2` and
  `google.golang.org/genproto/googleapis/api`.

The gateway's `go.sum` carries 21 modules against 9 actually linked, because grpc-gateway's root
module pulls in the `go-openapi/*` family, OpenTelemetry, and `honnef.co/go/tools` for its own
plugins and tests. A go-boot user importing only `/runtime` does not link those, but they land in
the user's `go.sum` and lockfile audits, and they will show up in `go mod graph` and vulnerability
scans.

**connect-go's `go.sum` is literally two lines' worth of modules** — and one of them,
`google.golang.org/protobuf`, is unavoidable for any protobuf-based transport. That makes
connect-go's *marginal* dependency cost over "we already use protobuf" exactly **one module**.

Against the map's standing constraint "one well-known third-party library only where stdlib
clearly falls short", connect-go is as close to a single-dependency answer as gRPC can get.

---

## 7. Port sharing with a plain `net/http` server

This is where the two options diverge most sharply.

**connect-go: yes, natively, and it is the normal way to use it.** A Connect handler *is* an
`http.Handler`. From [getting started](https://connectrpc.com/docs/go/getting-started/), handlers
mount on an ordinary `http.ServeMux`. Section 3's measurement confirms gRPC, gRPC-Web and Connect
JSON all served from one `http.Server` on one cleartext port, alongside anything else on that mux.
Because Go 1.24's server "can accept both HTTP/1 and unencrypted HTTP/2 on the same port"
([Go 1.24 notes](https://go.dev/doc/go1.24)), no TLS and no protocol sniffing are required.

This matters for go-boot specifically: the Actuator and the HTTP Transport can share a listener
with the gRPC Transport, with no second port and no special casing.

**grpc-go: possible, but the mechanism is marked EXPERIMENTAL and lossy.** `*grpc.Server` does
implement `http.Handler`, but its doc comment on `master`
([`server.go`](https://github.com/grpc/grpc-go/blob/master/server.go), ~line 1116) is a list of
warnings:

> The provided HTTP request must have arrived on an HTTP/2 connection. When using the Go standard
> library's server, practically this means that the Request must also have arrived over TLS.
>
> To share one port (such as 443 for https) between gRPC and an existing http.Handler, use a root
> http.Handler such as:
>
> ```go
> if r.ProtoMajor == 2 && strings.HasPrefix(
>     r.Header.Get("Content-Type"), "application/grpc") {
>     grpcServer.ServeHTTP(w, r)
> } else {
>     yourMux.ServeHTTP(w, r)
> }
> ```
>
> Note that ServeHTTP uses Go's HTTP/2 server implementation which is totally separate from
> grpc-go's HTTP/2 server. Performance and features may vary between the two paths. **ServeHTTP
> does not support some gRPC features available through grpc-go's HTTP/2 server.**
>
> \# Experimental
>
> Notice: This API is EXPERIMENTAL and may be changed or removed in a later release.

So sharing a port with grpc-go means: hand-rolled content-type sniffing in a root handler, a
second HTTP/2 stack, an unspecified set of missing gRPC features, and an API the maintainers
reserve the right to remove. The supported path is `grpc.Server.Serve(lis)` on **its own port**.

Adding grpc-gateway on top adds a *further* listener in the supported (out-of-process)
configuration, since the gateway dials the gRPC server over TCP. A go-boot service using
grpc-gateway properly would run: HTTP mux, gRPC server, and a gateway that loops back to the gRPC
server — two listeners plus a self-directed network hop.

---

## 8. On the "no transcoding for v1" decision

**The decision holds. This research strengthens it rather than undermining it.** Both transcoding
routes are worse than the map assumed:

- Vanguard, the route that would preserve the `net/http` shape, is **Alpha by its maintainers' own
  declaration** and went 18 months between releases (§4).
- grpc-gateway's *in-process* mode — the only one that avoids a second listener — supports **unary
  RPCs only** and **silently disables gRPC interceptors**, blocked on a ten-year-old upstream
  issue (§5).
- grpc-gateway's *out-of-process* mode works fully, but costs a second listener and a loopback
  hop inside one process (§7).

There is no cheap, stable way to drive one proto into both a REST and a gRPC surface in Go today.
Keeping HTTP and gRPC as independent Transports over a shared Service Layer sidesteps all of it,
and costs go-boot nothing it would otherwise get for free.

**One caveat, stated plainly.** The decision does forfeit generated OpenAPI documentation (§5).
If go-boot later decides published API docs are a v1 requirement, that requirement — not
transcoding — is what would reopen this, and grpc-gateway would be the reason. Worth recording in
the map's "Not yet specified" list rather than treating as settled.

---

## 9. Finding that affects the map: the Go 1.22 minimum is not achievable

Issue [#1](https://github.com/squall-chua/go-boot/issues/1) fixes **Go 1.22 minimum**, for
`net/http` method and wildcard routing. **No gRPC option can meet it.** Current `go` directives,
read from each project's `go.mod`:

| Module | `go` directive | Source |
|---|---|---|
| `connectrpc.com/connect` | **1.25.0** | [go.mod](https://github.com/connectrpc/connect-go/blob/main/go.mod) |
| `google.golang.org/grpc` | **1.25.0** | [go.mod](https://github.com/grpc/grpc-go/blob/master/go.mod) |
| `github.com/grpc-ecosystem/grpc-gateway/v2` | **1.26.0** | [go.mod](https://github.com/grpc-ecosystem/grpc-gateway/blob/main/go.mod) |
| `connectrpc.com/vanguard` | **1.25.0** | [go.mod](https://github.com/connectrpc/vanguard-go/blob/main/go.mod) |

Since Go 1.21 the `go` directive is a hard floor, not a hint — a Go 1.22 toolchain cannot build
these modules. connect-go additionally commits in its README only to "the two most recent major
releases of Go", so its floor will keep advancing.

**Three implications for the spec:**

1. **The gRPC Starter's floor is Go 1.25 at minimum**, whichever library is chosen. Because gRPC
   is an optional Starter in a single module, go-boot's own `go` directive would have to rise to
   match — a single-module layout cannot hold two different floors.
2. **Choosing Go 1.24+ is what makes the `h2c` dependency disappear** (§2). At Go 1.22, connect-go
   would still need `golang.org/x/net/http2/h2c`. The stdlib-first argument for connect-go is
   *stronger* at a modern floor, not weaker.
3. grpc-gateway's 1.26.0 floor is stricter than everything else, and would drag go-boot's minimum
   up one release further than connect-go would.

**Recommendation to the map: raise the minimum to Go 1.25.** It is forced regardless of which
library wins, and it buys `http.Protocols` outright.

---

## Recommendation

**Use `connectrpc.com/connect` (connect-go) for the gRPC Transport Starter.**

Judged on the ticket's stated criterion — "which serves gRPC well and stays closest to
`net/http`" — connect-go wins on both halves, and the second half is not close.

**Serves gRPC well.** A real, unmodified grpc-go client called a connect-go handler over
cleartext with no proxy, verified on this machine (§3). Streaming, trailers and error details are
supported, and any gRPC client in any language can call it
([introduction](https://connectrpc.com/docs/introduction/)). Reflection and health checks exist as
separate modules and are the pieces the Actuator would need.

**Stays closest to `net/http`.** A Connect handler *is* an `http.Handler`. It mounts on the same
`http.ServeMux` as the HTTP Transport and the Actuator, on one port, with no TLS requirement and
no content-type sniffing (§7). grpc-go's equivalent is documented as EXPERIMENTAL, uses a second
HTTP/2 stack, and "does not support some gRPC features". For a framework whose whole pitch is a
short `main` and plain Go wiring, "one mux, one port, one server" versus "two listeners and a
sniffing root handler" is decisive.

**Costs least.** Two modules in `go.sum` — one of which is `protobuf`, which any protobuf
transport needs anyway — against six for grpc-go and 21 for grpc-gateway. 3.76 MB over a bare
`net/http` binary against 9.73 MB (§6). Two codegen plugins against three-to-five (§5). Against
the standing "stdlib first, one well-known third-party library only where stdlib clearly falls
short" rule, connect-go is the smallest thing that clears the bar.

**What you give up, honestly.**

- **Adoption.** 5,924 known importers against grpc-go's 270,134. Buf is the only named production
  user in primary sources. This is the single real risk, and it is a governance risk more than a
  technical one — mitigated, not eliminated, by CNCF sandbox status, the Apache-2.0 licence, the
  explicit "no breaking changes in 1.x" promise, and continuous commits over the last year.
- **Generated OpenAPI docs.** Connect declines to provide them on principle
  ([FAQ](https://connectrpc.com/docs/faq/)). grpc-gateway does. See §8.
- **Deep gRPC client-side machinery** — xDS, custom resolvers, load-balancing policies — lives in
  grpc-go's *client*. Callers using grpc-go keep all of it; go-boot's server side does not provide
  it. *Unverified:* not tested here, and out of scope for a server-side Starter.

**Do not adopt `connectrpc.com/vanguard`.** Alpha by its own README on `main`, an 18-month release
gap, v0.x with no compatibility promise (§4). It is the right shape for go-boot and the wrong
maturity. Revisit only if it reaches v1.

**Also for the map:** raise the Go minimum to **1.25** (§9). It is forced by every candidate
library, and it is what removes the `golang.org/x/net/http2/h2c` dependency from the
recommendation above.

---

### Reproducing the measurements

Scratch modules live at
`/tmp/claude-1000/-home-squallchua-go-boot/1d4560f2-e3f9-495a-b56c-f7551a4863b4/scratchpad/`
(`m-base`, `m-connect`, `m-grpc`, `m-gw`, `m-client`). They are throwaway; nothing was added to
the go-boot module. Per module:

```sh
wc -l go.sum
go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' . | sort -u
go build -o app . && stat -c %s app
```

The cross-protocol check in §3 runs `m-connect/app`, then `m-client/client` (a grpc-go client
using `insecure.NewCredentials()`), then the two `curl` invocations.
