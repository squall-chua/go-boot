# The gRPC Starter owns no server

`goboot/grpc` has no listener, no `Component` and no config. connect-go's generated constructor
returns `(string, http.Handler)` — which is exactly the signature of `goboot/web`'s
`Handle(pattern string, h http.Handler)` — so a connect service mounts on the HTTP Starter's server
with no adapter and no second port. What is left in `goboot/grpc` is the handler options that make
connect safe by default: panic recovery, error sanitising, and the required protocol header.

## Considered options

- **Its own listener**, as the throwaway prototype had it: `grpc.New(cfg, log)` wrapping a second
  server on `grpc.addr`, default `:8081`. Rejected because nothing needs it. Since Go 1.24 one
  cleartext port serves HTTP/1 and HTTP/2 together, so one `net/http` server answers gRPC, gRPC-Web,
  Connect JSON and plain REST at once — measured in [#5](https://github.com/squall-chua/go-boot/issues/5).
  A user who genuinely wants two ports makes a second `web.Server`; that needs no type of its own.
- **Both a `Handle` and a `Mount` method**, which [#10](https://github.com/squall-chua/go-boot/issues/10)
  and [#11](https://github.com/squall-chua/go-boot/issues/11) both expected to be necessary. The
  question dissolved once the generated signature was read: `Mount` would have been `Handle` under a
  second name.

## Consequences

- **There is no `grpc.addr`.** It is the first thing a reader will look for, so the spec has to say
  outright that the address belongs to `goboot/web`.
- **`goboot/grpc` depends on `goboot/web`.** This softens the map's "HTTP and gRPC are independent
  Starters" to *independent in protocol and code generation, sharing one listener*. It costs no
  weight: a build importing only the base and `goboot/web` links **0** third-party modules.
- **A gRPC-only service hosts the Actuator for free.** #10's fallback — "or the user sets
  `actuator.addr` and the Actuator runs its own listener" — is dead, because a gRPC-only service
  already has a `web.Server`.
- **`web.DefaultMiddleware` runs in front of every RPC**, so `goboot.LoggerFrom(ctx)` and the request
  ID reach a connect handler with no interceptor of go-boot's own.
- **The HTTP access log records 200 for a failed RPC.** gRPC and gRPC-Web carry the status in
  trailers; only the Connect protocol maps errors to HTTP status codes. go-boot's error interceptor
  logs the real code on failure, and `otelconnect` reports `rpc.grpc.status_code` on the span.
- **Tracing needs a filter, not a second span.** `otelhttp` would wrap every RPC in a redundant
  parent, so `goboot/trace` skips any request whose content type starts with `application/grpc` or
  which carries a `Connect-Protocol-Version` header.
