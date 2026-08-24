# Transactions are an explicit closure, and do not nest

go-boot's transaction helper is one function:

```go
func WithTx(ctx context.Context, db *sql.DB, fn func(context.Context, *sql.Tx) error) error
```

It begins a transaction, runs `fn`, and commits. It rolls back if `fn` returns an error or panics,
and it returns the commit error. It does not put the `*sql.Tx` in the context, it does not nest,
and it takes no options.

This is the place where go-boot deliberately does not copy Spring Boot. `@Transactional` works by
putting the transaction in a thread-local, so any repository call inside a marked method joins it.
Go's equivalent of a thread-local is `context.Context`, and using it that way would make a
function's behaviour depend on a value the reader cannot see. The project rules that out.

## Considered options

- **An explicit closure.** Chosen. The transaction is a parameter, so the reader can see it.
- **A transaction carried in the context**, with a `From(ctx)` that returns the transaction if one
  is running and the pool otherwise. This is Spring's model and it does make nested calls join
  automatically. It is also ambient magic by the project's own definition.
- **No helper at all.** `database/sql` already has `BeginTx`, `Commit` and `Rollback`. But the
  fifteen lines around them encode rollback on error, rollback on panic, and returning the commit
  error — the same class of "not optional" as the recovery middleware in ADR 0004.

## Consequences

- **Nesting is not supported, and could not be.** `*sql.Tx` has no `Begin` method;
  `database/sql` cannot nest transactions at all. This is a statement about the standard library,
  not a limitation go-boot chose.
- **A method that must run inside a caller's transaction takes it as a parameter.** The Go answer
  to "two Service Layer methods both want the transaction" is that the inner one accepts a value
  that both `*sql.DB` and `*sql.Tx` satisfy. sqlc already generates exactly that interface.
- **go-boot defines no `DBTX` interface.** Every query layer already has its own, and a go-boot
  one would be a third that everybody has to convert to.
- **No isolation level parameter.** The transaction runs at the database's default. A service that
  needs `Serializable` calls `BeginTx` itself, which is three lines and reads no worse.
- **Retry is not go-boot's job.** A serialization failure under `Serializable` needs a retry loop
  with a backoff policy the service owns. Building that in would mean guessing at the policy.
