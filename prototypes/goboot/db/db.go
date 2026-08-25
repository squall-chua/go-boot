// Package db is a THROWAWAY stub of the go-boot database Starter as settled in
// #13: pool, migrations (goose, session lock ON), readiness check. It imports
// NO driver — the user blank-imports their own.
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

	"goboot-prototype/goboot"
)

type Config struct {
	DSN             string        `yaml:"dsn"`
	Driver          string        `yaml:"driver"`          // "pgx"
	MaxOpenConns    int           `yaml:"maxOpenConns"`    // 10
	MaxIdleConns    int           `yaml:"maxIdleConns"`    // 10
	ConnMaxIdleTime time.Duration `yaml:"connMaxIdleTime"` // 5m
	ConnMaxLifetime time.Duration `yaml:"connMaxLifetime"` // 30m
	MigrateOnStart  bool          `yaml:"migrateOnStart"`  // local dev only
}

// Component is the lifecycle half. The pool is handed back separately.
type Component struct {
	pool       *sql.DB
	cfg        Config
	log        *slog.Logger
	migrations fs.FS
}

// New returns the plain pool and the Component. migrations may be nil.
// The plain *sql.DB states go-boot's query-layer neutrality in the type.
func New(cfg Config, log *slog.Logger, migrations fs.FS) (*sql.DB, *Component, error) {
	if cfg.Driver == "" {
		cfg.Driver = "pgx"
	}
	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = 10
	}
	pool, err := sql.Open(cfg.Driver, cfg.DSN)
	if err != nil {
		// names its own fix: unknown driver "pgx" (forgotten import?)
		return nil, nil, err
	}
	pool.SetMaxOpenConns(cfg.MaxOpenConns)
	pool.SetMaxIdleConns(cmp(cfg.MaxIdleConns, 10))
	pool.SetConnMaxIdleTime(cmpd(cfg.ConnMaxIdleTime, 5*time.Minute))
	pool.SetConnMaxLifetime(cmpd(cfg.ConnMaxLifetime, 30*time.Minute))
	return pool, &Component{pool: pool, cfg: cfg, log: log, migrations: migrations}, nil
}

// NewProvider is the ONE place WithSessionLocker is wired. Start and the
// user's `myservice migrate` subcommand both call it.
func NewProvider(pool *sql.DB, driver string, migrations fs.FS, log *slog.Logger) (*goose.Provider, error) {
	// goose.Dialect("pgx") is "unknown dialect", so a switch, not a cast.
	var d goose.Dialect
	switch driver {
	case "pgx", "postgres":
		d = goose.DialectPostgres
	default:
		return nil, fmt.Errorf("db: no goose dialect for driver %q", driver)
	}
	// LOCKING IS OPT-IN IN GOOSE. Forgetting this line is the known bug.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(d, pool, migrations,
		goose.WithSessionLocker(locker),
		goose.WithLogger(slogLogger{log}),
	)
}

// WithTx runs fn in a transaction. It does not nest and takes no options.
func WithTx(ctx context.Context, db *sql.DB, fn func(context.Context, *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (c *Component) Name() string      { return "db" }
func (c *Component) Tier() goboot.Tier { return goboot.TierResource }

func (c *Component) Start(ctx context.Context) (<-chan error, error) {
	if err := c.pool.PingContext(ctx); err != nil {
		return nil, err
	}
	if c.migrations == nil {
		return nil, nil
	}
	p, err := NewProvider(c.pool, c.cfg.Driver, c.migrations, c.log)
	if err != nil {
		return nil, err
	}
	if c.cfg.MigrateOnStart {
		res, err := p.Up(ctx)
		if err != nil {
			return nil, err
		}
		c.log.Info("migrations applied", "count", len(res))
		return nil, nil
	}
	pending, err := p.HasPending(ctx)
	if err != nil {
		return nil, err
	}
	if pending {
		// ADR 0007: refuse to start. The Job must run before the rollout.
		return nil, fmt.Errorf("db: migrations pending; run `myservice migrate`")
	}
	return nil, nil // a pool cannot die; nil blocks forever, correct and free
}

// Stop closes the pool. Deliberately no Drain: #8 drains in START order, so
// closing the pool there would kill in-flight requests.
func (c *Component) Stop(ctx context.Context) error { return c.pool.Close() }

func (c *Component) Check(ctx context.Context) error { return c.pool.PingContext(ctx) }

func cmp(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
func cmpd(v, def time.Duration) time.Duration {
	if v == 0 {
		return def
	}
	return v
}

type slogLogger struct{ log *slog.Logger }

func (l slogLogger) Printf(format string, v ...any) { l.log.Info(fmt.Sprintf(format, v...)) }
func (l slogLogger) Fatalf(format string, v ...any) { l.log.Error(fmt.Sprintf(format, v...)) }
