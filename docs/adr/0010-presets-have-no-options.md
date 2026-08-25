# A Preset is one function with no options

go-boot ships **one** Preset in v1, `preset.Full`, plus its tracing twin `traced.Full`. Neither
takes options, flags or negation config keys. To change what a Preset wires, you copy its body
into your own `main` and edit it. There is no middle setting.

The Preset survives at all on one argument, and it is not line count: **wiring held in a Preset
gets fixed by `go get -u`; wiring held in the user's `main` does not.** If go-boot later learns
that a fourth middleware belongs in the default set, every Preset user picks that up on upgrade.
That is the whole product, and it is why the alternative — having the Scaffold write the explicit
lines into `main` — was rejected.

## Considered options

- **No options; copy the body.** Chosen. It is what `CONTEXT.md` already promised a Preset was,
  and what #11 already said about `web.DefaultMiddleware`.
- **Functional options**, `preset.Full(cfg, migrations, preset.WithoutRecovery())`. Every option is
  a permanent promise, and the set only ever grows. It is also the shape that turns a Preset into
  the auto-configuration the project rules out — the difference between "read this function" and
  "read this function plus the eleven options that rewrite it".
- **Config-driven toggles**, an `actuator.enabled`-style key per Starter. Worse than options: the
  wiring becomes invisible in the code and only observable at runtime, and a reader of `main` can
  no longer tell what the service does.

## Consequences

- **A Preset is take-it-or-leave-it.** The returned struct lets you *add* — `app.Add(consumer)`,
  `app.Web.Handle(...)`, `app.Web.Use(...)` — because the `*goboot.App` is embedded. It does not
  let you remove or reorder. Middleware order is the sharp case: `Use` appends, so anything added
  afterwards lands innermost.
- **Copying the body forfeits the upgrade path**, which was the only argument for the Preset. That
  is the real trade-off and it should be stated in the docs, not softened. A user who copies has
  chosen to own their wiring, exactly as if they had never used a Preset.
- **The explicit form must ship as compiling, CI-built code**, one example directory per Preset
  holding both forms. If copying the body is the supported escape hatch, the copy has to be
  something the build keeps honest. A snippet in a doc page rots; a build failure does not.
- **Two Preset packages, not one function with a tracing flag.** Go links by import, so a `trace`
  option could not have worked: naming `goboot/trace` in `goboot/preset` makes every Preset user
  pay +9.4 MB stripped and 19 indirect modules whether the option is set or not. `traced.Full` is a
  copy of `preset.Full`, not a wrapper around it, because a wrapper would add the trace middleware
  in the wrong position.
- **Adding options later is additive; removing them is breaking.** Shipping none is the choice that
  keeps the option open.
