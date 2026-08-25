// Package goboot is the base Starter: the Component lifecycle, the logger and
// graceful shutdown. It imports no other Starter, and neither do its tests,
// because go mod tidy leaks through test imports and a leak here would make
// every user of this package pay for a dependency they never asked for.
package goboot

import "context"

// Tier fixes when a Component starts. Start runs low Tier to high; Stop runs
// the reverse. A Component declares its own Tier, so the wiring order written
// in main cannot be wrong.
type Tier int

const (
	TierObserve   Tier = iota // Actuator, tracing. Starts first, stops last.
	TierResource              // database pool, cache. Starts before Transports.
	TierTransport             // HTTP, gRPC, consumers. Starts last, stops first.
)

// Component is anything go-boot starts and stops in order.
type Component interface {
	Name() string
	Tier() Tier
	// Start returns once the Component is ready and must not block. Its ctx
	// carries the start timeout and is cancelled once the whole start
	// sequence is over, so it is not a ctx to keep for background work. The
	// channel reports a failure that happens after startup; return nil if
	// that cannot happen.
	Start(ctx context.Context) (<-chan error, error)
	Stop(ctx context.Context) error
}

// Drainer is optional: stop taking new work. Drain runs in START order,
// before any Stop.
type Drainer interface{ Drain(ctx context.Context) }

// Checker is optional: the Actuator registers it under Name(). Nothing is
// wired in main.
type Checker interface {
	Check(ctx context.Context) error
}
