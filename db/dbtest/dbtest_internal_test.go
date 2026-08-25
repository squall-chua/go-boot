package dbtest

import (
	"strings"
	"testing"
)

// The three findings, each from a table that breaks one rule. Tested against
// the unexported lint, because the exported one reports through testing.TB
// and a test cannot assert that a *testing.T was failed on purpose.
func TestLintJPAConventionsReportsEachViolation(t *testing.T) {
	t.Parallel()
	pool := Start(t, nil)

	for _, ddl := range []string{
		// A quoted camelCase identifier is what Hibernate's naming strategy
		// never produces, so a Go-side one breaks the shared schema.
		`CREATE TABLE "camelCase" (id bigint, version bigint)`,
		// LocalDateTime maps to this, and the UTC offset is lost.
		`CREATE TABLE no_zone (id bigint, version bigint, created_at timestamp)`,
		// No version column, so a Hibernate write commits over a Go write
		// and neither side raises anything.
		`CREATE TABLE unversioned (id bigint)`,
	} {
		if _, err := pool.ExecContext(t.Context(), ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	findings, err := lintJPAConventions(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(findings, "\n")
	for _, want := range []string{"camelCase", "no_zone.created_at", "unversioned"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lint missed %s:\n%s", want, joined)
		}
	}
	if len(findings) != 3 {
		t.Errorf("got %d findings, want one per rule:\n%s", len(findings), joined)
	}
}
