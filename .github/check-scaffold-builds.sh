#!/usr/bin/env bash
# CI builds what the Scaffold writes: docs/spec.md 15, which carried this as a
# written-down hole until #55. `go build ./...` compiles the two projects under
# cmd/goboot/scaffold and TestWriteProducesAProjectThatParses proves a copy is
# valid Go; neither one BUILDS a copy. Section 15 has the whole argument — why
# it is the gRPC copy that matters, and why an HTTP-only version was refused.
#
# Three things live here rather than there:
#
#   - each project gets a `replace` back to this checkout, so what is built is
#     the go-boot in the working tree rather than whatever the proxy last
#     published. That is also what makes the job work while the repository is
#     private.
#   - `buf generate` runs BEFORE `go mod tidy`, because internal/gen is not
#     checked in and tidy cannot resolve imports naming a package that is not
#     written yet. The generated README gives the same order — but this script
#     hardcodes it rather than reading it, so a README reordered back the wrong
#     way would pass here. #55 found that order wrong once; nothing here stops
#     it going wrong again.
#   - buf and the two plugins have to be on PATH. The workflow installs them
#     the way the generated README tells a user to.
set -euo pipefail
cd "$(dirname "$0")/.."
repo="$PWD"

for tool in buf protoc-gen-go protoc-gen-connect-go; do
	command -v "$tool" > /dev/null || { echo "$tool is not on PATH"; exit 1; }
done

dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT

go build -o "$dir/goboot" ./cmd/goboot

# build <label> [-grpc]: write one project into its own directory and compile it.
# `go mod tidy` is quiet unless it fails, because it says "go: found ..." about
# every dependency of a project that has none resolved yet. The -grpc flag is
# read from the arguments the CLI is given, not from the label, so the two
# cannot disagree about which project this is.
build() {
	local label=$1
	shift
	mkdir -p "$dir/$label"
	(
		cd "$dir/$label"
		"$dir/goboot" new "$@" github.com/acme/orders > /dev/null
		cd orders
		printf '\nreplace github.com/squall-chua/go-boot => %s\n' "$repo" >> go.mod
		if [ "${1:-}" = -grpc ]; then
			buf generate
		fi
		if ! out=$(go mod tidy 2>&1); then
			echo "$out"
			exit 1
		fi
		go build ./...
	)
	printf '%-6s %s\n' ok "the $label project the Scaffold writes builds"
}

build http
build grpc -grpc
