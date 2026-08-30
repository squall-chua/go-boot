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
| `service.go` | the Service Layer and the HTTP shell over it — delete and write your own |
| `app.yaml` | the embedded defaults. A file of the same name on disk wins over it, and `MYSERVICE_` environment variables win over that |
| `migrations/` | goose migrations, applied by `myservice migrate` |

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
