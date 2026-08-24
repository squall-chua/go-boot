// Package goboot is a THROWAWAY stub of the go-boot base Starter, written for
// ticket #2 to make the cmd/ main.go files real. It is not the library.
package goboot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Component is anything go-boot starts and stops in order.
type Component interface {
	Name() string
	Start(ctx context.Context) error // must return once ready; must not block
	Stop(ctx context.Context) error
}

// Drainer is an optional Component interface. Every Drain runs, in reverse
// order, BEFORE any Stop. Without it an Actuator cannot report 503 on /readyz
// while the Transports are still finishing in-flight requests.
type Drainer interface {
	Drain(ctx context.Context)
}

// LogConfig is the base Starter's slice of config.
type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info | warn | error
	Format string `yaml:"format"` // text | json
}

// App holds the logger, the runtime log level and the Component list.
type App struct {
	Log             *slog.Logger
	Level           *slog.LevelVar
	ShutdownTimeout time.Duration

	comps []Component
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
	return &App{Log: slog.New(h), Level: lvl, ShutdownTimeout: 10 * time.Second}, nil
}

// Add appends Components. Start order is add order; stop order is the reverse.
func (a *App) Add(c ...Component) { a.comps = append(a.comps, c...) }

// Run starts every Component, blocks until SIGINT/SIGTERM, then drains and
// stops them in reverse order.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	started := 0
	for _, c := range a.comps {
		if e := c.Start(ctx); e != nil {
			err = fmt.Errorf("start %s: %w", c.Name(), e)
			break
		}
		a.Log.Info("started", "component", c.Name())
		started++
	}
	if err == nil {
		a.Log.Info("running", "pid", os.Getpid())
		<-ctx.Done()
		a.Log.Info("shutting down")
	}

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.ShutdownTimeout)
	defer cancel()
	for i := started - 1; i >= 0; i-- {
		if d, ok := a.comps[i].(Drainer); ok {
			d.Drain(sctx)
		}
	}
	for i := started - 1; i >= 0; i-- {
		if e := a.comps[i].Stop(sctx); e != nil {
			err = errors.Join(err, fmt.Errorf("stop %s: %w", a.comps[i].Name(), e))
		}
	}
	return err
}
