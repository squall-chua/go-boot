# The module path for go-boot — name check

Research for [#20](https://github.com/squall-chua/go-boot/issues/20). Date: 2026-08-25.
Toolchain used for the build check: `go1.26.3 linux/amd64`.

**Decision up front: the module path is `github.com/squall-chua/go-boot`, with the library
packages at the repository root.** So the base Starter is imported as
`github.com/squall-chua/go-boot` and binds to the identifier `goboot`, and a Starter is
`github.com/squall-chua/go-boot/web`. Reasoning in [4. The choice](#4-the-choice).

Every claim below carries a source URL or a command you can re-run.

---

## 1. Is the `goboot` package identifier taken?

It is crowded, and it is free. Go scopes a package identifier to its import path, so "taken" here
means "a reader may confuse us with them", not "we cannot use it".

Modules that already declare `package goboot` or ship a `goboot`-named package:

| Module | Stars | What it is |
| --- | --- | --- |
| [github.com/zombocoder/goboot](https://pkg.go.dev/github.com/zombocoder/goboot) | 5 | Spring Boot–style DI, HTTP, config and SQL for Go, generated at compile time. **The closest in concept.** |
| [github.com/it-timo/goboot](https://pkg.go.dev/github.com/it-timo/goboot) | 5 | Go project generator for CLI, REST and backend tools |
| [github.com/trainking/goboot](https://pkg.go.dev/github.com/trainking/goboot) | 7 | Scaffolding for HTTP, gRPC and game servers |
| [github.com/nielskrijger/goboot](https://pkg.go.dev/github.com/nielskrijger/goboot) | 0 | Service bootstrap helpers |
| [github.com/narup/goboot](https://github.com/narup/goboot) | 3 | Ultralight web framework. Not on the proxy. |
| [github.com/lamgor666/goboot-common](https://pkg.go.dev/github.com/lamgor666/goboot-common) | — | Part of a `goboot-*` family (`goboot-dal`, `goboot-gin`) |
| [github.com/gabstv/goboots](https://pkg.go.dev/github.com/gabstv/goboots) | — | Older MVC web framework, `package goboots` |

Method: GitHub repository search for `goboot in:name` (166 results), GitHub code search for
`"package goboot" language:go` (71 results), and the pkg.go.dev search for `goboot`.

**None of them is large.** The biggest is 22 stars, and none appears in the Go ecosystem's common
reading. There is no risk of a reader assuming `goboot.New` means one of these.

## 2. Is the `go-boot` project name taken?

Once, by a project in a completely different field:

- [github.com/usbarmory/go-boot](https://pkg.go.dev/github.com/usbarmory/go-boot) — 229 stars,
  "The bare metal Go UEFI boot manager". Checked its
  [`go.mod`](https://proxy.golang.org/github.com/usbarmory/go-boot/@v/v1.8.1.mod): the module path
  is `github.com/usbarmory/go-boot`, it declares `go 1.26.5`, and pkg.go.dev lists only `shell` and
  `uapi` — **there is no importable root package**. It is a command, not a library.

So there is no import-path conflict, because the paths differ by owner. The cost is search
confusion: a plain web search for "go-boot" finds the UEFI project first today. This is accepted,
and it is the one real downside of the name.

## 3. Is the path itself free?

Both candidates return HTTP 404 on the module proxy, meaning no module has ever been published at
either path:

```
curl -s -o /dev/null -w '%{http_code}\n' https://proxy.golang.org/github.com/squall-chua/go-boot/@v/list   # 404
curl -s -o /dev/null -w '%{http_code}\n' https://proxy.golang.org/github.com/squall-chua/goboot/@v/list    # 404
```

## 4. The choice

Three layouts were on the table.

| Layout | Import of the base Starter | Import of a Starter |
| --- | --- | --- |
| **A. Library at the repo root** | `github.com/squall-chua/go-boot` | `github.com/squall-chua/go-boot/web` |
| B. Rename the repo to `goboot` | `github.com/squall-chua/goboot` | `github.com/squall-chua/goboot/web` |
| C. Library under a `goboot/` directory | `github.com/squall-chua/go-boot/goboot` | `github.com/squall-chua/go-boot/goboot/web` |

**A wins.** It needs no repository rename, and it does not stutter. B costs a rename and then leaves
the project called "go-boot" everywhere in the documentation while the path says `goboot`. C is the
prototype's layout carried over literally, and it puts `go-boot/goboot` on every import line for no
gain — the prototype only has that directory because its module is called `goboot-prototype`.

A's one oddity is that the last element of the path (`go-boot`) is not the package name (`goboot`).
That is ordinary in Go — `gopkg.in/yaml.v3` binds to `yaml`, `google.golang.org/grpc` to `grpc` —
but it is the kind of thing worth checking rather than assuming. It was checked: a throwaway module
with path `example.com/squall-chua/go-boot`, root package `goboot` and a `web` subpackage was built,
and a consumer imported both with **no alias**:

```go
import (
	"net/http"

	"example.com/squall-chua/go-boot"     // binds to goboot
	"example.com/squall-chua/go-boot/web" // binds to web
)
```

`go build ./...` and `go vet ./...` both pass clean.

## 5. Does any identifier force an import alias?

No. This is the check the ticket asked for, and it was run against the real call sites in
`prototypes/`, not reasoned about in the abstract.

Method: for every `.go` file outside generated code, take the last element of every import path and
look for duplicates; then grep for named imports.

```
for f in $(find . -name '*.go' -not -path './internal/gen/*'); do
  awk '/^import \(/,/^\)/' $f | grep -oE '"[^"]+"' | tr -d '"' | awk -F/ '{print $NF}' | sort | uniq -d
done
```

**Zero files import two packages sharing a base name.** Exactly two named imports exist in the whole
prototype, and neither is caused by a go-boot name:

- `yaml "go.yaml.in/yaml/v3"` — inside go-boot's own loader; the package is already called `yaml`,
  so the alias is cosmetic.
- `greetv1 "goboot-prototype/internal/gen/greet/v1"` — the user's own generated code.

Two near-misses are worth naming, because both are real and both are already avoided:

- **`goboot/trace` against `go.opentelemetry.io/otel/trace`.** Both bind to `trace`. They never meet:
  `goboot/trace` is wiring and is imported in `main.go`, while a hand-written span belongs in the
  Service Layer, which imports OTel and never imports go-boot.
- **`goboot/grpc` against `google.golang.org/grpc`.** Both bind to `grpc`. They never meet either,
  because go-boot's gRPC Transport is connect-go (`connectrpc.com/connect`, binding to `connect`)
  and §4.4 of the spec says go-boot never imports grpc-go. A user bringing an existing grpc-go
  server into the same `main.go` would need one alias. That is the known ceiling, and it is narrow.

The names that were already changed for this exact reason are `goboot/web` (not `goboot/http`,
ADR `0005`) and `goboot/grpc/reflection` (not `reflect`). This check confirms nothing else needs it.

## 6. Sources

- Module proxy: `https://proxy.golang.org/<path>/@v/list` and `/@v/<version>.mod`
- [pkg.go.dev search for `goboot`](https://pkg.go.dev/search?q=goboot)
- GitHub search API: `search/repositories?q=goboot in:name`, `search/code?q="package goboot" language:go`
- [Go Modules Reference, module paths](https://go.dev/ref/mod#module-path)
- The call sites checked in §5: `prototypes/`
