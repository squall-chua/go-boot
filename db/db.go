// Package db is the database Starter: a pool with defaults a service can
// live with, goose migrations behind a session lock, a transaction helper and
// a readiness Check.
//
// It imports no database driver. A driver is the second-heaviest dependency
// in the project — pgx/v5/stdlib is +7.64 MB stripped — and a MySQL user
// would have paid all of it and still had no driver. The user blank-imports
// their own:
//
//	import _ "github.com/jackc/pgx/v5/stdlib"
//
// Forgetting that line is not confusing: sql.Open reports
// `sql: unknown driver "pgx" (forgotten import?)`, which names its own fix,
// and New runs in main before app.Run.
//
// # The deploy-ordering contract
//
// By default a service REFUSES TO START when migrations are pending (ADR
// 0007). If migrations run as a separate Kubernetes Job, that Job must finish
// before the rollout starts. If it does not, every new pod crashloops until
// it does. The Job runs the same image as the pods, which is what stops code
// and schema drifting.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/squall-chua/go-boot"
)

// Config is the db section. Go's own pool defaults are wrong for a service:
// unlimited open connections, two idle, and connections that live forever.
// Every field here has a working default, written beside it.
type Config struct {
	// DSN is the connection string. Keep it in an environment variable: it
	// carries the password, and goboot.Load reads the environment last.
	DSN string `yaml:"dsn"`
	// Driver is the name the user blank-imported. Default "pgx". It is also
	// what the goose dialect is derived from.
	Driver string `yaml:"driver"` // "pgx"
	// MaxOpenConns caps connections per pod. A stock PostgreSQL allows about
	// 97, so the default of 10 is TEN PODS before the database runs out.
	MaxOpenConns int `yaml:"maxOpenConns"` // 10
	// MaxIdleConns matches MaxOpenConns on purpose. Go's default of 2 means
	// 8 of 10 connections are closed and reopened on every burst.
	MaxIdleConns int `yaml:"maxIdleConns"` // 10
	// ConnMaxIdleTime gives slots back after a scale-down.
	ConnMaxIdleTime time.Duration `yaml:"connMaxIdleTime"` // 5m
	// ConnMaxLifetime lets connections rebalance after a failover or a proxy
	// restart. Go's default is forever.
	ConnMaxLifetime time.Duration `yaml:"connMaxLifetime"` // 30m
	// MigrateOnStart applies pending migrations during Start instead of
	// refusing to start. It is for LOCAL DEVELOPMENT ONLY: every pod races
	// every other pod at rollout, and the whole run is bounded by
	// lifecycle.startTimeout, which is 30s.
	MigrateOnStart bool `yaml:"migrateOnStart"` // false
}

// Component is the lifecycle half. The pool is handed back separately, so the
// application never reaches through the Component to query.
type Component struct {
	pool       *sql.DB
	cfg        Config
	log        *slog.Logger
	migrations fs.FS
}

// New opens the pool and returns it FIRST, as a plain *sql.DB. That states
// go-boot's query-layer neutrality in the type rather than in prose: sqlc's
// generated DBTX, entgo.io/ent and gorm.io/gorm all take one unchanged, and
// go-boot defines no interface of its own for them to convert to (ADR 0009).
//
// migrations may be nil, which is a supported mode for a service that does
// not own its schema. It skips both the migration run and the
// pending-migration refusal, leaving a pool and a Check.
func New(cfg Config, log *slog.Logger, migrations fs.FS) (*sql.DB, *Component, error) {
	if cfg.Driver == "" {
		cfg.Driver = "pgx"
	}
	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = 10
	}
	if cfg.MaxIdleConns == 0 {
		cfg.MaxIdleConns = 10
	}
	if cfg.ConnMaxIdleTime == 0 {
		cfg.ConnMaxIdleTime = 5 * time.Minute
	}
	if cfg.ConnMaxLifetime == 0 {
		cfg.ConnMaxLifetime = 30 * time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	// Returned unwrapped: on a forgotten blank import this is
	// `sql: unknown driver "pgx" (forgotten import?)`, which already names
	// its own fix, and a prefix would only push that text further right.
	pool, err := sql.Open(cfg.Driver, cfg.DSN)
	if err != nil {
		return nil, nil, err
	}
	pool.SetMaxOpenConns(cfg.MaxOpenConns)
	pool.SetMaxIdleConns(cfg.MaxIdleConns)
	pool.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	pool.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	return pool, &Component{pool: pool, cfg: cfg, log: log, migrations: migrations}, nil
}

