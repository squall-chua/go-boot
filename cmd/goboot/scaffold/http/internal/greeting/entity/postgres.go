package entity

import (
	"context"
	"database/sql"
	"errors"
)

// Repository is the persistence for the Entities in this package, and the
// only type in the feature that writes SQL or names a column.
//
// It satisfies greeting.Repository, the interface the domain declares.
// Nothing here says so out loud: the domain states what it needs, and this
// type happens to fit, which is how Go keeps the dependency pointing inward.
type Repository struct{ db *sql.DB }

// NewRepository returns the concrete type rather than an interface. The
// consumer declares the interface it needs, and that is greeting.Repository.
func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// ByLang maps sql.ErrNoRows onto this package's own ErrNotFound. Doing that
// mapping here is the whole job, and it is what keeps database/sql out of
// every layer above.
func (r *Repository) ByLang(ctx context.Context, lang string) (Greeting, error) {
	var g Greeting
	err := r.db.QueryRowContext(ctx,
		`SELECT id, lang, message, created_at FROM greeting WHERE lang = $1`, lang,
	).Scan(&g.ID, &g.Lang, &g.Message, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Greeting{}, ErrNotFound
	}
	if err != nil {
		return Greeting{}, err
	}
	return g, nil
}
