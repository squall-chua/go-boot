# Prometheus owns every metric, OTel owns traces

go-boot runs **two pipelines on purpose**, and the line between them is not negotiable per Starter:
**a metric go-boot ships is registered on `prometheus.DefaultRegisterer` and is readable at
`/actuator/metrics`.** Traces go to OTel and nothing else. An operator asking "how many of my RPCs
failed" has one place to look, and that answer does not change when HTTP metrics arrive.

This was implicit from [#7](https://github.com/squall-chua/go-boot/issues/7) onward — it is why #10
removed `Actuator.Registry` and why `goboot/trace/rpc` passes `otelconnect.WithoutMetrics()` — but
it was never written as a rule, so v1 shipped with the RPC half of the metric surface missing and
recorded that as a known gap.
[#41](https://github.com/squall-chua/go-boot/issues/41) had to settle the rule before it could add
a single counter. The rule is `docs/spec.md` §4.2; the package it produced is `goboot/grpc/metrics`
(§4.4).

## Considered options

### Which pipeline owns metrics

- **Prometheus, with go-boot writing its own interceptor.** Chosen. `goboot/grpc/metrics` registers
  `rpc_requests_total` and `rpc_duration_seconds` on the default registry that `promhttp.Handler()`
  already serves. It costs **no new module**: `github.com/prometheus/client_golang` is the
  dependency the Actuator has linked since #7, so the counter and the histogram come from a package
  already in the graph. Measured, `goboot/grpc/metrics` links **10 modules**, and no other package's
  pinned count moved.
- **Bridge OTel into the Prometheus registry**: drop `WithoutMetrics()`, install a `MeterProvider`
  whose reader is `go.opentelemetry.io/otel/exporters/prometheus`, and let `otelconnect` emit the
  metrics it already knows how to emit. This is the tidy answer on paper — the instrumentation is
  written and follows OTel semantic conventions — and it still lands in one registry, so it does
  satisfy "one endpoint". Rejected on **conditionality first, weight second**:
  - It makes RPC metrics require `goboot/trace/rpc` to be imported **and** tracing to be switched
    on. A service that wants a request count and runs no collector could not have one. That is a
    strictly smaller product for a strictly larger dependency.
  - Measured against the chosen path, same basis as `.github/module-counts.txt`: **10 linked
    modules becomes 22**, and **three are new to `go.mod`** —
    `go.opentelemetry.io/otel/sdk/metric`, `go.opentelemetry.io/otel/exporters/prometheus` and
    `github.com/prometheus/otlptranslator`. Stripped, for a service that does not already trace:
    **8,179,977 bytes becomes 9,117,961**, so **+0.94 MB**. For one that already traces the delta
    is only **+0.07 MB** (9,044,233 → 9,117,961) — which is the honest number, and it is why the
    weight is the second argument and not the first.
- **OTel owns all metrics**, with `/actuator/metrics` serving an OTel exporter or going away.
  Rejected outright. It throws away the **38 metric families** the default registry carries for
  free — `go_goroutines`, `process_cpu_seconds_total` and the rest, measured in #7 — and it makes
  every Actuator user link an OTel metric SDK to keep an endpoint they already had. §9 already
  records that every Actuator user links Prometheus; this would add a second metrics dependency on
  top, which is precisely what #41 said must not break.

### Where the RPC metrics live

- **A new opt-in subpackage, `goboot/grpc/metrics`.** Chosen, by the optional-subpackage rule
  (`CONTEXT.md`, `docs/spec.md` §1) and by exactly the precedent `goboot/grpc/health` and
  `goboot/grpc/reflection` set. It joins assertion 2's heavy list in `.github/check-imports.sh`, so
  `goboot/grpc` can never reach it.
- **On `goboot/grpc` itself.** Rejected. It would take that package from 4 linked modules to 10 and
  charge every gRPC user for Prometheus whether or not they run an Actuator — the leak the
  import-leak check exists to catch.
- **On `goboot/trace/rpc`, next to the spans.** Rejected. It is the tracing package; putting the
  metrics there recreates the conditionality that got the bridge rejected, without even the
  benefit of otelconnect's instrumentation.

### Whether registration happens in `Options`

- **At package init.** Chosen, and it is forced: connect handler options are per **service**, so
  `Options` is called once per mount and a `MustRegister` inside it would panic on the second
  service. It also means `Options` has no error to return, which is the one place its signature
  differs from `rpc.Options`.

## Consequences

- **`goboot/trace/rpc` does not change.** `WithoutMetrics()` stays, and its package comment now
  says where the metrics went instead of recording a gap.
- **The public surface grew from twelve packages to thirteen**, and §12's rule that the surface
  list and `.github/module-counts.txt` move in the same commit got its first real test. It held.
- **Nobody who does not import `goboot/grpc/metrics` pays anything.** No new module in `go.mod`, no
  pre-existing row in the golden file moved, and the one row added is `goboot/grpc/metrics 10 12`.
- **The `code` label is `connect.CodeOf(err)` — the code the caller receives**, not the handler's
  raw error. The interceptor sits inside the sanitiser, so it reads the raw error and maps it the
  same way the sanitiser does: a bare `error` is `unknown`.
- **A panicking handler is counted, and that needed a `defer`.** `connect.WithRecover` is itself an
  interceptor (`option.go` builds it with `WithInterceptors`) and `grpc.DefaultOptions` puts it
  first, so it wraps this one: anything written after `next()` is skipped when a handler panics,
  and the one failure an operator most wants to see would have been the one failure not recorded.
  A test fails without the `defer`.
- **`http.ErrAbortHandler` is passed through uncounted**, because every other layer already treats
  it as a deliberate abort rather than a failure: connect re-panics it, `web.Recovery` re-panics it
  rather than writing a 500, and `web.Logging` writes no access line for it at all — it logs after
  the call, not in a `defer`. Counting it would have made this the one place in go-boot that calls
  a dropped connection a server error.
- **HTTP metrics were not in this change, and had nothing left to decide.**
  [#45](https://github.com/squall-chua/go-boot/issues/45) has since spent this rule and shipped
  `goboot/web/metrics`. It re-opened nothing here — same registry, same endpoint, same
  opt-in-subpackage shape — which is what writing the rule down once was for; the questions it did
  have to settle were all local to the middleware, and are recorded in `docs/spec.md` §4.3 rather
  than restated here.
- **A future Starter has no decision to make.** If it emits a metric, the metric goes on the
  Prometheus default registry, and if it needs a dependency to do so, it goes in a subpackage the
  user imports. That is the point of writing it down once.
