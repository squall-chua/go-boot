package db_test

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/db"
)

//go:embed testdata/migrations/*.sql
var migrationsFS embed.FS

// migrations is the embed.FS rooted at the SQL files, which is the shape a
// service passes in.
func migrations(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(migrationsFS, "testdata/migrations")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// pgServer is a PostgreSQL this test can stop and start again, which is what
// the readiness test needs and what dbtest.Start deliberately does not offer.
// The recipe — share the binaries, isolate the runtime and data paths, take a
// free port — is the same one dbtest documents.
type pgServer struct {
	pg  *embeddedpostgres.EmbeddedPostgres
	DSN string
	up  bool
}

func startPG(t *testing.T) *pgServer {
	t.Helper()
	dir := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	cfg := embeddedpostgres.DefaultConfig().
		Port(port).
		BinariesPath(binariesPath()).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		Logger(io.Discard)

	s := &pgServer{pg: embeddedpostgres.NewDatabase(cfg), DSN: cfg.GetConnectionURL() + "?sslmode=disable"}
	s.start(t)
	t.Cleanup(func() {
		if s.up {
			_ = s.pg.Stop()
		}
	})
	return s
}

func (s *pgServer) start(t *testing.T) {
	t.Helper()
	if err := s.pg.Start(); err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	s.up = true
}

func (s *pgServer) stop(t *testing.T) {
	t.Helper()
	if err := s.pg.Stop(); err != nil {
		t.Fatalf("stop postgres: %v", err)
	}
	s.up = false
}

// binariesPath mirrors dbtest's: shared, so the 114 MB download happens once.
func binariesPath() string {
	if p := os.Getenv("GOBOOT_PG_BINARIES"); p != "" {
		return p
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	return filepath.Join(cache, "go-boot", "embedded-postgres")
}

func newPool(t *testing.T, cfg db.Config, log *slog.Logger, m fs.FS) (*sql.DB, *db.Component) {
	t.Helper()
	pool, comp, err := db.New(cfg, log, m)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool, comp
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// --- migrations -------------------------------------------------------------

// A pod that comes up against a schema it was not built for fails later, as
// wrong results on one endpoint, and nobody links it back to the deploy. So
// it refuses to start instead (ADR 0007).
func TestStartRefusesWhenMigrationsArePending(t *testing.T) {
	t.Parallel()
	pg := startPG(t)
	_, comp := newPool(t, db.Config{DSN: pg.DSN}, discard(), migrations(t))

	_, err := comp.Start(t.Context())
	if err == nil {
		t.Fatal("Start came up with migrations pending")
	}
	// The message has to name its own fix, and must carry no DSN: a Check
	// error can be put on a public endpoint by actuator.showDetails.
	if !strings.Contains(err.Error(), "myservice migrate") || !strings.Contains(err.Error(), "migrateOnStart") {
		t.Errorf("error does not name its own fix: %v", err)
	}
	if strings.Contains(err.Error(), pg.DSN) {
		t.Errorf("error carries the DSN: %v", err)
	}
}

func TestMigrateOnStartAppliesThemAndLogsFirst(t *testing.T) {
	t.Parallel()
	pg := startPG(t)

	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, nil))
	pool, comp := newPool(t, db.Config{DSN: pg.DSN, MigrateOnStart: true}, log, migrations(t))

	if _, err := comp.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := pool.ExecContext(t.Context(), "SELECT 1 FROM widget"); err != nil {
		t.Fatalf("the migration did not run: %v", err)
	}
	// Logged BEFORE applying, not only after: the run is bounded by
	// lifecycle.startTimeout, and a migration killed part-way leaves nothing
	// else in the log to say what was happening.
	before := strings.Index(logged.String(), "applying migrations")
	after := strings.Index(logged.String(), "migrations applied")
	if before < 0 {
		t.Fatalf("nothing logged before applying:\n%s", logged.String())
	}
	if after < 0 || after < before {
		t.Errorf("the two migration log lines are out of order:\n%s", logged.String())
	}
}

// A service that does not own its schema passes nil, and gets a pool and a
// Check with neither the migration nor the refusal.
func TestNilMigrationsSkipsBoth(t *testing.T) {
	t.Parallel()
	pg := startPG(t)
	pool, comp := newPool(t, db.Config{DSN: pg.DSN}, discard(), nil)

	if _, err := comp.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := comp.Check(t.Context()); err != nil {
		t.Errorf("Check: %v", err)
	}
	// No goose bookkeeping table, so nothing touched the schema at all.
	var n int
	err := pool.QueryRowContext(t.Context(),
		"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'").Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("nil migrations left %d tables behind, want 0", n)
	}
}

