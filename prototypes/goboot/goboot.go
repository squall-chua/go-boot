// Package goboot is a THROWAWAY stub of the go-boot base Starter. It was
// written for ticket #2 and reshaped for #14 to the contract settled in
// #8 (ADR 0001). It is not the library.
package goboot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"sync/atomic"
	"syscall"
	"time"
)

// Tier is the rank a Component declares. Start runs low Tier to high; Drain
// runs in the same order; Stop runs the reverse. Because the Component picks
// its own Tier, the order written in main cannot be wrong.
type Tier int

const (
	TierObserve   Tier = iota // Actuator, tracing
	TierResource              // database pool
	TierTransport             // HTTP, gRPC, message consumers
)

// Component is anything go-boot starts and stops in order.
type Component interface {
	Name() string
	Tier() Tier
	// Start returns once the Component is ready. The channel it returns
	// carries a later death; nil means it cannot die once started.
	Start(ctx context.Context) (<-chan error, error)
	Stop(ctx context.Context) error
}

// Drainer is optional. Drain runs on every Component in START order, before
// any Stop, then the App sleeps DrainDelay so a load balancer sees the 503.
type Drainer interface {
	Drain(ctx context.Context)
}

// Checker is optional. The Actuator reads App.Checks during its own Start.
type Checker interface {
	Check(ctx context.Context) error
}

// LogConfig is the base Starter's slice of config.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// App holds the logger, the runtime log level and the Component list.
type App struct {
	Log             *slog.Logger
	Level           *slog.LevelVar
	StartTimeout    time.Duration
	DrainDelay      time.Duration
	ShutdownTimeout time.Duration

	comps []Component
	ready atomic.Bool
}

// New builds the logger and the runtime-settable level.
func New(cfg LogConfig) (*App, error) {
	lvl := new(slog.LevelVar)
	if cfg.Level != "" {
		if err := lvl.UnmarshalText([]byte(cfg.Level)); err != nil {
			return nil, fmt.Errorf("log level %q: %w", cfg.Level, err)
		}
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if cfg.Format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return &App{
		Log:             slog.New(h),
		Level:           lvl,
		StartTimeout:    30 * time.Second,
		DrainDelay:      5 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	}, nil
}

// Add appends Components. The order given is ignored; Tier decides.
func (a *App) Add(c ...Component) { a.comps = append(a.comps, c...) }

// Checks returns every Component that offers one, keyed by Name. The Actuator
// pulls this; nothing pushes. Base may never import a Starter, so the
// dependency has to point this way.
func (a *App) Checks() map[string]func(context.Context) error {
	out := map[string]func(context.Context) error{}
	for _, c := range a.comps {
		if ck, ok := c.(Checker); ok {
			out[c.Name()] = ck.Check
		}
	}
	return out
}

// Ready reports whether every Component has started. /readyz answers 503
// until it is true, so the Actuator cannot say UP during a migration.
func (a *App) Ready() bool { return a.ready.Load() }

// Run starts every Component in Tier order, blocks until a signal or a
// Component death, then drains, waits and stops in reverse.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	order := append([]Component(nil), a.comps...)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Tier() < order[j].Tier() })

	deaths := make(chan error, len(order))
	started := 0
	var err error
	sctx, cancel := context.WithTimeout(ctx, a.StartTimeout)
	for _, c := range order {
		death, e := c.Start(sctx)
		if e != nil {
			err = fmt.Errorf("start %s: %w", c.Name(), e)
			break
		}
		go func() {
			if d := <-death; d != nil {
				deaths <- fmt.Errorf("%s died: %w", c.Name(), d)
			}
		}()
		a.Log.Info("started", "component", c.Name(), "tier", c.Tier())
		started++
	}
	cancel()

	if err == nil {
		a.ready.Store(true)
		a.Log.Info("running", "pid", os.Getpid())
		select {
		case <-ctx.Done():
		case err = <-deaths: // a death is fatal
			a.Log.Error("component died", "err", err)
		}
		a.ready.Store(false)
		a.Log.Info("shutting down")
	}

	dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), a.ShutdownTimeout)
	defer dcancel()
	for i := range started { // Drain runs in START order
		if d, ok := order[i].(Drainer); ok {
			d.Drain(dctx)
		}
	}
	if started > 0 {
		time.Sleep(a.DrainDelay)
	}
	for i := started - 1; i >= 0; i-- {
		if e := order[i].Stop(dctx); e != nil {
			err = errors.Join(err, fmt.Errorf("stop %s: %w", order[i].Name(), e))
		}
	}
	return err
}
