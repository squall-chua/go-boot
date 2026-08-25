#!/usr/bin/env bash
# ADR 0009 says go-boot hands back a plain *sql.DB so any query layer takes
# it. That claim is checked by COMPILING, not by reading.
#
# ent and gorm are not go-boot dependencies and must not become ones:
# docs/spec.md 7 fixes the dependency list. So this builds them in a throwaway
# module in a temporary directory, which leaves go.mod alone. sqlc needs
# nothing here — its DBTX is generated into the user's own repository, and
# db/db_test.go asserts against it directly.
set -euo pipefail

dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT

cat > "$dir/main.go" <<'GO'
package main

import (
	"database/sql"

	entsql "entgo.io/ent/dialect/sql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// pool is exactly what db.New returns first: a plain *sql.DB, with no
// go-boot type wrapped round it.
var pool *sql.DB

func main() {
	_ = entsql.OpenDB("postgres", pool)
	if _, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{}); err != nil {
		panic(err)
	}
}
GO

cd "$dir"
go mod init goboot-neutrality-check > /dev/null 2>&1
# tidy, not get: it resolves the transitive go.sum entries ent needs.
go mod tidy > /dev/null 2>&1
go build ./...
echo "a plain *sql.DB compiles against ent and gorm"
