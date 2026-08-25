#!/usr/bin/env bash
# Prototype of the import-leak CI check owned by #14.
#
# Go links by import. A package that names a heavy dependency makes every
# importer pay for it, so these rules are what keep the optional-subpackage
# convention in CONTEXT.md honest.
set -euo pipefail
M=goboot-prototype
fail=0
say() { printf '%-6s %s\n' "$1" "$2"; }

deps() { go list -deps -f '{{.ImportPath}}' "$1"; }
mods() { go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' "$1" | sort -u | grep -v "^$M\$" | grep . || true; }

# 1. The base package and its TESTS import no Starter (#3's hard rule).
#    -deps alone misses test imports, so ask for them explicitly.
bad=$(go list -f '{{join .Deps "\n"}}{{"\n"}}{{join .TestImports "\n"}}{{"\n"}}{{join .XTestImports "\n"}}' $M/goboot \
      | grep "^$M/goboot/" || true)
[ -z "$bad" ] || { say FAIL "base imports a Starter: $bad"; fail=1; }
[ -n "$bad" ] || say ok "1. base and its tests import no Starter"

# 2. No short-path package reaches a heavy optional package.
#    In the real repo the list is: goboot/trace, goboot/grpc/health,
#    goboot/grpc/reflection, goboot/trace/rpc, goboot/db/dbtest.
leaked=0
for p in goboot goboot/web goboot/db goboot/actuator goboot/grpc goboot/preset; do
  for heavy in goboot/trace; do
    if deps "$M/$p" | grep -qx "$M/$heavy"; then
      say FAIL "$p reaches $heavy"; leaked=1; fail=1
    fi
  done
done
[ "$leaked" = 1 ] || say ok "2. no short-path package reaches goboot/trace"

# 3. goboot/db links NO driver. #13 measured pgx at +7.64 MB; a MySQL user
#    would have paid all of it for nothing.
drv=$(deps $M/goboot/db | grep -E 'jackc|go-sql-driver|lib/pq|mattn/go-sqlite3' || true)
[ -z "$drv" ] || { say FAIL "goboot/db links a driver: $drv"; fail=1; }
[ -z "$drv" ] && say ok "3. goboot/db links no driver"

# 4. Pinned module count per package. This is the one that catches the NEXT
#    leak, the one nobody predicted. Regenerate deliberately, never silently.
golden=scripts/module-counts.txt
: > "$golden.new"
for p in goboot goboot/web goboot/actuator goboot/db goboot/grpc goboot/trace goboot/preset goboot/preset/traced; do
  printf '%s %s\n' "$p" "$(mods $M/$p | wc -l)" >> "$golden.new"
done
if [ -f "$golden" ] && ! diff -u "$golden" "$golden.new"; then
  say FAIL "4. module counts moved; review, then commit the new $golden"; fail=1
else
  mv "$golden.new" "$golden"; say ok "4. module counts pinned"
fi
rm -f "$golden.new"
exit $fail
