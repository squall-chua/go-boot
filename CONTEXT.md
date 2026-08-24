# go-boot

A Go library that removes the repeated setup work at the start of a service, plus a thin
CLI that writes a new project. It takes its shape from Spring Boot: sane defaults and one
short `main`, but with plain Go wiring instead of reflection.

## Language

### The pieces

**Starter**:
A subpackage of go-boot providing one capability with sane defaults, such as `goboot/http`
or `goboot/db`. The name is borrowed from Spring Boot.
_Avoid_: module (collides with Go modules — go-boot ships as one Go module), package, plugin

**Preset**:
A single function that wires several Starters together with defaults, written in plain Go
so a reader can copy its body and edit it.
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
and gRPC. They are independent — neither is generated from the other.
_Avoid_: door, adapter, endpoint, protocol

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
fixes still arrive through `go get -u` rather than regeneration.
_Avoid_: generator, template, initializr
