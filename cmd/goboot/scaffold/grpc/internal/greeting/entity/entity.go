// Package entity holds one feature's Entity and the persistence that loads
// it. The two live together because they change together: a column added to
// the table is a field added to the struct and a name added to the SELECT.
//
// It imports nothing of this project. Everything else points at it.
//
// go-boot itself ships no Entity or Repository type — it hands back a plain
// *sql.DB and stops, which is ADR 0009. So this package is YOURS: put sqlc,
// ent or gorm in here instead and nothing outside this directory changes.
// They all take a plain *sql.DB.
package entity

import (
	"errors"
	"time"
)

// Greeting is the Entity: one row of the greeting table as a Go type.
//
// If a Spring Data JPA service shares this database and its entity carries
// @Version, add the matching field HERE and make every UPDATE write
// `version = version + 1`. Without the bump Hibernate commits over your write
// and neither side raises an error — see docs/jpa-interop.md in go-boot.
type Greeting struct {
	ID        int64
	Lang      string
	Message   string
	CreatedAt time.Time
}

// ErrNotFound is what a query returns when the row is not there.
//
// It is declared beside the Entity rather than inside postgres.go, so that
// swapping the query layer cannot change what "not found" means. The Service
// Layer tests for it without ever importing database/sql.
var ErrNotFound = errors.New("greeting: not found")
