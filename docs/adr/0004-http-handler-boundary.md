# HTTP handlers stay `http.HandlerFunc`

go-boot's HTTP Transport never sees a handler. There is no `func(w, r) error` signature, no go-boot
error type a handler returns, and no request or response wrapper. Everything the Transport offers —
`WriteProblem`, `DecodeJSON`, `WriteJSON` — is a plain function the user calls from inside an
ordinary `http.HandlerFunc`.

## Considered options

- **`func(w, r) error`, with go-boot mapping the returned error to a response.** This is what most Go
  web frameworks do, and it is the only way to make a consistent error body automatic rather than a
  convention. Rejected because it needs an adapter for every handler, every test and every piece of
  third-party middleware, and because it makes go-boot's error type mandatory for anyone who writes
  a route.
- **A go-boot request/response wrapper**, the way Gin and Echo have a context object. Rejected for
  the same reason plus a larger one: it ends the ability to use any `net/http` middleware unchanged.

## Consequences

- **Error bodies are consistent by convention, not by force.** A handler that forgets
  `web.WriteProblem` and calls `http.Error` produces a plain-text body. That is the price.
- **The recovery middleware uses the same `WriteProblem`**, so a panic and a hand-written 400 come
  out in the same RFC 7807 shape.
- **`go-playground/validator` does not need wiring.** A user who wants it calls it inside their
  handler and go-boot never knows — which is only possible because the signature stayed standard.
  Measured, wiring it would have cost every other user 8 modules and 3.11 MB stripped.
- **The go-boot error type is deferred**, not rejected. The map's "Error handling convention across
  go-boot" item now owns it, and should settle it for HTTP and gRPC together rather than twice.
- **Any `net/http` middleware works unchanged**, including `otelhttp`, which is why tracing is one
  line from a separate Starter rather than a feature of this one.
