package db_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/db"
)

// stubDriver stands in for a real driver so New can open a pool without a
// database behind it. sql.Open does not connect, so Open is never called.
type stubDriver struct{}

func (stubDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("the stub driver never connects")
}

func init() { sql.Register("goboot-db-stub", stubDriver{}) }

// oneMigration is enough for NewProvider to have something to scan. What it
// contains does not matter: nothing here runs it.
var oneMigration = fstest.MapFS{
	"00001_stub.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n")},
}

func TestNewAppliesPoolDefaults(t *testing.T) {
	pool, comp, err := db.New(db.Config{Driver: "goboot-db-stub"}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if comp == nil {
		t.Fatal("New returned no Component")
	}
	// Stats exposes MaxOpenConnections and nothing else, so the other three
	// are read off the struct. They are what the table in docs/spec.md
	// promises, and a silent drift back to Go's own defaults — unlimited
	// open, 2 idle, connections that live forever — is the failure this
	// catches.
	if got := pool.Stats().MaxOpenConnections; got != 10 {
		t.Errorf("maxOpenConns = %d, want 10", got)
	}
	if got := poolField(t, pool, "maxIdleCount").Int(); got != 10 {
		t.Errorf("maxIdleConns = %d, want 10", got)
	}
	if got := time.Duration(poolField(t, pool, "maxIdleTime").Int()); got != 5*time.Minute {
		t.Errorf("connMaxIdleTime = %s, want 5m", got)
	}
	if got := time.Duration(poolField(t, pool, "maxLifetime").Int()); got != 30*time.Minute {
		t.Errorf("connMaxLifetime = %s, want 30m", got)
	}
}

func TestNewKeepsConfiguredPoolSettings(t *testing.T) {
	cfg := db.Config{
		Driver:          "goboot-db-stub",
		MaxOpenConns:    3,
		MaxIdleConns:    2,
		ConnMaxIdleTime: time.Minute,
		ConnMaxLifetime: 2 * time.Minute,
	}
	pool, _, err := db.New(cfg, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := pool.Stats().MaxOpenConnections; got != 3 {
		t.Errorf("maxOpenConns = %d, want 3", got)
	}
	if got := poolField(t, pool, "maxIdleCount").Int(); got != 2 {
		t.Errorf("maxIdleConns = %d, want 2", got)
	}
}

// TestNewNamesTheFixForAForgottenDriverImport is the whole reason goboot/db
// links no driver: the failure it leaves behind has to be self-explaining,
// and it has to happen in main, before app.Run.
func TestNewNamesTheFixForAForgottenDriverImport(t *testing.T) {
	// Not "pgx": db_pg_test.go blank-imports that one, so inside this test
	// binary it is linked. Any unlinked name produces the same message.
	_, _, err := db.New(db.Config{Driver: "mysql"}, nil, nil)
	if err == nil {
		t.Fatal("New with no driver linked returned no error")
	}
	if !strings.Contains(err.Error(), "forgotten import?") {
		t.Errorf("error does not name its own fix: %v", err)
	}
}

func TestNewProviderDerivesTheDialectFromTheDriver(t *testing.T) {
	pool, _, err := db.New(db.Config{Driver: "goboot-db-stub"}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, driver := range []string{"pgx", "postgres", "mysql", "sqlite", "sqlite3"} {
		if _, err := db.NewProvider(pool, driver, oneMigration, nil); err != nil {
			t.Errorf("NewProvider(%q): %v", driver, err)
		}
	}
	_, err = db.NewProvider(pool, "oracle", oneMigration, nil)
	if err == nil {
		t.Fatal("NewProvider with an unsupported driver returned no error")
	}
	// The error has to say what IS supported, or the reader is left guessing
	// at the spelling.
	for _, want := range []string{"oracle", "pgx", "postgres", "mysql", "sqlite3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestStarterLinksNoDriver is assertion 3 of the import-leak check in
// docs/spec.md 8.1. A driver is +7.64 MB stripped, and a MySQL user would
// have paid all of it for a driver they cannot use.
func TestStarterLinksNoDriver(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, driver := range []string{"jackc", "go-sql-driver", "lib/pq", "mattn/go-sqlite3"} {
		for _, dep := range strings.Split(string(out), "\n") {
			if strings.Contains(dep, driver) {
				t.Errorf("goboot/db links a driver: %s", dep)
			}
		}
	}
}

// TestPoolIsNeutralAboutTheQueryLayer states ADR 0009 as a compile-time
// check. DBTX is sqlc's generated interface, copied verbatim — sqlc writes it
// into the user's own repository, so there is nothing to import.
//
// ent and gorm take a plain *sql.DB the same way, and that is checked by
// compiling too — in .github/check-query-layer-neutrality.sh, which builds
// them in a throwaway module. They are not linked here because docs/spec.md 7
// fixes go-boot's dependency list and neither is on it.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

var _ DBTX = (*sql.DB)(nil)

// The pool is the FIRST return value, and it is a plain *sql.DB. Written as a
// compiling assignment, because that is the claim.
func TestPoolIsNeutralAboutTheQueryLayer(t *testing.T) {
	pool, _, err := db.New(db.Config{Driver: "goboot-db-stub"}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var queries DBTX = pool
	if queries == nil {
		t.Fatal("pool does not satisfy a generated query layer's interface")
	}
}

// The pool must NOT be a Drainer. Drain runs in START order, so a draining
// pool would stop taking work before the Transports had finished with it.
// Stop runs in reverse, so the pool closes last anyway. True by construction,
// asserted because the opposite looks obviously right until you notice the
// direction.
func TestComponentHasNoDrain(t *testing.T) {
	_, comp, err := db.New(db.Config{Driver: "goboot-db-stub"}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := any(comp).(goboot.Drainer); ok {
		t.Error("the db Component has a Drain, so the pool would stop taking work too early")
	}
}

func poolField(t *testing.T, pool *sql.DB, name string) reflect.Value {
	t.Helper()
	v := reflect.ValueOf(pool).Elem().FieldByName(name)
	if !v.IsValid() {
		t.Fatalf("database/sql no longer has a %q field; this test needs rewriting", name)
	}
	return v
}
