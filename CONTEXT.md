# go-boot

A Go library that removes the repeated setup work at the start of a service, plus a thin
CLI that writes a new project. It takes its shape from Spring Boot: sane defaults and one
short `main`, but with plain Go wiring instead of reflection.

## Language

### The pieces

**Starter**:
A subpackage of go-boot providing one capability with sane defaults, such as `goboot/web`
or `goboot/db`. The name is borrowed from Spring Boot. A Starter may split a dependency that
only some of its users need into a subpackage of its own, because Go links by import: a
dependency named in the parent package is paid for by everyone who imports it.
_Avoid_: module (collides with Go modules — go-boot ships as one Go module), package, plugin

**Preset**:
A single function that wires several Starters together with defaults, written in plain Go
so a reader can copy its body and edit it. A Preset takes **no options**: copying the body is
the only way to change what it wires. Like a Starter, a Preset splits a dependency that only
some of its users need into a subpackage of its own, because Go links by import. v1 ships
exactly one Preset, `preset.Full`, and its tracing twin `traced.Full`.
_Avoid_: auto-configuration, magic, bundle

**Component**:
Anything go-boot starts and stops in order during startup and shutdown. A Component has three
phases: **Start**, which returns once it is ready; **Drain**, which stops it taking new work and
is optional; and **Stop**.
_Avoid_: bean, service, runnable

**Check**:
A readiness test a Component offers, written as `Check(ctx) error`. The Actuator collects every
Component's Check when it starts and runs them all on each request to `/readyz`. A Check **must
respect its context**: it runs synchronously inside the probe's own deadline, so one that ignores
cancellation blocks a goroutine on every probe. Liveness never runs Checks, because a liveness test
that touches a dependency turns a database outage into a restart loop.
_Avoid_: health indicator, probe (collides with the Kubernetes probe that calls it), healthcheck

**Tier**:
The rank a Component declares to fix when it starts. There are three, in start order: Observe
(the Actuator and tracing), Resource (the database pool) and Transport (HTTP, gRPC, message consumers).
Start runs from the lowest Tier to the highest. Drain runs in the same order; Stop runs the
reverse. Because a Component declares its own Tier, the wiring in `main` cannot put them in the
wrong order.
_Avoid_: layer (collides with Service Layer), phase (means Start, Drain or Stop), priority, stage

**Profile**:
A named set of config overrides, chosen at startup, that layers a second file over the base one —
`app.yaml` and then `app-local.yaml`. The name is borrowed from Spring Boot.
_Avoid_: environment (collides with environment variables), stage, mode

### The service shape

**Transport**:
A protocol front end that exposes the Service Layer to callers. go-boot v1 has two: HTTP
and gRPC. They are independent — neither is generated from the other — but they share one
listener. A **Consumer** is not a Transport, though it starts at the same Tier — see the
next entry.
_Avoid_: door, adapter, endpoint, protocol

**Consumer**:
A Component that pulls messages from a broker and hands each to a Handler, one at a time per
partition or per queue. It is `TierTransport` and it is the named user of the optional `Drainer`:
on Drain it stops taking new messages and returns at once, and the waiting for work in flight
happens in Stop, which is the only phase with a budget. Delivery is at-least-once, so a Handler
must be idempotent.

**It is not a Transport, and the Tier is the only thing they share.** `goboot/kafka` and
`goboot/rabbit` declare `TierTransport` because the lifecycle reason is the same — start last,
stop first, so nothing takes new work while the pool it needs is going away. But a Transport is
**called**, and a Consumer **calls out**: it fetches its own work from a broker, exposes no port
and shares no listener. That is why the Tier above lists message consumers and the Transport
entry does not.
_Avoid_: listener (collides with the HTTP listener), subscriber, worker, Transport

**Service Layer**:
Plain Go code holding the application's behaviour. Every Transport calls into it. It knows
nothing about HTTP or gRPC.
_Avoid_: handler, controller, business logic

**Actuator**:
The group of operational endpoints a running service exposes for humans and for
Kubernetes: health, readiness, metrics, runtime log level, build info. The name is
borrowed from Spring Boot.
_Avoid_: management endpoints, admin API, ops endpoints

**Scaffold**:
The CLI that writes a new project which then calls go-boot. It writes a thin `main`, so
fixes still arrive through `go get -u` rather than regeneration. What it copies is not a
template but **a project**: the two it copies live in this repository as ordinary `package
main` packages that CI compiles, so use *project* for them too.
_Avoid_: generator, template, initializr
