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
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// LogConfig is the base Starter's slice of config.
type LogConfig struct {
	Level  string `yaml:"level"`  // slog level name; default INFO
	Format string `yaml:"format"` // "text" or "json"; default text
}

// LifecycleConfig is one budget per phase for every Component together, not
// one budget each. A zero field takes the default written beside it, and 5s
// plus 10s fits inside Kubernetes' 30s default grace period with room spare.
type LifecycleConfig struct {
	StartTimeout time.Duration `yaml:"startTimeout"` // the whole start sequence; default 30s
	DrainDelay   time.Duration `yaml:"drainDelay"`   // the pause after Drain, so a load balancer sees the 503; default 5s
	StopTimeout  time.Duration `yaml:"stopTimeout"`  // the whole stop sequence; default 10s
}

// Config is what New takes. Later Starters add their own sections beside it,
// and Load fills the whole struct from a file and the environment.
type Config struct {
	Log       LogConfig       `yaml:"log"`
	Lifecycle LifecycleConfig `yaml:"lifecycle"`
}

// App holds the logger, the runtime log level and the Component list. One
// goroutine drives it: Start then Stop, or Run on its own.
type App struct {
	Log   *slog.Logger
	Level *slog.LevelVar // the Actuator's /actuator/loglevel writes this

	life    LifecycleConfig
	comps   []Component
	order   []Component // comps sorted by Tier, fixed when Start runs
	started int         // how many of order are running
	ready   atomic.Bool

	death     chan error    // the first Component death, from watch
	stopWatch chan struct{} // closed when shutdown begins, to end the watchers
	watchMu   sync.Mutex    // so two Stops cannot close stopWatch twice
}

// New builds the logger and the runtime-settable level. It returns an error
// rather than panicking on a log level it cannot parse.
func New(cfg Config) (*App, error) {
	lvl := new(slog.LevelVar)
	if cfg.Log.Level != "" {
		if err := lvl.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
			return nil, fmt.Errorf("log.level: %q is not a level: %w", cfg.Log.Level, err)
		}
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if cfg.Log.Format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	life := cfg.Lifecycle
	if life.StartTimeout == 0 {
		life.StartTimeout = 30 * time.Second
	}
	if life.DrainDelay == 0 {
		life.DrainDelay = 5 * time.Second
	}
	if life.StopTimeout == 0 {
		life.StopTimeout = 10 * time.Second
	}
	return &App{Log: slog.New(h), Level: lvl, life: life}, nil
}

// Add appends Components. The order given is ignored; Tier decides.
func (a *App) Add(c ...Component) { a.comps = append(a.comps, c...) }

// Ready reports whether every Component has started. It turns false again the
// moment shutdown begins, so /readyz answers 503 before anything is torn down.
func (a *App) Ready() bool { return a.ready.Load() }

// Start starts every Component from the lowest Tier to the highest, with the
// whole sequence inside one StartTimeout. It takes no signals, so a test can
// drive it directly. On a failure it stops the ones that did start, in
// reverse, and returns the start error joined with any stop errors.
func (a *App) Start(ctx context.Context) error {
	if err := a.rejectDuplicateNames(); err != nil {
		return err
	}
	a.order = slices.Clone(a.comps)
	slices.SortStableFunc(a.order, func(x, y Component) int {
		return cmp.Compare(x.Tier(), y.Tier())
	})
	a.death = make(chan error, 1)
	a.stopWatch = make(chan struct{})

	startCtx, cancel := context.WithTimeout(ctx, a.life.StartTimeout)
	defer cancel()

	for _, c := range a.order {
		deathc, err := c.Start(startCtx)
		if err != nil {
			err = fmt.Errorf("start %s: %w", c.Name(), err)
			// No drain and no drain delay: the pod never passed readiness,
			// so no load balancer is sending it traffic. Stop is given a
			// fresh context because ctx may be the one that just ran out.
			a.beginShutdown()
			return errors.Join(err, a.stopStarted(context.WithoutCancel(ctx)))
		}
		a.started++
		a.watch(c.Name(), deathc)
		a.Log.Info("started", "component", c.Name(), "tier", c.Tier())
	}
	a.ready.Store(true)
	return nil
}

