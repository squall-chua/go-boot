# Errors are plain wrapped values, and misconfiguration comes back from the constructor

go-boot has **no error type and no sentinel errors**. A Starter returns a plain `error` from
`errors.New` or `fmt.Errorf`, always wrapped with `%w`, and always opening with a locator — the
config key path, the config file, the Component name, or the package identifier. A caller that
needs to branch matches on somebody else's sentinel: `sql.ErrNoRows`, `fs.ErrNotExist`,
`http.MaxBytesError`.

And **a constructor validates its own config and returns `(T, error)`. `Start` reports only what
needs the world.** That rule made `web.New` and `actuator.New` grow an `error` return, which is the
signature change [#38](https://github.com/squall-chua/go-boot/issues/38) gated `v1.0.0` on.

The full convention, with the table of what each constructor rejects, is `docs/spec.md` §4.0.

## Considered options

### Whether an error type exists

- **No type, no sentinels.** Chosen. An error type would exist for `main` alone, and `main` has one
  error path with one thing to do at the end of it: print and exit non-zero. ADR `0004` already
  refused an error type one level down, for handlers. Adding `goboot.ErrConfig` later is additive,
  so shipping none keeps the option open — the same argument ADR `0010` made against Preset
  options.
- **One sentinel, `goboot.ErrConfig`, wrapped by every misconfiguration.** It buys `main` the
  ability to exit 2 on a config typo and 1 on a dependency being down. Rejected because nothing
  measured needs that distinction: Kubernetes restarts the pod either way, and the message already
  names the key to edit. It is a promise across twelve packages bought for a use nobody has yet.
- **A `goboot.Error` struct with a code.** Rejected outright. It is the gRPC code table §10 already
  refused, wearing a different hat, and it makes every Starter's error a type the caller must learn
  before it can read it.

### Where misconfiguration surfaces

- **The constructor, `(T, error)`.** Chosen. Every one of the four config faults in v1 —
  `log.level`, `db.driver`, `web.tls`, `actuator.expose` — is pure validation of a `Config` struct
  that touches nothing outside it. Putting them all in the constructor gives a reader one rule:
  **if the constructor returned no error, nothing about your config can be wrong yet.**
- **`Start`, with constructors that never fail.** Rejected. It is more churn, not less —
  `goboot.New`, `db.New` and `preset.Full` would all have to change — and `db.New` cannot honestly
  do it, because `sql.Open` returns an error.
- **Leave the split and describe it.** Rejected. It is a description, not a rule: a new Starter's
  author could not tell from it where their own validation belongs, and a reader would have to
  check per Starter whether this one fails early or late. That per-Starter check is exactly what
  the convention exists to delete.

## Consequences

- **`main` is six lines longer in the explicit form**, because `actuator.New` and `web.New` each
  gain an `if err != nil`. That cost is real and the spec states it rather than talking it away.
  The rule a reader gets to hold is what was bought.
- **It is a breaking change, and it must ship in a `v0` release.** `v1.0.0` is cut from a release
  that already carries it, not from the release that introduces it.
- **The gRPC leak was bigger than the known gap, and the bigger half is closed.** §9 recorded that
  no Preset can carry the mount line, so `grpc.DefaultOptions` can be forgotten. Measuring it
  turned up worse: the adapter `docs/spec.md` §4.4 itself printed leaked the same password string
  **with every option correctly wired**, because `connect.NewError(code, err)` makes `err`'s own
  text the caller's message and the sanitiser passes a `*connect.Error` through by design. The
  adapter in the spec and in `examples/full` now returns the error bare, and three tests in
  `goboot/grpc` hold it: the wrapping form is pinned as leaking, the documented form does not leak,
  and a handler's own `*connect.Error` still arrives intact.
- **A `grpc.Handle` that carried the options was tried and refused.** Go's type inference binds the
  helper's type parameter to the generated *interface*, so the user's adapter no longer unifies.
  The shapes that compile are an explicit type argument (longer than the raw mount, and it makes
  the user name a generated identifier), a closure per mount (three lines for one), or `svc any`
  with a type assertion (a compile error turned into a startup panic, which is what §10 rejects a
  DI container for). A mount helper longer than the raw mount is one nobody reaches for. The §9 gap
  stands, and `goboot/grpc` keeps exactly one exported function.
- **`DefaultOptions` stays the only exported function in `goboot/grpc`.** It is the slice you can
  print, edit or splice, the same shape as `web.DefaultMiddleware`.
- **The Actuator's two `/actuator/loglevel` 400s were the HTTP half of the same leak**, answering
  with `err.Error()` from the JSON decoder and from `slog.Level`. They now carry the Actuator's own
  words. They stay plain text rather than RFC 7807: every other Actuator body is plain JSON, and
  `goboot/actuator` does not import `goboot/web` (ADR `0003`).
- **`goboot/db/dbtest` is the one package exempt from the locator rule**, because every export
  takes a `testing.TB` and calls `Fatal` on it. Its text never reaches a `main`, so it names the
  check that failed rather than a config key.
- **A future Starter has no decision to make.** Its config faults go in its constructor and its
  messages open with its config keys. That is the point of writing it down once.
