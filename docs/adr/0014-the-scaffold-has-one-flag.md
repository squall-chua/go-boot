# 14. The Scaffold has one flag, and the rule that decides it

Date: 2026-08-30
Status: accepted
Ticket: [#37](https://github.com/squall-chua/go-boot/issues/37)

## Context

The Scaffold writes a new project. Every scaffold ever written grows flags: `--no-database`,
`--with-tracing`, `--actuator=false`. Each one is a permanent promise, the set only ever grows, and
the combinations multiply — which is the same shape ADR `0010` refused for Presets one level down.

The difference is that a Preset is a function a user keeps calling, and the Scaffold runs once. The
moment it finishes, the project is the user's: `main.go`, `app.yaml` and the migration are files
they own, read and edit. A flag that removes tracing from a generated `main.go` is asking the CLI to
delete four lines the user could delete themselves, in a file they are about to open anyway.

That is not true of everything. Choosing gRPC writes `buf.yaml`, `buf.gen.yaml` and a `.proto`,
and adds a `buf generate` step the project does not compile without. Nobody arrives at those by
deleting lines, and a user who did not want them would be deleting files and uninstalling a build
tool.

## Decision

**A flag exists only when it changes which FILES are written, never which lines.**

In v1 exactly one thing qualifies, so the Scaffold ships exactly one flag: `-grpc`.

## Considered options

- **One flag, by the rule above.** Chosen. The rule is checkable rather than a matter of taste: for
  any proposed flag, look at whether its absence is a file that is not there or a line that is not
  there.
- **A flag per Starter**, `-db=false`, `-trace`, `-actuator=false`. Every one is a promise, and the
  set only grows. It also makes the generated `main.go` a thing the CLI configures rather than a
  thing the user owns, which is the opposite of what the Scaffold is for.
- **An interactive prompt**, the `create-*-app` shape. Same combinations, plus a CLI that cannot be
  scripted, plus more code in a binary whose whole job is to copy a directory.
- **No flag at all**, always write the gRPC files. Makes every user delete a build tool's config
  and an unused proto, and `docs/spec.md` 4.4 already settled that go-boot requires no codegen.

## Consequences

- **Removing something means editing the project, and that is the supported answer.** The generated
  `README.md` says so, because a user who does not know it will look for a flag first.
- **The rule has to be applied to the next request, not argued again.** "Can it write a project
  without a database?" is answered by asking what a `-db=false` would remove: lines. So no.
- **Two complete projects, not one plus an overlay.** Because a flag switches which whole project is
  copied and both have to compile, the gRPC one duplicates the HTTP one's `app.yaml` and migration.
  `docs/spec.md` 15 names that price and the test that holds the pair together.
- **A future flag is additive and costs nothing today.** Shipping one is the choice that keeps that
  open, exactly as ADR `0010` says about shipping no options.
