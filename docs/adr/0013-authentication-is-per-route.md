# 13. Authentication is per route, not a global gate

Date: 2026-08-26
Status: accepted
Ticket: [#34](https://github.com/squall-chua/go-boot/issues/34)

## Context

The Security Starter has to decide where a request is rejected. Spring Security answers with a
filter chain and a matcher DSL: a default-deny rule plus a list of exceptions, all of it declared
away from the handlers it protects.

go-boot cannot copy that, and the reason is structural rather than a matter of taste. `/livez`,
`/readyz` and `/actuator/*` share the HTTP listener with the service's own routes (ADR `0003` for
why the Actuator mounts on the caller's handler, ADR `0006` for why gRPC shares the port). A
middleware that demanded a bearer token on every request would refuse the Kubernetes probes, and
the pod would never go ready.

The obvious repair is a path allowlist in config. That moves a security decision into a YAML file
where no compiler checks it, and where a typo silently opens a route rather than failing a build.

## Decision

Authentication and authorization are split, and they sit in different places.

- `security.Authenticate` is a middleware on the server. It verifies a bearer token **when one is
  present**, puts the `Principal` in the request context, and passes a request carrying no token
  straight through. A token that is present and **invalid** is a 401 immediately.
- `security.RequireScope` and `security.RequireAnyScope` are ordinary `web.Middleware` values
  applied **at the mount**, wrapping one handler:

  ```go
  srv.Handle("POST /orders", security.RequireScope("orders:write")(orders))
  ```

There is no matcher DSL, no config-driven rule list and no default-deny switch.

## Consequences

**A route nobody wrapped has no authorization, and go-boot cannot catch it.** This is the real cost
and it is written down rather than designed away — in `docs/spec.md` 4.7 and in `README.md`. The
mitigation is that the wrapper is short and sits next to the handler, so its absence is visible in
review of the same few lines that add the route.

**The Actuator is guardable by the service, not by go-boot.** `actuator.Handler` is one method, so a
service passes its own `Handle` and wraps each endpoint on the way past. Liveness and readiness must
be left open in any such wrapper, for the reason above. `examples/http-secure` is the worked form,
and its test drives it.

**A preflight is not authenticated**, because `CORS` answers it above the mux and `RequireScope`
lives below the mux. That is required rather than incidental: browsers send no `Authorization`
header on a preflight, so a preflight that reached a guarded route would be refused and the real
request would never follow.

**Adding a global gate later stays possible.** It would be a new exported middleware, which is
additive; the reverse — shipping one and taking it away — is breaking. This is the same argument
ADR `0010` used against Preset options and `docs/spec.md` 4.0 used against error sentinels.
