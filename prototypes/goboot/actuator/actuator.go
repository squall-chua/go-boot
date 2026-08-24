// Package actuator is a THROWAWAY stub of the go-boot Actuator Starter:
// health, readiness, metrics, runtime log level, build info.
package actuator

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	gbhttp "goboot-prototype/goboot/http"
)

type Config struct {
	Addr string `yaml:"addr"` // own port, so /metrics is not public
}

// Check is a readiness probe. Liveness never runs checks: a liveness probe that
// touches a dependency turns a database outage into a restart loop.
type Check func(context.Context) error

type Actuator struct {
	srv      *gbhttp.Server
	Registry *prometheus.Registry

	mu       sync.Mutex
	checks   map[string]Check
	draining atomic.Bool
}

func New(cfg Config, log *slog.Logger, lvl *slog.LevelVar) *Actuator {
	if cfg.Addr == "" {
		cfg.Addr = ":9090"
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	a := &Actuator{
		srv:      gbhttp.Named("actuator", gbhttp.Config{Addr: cfg.Addr}, log),
		Registry: reg,
		checks:   map[string]Check{},
	}
	a.srv.Handle("GET /livez", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "UP"})
	}))
	a.srv.Handle("GET /readyz", http.HandlerFunc(a.readyz))
	a.srv.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	a.srv.Handle("/loglevel", logLevel(lvl))
	a.srv.Handle("GET /info", http.HandlerFunc(buildInfo))
	return a
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
	if a.draining.Load() {
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

func (a *Actuator) Name() string                    { return "actuator" }
func (a *Actuator) Start(ctx context.Context) error { return a.srv.Start(ctx) }
func (a *Actuator) Stop(ctx context.Context) error  { return a.srv.Stop(ctx) }
func (a *Actuator) Addr() string                    { return a.srv.Addr() }
func (a *Actuator) Drain(ctx context.Context)       { a.draining.Store(true) }
