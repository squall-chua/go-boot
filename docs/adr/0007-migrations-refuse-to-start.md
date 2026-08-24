# A service refuses to start when migrations are pending

By default the database Starter checks `HasPending` during `Start`. If migrations are outstanding
and `db.migrateOnStart` is off, `Start` returns an error and the process does not come up. The
alternative was to log a warning and serve anyway.

This is a contract go-boot imposes on the people who deploy it, not just a code choice. If
migrations run as a separate Kubernetes Job, **that Job must finish before the rollout starts**.
If it does not, every new pod crashloops until it does. That has never been written down before,
and it is the reason this ADR exists.

## Considered options

- **Refuse to start.** Chosen. A pod that will not start is loud, and the orchestrator already
  knows how to report it.
- **Log a warning and serve.** The service comes up against a schema it was not built for. The
  failure then appears later, as wrong query results or errors on one endpoint, and nobody links
  it back to the deploy.
- **Apply migrations at startup by default.** Every pod races every other pod at rollout. goose's
  session lock makes that safe, but it also means a slow migration blocks the whole fleet's
  startup, and it takes schema changes out of the deploy pipeline where they can be reviewed.

## Consequences

- **The migration Job must be ordered before the rollout.** This is the operational contract. It
  belongs on the first page of the database Starter's documentation, not in a footnote.
- **`db.migrateOnStart` exists for local development.** It is off by default. It is bounded by
  `lifecycle.startTimeout` (30s, settled in [#8](https://github.com/squall-chua/go-boot/issues/8)), which is ample for the small migrations you
  write on a laptop and is *not* ample for a large production migration. That is deliberate:
  turning this on in production is not a supported way to run.
- **The error message names its own fix.** It says to run `<binary> migrate`, or to set
  `db.migrateOnStart` for local development. It carries no DSN, because [#10](https://github.com/squall-chua/go-boot/issues/10)
  lets an operator expose Check errors on a public endpoint with `actuator.showDetails`.
- **Startup failure is already clean.** [#8](https://github.com/squall-chua/go-boot/issues/8) stops the
  Components that did start, in reverse, with no drain and no drain delay. Nothing extra is needed here.
- **A service that does not own its schema opts out.** Passing a nil migrations `fs.FS` skips both
  the migration and the `HasPending` check. The Starter then provides a pool and a Check only.