// Goose leaves locking OFF unless you ask for it, and forgetting to ask is
// the known bug. NewProvider is the one place it is asked for, so two pods
// rolling out together cannot both apply the same migration — and the
// migration here has no IF NOT EXISTS, so a second apply would error.
func TestTwoProvidersDoNotBothApplyOneMigration(t *testing.T) {
	t.Parallel()
	pg := startPG(t)
	pool, _ := newPool(t, db.Config{DSN: pg.DSN}, discard(), nil)
	m := migrations(t)

	applied := make([]int, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range applied {
		wg.Add(1)
		go func() {
			defer wg.Done()
			provider, err := db.NewProvider(pool, "pgx", m, discard())
			if err != nil {
				errs[i] = err
				return
			}
			results, err := provider.Up(t.Context())
			applied[i], errs[i] = len(results), err
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("provider %d: %v", i, err)
		}
	}
	if applied[0]+applied[1] != 1 {
		t.Errorf("the migration was applied %d times, want 1", applied[0]+applied[1])
	}
}

// --- transactions -----------------------------------------------------------

func TestWithTx(t *testing.T) {
	t.Parallel()
	pg := startPG(t)
	pool, comp := newPool(t, db.Config{DSN: pg.DSN, MigrateOnStart: true}, discard(), migrations(t))
	if _, err := comp.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	insert := func(ctx context.Context, tx *sql.Tx, name string) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO widget (name) VALUES ($1)", name)
		return err
	}
	count := func(name string) int {
		t.Helper()
		var n int
		if err := pool.QueryRowContext(t.Context(),
			"SELECT count(*) FROM widget WHERE name = $1", name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("commits", func(t *testing.T) {
		err := db.WithTx(t.Context(), pool, func(ctx context.Context, tx *sql.Tx) error {
			return insert(ctx, tx, "kept")
		})
		if err != nil {
			t.Fatalf("WithTx: %v", err)
		}
		if got := count("kept"); got != 1 {
			t.Errorf("committed rows = %d, want 1", got)
		}
	})

	t.Run("rolls back on error", func(t *testing.T) {
		want := context.Canceled // any error will do; identity is what matters
		err := db.WithTx(t.Context(), pool, func(ctx context.Context, tx *sql.Tx) error {
			if err := insert(ctx, tx, "dropped"); err != nil {
				return err
			}
			return want
		})
		if err != want {
			t.Errorf("WithTx returned %v, want the error fn returned", err)
		}
		if got := count("dropped"); got != 0 {
			t.Errorf("rolled-back rows = %d, want 0", got)
		}
	})

	t.Run("rolls back on panic, and keeps panicking", func(t *testing.T) {
		// Swallowing the panic would turn a bug into a silent no-op, so the
		// rollback and the re-panic are both part of the contract.
		panicked := func() (p any) {
			defer func() { p = recover() }()
			_ = db.WithTx(t.Context(), pool, func(ctx context.Context, tx *sql.Tx) error {
				_ = insert(ctx, tx, "panicked")
				panic("boom")
			})
			return nil
		}()
		if panicked != "boom" {
			t.Errorf("recovered %v, want the original panic", panicked)
		}
		if got := count("panicked"); got != 0 {
			t.Errorf("rolled-back rows = %d, want 0", got)
		}
	})
}

// --- readiness --------------------------------------------------------------

// The Check is what turns a database outage into a 503 rather than into a
// restart loop: /livez never runs one.
func TestReadinessFollowsTheDatabase(t *testing.T) {
	t.Parallel()
	pg := startPG(t)
	_, comp := newPool(t, db.Config{DSN: pg.DSN}, discard(), nil)

	app, err := goboot.New(goboot.Config{})
	if err != nil {
		t.Fatal(err)
	}
	act, err := actuator.New(actuator.Config{}, app)
	if err != nil {
		t.Fatal(err)
	}
	// A plain ServeMux satisfies actuator.Handler, so this test needs no
	// Transport Starter at all.
	mux := http.NewServeMux()
	act.MountOn(mux)
	app.Add(comp, act)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	readyz := func() int {
		t.Helper()
		resp, err := ts.Client().Get(ts.URL + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if (resp.StatusCode == http.StatusOK) != (body.Status == "UP") {
			t.Errorf("status %d does not match body %q", resp.StatusCode, body.Status)
		}
		return resp.StatusCode
	}

	if got := readyz(); got != http.StatusOK {
		t.Fatalf("/readyz = %d with the database up, want 200", got)
	}
	pg.stop(t)
	if got := readyz(); got != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d with the database stopped, want 503", got)
	}
	pg.start(t)
	// database/sql hands out a pooled connection that is now dead, and only
	// finds out when it uses it. Ping retries, but the first one after a
	// restart can still lose the race, so this waits rather than asserting
	// on the first answer.
	deadline := time.Now().Add(10 * time.Second)
	for readyz() != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("/readyz never came back to 200 after the database restarted")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
