# 15. The Scaffold writes patterns go-boot refuses to ship

Date: 2026-08-30
Status: accepted

## Context

Two ADRs say go-boot ships neither of two things:

- ADR `0009` — go-boot defines no `Repository` interface and no `Entity` type. `goboot/db` hands
  back a plain `*sql.DB` and stops.
- ADR `0004` — go-boot's HTTP Transport takes an `http.Handler` and nothing else. There is no
  `func(w, r) error`, no request wrapper, no response wrapper.

`docs/spec.md` 10 lists both under what go-boot does not do.

The Scaffold now writes a project that has all three. `internal/greeting/greeting.go` declares a
`Repository` interface. `internal/greeting/entity/` holds an `Entity` and the SQL that loads it.
`internal/transport/` is a handler adapter: a request DTO in, a response DTO out, no
`(w, r)` in sight.

Read quickly, that looks like the Scaffold shipping what the ADRs refused. It is not, and the
difference is worth writing down, because the next reader will hit the same apparent conflict.

## Decision

**The ADRs govern go-boot's public API. The Scaffold governs the generated application's own
code. A pattern go-boot refuses to define is still a pattern the Scaffold may write.**

Nothing in `cmd/goboot/scaffold/` becomes part of go-boot's API. It is copied into the user's
module on the first second, and every line of it is theirs to edit or delete.

## Considered options

- **Write the patterns into the generated project.** Chosen. A user who runs `goboot new` gets a
  layout that answers "where does feature two go" on the first day, and the answer is in files they
  own.
- **Write no Repository, Entity or handler adapter**, so the Scaffold matches the ADRs line for
  line. Rejected: it makes the generated project a bare `main.go` with SQL in the handler, which is
  the shape every real service grows out of in a week. The ADRs are about what a *library* forces
  on every user, not about what good application code looks like.
- **Move the patterns into go-boot** so the two agree the other way. Rejected — that is exactly
  what ADR `0009` and ADR `0004` refused, and for reasons that have not changed.

## Consequences

- **The test of whether something belongs in go-boot is unchanged**: would every user be forced to
  take it? A `Repository` in go-boot is mandatory for anyone who touches the database. A
  `Repository` in the generated project is four lines a user can delete.
- **`internal/transport` is what makes ADR `0004` affordable.** go-boot only ever sees the
  `http.Handler` that `transport.Handle` returns, so `otelhttp` and every other `net/http`
  middleware still works unchanged. The typed signature stops at the module boundary. That is the
  whole reason the adapter can exist in the project and could not exist in the library.
- **The Repository interface being the user's is what makes it useful.** They can widen it, split
  it per aggregate, or put `sqlc`, `ent` or `gorm` behind it. All three take a plain `*sql.DB`,
  which is what ADR `0009` handed them.
- **The distinction has to be repeated where a reader meets it.** It is written in both scaffold
  `README.md` files, in `internal/transport`'s package doc and in `internal/greeting/entity`'s
  package doc, because a user reading generated code does not read this file first.
- **`examples/` carries the same shape**, so a reader meets one layout everywhere and not two.
  The cost is real: `README.md` lifts twelve blocks out of those files verbatim and CI checks every
  one, so an example and its README block move together or the build fails.
