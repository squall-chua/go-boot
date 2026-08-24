# Migrations: goose vs golang-migrate vs atlas

Research for [#6](https://github.com/squall-chua/go-boot/issues/6). Date: 2026-08-24.
Sources are primary only: upstream source code (read from the Go module cache at the exact
versions named), official docs, READMEs, GitHub API.

**Recommendation: `pressly/goose` v3, driven through its `Provider` API, with
`lock.NewPostgresSessionLocker()` wired on by default.** Reasoning at the end.

---

## Versions examined

| Tool | Version | Released | Licence |
|---|---|---|---|
| `github.com/pressly/goose/v3` | v3.27.3 | 2026-07-22 | MIT ([LICENSE](https://github.com/pressly/goose/blob/master/LICENSE)) |
| `github.com/golang-migrate/migrate/v4` | v4.19.1 | 2025-11-29 | MIT ([LICENSE](https://github.com/golang-migrate/migrate/blob/master/LICENSE)) |
| `ariga.io/atlas` (library) | v1.3.0 | 2026-08-02 | Apache-2.0 ([LICENSE](https://github.com/ariga/atlas/blob/master/LICENSE)) |

Release dates and licences from the GitHub API (`gh api repos/<repo>/releases/latest`,
`gh api repos/<repo>`), 2026-08-24.

---

## 1. Library API — can it be driven in-process?

go-boot needs both: an in-process call for the opt-in startup migration, and a
`goboot migrate` command. All three ship a CLI; the question is the library.

### goose — yes, first-class

`goose.NewProvider(dialect, *sql.DB, fs.FS, opts...)` is the whole entry point. It takes a
`database/sql` handle and an `fs.FS` (so `embed.FS` works directly, no adapter).

Source: [`provider.go`](https://github.com/pressly/goose/blob/master/provider.go),
[`provider_options.go`](https://github.com/pressly/goose/blob/master/provider_options.go).

```go
func NewProvider(dialect Dialect, db *sql.DB, fsys fs.FS, opts ...ProviderOption) (*Provider, error)
```

Provider methods (verified against v3.27.3 `provider.go`):
`Up`, `UpByOne`, `UpTo`, `Down`, `DownTo`, `ApplyVersion`, `Status`, `HasPending`,
`GetVersions`, `GetDBVersion`, `ListSources`, `Ping`, `Close`.

`HasPending` is directly useful to go-boot: a Starter can fail fast at startup when
migrations are outstanding and the opt-in auto-migrate is off.

Options relevant here: `WithSessionLocker`, `WithLocker`, `WithTableName`, `WithSlog`
(structured logging into go-boot's logger), `WithGoMigrations`, `WithAllowOutofOrder`,
`WithDisableGlobalRegistry`.

### golang-migrate — yes, but a URL-shaped API

Source: [`migrate.go`](https://github.com/golang-migrate/migrate/blob/master/migrate.go).

```go
func New(sourceURL, databaseURL string) (*Migrate, error)
func NewWithDatabaseInstance(sourceURL string, databaseDriverName string, databaseInstance database.Driver) (*Migrate, error)
func NewWithInstance(sourceName string, sourceInstance source.Driver, databaseDriverName string, databaseInstance database.Driver) (*Migrate, error)
```

Methods: `Up`, `Down`, `Steps`, `Migrate(version)`, `Drop`, `Force`, `Version`, `Close`.

To reuse go-boot's existing pool you go through `postgres.WithInstance(db, &postgres.Config{})`
(or `pgx/v5.WithInstance`), and for `embed.FS` you wrap with
[`source/iofs.New(fsys, path)`](https://github.com/golang-migrate/migrate/blob/master/source/iofs/iofs.go).
Workable, but noticeably more assembly than goose, and the driver-registry-by-string design
(`database.Register("postgres", ...)` in each driver's `init()`) is import-side-effect wiring
that fits a CLI better than a library.

Two API warts for a framework:

- No `context.Context` anywhere. `Up()` takes no ctx; cancellation is via a
  `GracefulStop chan bool` field. go-boot's Component lifecycle is ctx-based, so this needs a
  shim.
- The **dirty flag**. Per the upstream
  [FAQ](https://github.com/golang-migrate/migrate/blob/master/FAQ.md): *"Before a migration
  runs, each database sets a dirty flag. Execution stops if a migration fails and the dirty
  state persists... You need to manually fix the error and then 'force' the expected
  version."* A failed migration therefore wedges every subsequent pod start until a human runs
  `migrate force`. That is a poor fit for an opt-in startup migration.

### Atlas — **no**, not for versioned migrations with revision tracking

This is the decisive finding for Atlas, and it is easy to miss.

The Apache-2.0 library `ariga.io/atlas/sql/migrate` does expose an `Executor`:

```go
func NewExecutor(drv Driver, dir Dir, rrw RevisionReadWriter, opts ...ExecutorOption) (*Executor, error)
```
(source: [`sql/migrate/migrate.go:586`](https://github.com/ariga/atlas/blob/master/sql/migrate/migrate.go))

But the only `RevisionReadWriter` implementation in the public library is
`NopRevisionReadWriter` — a no-op that tracks nothing
([`sql/migrate/migrate.go:1147-1174`](https://github.com/ariga/atlas/blob/master/sql/migrate/migrate.go)).
The real database-backed revision store, `EntRevisions` writing the `atlas_schema_revisions`
table, lives in
[`cmd/atlas/internal/migrate/migrate.go`](https://github.com/ariga/atlas/blob/master/cmd/atlas/internal/migrate/migrate.go)
— an **`internal/` package of a separate Go module** (`cmd/atlas/go.mod` declares
`module ariga.io/atlas/cmd/atlas`). It is not importable from outside that module.

Verified by grep over `ariga.io/atlas@v1.3.0`: the string `atlas_schema_revisions` appears
nowhere in the library outside a test file.

So driving Atlas versioned migrations in-process means either reimplementing the revision
store yourself, or shelling out. The official in-process story is the latter:
[`ariga.io/atlas-go-sdk/atlasexec`](https://pkg.go.dev/ariga.io/atlas-go-sdk/atlasexec) —
`NewClient(workingDir, execPath)`, `MigrateApply(...)`, `MigrateStatus(...)` — which
**runs the `atlas` CLI binary as a subprocess and parses its JSON output**. That requires the
`atlas` binary on `PATH` of every container that runs go-boot. Against the map's "stdlib
first, one well-known library" rule and against a self-contained single binary, that is
disqualifying.

---

## 2. Migration format

| | Format |
|---|---|
| goose | Plain `.sql` files with `-- +goose Up` / `-- +goose Down` annotations, **or Go functions** registered via `WithGoMigrations` / the global registry ([README](https://github.com/pressly/goose)) |
| golang-migrate | Plain SQL only, paired `NNN_name.up.sql` / `NNN_name.down.sql` ([README](https://github.com/golang-migrate/migrate)) |
| Atlas | Versioned SQL files **or** declarative desired-state schema in HCL / SQL / ORM ([atlasgo.io](https://atlasgo.io/)) |

Plain SQL files suit a Postgres service best and are what the map implies: sqlc reads them
directly (§7), they diff in review, and they need no tool to interpret.

goose's Go-function migrations are the tiebreaker over golang-migrate. Data backfills that
need real logic — the case where you must read rows, transform in Go, write back — have no
SQL-only answer. goose lets that migration sit in the same ordered sequence as the SQL ones.
golang-migrate has no equivalent; you write a separate one-off program and remember to run it
in the right place.

Atlas's declarative mode is genuinely more powerful (it builds a graph of schema entities and
plans the diff) but it is a different working model, it wants a dev database to compute
diffs, and its planner/linter is the part that is commercially gated (§6). For a framework
that should be boring, versioned SQL is the right default.

---

## 3. LOCKING — the decisive safety question

Two pods starting at once must not race the same migration.

### goose — Postgres advisory lock, **opt-in**, bounded retry

Source: [`lock/postgres.go`](https://github.com/pressly/goose/blob/master/lock/postgres.go)
(v3.27.3, lines 61-145).

```go
func (l *postgresSessionLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	return retry.Do(ctx, l.retryLock, func(ctx context.Context) error {
		row := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", l.lockID)
		var locked bool
		if err := row.Scan(&locked); err != nil {
			return fmt.Errorf("failed to execute pg_try_advisory_lock: %w", err)
		}
		if locked {
			return nil
		}
		return retry.RetryableError(errors.New("failed to acquire lock"))
	})
}
```

Unlock is `SELECT pg_advisory_unlock($1)` on the same connection.

Scope: `Provider.initialize` takes one `*sql.Conn` from the pool, locks that session, runs
**all** migrations on that same conn, then unlocks with a `context.WithoutCancel` detached
context so a cancelled run still releases the lock
([`provider_run.go:277-336`](https://github.com/pressly/goose/blob/master/provider_run.go)).
Session-level, so a crashed pod's lock dies with its connection.

Defaults from `NewPostgresSessionLocker`: lock ID `4097083626` (crc64 of "goose"), retry every
5s up to 60 times (5 minutes), unlock retry every 2s up to 30 times (1 minute). All
overridable via `SessionLockerOption`.

**It is off unless you ask for it.** From `provider_options.go:72`: *"If WithSessionLocker is
not called, locking is disabled."* The docs say the same
([goose provider docs](https://pressly.github.io/goose/documentation/provider/)). For go-boot
this is fine — arguably better, since go-boot wires it on deliberately and the user cannot
forget — but it must be wired, and it must be documented, or the Starter ships a race.

v3.27.3 also adds `lock.NewPostgresTableLocker()`: a `goose_lock` table with a 30s lease and
a 5s heartbeat, for cases where advisory locks are unavailable (e.g. pooled connections
through PgBouncer in transaction mode, where session state is not stable). Useful escape
hatch; not the default.

One documented sharp edge: Go migrations run on the `*sql.DB`, not the locked `*sql.Conn`.
With `MaxOpenConns == 1` and a non-transactional Go migration, that deadlocks; goose detects
this combination and returns an error rather than hanging
([`provider_run.go:33-56`](https://github.com/pressly/goose/blob/master/provider_run.go)).
Only bites at `MaxOpenConns == 1`.

### golang-migrate — Postgres advisory lock, **always on**, unbounded wait

Source:
[`database/postgres/postgres.go`](https://github.com/golang-migrate/migrate/blob/master/database/postgres/postgres.go):

```go
func (p *Postgres) Lock() error {
	return database.CasRestoreOnErr(&p.isLocked, false, true, database.ErrLocked, func() error {
		aid, err := database.GenerateAdvisoryLockId(p.config.DatabaseName, p.config.migrationsSchemaName, p.config.migrationsTableName)
		if err != nil {
			return err
		}
		// This will wait indefinitely until the lock can be acquired.
		query := `SELECT pg_advisory_lock($1)`
		if _, err := p.conn.ExecContext(context.Background(), query, aid); err != nil {
			return &database.Error{OrigErr: err, Err: "try lock failed", Query: []byte(query)}
		}
		return nil
	})
}
```

`Migrate.Up()` calls `m.lock()` first and `m.unlockErr(...)` on every exit path
([`migrate.go:265`](https://github.com/golang-migrate/migrate/blob/master/migrate.go)); the
same holds for `Down`, `Steps`, `Migrate`, `Drop`, `Run`, `Force`. So locking is on by
default — a real advantage in principle.

The `database/pgx/v5` driver has the identical implementation
([`database/pgx/v5/pgx.go`](https://github.com/golang-migrate/migrate/blob/master/database/pgx/v5/pgx.go)).

Two caveats against the startup-migration use case:

- **Blocking, not try-with-timeout.** `pg_advisory_lock` waits forever, and it is called with
  `context.Background()` — the caller's context cannot cancel it. If a pod dies mid-migration
  in a way that leaves the session alive (rare but possible behind a proxy), or a long
  migration is running, every other pod blocks indefinitely on startup with no readiness
  signal. goose's `pg_try_advisory_lock` + bounded retry surfaces a real error after 5 minutes
  instead.
- **Lock does not save you from the dirty flag.** The lock prevents concurrent execution, but
  a failure still sets `dirty` and requires manual `force` (§1).

The upstream FAQ is honest that locking is per-driver, not universal: *"Database-specific
locking features are used by **some** database drivers"*
([FAQ.md](https://github.com/golang-migrate/migrate/blob/master/FAQ.md)).

### Atlas — advisory lock exists, but **the library Executor never takes it**

`schema.Locker` is part of `migrate.Driver`
([`sql/migrate/migrate.go:97`](https://github.com/ariga/atlas/blob/master/sql/migrate/migrate.go)),
and the Postgres driver implements it with `pg_try_advisory_lock` and exponential backoff
([`sql/postgres/driver.go:368-400`](https://github.com/ariga/atlas/blob/master/sql/postgres/driver.go)).

But grepping `ariga.io/atlas@v1.3.0` for calls to `.Lock(ctx` in non-test code returns
**nothing**. Every call site is in the CLI module:

```
cmd/atlas/internal/cmdapi/migrate.go:937   unlock, err := client.Driver.Lock(ctx, applyLockValue, 0)
cmd/atlas/internal/cmdapi/migrate.go:1758  unlock, err := client.Driver.Lock(ctx, applyLockValue, flags.lockTimeout)
cmd/atlas/internal/cmdapi/cmdapi.go:819    unlock, err := dev.Lock(ctx, "atlas_migrate_diff", flags.lockTimeout)
cmd/atlas/internal/migratelint/lint.go:413 unlock, err := d.Dev.Driver.Lock(ctx, name, 0)
```
with `const applyLockValue = "atlas_migrate_execute"`
([`cmd/atlas/internal/cmdapi/migrate.go:1230`](https://github.com/ariga/atlas/blob/master/cmd/atlas/internal/cmdapi/migrate.go)).

So the safety go-boot needs is in the binary, not in the library. If go-boot embedded the
Atlas library it would have to call `drv.Lock(ctx, name, timeout)` itself around the
`Executor` — doable, since the method is public, but it means go-boot owns a safety property
the upstream tool owns for its own users.

### Locking summary

| | Postgres | MySQL | SQLite |
|---|---|---|---|
| goose | `pg_try_advisory_lock` + bounded retry; **opt-in** | none — `lock/` package has only `postgres.go` | none |
| golang-migrate | `pg_advisory_lock`, blocking forever; **on by default** | `GET_LOCK(?, 10)` ([mysql.go](https://github.com/golang-migrate/migrate/blob/master/database/mysql/mysql.go)) | **no-op** — `Lock()` only flips an in-process atomic bool ([sqlite3.go](https://github.com/golang-migrate/migrate/blob/master/database/sqlite3/sqlite3.go)) |
| Atlas (library) | driver implements it; **Executor never calls it** | `GET_LOCK` in driver, same gap | — |
| Atlas (CLI) | yes, `atlas_migrate_execute` | yes | — |

goose's Postgres-only locking is not a real gap for the map's priorities: Postgres is the
tested default, and golang-migrate's SQLite lock is a no-op anyway (SQLite is single-node, so
the question barely arises). If go-boot later adds a MySQL default, a `lock.SessionLocker`
implementation over `GET_LOCK` is ~30 lines against goose's public interface.

---

## 4. Dependency weight — measured

Method: three scratch modules, each with a `main.go` importing the migration library plus
`github.com/jackc/pgx/v5/stdlib`, then `go mod tidy` and `go build`. Go 1.26.3, linux/amd64,
2026-08-24. A **baseline** module importing only `pgx/v5/stdlib` isolates what each tool
actually adds. `go.sum` count is unique `module version` pairs excluding `/go.mod`-only lines.
"Module roots in build" counts distinct third-party module paths reachable from
`go list -deps`, i.e. code that actually links.

| | baseline (pgx only) | goose v3.27.3 | golang-migrate v4.19.1 | atlas v1.3.0 (lib) |
|---|---|---|---|---|
| `go.sum` modules | 10 | **24** (+14) | **37** (+27) | **29** (+19) |
| `go list -m all` (module graph) | 15 | **86** (+71) | **212** (+197) | **41** (+26) |
| module roots in build | 6 | **10** (+4) | **8** (+2) | **18** (+12) |
| binary size | 13,559,367 B | **14,561,234 B** (+1.00 MB) | **14,409,198 B** (+0.85 MB) | **18,673,054 B** (+5.11 MB) |

What each actually links, beyond the pgx driver group:

- **goose** (+4): `mfridman/interpolate`, `sethvargo/go-retry`, `go.uber.org/multierr`,
  `golang.org/x/sync`. All small and single-purpose.
- **golang-migrate** (+2, using `database/postgres`): `lib/pq`, `golang.org/x/sync`. Note the
  `database/postgres` driver imports `github.com/lib/pq` at
  [postgres.go:21](https://github.com/golang-migrate/migrate/blob/master/database/postgres/postgres.go)
  even when you hand it a pgx-backed `*sql.DB`. Using `database/pgx/v5` instead drops it.
- **atlas** (+12): `hashicorp/hcl`, `zclconf/go-cty`, `zclconf/go-cty-yaml`,
  `agext/levenshtein`, `apparentlymart/go-textseg`, `bmatcuk/doublestar`,
  `go-openapi/inflect`, `google/go-cmp`, `mitchellh/go-wordwrap`, `gopkg.in/yaml.v3`, plus
  `ariga.io/atlas/schemahcl` and `sql`. The HCL machinery is the bulk of the +5 MB.

The `go list -m all` number for golang-migrate deserves a note: **212 modules** in the
requirement graph against 15 for baseline. golang-migrate ships all ~24 database drivers —
Spanner, MongoDB, Snowflake, Neo4j, Cassandra, cloud storage source drivers — in a **single
module**, so their requirements land in your MVS graph and `go.sum` even though only two of
them link into your binary. That is not runtime weight, but it is `go mod` resolution time,
`go.sum` churn, and a much wider surface for `govulncheck` noise and dependabot traffic. For
a framework that other people depend on, this propagates.

Verdict on weight: **goose and golang-migrate are within a rounding error on linked bytes**
(1.00 MB vs 0.85 MB); goose is far cleaner on the module graph (86 vs 212). **Atlas costs 5×
more binary and 12 extra module roots** for the library alone — and that library still cannot
do the job (§1).

---

## 5. Postgres support quality, and MySQL / SQLite

The map wants Postgres as the tested default with the MySQL/SQLite door left open.

- **goose** — supported dialects, from
  [`dialect.go`](https://github.com/pressly/goose/blob/master/dialect.go) at v3.27.3:
  Postgres, MySQL, SQLite3, ClickHouse, MSSQL, Redshift, Spanner, Starrocks, TiDB, Turso,
  YDB, Aurora DSQL (and Vertica, marked deprecated). Postgres is the primary target — it is the only dialect with a locker, and
  `NewProvider(DialectPostgres, ...)` is the documented happy path. A `DialectCustom` +
  `WithStore(database.Store)` escape hatch exists for anything unsupported.
- **golang-migrate** — the widest coverage: 24+ database drivers plus source drivers for S3,
  GCS, GitHub, GitLab (README). Postgres has two drivers (`postgres` via lib/pq, `pgx/v5`),
  both advisory-locked. If go-boot ever needed CockroachDB or Spanner this is the tool with a
  driver already written.
- **Atlas** — deepest Postgres modelling of the three (it inspects and diffs real schema
  objects), but per the
  [Community Edition page](https://atlasgo.io/community-edition), the Apache-2.0 community
  build **omits** "support for database objects like views, triggers, functions, and
  partitioning" and "additional database drivers (SQL Server, ClickHouse, Redshift, Oracle,
  Snowflake, etc.)". So Atlas's headline Postgres depth is partly on the paid side.

For go-boot's actual needs — Postgres tested, MySQL and SQLite reachable — goose and
golang-migrate both clear the bar. golang-migrate wins on breadth; goose wins on the quality
of the Postgres path specifically (locking, `context`, `fs.FS`, `slog`).

---

## 6. Maintenance, stability, licence

From the GitHub API, 2026-08-24:

| | stars | last release | last push | commits in last 12 months | open issues | licence |
|---|---|---|---|---|---|---|
| goose | 11,355 | v3.27.3, 2026-07-22 | 2026-08-22 | **60** | 139 | MIT |
| golang-migrate | 18,847 | v4.19.1, **2025-11-29** | 2026-07-05 | **25** | **489** | MIT |
| ariga/atlas | 8,674 | v1.3.0, 2026-08-02 | 2026-08-02 | not measured | 271 | Apache-2.0 (repo) |

(`gh api repos/<r>`, `gh api repos/<r>/releases/latest`,
`gh api "repos/<r>/commits?since=2025-08-24T00:00:00Z&per_page=100" --jq length`.)

**goose** is the most actively maintained: 60 commits in a year, a release five weeks ago,
pushed two days ago. The `Provider` API landed in v3.something and has been additive since —
the `v3` major has held for years, and the older global API (`goose.Up(db, dir)`) is still
present alongside it, which is evidence of a maintainer who does not break users. Note that
go-boot should use `Provider`, not the global API: the global API has no locking and uses a
package-level migration registry.

**golang-migrate** is stable in the "frozen" sense. Its README states *"API is stable and
frozen for this release (v3 & v4)"* — good for a framework dependency. But nine months since
the last release, 25 commits in a year, and 489 open issues is a project in maintenance
rather than development. It is not abandoned; it is not moving. Missing `context.Context`
support is unlikely to ever be fixed given the freeze.

**Atlas licensing — the thing to actually check.** The `ariga/atlas` repo is Apache-2.0, and
the `ariga.io/atlas` Go library is genuinely Apache-2.0 (verified: the module's `LICENSE` is
the Apache 2.0 text). But per
[atlasgo.io/community-edition](https://atlasgo.io/community-edition), the **default
distributed `atlas` binary is not**: the standard distribution is governed by the Atlas
MSA/EULA and unlocks "Atlas Pro" after `atlas login`, with *"New users get a free 30-day
trial, after which a license is required to continue using Atlas Pro."* Only the separately
built **Community Edition** binary is Apache-2.0, and it drops `migrate down`, `migrate
checkpoint`, `migrate rebase`, linting, declarative `schema plan`/`validate`, pre-migration
checks, drift detection, the registry, and the testing framework.

Since the only supported in-process route for Atlas is shelling out to that binary (§1),
go-boot would be telling its users to install a commercially licensed CLI, or to build the
crippled community edition from source, in order to run `goboot migrate`. **That alone rules
Atlas out for a library that wants to be freely usable.**

---

## 7. Cooperation with sqlc

The Scaffold generates sqlc by default, so sqlc must be able to read the migration directory
as its schema source. Per the sqlc docs
([Managing schema changes / DDL](https://docs.sqlc.dev/en/latest/howto/ddl.html)), sqlc parses
migrations from: **atlas, dbmate, golang-migrate, goose, sql-migrate, tern**. All three
candidates are supported.

One shared caveat, called out for both goose and golang-migrate in those docs: *"sqlc parses
migration files in lexicographic order"* while the tools order numerically. Zero-pad the
prefixes — `001_initial.sql`, not `1_initial.sql` — or sqlc will read them out of order once
you pass ten migrations.

Practical note for the Scaffold: goose's default `goose create` timestamp format
(`20260824120000_name.sql`) is already lexicographically ordered and needs no padding. That
is a small but real reason to prefer goose's timestamp naming over golang-migrate's sequential
integers. **Unverified:** I did not run sqlc against a goose directory to confirm end to end;
this is read from the sqlc docs only.

sqlc also strips the `-- +goose Up` / `-- +goose Down` annotations itself, which is why goose
is on the supported list rather than needing a plain-SQL directory alongside. Also unverified
by execution.

---

## 8. Is there a newer community default?

Checked the field beyond the three named. Nothing has displaced goose/golang-migrate for
"library-driven versioned SQL migrations in a Go service".

**`jackc/tern` v2.4.3** (MIT, 1,316 stars, released 2026-08-23 — very active; same author as
pgx). The closest thing to a hidden gem here, and it is *better* on locking than goose's
default: it takes `pg_advisory_lock` unconditionally
([`migrate/migrate.go:359`](https://github.com/jackc/tern/blob/master/migrate/migrate.go)),
supports SQL and Go migrations, and is usable as a library. **But it is disqualified by two
map constraints**: its API is `pgx`-native, not `database/sql` —
`migrate.NewMigrator(ctx context.Context, conn *pgx.Conn, versionTable string)`, and
`MigrationFunc = func(context.Context, *pgx.Conn) error` — and it is Postgres-only, closing
the MySQL/SQLite door the map wants left open. Worth revisiting only if go-boot ever decides
to standardise on the pgx native interface instead of `database/sql`.

**`amacneil/dbmate` v2.35.0** (MIT, 7,165 stars, released 2026-08-07). Actively maintained,
sqlc-supported, multi-database. Primarily a CLI; its Go packages exist but the project
positions itself as a language-agnostic binary. No advantage over goose for a Go framework.

**`xataio/pgroll` v0.16.2** (Apache-2.0, 6,558 stars, released 2026-05-12). Solves a different
problem: zero-downtime expand/contract schema changes, keeping old and new schema versions
valid simultaneously behind versioned views. Postgres-only, migrations declared as JSON/YAML
operations rather than SQL. This is a *complement* to a versioned migrator for the specific
case of a change that would lock a large table, not a replacement. Out of scope for v1; worth
a line in the docs as "if you need online DDL, reach for pgroll or pg-osc".

The 2026 landscape sorts into versioned-migration CLIs (goose, golang-migrate, dbmate, Flyway,
Sqitch), declarative schema-as-code (Atlas, Skeema), ORM-integrated migrators, and governance
platforms (Bytebase). go-boot wants the first category, and goose is the Go-native member of
it with the best library API.

---

## Recommendation

**Wire `github.com/pressly/goose/v3` in the db Starter, via `goose.NewProvider`, with
`lock.NewPostgresSessionLocker()` enabled by default.**

Sketch of the Starter's core, all public API:

```go
locker, err := lock.NewPostgresSessionLocker() // pg_try_advisory_lock, 5s × 60 retry
p, err := goose.NewProvider(goose.DialectPostgres, db, migrationsFS,
	goose.WithSessionLocker(locker),
	goose.WithSlog(logger),
)
// goboot migrate  -> p.Up(ctx)
// startup opt-in  -> p.Up(ctx); otherwise p.HasPending(ctx) and refuse to start if true
```

Why goose over golang-migrate — these are close, and golang-migrate is the safer-sounding
choice because its lock is on by default. goose still wins:

1. **The library API is designed for this.** `*sql.DB` in, `fs.FS` in, `context.Context`
   throughout, `*slog.Logger` supported, functional options. golang-migrate has no `ctx`
   anywhere and is frozen, so it never will. go-boot's Component lifecycle is ctx-shaped.
2. **Bounded lock acquisition.** `pg_try_advisory_lock` with a 5-minute retry budget fails
   loudly; `pg_advisory_lock` with `context.Background()` hangs a pod forever with no
   cancellation path. For a *startup* migration behind a Kubernetes readiness probe, failing
   loudly is correct.
3. **No dirty flag.** A failed goose migration does not wedge the database behind a manual
   `migrate force`.
4. **Go migrations** cover the data-backfill case that SQL alone cannot.
5. **`HasPending`** gives the default (migrations-as-separate-command) mode a clean fail-fast
   check with no extra code.
6. **Maintained, and much lighter on the module graph** — 86 modules vs 212, for the same
   linked size.
7. **MIT, no commercial tier, no binary to install.**

The one thing that must not be forgotten: **goose does not lock unless you call
`WithSessionLocker`**. Wire it on by default in the Starter and make it hard to turn off —
that single line is the whole safety story.

Atlas is ruled out on two independent grounds, either of which is sufficient: the public
library has no working revision store so it cannot run versioned migrations in-process at all,
and the only in-process path (`atlasexec`) shells out to a binary whose default distribution
is commercially licensed.

### Follow-ups worth a ticket

- Verify end to end that sqlc reads a goose migration directory with `-- +goose` annotations
  (read from docs, not executed here).
- Decide whether the Starter defaults to failing startup when `HasPending` is true, or only
  logs a warning.
- Note in the docs that `MaxOpenConns == 1` plus a non-transactional Go migration is rejected
  by goose; go-boot's pool defaults should be well above 1 anyway.
- If go-boot ever adds MySQL as a tested default, contribute or vendor a `GET_LOCK`-based
  `lock.SessionLocker`. goose's interface is two methods.