// rejectDuplicateNames runs before anything starts. Two Components sharing a
// name would file their Checks under one key in the Actuator and silently
// overwrite each other.
func (a *App) rejectDuplicateNames() error {
	seen := make(map[string]bool, len(a.comps))
	for _, c := range a.comps {
		if seen[c.Name()] {
			return fmt.Errorf("duplicate component name %q", c.Name())
		}
		seen[c.Name()] = true
	}
	return nil
}

// watch forwards the first death on ch to a.death. A Component that cannot
// die once started returns a nil channel, which gets no goroutine at all.
func (a *App) watch(name string, ch <-chan error) {
	if ch == nil {
		return
	}
	death, stop := a.death, a.stopWatch
	go func() {
		select {
		case err := <-ch:
			d := fmt.Errorf("component %s died", name)
			if err != nil {
				d = fmt.Errorf("component %s died: %w", name, err)
			}
			select {
			case death <- d:
			default: // one death is enough to shut the App down
			}
		case <-stop:
		}
	}()
}

// Stop runs the shutdown phases in order: readiness turns false and the death
// channels stop being watched, every Drainer runs in START order, the App
// waits DrainDelay, then every started Component is stopped in reverse inside
// one StopTimeout, joining errors so one bad Stop does not hide the rest. It
// takes no signals.
func (a *App) Stop(ctx context.Context) error {
	a.beginShutdown()
	a.drain(ctx)
	return a.stopStarted(ctx)
}

// beginShutdown is the first moment of shutdown: /readyz starts answering 503,
// and a death arriving from here on is ignored because one is already underway.
func (a *App) beginShutdown() {
	a.ready.Store(false)
	// A second Stop must not close the channel again: spec 1 rules out a
	// panic in go-boot's own code paths.
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	if a.stopWatch != nil {
		close(a.stopWatch)
		a.stopWatch = nil // so a second Stop does not close it twice
	}
}

// drain runs every Drainer in START order — not reverse — then waits
// DrainDelay. Reverse would drain the Actuator last, so the 503 would land
// after the Transports had already let go. Announce first, tear down last.
func (a *App) drain(ctx context.Context) {
	for i := range a.started {
		c := a.order[i]
		if d, ok := c.(Drainer); ok {
			d.Drain(ctx)
			a.Log.Info("drained", "component", c.Name())
		}
	}
	// The delay is what gives a load balancer time to see the 503.
	select {
	case <-time.After(a.life.DrainDelay):
	case <-ctx.Done():
	}
}

// stopStarted stops the started Components in reverse, inside one StopTimeout.
func (a *App) stopStarted(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, a.life.StopTimeout)
	defer cancel()
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

// Run is Start, wait for SIGINT, SIGTERM or a death, then Stop.
func (a *App) Run(ctx context.Context) error {
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	if err := a.Start(sigCtx); err != nil {
		stopSignals()
		return err
	}
	a.Log.Info("running", "pid", os.Getpid())

	// A death is fatal: it comes back joined with whatever Stop reports, so
	// the orchestrator sees why the pod is going away and restarts it.
	var death error
	select {
	case <-sigCtx.Done():
	case death = <-a.death:
		a.Log.Error("component died", "err", death)
	}
	// Hand the signals back to Go's default handling straight away, so a
	// second one kills the process instead of queueing behind a slow stop.
	stopSignals()
	a.Log.Info("shutting down")
	return errors.Join(death, a.Stop(context.WithoutCancel(ctx)))
}

// Checks returns every Component that offers one, keyed by its Name. The
// Actuator pulls these in its own Start, so nothing in main registers a Check
// by hand. Duplicate names are already rejected, so no Check can overwrite
// another.
func (a *App) Checks() map[string]Checker {
	m := make(map[string]Checker)
	for _, c := range a.comps {
		if ch, ok := c.(Checker); ok {
			m[c.Name()] = ch
		}
	}
	return m
}
