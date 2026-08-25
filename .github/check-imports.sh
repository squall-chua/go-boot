#!/usr/bin/env bash
# The import-leak check: docs/spec.md 8.1, four assertions settled in #14,
# built for real by #32.
#
# Go links by import. A package that names a heavy dependency makes every
# importer pay for it, so these four rules are what keep the
# optional-subpackage convention in CONTEXT.md honest. Reading the imports by
# eye cannot keep them true; only asking the toolchain on every push can.
#
# Two Go tests overlap with this script and stay where they are, because each
# is stronger than the script on its own ground. Named here so the pair does
# not drift apart:
#
#   - db/db_test.go TestStarterLinksNoDriver is assertion 3, in the package
#     it guards, so it fails under `go test ./db` with no CI in the loop.
#   - trace/trace_test.go TestABuildWithNoTracingLinksNoTracing is the
#     tracing third of assertion 2, and goes further than this script does:
#     it checks the examples too, and asserts goboot/trace does NOT link
#     otelconnect, which is a rule about a heavy package rather than about a
#     short path.
#
# Run with --update to rewrite the golden module counts of assertion 4.
set -euo pipefail
cd "$(dirname "$0")/.."

M=github.com/squall-chua/go-boot
golden=.github/module-counts.txt
update=0
[ "${1:-}" = --update ] && update=1

fail=0
say() { printf '%-6s %s\n' "$1" "$2"; }

# report <what> <lines>: ok when there is nothing to report, else FAIL with
# the offending lines indented under it.
report() {
	if [ -n "$2" ]; then
		say FAIL "$1"
		echo "$2" | sed 's/^/       /'
		fail=1
	else
		say ok "$1"
	fi
}

deps() { go list -deps -f '{{.ImportPath}}' "$1"; }

# Linked non-stdlib module roots, not counting go-boot itself. Stdlib
# packages carry no .Module, so the template drops them. go list runs on a
# line of its own: inside a pipeline, `pipefail` plus the `|| true` that grep
# needs for the no-match case would let a toolchain failure read as a package
# with no dependencies at all.
mods() {
	local all
	all=$(go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' "$1")
	printf '%s\n' "$all" | sort -u | grep -vxF "$M" || true
}

# 1. The base package and its TESTS import no Starter. This is #3's hard
#    rule, and it is about `go mod tidy`, not about the build: tidy leaks
#    through test imports, and `go list -deps` does not report those, so ask
#    for them by name. Any go-boot subpackage counts, not only a Starter —
#    base reaching internal/gen would drag protobuf in just the same.
bad=$(go list -f '{{join .Deps "\n"}}{{"\n"}}{{join .TestImports "\n"}}{{"\n"}}{{join .XTestImports "\n"}}' "$M" \
      | grep "^$M/" | sort -u || true)
report "1. the base package and its tests import no go-boot subpackage" "$bad"

# 2. No short-path package reaches a heavy optional package. The rule as
#    first written listed only `goboot`, and missed goboot/preset, whose
#    Preset dragged the Actuator into an HTTP-only binary.
heavy="$M/trace $M/grpc/health $M/grpc/reflection $M/trace/rpc $M/db/dbtest"
leaked=0
reaches() { # <package> <packages it must not reach>...
	local p=$1 d h
	shift
	d=$(deps "$p")
	for h in "$@"; do
		if grep -qxF "$h" <<<"$d"; then
			say FAIL "2. $p reaches $h"
			leaked=1
			fail=1
		fi
	done
}
for p in "$M" "$M/web" "$M/db" "$M/actuator" "$M/grpc" "$M/preset"; do
	reaches "$p" $heavy
done
# goboot/preset/traced is the tracing twin. It is the one package allowed to
# reach goboot/trace — and ONLY that one, or the twin quietly becomes the
# place every heavy dependency goes to hide. Both lists are left unquoted on
# purpose: $heavy is a fixed list of import paths, and the word splitting is
# what turns it into arguments.
reaches "$M/preset/traced" $(printf '%s\n' $heavy | grep -vxF "$M/trace")
[ "$leaked" = 1 ] || say ok "2. no short-path package reaches a heavy optional package"

# 3. goboot/db links NO driver. #13 measured pgx/v5/stdlib at +7.64 MB; a
#    MySQL user would have paid all of it for nothing. The user blank-imports
#    their own driver in main.
drv=$(deps "$M/db" | grep -E 'jackc|go-sql-driver|lib/pq|mattn/go-sqlite3' | sort -u || true)
report "3. goboot/db links no driver" "$drv"

# 4. A pinned module count per package. This is the one that catches the NEXT
#    leak, the one nobody predicted, and the one no hand-written rule above
#    names. The package list comes from `go list`, so a new package is a
#    golden-file change too.
#
#    Two directories are left out. `examples` holds binaries, and their weight
#    is measured in docs/spec.md 6 rather than pinned here. `internal` is
#    generated protobuf code that no user can import, so its module count is a
#    fact about buf's output, not about what go-boot links — but base reaching
#    it is still caught, by assertion 1 above.
counts=$(mktemp)
trap 'rm -f "$counts"' EXIT
pkgs=$(go list ./...)
for p in $(printf '%s\n' "$pkgs" | grep -vE "^$M/(examples|internal)/"); do
	printf '%s %s\n' "goboot${p#"$M"}" "$(mods "$p" | wc -l)"
done > "$counts"

if [ "$update" = 1 ]; then
	cp "$counts" "$golden"
	say ok "4. module counts written to $golden"
elif ! diff -u "$golden" "$counts"; then
	say FAIL "4. module counts moved. Read the diff above. If the change is"
	say "" "   wanted, run .github/check-imports.sh --update and commit it."
	fail=1
else
	say ok "4. module counts pinned"
fi

exit $fail
