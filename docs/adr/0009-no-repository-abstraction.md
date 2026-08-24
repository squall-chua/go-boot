# go-boot ships no Repository or Entity abstraction

go-boot wires the pool, migrations and transactions, and then stops. It defines no `Repository`
interface, no `Entity` type and no base struct for a row. The application uses `sqlc`, `ent`,
`gorm` or hand-written SQL, and go-boot does not know which.

This was already implied by the neutrality clause in the project's charter. It is written down
here because the question that provoked it deserves a real answer: **can the same database be
shared with a Spring Data JPA service?** It can — and adding a Repository abstraction to go-boot
would not have helped, which is the part worth recording.

## Considered options

- **No abstraction.** Chosen. The Starter hands back a plain `*sql.DB`.
- **A `Repository[T]` interface**, in the shape a Spring developer expects. It would have exactly
  one implementation, it would need an opinion about identity, mapping and change tracking, and
  every query layer already has its own answer to all three.
- **An `Entity` marker or base struct.** Same objection, plus it would put go-boot in the middle of
  the mapping between rows and Go types, which is the one place a query layer earns its keep.

## Consequences

- **Compatibility with Spring Data JPA is a property of the schema, not of any Go type.** What
  makes a shared database work is column naming, identifier generation, the optimistic-locking
  column and the timestamp types. None of that is affected by the shape of a Go interface. This
  was measured against Hibernate 6.6 rather than reasoned about; see `docs/research/jpa-interop.md`.
- **The interoperability rules ship as documentation and a lint, not as an abstraction.** A lint
  can only check a convention. It cannot read Java classes, so it can never confirm that a schema
  matches a particular JPA model. Only Hibernate can do that, by validating on its own startup.
- **Handing back `*sql.DB` is what makes this work.** Verified by compiling against them:
  `entgo.io/ent`, `gorm.io/gorm` and sqlc's generated interface all accept a plain `*sql.DB`.
- **This is reversible in one direction only.** Adding a Repository later is additive. Removing one
  is a breaking change. Not shipping it is the choice that keeps the option open.
