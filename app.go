package goboot

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"sync/atomic"
	"syscall"
)

// LogConfig is the base Starter's slice of config.
type LogConfig struct {
	Level  string `yaml:"level"`  // slog level name; default INFO
	Format string `yaml:"format"` // "text" or "json"; default text
}

// Config is what New takes. Later Starters add their own sections beside it.
// The Lifecycle section holding the start, drain and stop timeouts arrives
// with #22; loading any of this from a file or the environment is #23.
type Config struct {
	Log LogConfig `yaml:"log"`
}

// App holds the logger, the runtime log level and the Component list.
type App struct {
	Log   *slog.Logger
	Level *slog.LevelVar // the Actuator's /actuator/loglevel writes this

	comps   []Component
	order   []Component // comps sorted by Tier, fixed when Start runs
	started int         // how many of order are running
	ready   atomic.Bool
}

// New builds the logger and the runtime-settable level. It returns an error
// rather than panicking on a log level it cannot parse.
func New(cfg Config) (*App, error) {
	lvl := new(slog.LevelVar)
	if cfg.Log.Level != "" {
		if err := lvl.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
			return nil, fmt.Errorf("log level %q: %w", cfg.Log.Level, err)
		}
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if cfg.Log.Format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return &App{Log: slog.New(h), Level: lvl}, nil
}

// Add appends Components. The order given is ignored; Tier decides.
func (a *App) Add(c ...Component) { a.comps = append(a.comps, c...) }

// Ready reports whether every Component has started. It turns false again the
// moment shutdown begins, so /readyz answers 503 before anything is torn down.
func (a *App) Ready() bool { return a.ready.Load() }

// Start starts every Component from the lowest Tier to the highest. It takes
// no signals, so a test can drive it directly. On a failure it stops the ones
// that did start, in reverse, and returns the start error joined with any stop
// errors.
func (a *App) Start(ctx context.Context) error {
	a.order = slices.Clone(a.comps)
	slices.SortStableFunc(a.order, func(x, y Component) int {
		return cmp.Compare(x.Tier(), y.Tier())
	})
	for _, c := range a.order {
		// The death channel is read in #22, which makes a mid-life death
		// fatal. Nothing watches it yet.
		if _, err := c.Start(ctx); err != nil {
			err = fmt.Errorf("start %s: %w", c.Name(), err)
			return errors.Join(err, a.Stop(context.WithoutCancel(ctx)))
		}
		a.started++
		a.Log.Info("started", "component", c.Name(), "tier", c.Tier())
	}
	a.ready.Store(true)
	return nil
}

// Stop stops every started Component in reverse order, joining errors so one
// bad Stop does not hide the rest. It takes no signals.
//
// The drain phase — every Drainer in START order, then the drain delay — and
// the stop timeout are #22. Nothing calls Drain yet.
func (a *App) Stop(ctx context.Context) error {
	a.ready.Store(false)
	var err error
	for i := a.started - 1; i >= 0; i-- {
		c := a.order[i]
		if e := c.Stop(ctx); e != nil {
			err = errors.Join(err, fmt.Errorf("stop %s: %w", c.Name(), e))
		}
	}
	a.started = 0
	return err
}

// Run is Start, wait for SIGINT or SIGTERM, then Stop.
func (a *App) Run(ctx context.Context) error {
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	if err := a.Start(sigCtx); err != nil {
		stopSignals()
		return err
	}
	a.Log.Info("running", "pid", os.Getpid())
	<-sigCtx.Done()
	// Hand the signals back to Go's default handling straight away, so a
	// second one kills the process instead of queueing behind a slow stop.
	stopSignals()
	a.Log.Info("shutting down")
	return a.Stop(context.WithoutCancel(ctx))
}
