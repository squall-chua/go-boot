# myservice

Written by `goboot new`. Everything in here is yours to edit.

## Run it

```
go mod tidy
go run . migrate
go run .
curl localhost:8080/hello/world
```

`migrate` needs the PostgreSQL named in `app.yaml`. The quickest one:

```
docker run --rm -e POSTGRES_USER=myservice -e POSTGRES_PASSWORD=myservice \
  -e POSTGRES_DB=myservice -p 5432:5432 postgres:18
```

## What is where

| File | What it is |
|---|---|
| `main.go` | the wiring, one call to `preset.Full`, and the `myservice migrate` subcommand |
| `routes.go` | the list of FEATURES, two lines each. The routes themselves live in the feature package, so `serve()` never grows |
| `internal/greeting/` | one feature's DOMAIN: Entity, `ErrNotFound`, the `Repository` interface and the Service Layer — delete and write your own |
| `internal/greeting/entity/` | that feature's Entity and the SQL that loads it — the only package in it that names a column |
| `internal/greeting/rest/` | that feature's HTTP adapter: DTOs, bind, handler and routes, all private but `Routes` |
| `internal/transport/` | the adapter that lets a handler take a request DTO and return a response DTO instead of `(w, r)` — shared by every feature |
| `app.yaml` | the embedded defaults. A file of the same name on disk wins over it, and `MYSERVICE_` environment variables win over that |
| `migrations/` | goose migrations, applied by `myservice migrate` |

## Where feature two goes

`internal/greeting/` is one feature, and the shape grows **sideways**. A second
one is a sibling directory, not a new layer:

```
internal/
  greeting/              the DOMAIN — no SQL, no HTTP
    greeting.go          Repository interface, Service
    greeting_test.go     the Service against a fake Repository
    entity/              the Entity and the SQL that loads it
      entity.go          Greeting, ErrNotFound
      postgres.go        the only file that writes SQL
    rest/                DTOs, bind, handler, routes — all private but Routes
      rest.go
  orders/                feature two: the same three
    orders.go
    entity/
    rest/
  transport/             shared: typed handlers, written once
```

Each feature owns its routes. `rest.Routes` names its own patterns beside the
handlers they call, so adding a route touches **one file inside one
directory**. `routes.go` in the root gains two lines and nothing else moves:

```go
import (
	"myservice/internal/greeting"
	greetingentity "myservice/internal/greeting/entity"
	greetingrest "myservice/internal/greeting/rest"
	"myservice/internal/orders"
	ordersentity "myservice/internal/orders/entity"
	ordersrest "myservice/internal/orders/rest"
)

func addRoutes(app *preset.App, cfg config) {
	greet := greeting.New(greetingentity.NewRepository(app.DB), cfg.Greeting)
	greetingrest.Routes(app.Web, greet)

	ord := orders.New(ordersentity.NewRepository(app.DB))
	ordersrest.Routes(app.Web, ord)
}
```

`serve()` never changes again, whatever the service grows into.

**Every feature's sub-packages share the same names**, so each is imported
under a prefixed alias. That is the price of a folder per layer, and
`routes.go` is the only file that pays it.

## The three types, and why each one is separate

The dependencies run one way and never back:

```
Routes -> Handler -> Service -> Repository -> the database
```

Each arrow crosses a type the next layer does not know about.

- **Entity** (`Greeting` in `entity/entity.go`) is a row as a Go type. It sits
  with the SQL that loads it, in `entity/postgres.go`, because the two change
  together: a new column is a new field and a new name in the `SELECT`. Only
  that directory names a column.
- **Repository** is an interface, and it is declared in `greeting.go` beside
  the `Service` that *uses* it — not in the `entity` package that implements
  it. The dependency points INWARD, which is the whole point: `Service`
  imports no `database/sql`, and `greeting_test.go` swaps in a fake in four
  lines. `sql.ErrNoRows` is mapped to `entity.ErrNotFound` inside the adapter,
  so no layer above it knows what a database is.
