#!/usr/bin/env bash
# The import-leak check: docs/spec.md 8.1, five assertions. Four were settled
# in #14 and built for real by #32; the fifth was added by #33.
#
# Go links by import. A package that names a heavy dependency makes every
# importer pay for it, so these five rules are what keep the
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
# Run with --update to rewrite the golden module counts of assertions 4 and 5.
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

# mods <package> [extra go list flags]: linked non-stdlib module roots, not
# counting go-boot itself. `-test` is the extra flag assertion 5 passes, and
# it is the only difference between the two numbers in the golden file. Stdlib
# packages carry no .Module, so the template drops them. go list runs on a
# line of its own: inside a pipeline, `pipefail` plus the `|| true` that grep
# needs for the no-match case would let a toolchain failure read as a package
# with no dependencies at all.
mods() {
	local pkg=$1 all
	shift
	all=$(go list -deps "$@" -f '{{if .Module}}{{.Module.Path}}{{end}}' "$pkg")
	# No trailing newline: a package that links nothing but stdlib leaves
	# $all empty, and `printf '%s\n'` would turn that into one blank line,
	# which counts as a module. sort supplies the newline for a real list.
	printf '%s' "$all" | sort -u | grep -vxF "$M" || true
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
#    Preset dragged the Actuator into an HTTP-only binary. goboot/security
#    joined the list with #34: it is a package a user imports directly, so
#    the rule that keeps Prometheus out of goboot/web keeps it out of there.
heavy="$M/trace $M/grpc/health $M/grpc/metrics $M/grpc/reflection $M/trace/rpc $M/db/dbtest $M/web/metrics $M/rabbit $M/kafka"
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
for p in "$M" "$M/web" "$M/db" "$M/actuator" "$M/grpc" "$M/preset" "$M/security"; do
	reaches "$p" $heavy
done
# goboot/preset/traced is the tracing twin. It is the one package allowed to
# reach goboot/trace — and ONLY that one, or the twin quietly becomes the
# place every heavy dependency goes to hide. Both lists are left unquoted on
# purpose: $heavy is a fixed list of import paths, and the word splitting is
# what turns it into arguments.
reaches "$M/preset/traced" $(printf '%s\n' $heavy | grep -vxF "$M/trace")
# The consumer Starters are heavy packages that must not reach EACH OTHER: a
# service consuming from one broker links none of the other's client. That is
# CONTEXT.md's rule and #35's third acceptance box, and it needs no sixth
# assertion — assertion 2 already takes an arbitrary package and an arbitrary
# list, so each is checked against the heavy list minus itself.
reaches "$M/rabbit" $(printf '%s\n' $heavy | grep -vxF "$M/rabbit")
reaches "$M/kafka"  $(printf '%s\n' $heavy | grep -vxF "$M/kafka")
[ "$leaked" = 1 ] || say ok "2. no short-path package reaches a heavy optional package"

# 3. goboot/db links NO driver. #13 measured pgx/v5/stdlib at +7.64 MB; a
#    MySQL user would have paid all of it for nothing. The user blank-imports
#    their own driver in main.
drv=$(deps "$M/db" | grep -E 'jackc|go-sql-driver|lib/pq|mattn/go-sqlite3' | sort -u || true)
report "3. goboot/db links no driver" "$drv"

# 4. A pinned module count per package, and 5. a pinned count of the modules
#    that package's TESTS link. Assertion 4 is the one that catches the NEXT
#    leak, the one nobody predicted, and the one no hand-written rule above
#    names.
#
#    Assertion 5 is the same idea aimed at the door 8.1 did not name. `go list
#    -deps` excludes tests by design, so a heavy dependency added to a test
#    passes all four rules above — assertion 1 greps only for go-boot import
#    paths, and assertion 4 counts what a user links. But `go mod tidy` walks
#    test imports, so a test-only dependency still lands in every consumer's
#    module graph. It is a real cost, so it gets a real number.
#
#    Both numbers live in one golden file, one row per package, and the header
#    line names each column so they are never read as the same thing.
#    Assertion 4 goes on counting exactly what it always counted.
#
#    Three directories are left out. `examples` holds binaries, and their
#    weight is measured in docs/spec.md 6 rather than pinned here. `internal`
#    is generated protobuf code that no user can import, so its module count
#    is a fact about buf's output, not about what go-boot links — but base
#    reaching it is still caught, by assertion 1 above. `cmd/goboot/scaffold`
#    holds the two projects the Scaffold copies, which are examples by another
#    name: they import the heavy packages on purpose and their counts move
#    whenever one is edited (docs/spec.md 15).
#
#    `cmd/goboot` itself is NOT left out, and its row is the one that matters
#    most in this file. `go mod tidy` walks cmd/ like anything else, so a
#    dependency the Scaffold links lands in EVERY go-boot user's module graph.
#    Pinned at 0 and 0, that cannot happen quietly.

#    Assertion 5 inherits that exclusion, and there it leaves a real hole: `go
#    mod tidy` walks the tests under `examples` too, so a heavy dependency
#    added to an example's test still reaches every consumer's module graph
#    unchecked. It is left open on purpose. Those binaries import the heavy
#    packages by design, so their counts move whenever an example is edited,
#    and a number that moves for ordinary work pins nothing.
counts=$(mktemp)
trap 'rm -f "$counts"' EXIT
pkgs=$(go list ./...)
{
	echo '# package / modules a user links (4) / modules its tests link (5)'
	for p in $(printf '%s\n' "$pkgs" | grep -vE "^$M/(examples|internal|cmd/goboot/scaffold)/"); do
		printf '%-23s %3d %3d\n' "goboot${p#"$M"}" \
			"$(mods "$p" | wc -l)" "$(mods "$p" -test | wc -l)"
	done
} > "$counts"

# col <n> <file>: the package name and column n, so one assertion's numbers
# can be compared without the other's.
col() { awk -v c="$1" '/^#/ { next } { print $1, $c }' "$2"; }

# pinned <column> <assertion number> <what the column counts>
pinned() {
	if diff -q <(col "$1" "$golden") <(col "$1" "$counts") > /dev/null; then
		say ok "$2. $3 pinned"
	else
		say FAIL "$2. $3 moved. Read the diff above. If the change is"
		say "" "   wanted, run .github/check-imports.sh --update and commit it."
		fail=1
	fi
}

if [ "$update" = 1 ]; then
	cp "$counts" "$golden"
	say ok "4, 5. module counts written to $golden"
elif diff -u "$golden" "$counts"; then
	say ok "4. the modules a user links pinned"
	say ok "5. the modules the tests link pinned"
else
	# The file moved, and the diff above is already printed. Say WHICH column
	# moved, so the two numbers are never read as one.
	before=$fail
	pinned 2 4 "the modules a user links"
	pinned 3 5 "the modules the tests link"
	# Neither column moved, so what moved is the header line or the spacing.
	# That is still a golden-file change, and the header is the only thing
	# telling a reader which column is which, so it fails too.
	if [ "$fail" = "$before" ]; then
		say FAIL "4, 5. the golden file's header or spacing moved. Read the"
		say "" "   diff above, then run .github/check-imports.sh --update."
		fail=1
	fi
fi

exit $fail
