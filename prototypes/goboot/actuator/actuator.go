// Package actuator is a THROWAWAY stub of the go-boot Actuator Starter:
// health, readiness, metrics, runtime log level, build info.
package actuator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"goboot-prototype/goboot"
)

type Config struct {
	// Addr moves the Actuator to a private listener. Empty means it shares
	// the application's port under /actuator (#10, ADR 0003).
	Addr string `yaml:"addr"`
	// Expose is a whitelist. Anything not named answers 404, so a wrong
	// Ingress rule has nothing to leak.
	Expose []string `yaml:"expose"` // default livez,readyz,info
}

// Handler is the structural interface MountOn takes. *web.Server satisfies it,
// so the Actuator imports no Starter.
type Handler interface {
	Handle(pattern string, h http.Handler)
}

// Check is a readiness probe. Liveness never runs checks: a liveness probe that
// touches a dependency turns a database outage into a restart loop.
type Check func(context.Context) error

type Actuator struct {
	cfg  Config
	app  *goboot.App
	own  *http.Server // only when cfg.Addr is set
	errc chan error

	mu       sync.Mutex
	checks   map[string]Check
	draining atomic.Bool
}

// New takes the App because it pulls the App's checks, logger and level from
// it. #3's hard rule is why the dependency points this way.
func New(cfg Config, app *goboot.App) *Actuator {
	if len(cfg.Expose) == 0 {
		cfg.Expose = []string{"livez", "readyz", "info"}
	}
	return &Actuator{cfg: cfg, app: app, checks: map[string]Check{}, errc: make(chan error, 1)}
}

// MountOn registers the Actuator's routes on anything with Handle. With
// actuator.addr set it registers nothing and binds its own listener in Start.
func (a *Actuator) MountOn(h Handler) {
	if a.cfg.Addr != "" {
		mux := http.NewServeMux()
		a.routes(mux)
		a.own = &http.Server{Addr: a.cfg.Addr, Handler: mux}
		return
	}
	a.routes(h)
}

func (a *Actuator) routes(h Handler) {
	// /livez and /readyz sit at the root too: Kubernetes names.
	h.Handle("GET /livez", http.HandlerFunc(a.livez))
	h.Handle("GET /readyz", http.HandlerFunc(a.readyz))
	for _, name := range a.cfg.Expose {
		switch name {
		case "livez", "readyz":
		case "metrics":
			// promhttp.Handler reads the DEFAULT registry, which already
			// carries 38 metric families. No Prometheus type in the API.
			h.Handle("GET /actuator/metrics", promhttp.Handler())
		case "loglevel":
			h.Handle("/actuator/loglevel", logLevel(a.app.Level))
		case "info":
			h.Handle("GET /actuator/info", http.HandlerFunc(buildInfo))
		}
	}
}

func (a *Actuator) livez(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "UP"})
}

// Ready registers a readiness check under name.
func (a *Actuator) Ready(name string, c Check) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks[name] = c
}

func (a *Actuator) readyz(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"status": "UP", "checks": map[string]string{}}
	code := http.StatusOK
	if !a.app.Ready() || a.draining.Load() {
		out["status"] = "OUT_OF_SERVICE"
		code = http.StatusServiceUnavailable
	}
	a.mu.Lock()
	checks := make(map[string]Check, len(a.checks))
	for k, v := range a.checks {
		checks[k] = v
	}
	a.mu.Unlock()
	for name, c := range checks {
		if err := c(r.Context()); err != nil {
			out["checks"].(map[string]string)[name] = "DOWN: " + err.Error()
			out["status"] = "DOWN"
			code = http.StatusServiceUnavailable
			continue
		}
		out["checks"].(map[string]string)[name] = "UP"
	}
	writeJSON(w, code, out)
}

// logLevel is the 36-line handler from docs/research/observability.md §3.
func logLevel(lvl *slog.LevelVar) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]string{"level": lvl.Level().String()})
		case http.MethodPut:
			var body struct{ Level string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var l slog.Level
			if err := l.UnmarshalText([]byte(body.Level)); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			lvl.Set(l) // takes effect on the live logger, no rebuild
			writeJSON(w, http.StatusOK, map[string]string{"level": lvl.Level().String()})
		default:
			w.Header().Set("Allow", "GET, PUT")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func buildInfo(w http.ResponseWriter, r *http.Request) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{})
		return
	}
	out := map[string]string{"go": bi.GoVersion, "path": bi.Path}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" || s.Key == "vcs.time" || s.Key == "vcs.modified" {
			out[s.Key] = s.Value
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Actuator) Name() string              { return "actuator" }
func (a *Actuator) Tier() goboot.Tier         { return goboot.TierObserve }
func (a *Actuator) Drain(ctx context.Context) { a.draining.Store(true) }

// Start pulls every Component's Check. Nothing pushes; main writes no
// act.Ready("db", ...) line at all (#8).
func (a *Actuator) Start(ctx context.Context) (<-chan error, error) {
	a.mu.Lock()
	for name, fn := range a.app.Checks() {
		a.checks[name] = Check(fn)
	}
	a.mu.Unlock()
	if a.own == nil {
		return nil, nil // it lives on the application server; it cannot die
	}
	ln, err := net.Listen("tcp", a.own.Addr)
	if err != nil {
		return nil, err
	}
	go func() {
		err := a.own.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		a.errc <- err
	}()
	return a.errc, nil
}

func (a *Actuator) Stop(ctx context.Context) error {
	if a.own == nil {
		return nil
	}
	return a.own.Shutdown(ctx)
}
