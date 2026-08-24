# The HTTP Transport Starter is `goboot/web`, not `goboot/http`

Every `main.go` that touches an `http.Handler` — which is all of them — must import `net/http`. A
Starter at `goboot/http` therefore collides on the package identifier at every call site, forcing an
alias: `gbhttp "goboot/http"`. Renaming the package to `goboot/web` removes the alias permanently:
`web.New(cfg, log)`, `web.DefaultMiddleware(...)`, `web.WriteProblem(...)`.

## Considered options

- **Keep `goboot/http` and alias.** Universal in Go and costs nothing at runtime. Rejected because
  the alias is not a one-time cost — it appears in every example, every README snippet, and every
  `main` the Scaffold generates. Prototype note 4.5 called it "small, permanent, and it shows up in
  the very first example a newcomer reads."
- **`goboot/transport/http`.** Deeper path, same collision. No gain.
- **Directory `goboot/http` containing `package web`.** Legal Go, keeps the import path symmetric
  with `goboot/grpc`, and needs no alias. Rejected because a package whose name does not match its
  directory surprises both readers and tooling, and the surprise is permanent too.

## Consequences

- **The Starter names are asymmetric**: `web.New` sits next to `grpc.New`. This is the real cost and
  it does not go away.
- **`goboot/grpc` keeps its name.** It has no equivalent collision, because a user importing it
  writes `connectrpc.com/connect`, not `google.golang.org/grpc`.
- **"web" slightly oversells the package.** It serves JSON APIs and does no templating or static
  files, and nothing in go-boot plans to.
- **This is an import path, so it is a breaking change after v1.** Deciding it now, before anything
  is published, is the whole point of deciding it at all.
