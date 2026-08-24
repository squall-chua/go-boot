# prototypes/ — THROWAWAY

This is throwaway prototype code for [ticket #2](https://github.com/squall-chua/go-boot/issues/2)
("Prototype the day-one main.go"). **It is not the go-boot library.** Nothing here is meant to be
kept, reviewed for quality, or imported.

It is a **separate Go module** (`goboot-prototype`) on purpose. There is deliberately no `go.mod`
at the repo root — that is not this ticket's decision to make.

## What it answers

What the developer's day-one `main.go` actually looks like, for three shapes, in both the Preset
form and the explicit form the Preset expands to.

| Directory | Shape |
|---|---|
| `cmd/http-only/` | smallest useful service |
| `cmd/http-actuator-config/` | the realistic default |
| `cmd/full/` | HTTP + gRPC + database, the full v1 surface |

In each directory, `main.go` is the **Preset form** and `explicit.go` is the **explicit form**.
Both are real and both compile. Run the explicit form by passing `explicit` as the first argument:

```
go run ./cmd/http-only            # Preset form
go run ./cmd/http-only explicit   # explicit form
```

The `cd`-sensitive variants read `app.yaml` from the working directory:

```
cd cmd/http-actuator-config && go run .
```

`cmd/full` **requires Postgres and is compile-only here.** It was never run.

## What the stub under `goboot/` is

Just enough of `goboot` and its Starters to make the call sites in `cmd/` real. Every decision
inside it is a placeholder except the library choices, which are already settled by the closed
research tickets and are used as-is:

- config: stdlib + `go.yaml.in/yaml/v3`
- gRPC Transport: `connectrpc.com/connect`
- migrations: `pressly/goose` v3 via `NewProvider` with `lock.NewPostgresSessionLocker()`
- Actuator: `prometheus/client_golang` for metrics, `log/slog` + `slog.LevelVar` for log level,
  health written in-house

**Not included:** OTel tracing (the three variants in the ticket do not name it), profile
overlays, the flag layer of the config loader, TLS, middleware, and a drain grace period.

## Findings

See `../docs/prototypes-notes.md`.

## Regenerating the proto

`cmd/full` uses generated Connect code checked in under `internal/gen/`. To regenerate you need
`buf`, `protoc-gen-go` and `protoc-gen-connect-go` on `PATH`:

```
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.20.0
buf generate
```
