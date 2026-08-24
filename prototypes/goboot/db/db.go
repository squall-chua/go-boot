// Package db is a THROWAWAY stub of the go-boot database Starter: pool,
// migrations (goose, session lock ON), readiness check.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

type Config struct {
	DSN            string `yaml:"dsn"`
	MaxOpenConns   int    `yaml:"maxopenconns"`
	MigrateOnStart bool   `yaml:"migrateonstart"` // opt-in, local dev only
}

type DB struct {
	*sql.DB

	cfg        Config
	log        *slog.Logger
	migrations fs.FS
}

func New(cfg Config, log *slog.Logger, migrations fs.FS) (*DB, error) {
	if cfg.MaxOpenConns == 0 {
		cfg.MaxOpenConns = 10 // goose rejects 1 + non-transactional Go migrations
	}
	pool, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(cfg.MaxOpenConns)
	return &DB{DB: pool, cfg: cfg, log: log, migrations: migrations}, nil
}

func (d *DB) Name() string { return "db" }

func (d *DB) Start(ctx context.Context) error {
	if err := d.PingContext(ctx); err != nil {
		return err
	}
	if d.migrations == nil {
		return nil
	}
	// LOCKING IS OPT-IN IN GOOSE. Forgetting this line is the known bug.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, d.DB, d.migrations,
		goose.WithSessionLocker(locker),
		goose.WithLogger(slogLogger{d.log}),
	)
	if err != nil {
		return err
	}
	if d.cfg.MigrateOnStart {
		res, err := p.Up(ctx)
		if err != nil {
			return err
		}
		d.log.Info("migrations applied", "count", len(res))
		return nil
	}
	pending, err := p.HasPending(ctx)
	if err != nil {
		return err
	}
	if pending {
		return fmt.Errorf("db: migrations pending; run `goboot migrate` or set db.migrateonstart")
	}
	return nil
}

func (d *DB) Stop(ctx context.Context) error { return d.DB.Close() }

// Check is the readiness probe to hand the Actuator.
func (d *DB) Check(ctx context.Context) error { return d.PingContext(ctx) }

type slogLogger struct{ log *slog.Logger }

func (l slogLogger) Printf(format string, v ...any) { l.log.Info(fmt.Sprintf(format, v...)) }
func (l slogLogger) Fatalf(format string, v ...any) { l.log.Error(fmt.Sprintf(format, v...)) }