- **DTO** — `greetRequest` and `greetResponse` in the `rest` package, beside
  the `bindGreet` that fills them and the handler that uses them. All three
  stay **private**: keeping them in one package with the handler is what
  avoids exporting them. The response DTO is the shape on the wire and
  deliberately not the Entity — add a column to `entity.Greeting` and it
  cannot reach a client by accident.

## The handler never sees `(w, r)`

`internal/transport` is a small adapter of your own, so a handler is an
ordinary function — a request DTO in, a response DTO out:

```go
func greet(s *Service) transport.Handler[greetRequest, greetResponse] {
	return func(ctx context.Context, req greetRequest) (greetResponse, error) {
		out, err := s.Greet(ctx, req.Name)
		if err != nil {
			return greetResponse{}, err
		}
		return greetResponse{Greeting: out}, nil
	}
}
```

No `ResponseWriter`, no `*http.Request`, no status code, no JSON. A test calls
it directly. Three small rules make that work:

- **The `rest` package is the only one that touches `*http.Request`**, and
  `bindGreet` is the only function in it that does. It pulls the path values,
  query and body out into the request DTO, and stops. For a body,
  `web.DecodeJSON` is the call: it caps the size, rejects unknown fields, and
  its error text is already safe to show the caller. A bind error is a 400.
- **`transport.Status(404, "...")` chooses any other status.** Any other error
  is logged with the request ID and answered with a bare 500, so a driver error
  naming your database user can never reach a caller.
- **Success is 200.** A route that must answer 201 or 204 mounts a plain
  `http.HandlerFunc` instead. go-boot takes any `http.Handler`, so the adapter
  is a convenience, never a wall.

**This adapter is yours, and go-boot has nothing like it.** Its Transport takes
an `http.Handler` and nothing else (ADR `0004`), because a handler signature
inside the library would need a shim for every piece of `net/http` middleware
ever written. Keeping it in your module is what lets `otelhttp` and everything
else still work unchanged — go-boot only ever sees the `http.Handler` that
`transport.Handle` returns.

**This is your code, not go-boot's.** go-boot ships no `Repository` or
`Entity` type of its own — it hands back a plain `*sql.DB` and stops, which is
ADR `0009`. That is what lets this interface be yours: widen it, split it per
aggregate, or put `sqlc`, `ent` or `gorm` behind it. They all take a plain
`*sql.DB`, and nothing above `repository.go` changes when you switch.

## Two things to know before you deploy

- **The service refuses to start on a pending migration.** Run `myservice
  migrate` as a Kubernetes Job from the **same image** as the pods, and let it
  finish **before** the rollout starts. `migrateOnStart: true` in `app.yaml` is
  for local development only.
- **`maxOpenConns` is 10, which is ten pods** against a stock PostgreSQL,
  which allows about 97 connections.

## Sharing the database with a Spring Data JPA service

`migrations/00001_greeting.sql` already follows the convention: `timestamptz`,
an identity id, `lower_snake_case`. One line is commented out — the `version`
column, which is the only JPA-specific part. Uncomment it when the JPA entity
carries `@Version`, and then **every Go `UPDATE` to that table must write
`version = version + 1`**, or Hibernate commits over your write with no error
on either side.

`ddl-auto=validate` is the other half, and the Scaffold cannot write it for
you: only your Java build knows where its service lives. The three steps are
start a PostgreSQL, run `myservice migrate`, then boot the Java service with
`spring.jpa.hibernate.ddl-auto=validate`. See `docs/jpa-interop.md` in go-boot.

That page also has `dbtest.LintJPAConventions`, which checks the part
`ddl-auto=validate` cannot see. **Reach for it only once you have uncommented
`version`**: one of the three things it reports is a table with no `version`
column, so on the migration above, as shipped, it fails by design. That is the
lint telling you this schema is not yet a shared one, which is correct.