// NewProvider is the ONE place the session lock is wired. Start calls it, and
// so does the `myservice migrate` subcommand the Scaffold writes, so the two
// cannot disagree about locking. Goose leaves locking off unless you ask, and
// forgetting to ask is the known bug.
//
// There is no goboot migrate command and there could not have been:
// migrations live in the user's own embed.FS, which a generic go-boot binary
// can never see.
//
// driver is the same string as Config.Driver. The goose dialect is derived
// from it here by a switch, not by a cast, because goose.Dialect("pgx") is an
// unknown dialect — and taking a goose.Dialect instead would make the user's
// migrate subcommand know the mapping, which is the thing this saves it from.
func NewProvider(pool *sql.DB, driver string, migrations fs.FS, log *slog.Logger) (*goose.Provider, error) {
	var dialect goose.Dialect
	switch driver {
	case "pgx", "postgres":
		dialect = goose.DialectPostgres
	case "mysql":
		dialect = goose.DialectMySQL
	case "sqlite", "sqlite3":
		dialect = goose.DialectSQLite3
	default:
		return nil, fmt.Errorf("db: no goose dialect for driver %q; supported drivers are pgx, postgres, mysql, sqlite, sqlite3", driver)
	}
	if log == nil {
		log = slog.Default()
	}
	opts := []goose.ProviderOption{goose.WithSlog(log)}
	// goose ships a session locker for PostgreSQL only. On the other two
	// dialects there is nothing to wire, so two pods applying the same
	// migration are not protected — run the migration as a Job, which is the
	// documented way anyway.
	if dialect == goose.DialectPostgres {
		locker, err := lock.NewPostgresSessionLocker()
		if err != nil {
			return nil, err
		}
		opts = append(opts, goose.WithSessionLocker(locker))
	}
	return goose.NewProvider(dialect, pool, migrations, opts...)
}

// WithTx runs fn in a transaction: it commits, rolls back if fn returns an
// error, and rolls back and re-panics if fn panics.
//
// The transaction is a PARAMETER, not a value hidden in the context, so a
// reader can see it (ADR 0008). It takes no options and does not nest —
// *sql.Tx has no Begin, so database/sql cannot nest transactions at all.
// A method that must join a caller's transaction takes a value that both
// *sql.DB and *sql.Tx satisfy, which is the interface sqlc already generates.
func WithTx(ctx context.Context, db *sql.DB, fn func(context.Context, *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// A panic must roll back and then carry on panicking: swallowing it here
	// would turn a bug into a silent no-op.
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Name is the Component name, also the key the Actuator files its Check under.
func (c *Component) Name() string { return "db" }

// Tier starts the pool after the Actuator and before any Transport.
func (c *Component) Tier() goboot.Tier { return goboot.TierResource }

// Start pings the database, then either applies migrations or refuses to
// start because some are pending. It returns a nil channel: a pool does not
// die on its own, and a nil channel costs no goroutine.
func (c *Component) Start(ctx context.Context) (<-chan error, error) {
	if err := c.pool.PingContext(ctx); err != nil {
		return nil, err
	}
	if c.migrations == nil {
		return nil, nil
	}
	provider, err := NewProvider(c.pool, c.cfg.Driver, c.migrations, c.log)
	if err != nil {
		return nil, err
	}
	if c.cfg.MigrateOnStart {
		// Logged BEFORE applying, not only after. The whole run is inside
		// lifecycle.startTimeout, so a slow migration is killed part-way,
		// and without this line there is nothing in the log to say why.
		c.log.Info("applying migrations", "component", c.Name())
		applied, err := provider.Up(ctx)
		if err != nil {
			return nil, err
		}
		c.log.Info("migrations applied", "component", c.Name(), "count", len(applied))
		return nil, nil
	}
	pending, err := provider.HasPending(ctx)
	if err != nil {
		return nil, err
	}
	if pending {
		// No DSN in this text: actuator.showDetails can put a Check error on
		// a public endpoint, and the same restraint belongs here.
		return nil, fmt.Errorf("db: migrations are pending; run `myservice migrate` before the rollout, or set db.migrateOnStart for local development")
	}
	return nil, nil
}

// Stop closes the pool. There is deliberately no Drain: Drain runs in START
// order, so a draining pool would stop taking work before the Transports had
// finished with it. Stop runs in reverse, so the pool closes last anyway.
func (c *Component) Stop(ctx context.Context) error { return c.pool.Close() }

// Check is the readiness test. It respects ctx, because it runs inside the
// probe's own deadline.
func (c *Component) Check(ctx context.Context) error { return c.pool.PingContext(ctx) }
