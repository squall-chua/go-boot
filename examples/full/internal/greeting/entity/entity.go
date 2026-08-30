// Package entity holds this feature's Entity and the persistence that loads it.
// The two live together because they change together: a column added to the
// table is a field added to the struct and a name added to the SELECT.
//
// It imports nothing of this service. Everything else points at it.
//
// go-boot itself ships no Entity or Repository type — it hands back a plain
// *sql.DB and stops, which is ADR 0009. So this package is the APPLICATION's:
// put sqlc, ent or gorm in here instead and nothing outside this directory
// changes. They all take a plain *sql.DB. See ADR 0015.
package entity

import "errors"

// Greeting is the Entity: one row of the greetings table as a Go type.
type Greeting struct {
	Lang    string
	Message string
}

// ErrNotFound is what a query returns when the row is not there.
//
// It is declared beside the Entity rather than inside postgres.go, so that
// swapping the query layer cannot change what "not found" means. The Service
// Layer tests for it without ever importing database/sql.
var ErrNotFound = errors.New("greeting: not found")
