# Component start order comes from a declared Tier

Every go-boot service must start its Actuator first and its database before its Transports, and
must shut down in the reverse order. Rather than have `main` state that order, each Component
declares a **Tier** — Observe, Resource or Transport — and go-boot sorts by it. The order a
developer writes in `main` is ignored, so the wrong order is not something they can write.

## Considered options

- **Add order.** What the prototype did. It works, but the ordering lives in one silent line and a
  code comment, and a comment is not a contract.
- **Declared dependencies with a topological sort.** Catches more, including cycles. It is also a
  dependency-injection container by another name, which the project rules out.
- **Typed fields on the App struct**, giving a compile-time guarantee. Too rigid: the Messaging
  Starter has no field yet, and adding one is a breaking change.

## Consequences

- **Order inside a Tier is not promised.** The sort is stable, so it stays add order, but nothing
  relies on that. A case that genuinely needs order inside a Tier means a Tier is missing.
- **`Start` returns a death channel**: `Start(ctx) (<-chan error, error)`. The prototype's
  `Start`/`Stop` pair had nowhere to report a Component that started fine and died an hour later,
  and the prototype shipped exactly that bug. A Component that cannot die once started returns
  `nil`, and a nil channel blocks forever, which is correct and free. A death is fatal: go-boot
  drains, stops everything in reverse, and returns the error so the orchestrator restarts the pod.
- **The Actuator pulls its health checks; nothing pushes them.** A Component may implement
  `Check(ctx) error`, and the Actuator reads `app.Checks()` during its own `Start`. The base
  package may never import a Starter, so it cannot go looking for the Actuator — a Starter
  importing base is the allowed direction.
- **Readiness lives on the App, not on the Actuator.** `/readyz` answers 503 unless every
  Component has started and every check passes. Without this the Actuator, which starts first,
  reports UP while a migration is still running and nothing is serving.
- **Drain is its own phase and runs in start order**, not reverse. The service announces it is
  going away before it lets go, then sleeps a configurable delay so a load balancer can observe
  the 503, then stops in reverse.
