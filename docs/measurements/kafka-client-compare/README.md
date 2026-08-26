# kafka-client-compare — THROWAWAY

The harness behind one table: `docs/spec.md` §14's franz-go against
`segmentio/kafka-go` on consumer-group correctness
([#51](https://github.com/squall-chua/go-boot/issues/51)).

**It is not the go-boot library and nothing imports it.** It is a **separate Go module**
(`compare`) on purpose, because it links `segmentio/kafka-go`, which
[§7](../../spec.md#7-dependencies-and-the-ticket-that-chose-each-one) fixes go-boot's dependency
list against. `go build ./...` and `go test ./...` at the repo root do not see it. `prototypes/`
is the same idea for a different ticket.

It is kept because §14 now prints a number, and a number in that document has to be one a reader
can re-run rather than one they have to believe. It is not maintained: if it stops building
against a newer client, that is the harness rotting, not go-boot.

## What it answers

Whether the two clients differ on **consumer-group correctness** — cooperative rebalancing,
partition revocation during a commit, behaviour on a coordinator move. §14 said for one ticket
that this "needs a real cluster and has not been run". This is the run.

Both consumers keep `goboot/kafka`'s own discipline: auto-commit off, handle the record, then
commit it. Both are fed by the same franz-go producer. The consumer group is the only thing that
differs.

## How to run it

```sh
docker compose up -d
# wait for "Kafka Server started" in all three
go run . -client=franz     -topic=cmp-franz -group=cmp-franz
go run . -client=segmentio -topic=cmp-seg   -group=cmp-seg
docker compose down
```

Each run produces `-n` records, starts two group members, waits until they are working, then
restarts all three brokers in turn while they are still mid-flight. It prints distinct keys,
total deliveries, duplicates and drops, and exits non-zero if anything was dropped.

**Use a fresh `-topic` and `-group` per run.** A group that already has committed offsets measures
the tail of the last run.

## What it said, 2026-08-26

Three brokers, one group of two members, six partitions, 30,000 records, a 20 ms handler, a
rolling restart of all three brokers while both members were still mid-flight.

| Client | Distinct | Deliveries | Duplicated | Dropped | Errors the caller saw |
|---|---|---|---|---|---|
| `github.com/twmb/franz-go` v1.21.6 | 30,000 | 30,000 | 0 | 0 | 0 |
| `github.com/segmentio/kafka-go` v0.4.51 | 30,000 | 30,018 | 18 | 0 | 14 |

Neither dropped a message. segmentio's 14 were ten `[16] Not Coordinator For Group` as each
coordinator moved and four writes to a socket the restart had already closed; each is an
uncommitted offset, which is where its 18 duplicates came from. franz-go retried the same
coordinator moves inside the client and returned nothing.

**Sizing matters, and the first attempt got it wrong.** With 3,000 records and a 2 ms handler both
clients finished consuming before the first broker restart landed, and reported a meaningless
0/0. The workload has to outlast the roll or the run measures nothing.
