// Package dbtest brings up a real PostgreSQL for a test, with no Docker
// daemon. It is a separate package because it is heavy — the binaries are
// about 71 MB on disk and the first run needs network — and because it links a
// driver, which goboot/db deliberately does not.
//
// Embedded PostgreSQL rather than testcontainers-go: 3 linked module roots
// against 45, 16 go.sum modules against 128, and nothing to install first.
//
// The library's own defaults are parallel-unsafe in two measured ways: two
// instances collide on initdb, and isolating only the data directory still
// fails on the password file. The recipe that works is the one Start
// implements — share the binaries, isolate the runtime and data paths, and
// take a free port from the kernel.
//
// A first run downloads the binaries. Set GOBOOT_PG_BINARIES to a
// pre-seeded directory to run air-gapped; everything else is under the test's
// own temporary directory and goes away with it.
package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib" // the driver goboot/db refuses to link

	"github.com/squall-chua/go-boot/db"
)

// Start brings up a real PostgreSQL for one test and returns a pool on it.
// The server and the pool are torn down by t.Cleanup, so a caller closes
// nothing.
//
// migrations may be nil, in which case the schema is left empty. When it is
// not nil the migrations are applied through db.NewProvider, so the test runs
// against the same session lock and the same dialect switch the service does.
//
// It is safe to call from parallel tests: each call gets its own port,
// runtime directory and data directory.
func Start(tb testing.TB, migrations fs.FS) *sql.DB {
	tb.Helper()

	dir := tb.TempDir()
	cfg := embeddedpostgres.DefaultConfig().
		Port(freePort(tb)).
		// Shared, because this is the 71 MB the first run downloads. It is
		// the one path that must NOT be per-test.
		BinariesPath(binariesPath(tb)).
		// Isolated. Sharing the runtime path fails on the password file even
		// when the data path is already per-test, which is the trap.
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		// The default writes initdb chatter to stdout, which buries the
		// output of the test that failed.
		Logger(io.Discard)

	pg := embeddedpostgres.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		tb.Fatalf("dbtest: start postgres: %v", err)
	}
	tb.Cleanup(func() {
		if err := pg.Stop(); err != nil {
			tb.Errorf("dbtest: stop postgres: %v", err)
		}
	})

	// Registered after the server, so it runs before it: Cleanup is
	// last-in-first-out, and stopping a server with the pool still open
	// leaves the pool handing out dead connections.
	pool, err := sql.Open("pgx", cfg.GetConnectionURL()+"?sslmode=disable")
	if err != nil {
		tb.Fatalf("dbtest: open pool: %v", err)
	}
	tb.Cleanup(func() { _ = pool.Close() })

	if migrations == nil {
		return pool
	}
	provider, err := db.NewProvider(pool, "pgx", migrations, slog.New(slog.DiscardHandler))
	if err != nil {
		tb.Fatalf("dbtest: provider: %v", err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		tb.Fatalf("dbtest: apply migrations: %v", err)
	}
	return pool
}

// freePort asks the kernel for a port and gives it straight back. Two
// instances asking at the same moment get different answers, which is what
// makes parallel tests work.
func freePort(tb testing.TB) uint32 {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("dbtest: find a free port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return uint32(ln.Addr().(*net.TCPAddr).Port)
}

// binariesPath is shared by every instance on the machine, so the download
// happens once. GOBOOT_PG_BINARIES points it at a directory an air-gapped CI
// has already seeded.
func binariesPath(tb testing.TB) string {
	tb.Helper()
	if p := os.Getenv("GOBOOT_PG_BINARIES"); p != "" {
		return p
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	return filepath.Join(cache, "go-boot", "embedded-postgres")
}

// LintJPAConventions checks the live schema against docs/jpa-interop.md, the
// conventions that let a Spring Data JPA service share this database. It
// reports three things: an identifier that is not lower_snake_case, a
// timestamp column with no time zone, and a table with no version column.
//
// It checks a CONVENTION, not a JPA model, and it cannot check one: go-boot
// never sees your Java classes. Only Hibernate's own ddl-auto=validate can
// confirm that a schema matches particular entities, and that misses
// everything in this list. Run both.
//
// Known false positive: a @ManyToMany join table has no @Version, because
// Hibernate never puts one there.
func LintJPAConventions(tb testing.TB, pool *sql.DB) {
	tb.Helper()
	findings, err := lintJPAConventions(context.Background(), pool, skippedTables)
	if err != nil {
		tb.Fatalf("dbtest: lint: %v", err)
	}
	for _, f := range findings {
		tb.Errorf("dbtest: %s", f)
	}
}

// skippedTables are the migration tools' own bookkeeping tables. Skipping
// them is load-bearing rather than tidy: goose_db_version has a `tstamp
// timestamp` column and no `version` column, so without the skip this lint
// fails every schema go-boot itself creates. A test proves that.
var skippedTables = []string{
	"goose_db_version",                           // goose
	"flyway_schema_history",                      // Flyway
	"databasechangelog", "databasechangeloglock", // Liquibase
}

// lintJPAConventions is the lint itself, split out from the reporting so it
// can be tested against a schema that is deliberately wrong, and so a test
// can run it with nothing skipped.
func lintJPAConventions(ctx context.Context, pool *sql.DB, skipped []string) ([]string, error) {
	if skipped == nil {
		// `<> ALL(NULL)` is NULL, which would drop every row. An empty array
		// is the "skip nothing" the caller meant.
		skipped = []string{}
	}

	checks := []struct {
		what  string
		query string
	}{
		{"identifier is not lower_snake_case", `
			SELECT table_name || '.' || column_name
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name <> ALL($1)
			  AND (table_name !~ '^[a-z_][a-z0-9_]*$' OR column_name !~ '^[a-z_][a-z0-9_]*$')
			ORDER BY 1`},
		{"timestamp column has no time zone", `
			SELECT table_name || '.' || column_name
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name <> ALL($1)
			  AND data_type = 'timestamp without time zone'
			ORDER BY 1`},
		{"table has no version column, so JPA optimistic locking cannot work", `
			SELECT t.table_name
			FROM information_schema.tables t
			WHERE t.table_schema = 'public' AND t.table_type = 'BASE TABLE'
			  AND t.table_name <> ALL($1)
			  AND NOT EXISTS (
				SELECT 1 FROM information_schema.columns c
				WHERE c.table_schema = t.table_schema AND c.table_name = t.table_name
				  AND c.column_name = 'version')
			ORDER BY 1`},
	}

	var findings []string
	for _, check := range checks {
		found, err := queryStrings(ctx, pool, check.query, skipped)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", check.what, err)
		}
		if len(found) > 0 {
			findings = append(findings, check.what+": "+strings.Join(found, ", "))
		}
	}
	return findings, nil
}

func queryStrings(ctx context.Context, pool *sql.DB, query string, skipped []string) ([]string, error) {
	rows, err := pool.QueryContext(ctx, query, skipped)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
