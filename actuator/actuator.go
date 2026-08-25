// Package actuator serves the operational endpoints, and metrics answer 404
// until "metrics" is named in actuator.expose. The whitelist defaults to
// livez, readyz and info; anything not named is never registered, so a wrong
// Ingress rule has nothing to leak (ADR 0003).
//
// The endpoints are livez, readyz, info, metrics, loglevel and pprof. By
// default they live on the application's own port under /actuator, the way
// Spring Boot does when management.server.port is unset. Setting
// actuator.addr moves every one of them to a private listener the Actuator
// binds itself.
//
// These are deliberate omissions, so nobody files them as gaps. There is no
// effective-config endpoint, because it holds the database password. There is
// no discovery index at /actuator: the list of endpoints is the paragraph
// above, and Spring's index exists to find endpoints auto-configuration
// registered behind your back. There are no health groups, only the two fixed
// sets, liveness and readiness. There is no /shutdown, because a remote kill
// switch on a public port is not an ops tool. And there are no thread or heap
// dump endpoints, because pprof covers the same ground.
package actuator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/squall-chua/go-boot"
)

// Config is the actuator section.
type Config struct {
	// Addr moves the Actuator to a private listener it owns. Empty means it
	// shares the application's port under /actuator.
	Addr string `yaml:"addr"`
	// Expose is a whitelist. Anything not named is never registered and
	// answers 404. Default: livez, readyz, info. An entry naming an endpoint
	// that does not exist is a startup error.
	Expose []string `yaml:"expose"`
	// ShowDetails adds the detail to the readiness body when it is "always".
	// Anything else, "never" included, leaves the body bare. Spring's key
	// name.
	//
	// Beware: "always" prints the error text of every failing Check on an
	// endpoint the whitelist keeps public, and a database error text carries
	// the database host. Whichever way it is set, a failing Check is logged
	// at WARN with the full detail, so "never" costs an operator nothing that
	// the log does not already carry.
	ShowDetails string `yaml:"showDetails"`
}

// Handler is the structural interface MountOn takes. *web.Server satisfies
// it, so the Actuator imports no Starter.
type Handler interface {
	Handle(pattern string, h http.Handler)
}

// Check is a readiness test. Liveness never runs one: a liveness test that
// touches a dependency turns a database outage into a restart storm.
type Check func(context.Context) error

// Actuator is the Component that owns the operational endpoints.
type Actuator struct {
	cfg  Config
	app  *goboot.App
	own  *http.Server // only when cfg.Addr is set
	ln   net.Listener // the private listener, once Start has bound it
	errc chan error

	// checks is written once, in Start, and read by the handlers after that.
	// Nothing registers a Check later, so there is no lock.
	checks   map[string]Check
	draining atomic.Bool
}

// New takes the App because it pulls the App's Checks, logger and log level
// out of it.
//
// It validates its own config and returns an error if the expose whitelist
// names an endpoint that does not exist; Start reports only what needs the
// world, which here is binding the private listener. That split is the
// convention of docs/spec.md 4.0 and ADR 0011.
func New(cfg Config, app *goboot.App) (*Actuator, error) {
	if len(cfg.Expose) == 0 {
		cfg.Expose = []string{"livez", "readyz", "info"}
	}
	a := &Actuator{cfg: cfg, app: app, errc: make(chan error, 1)}
	if err := a.validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// MountOn registers the whitelisted endpoints. It is one line in main that is
// correct in both port modes: with actuator.addr set it registers nothing on
// h and prepares the private listener that Start binds instead.
func (a *Actuator) MountOn(h Handler) {
	if a.cfg.Addr != "" {
		mux := http.NewServeMux()
		a.routes(mux)
		a.own = &http.Server{
			Addr:    a.cfg.Addr,
			Handler: mux,
			// The same default the web Starter uses. Nothing else is in
			// front of this listener to hold a slow header off.
			ReadHeaderTimeout: 5 * time.Second,
		}
		return
	}
	a.routes(h)
}

// routes registers whatever the whitelist names. New has already rejected an
// entry that names nothing, so nothing here is skipped silently.
func (a *Actuator) routes(h Handler) {
	endpoints := a.endpoints()
	done := make(map[string]bool, len(a.cfg.Expose))
	for _, name := range a.cfg.Expose {
		// A name listed twice is registered once. net/http panics on a
		// pattern registered twice, and a repeated YAML entry must not take
		// the process down.
		if register, ok := endpoints[name]; ok && !done[name] {
			done[name] = true
			register(h)
		}
	}
}

// endpoints is the whole list of endpoint names, and the only one: routes
// registers from it and validate checks against it, so a name cannot exist in
// one place and not the other.
//
// /livez and /readyz answer at the root as well, because Kubernetes is what
// reads them and those are the names its own components use. One whitelist
// entry governs both paths: they are one endpoint with two names.
func (a *Actuator) endpoints() map[string]func(Handler) {
	return map[string]func(Handler){
		"livez": func(h Handler) {
			h.Handle("GET /livez", http.HandlerFunc(a.livez))
			h.Handle("GET /actuator/livez", http.HandlerFunc(a.livez))
		},
		"readyz": func(h Handler) {
			h.Handle("GET /readyz", http.HandlerFunc(a.readyz))
			h.Handle("GET /actuator/readyz", http.HandlerFunc(a.readyz))
		},
		"info": func(h Handler) {
			h.Handle("GET /actuator/info", http.HandlerFunc(info))
		},
		"metrics": func(h Handler) {
			// promhttp.Handler reads prometheus.DefaultGatherer, which
			// already carries 38 metric families with no registration code.
			// No Prometheus type appears in go-boot's public API.
			h.Handle("GET /actuator/metrics", promhttp.Handler())
		},
		"loglevel": func(h Handler) {
			// No method in the pattern: the handler answers GET and PUT, and
			// says so in Allow on anything else.
			h.Handle("/actuator/loglevel", logLevel(a.app.Level))
		},
		"pprof": func(h Handler) {
			h.Handle("GET /actuator/pprof/", pprofIndex())
			h.Handle("GET /actuator/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
			h.Handle("GET /actuator/pprof/profile", http.HandlerFunc(pprof.Profile))
			h.Handle("GET /actuator/pprof/trace", http.HandlerFunc(pprof.Trace))
			// Symbol is the one that also takes POST: go tool pprof posts the
			// addresses it wants resolved, and a GET-only route would break
			// symbolisation. Two patterns, not a method-less one, because
			// net/http rejects a method-less route sitting under a GET prefix.
			h.Handle("GET /actuator/pprof/symbol", http.HandlerFunc(pprof.Symbol))
			h.Handle("POST /actuator/pprof/symbol", http.HandlerFunc(pprof.Symbol))
		},
	}
}

// validate rejects a whitelist entry that names no endpoint. A typo in a probe
// path is a failure worth having at boot. It runs in New, not in Start: the
// whitelist is pure config validation and touches nothing outside Config.
// See docs/spec.md 4.0 and ADR 0011.
func (a *Actuator) validate() error {
	endpoints := a.endpoints()
	for _, name := range a.cfg.Expose {
		if _, ok := endpoints[name]; !ok {
			return fmt.Errorf("actuator.expose: no endpoint named %q", name)
		}
	}
	return nil
}

// health is the readiness body. checks is left out unless showDetails says
// always, so the default body is bare.
type health struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// livez answers UP unconditionally, without running a Check, even while a
// Check is failing.
func (a *Actuator) livez(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, health{Status: "UP"})
}

// readyz runs every Check on each request, synchronously: no background
// ticker, no cached result, no staleness window. The Checks get the request
// context, which already carries the probe's real deadline, and the Actuator
// adds no timeout of its own.
func (a *Actuator) readyz(w http.ResponseWriter, r *http.Request) {
	// No Check runs before the App is ready. The Actuator starts first, so at
	// that point it is holding Checks belonging to Components that have not
	// started yet.
	if !a.app.Ready() || a.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, health{Status: "DOWN"})
		return
	}
	out := health{Status: "UP", Checks: make(map[string]string, len(a.checks))}
	code := http.StatusOK
	for name, check := range a.checks {
		if err := check(r.Context()); err != nil {
			// Logged whichever way showDetails is set, so an operator who
			// left it at never still has the full text.
			a.app.Log.Warn("check failed", "check", name, "err", err)
			out.Status, code = "DOWN", http.StatusServiceUnavailable
			out.Checks[name] = "DOWN: " + err.Error()
			continue
		}
		out.Checks[name] = "UP"
	}
	if a.cfg.ShowDetails != "always" {
		out.Checks = nil
	}
	writeJSON(w, code, out)
}

// logLevel reads and writes the running level. The change reaches the live
// logger through the slog.LevelVar the App already handed every handler, so
// the next log line obeys it with no restart.
//
// Both 400s carry text this package wrote, never err.Error(). That is
// docs/spec.md 4.0: what a caller receives is what the handler chose. The
// json decoder and slog.Level between them would otherwise answer with
// "http: request body too large" or slog's own wording, neither of which
// tells the operator what to send instead.
func logLevel(lvl *slog.LevelVar) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]string{"level": lvl.Level().String()})
		case http.MethodPut:
			var body struct {
				Level string `json:"level"`
			}
			// The body is one short level name. Capped here because with
			// actuator.addr set there is no web Starter in front of this
			// handler to cap it.
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
				http.Error(w, `body must be {"level": "..."}`, http.StatusBadRequest)
				return
			}
			var l slog.Level
			if err := l.UnmarshalText([]byte(body.Level)); err != nil {
				http.Error(w, "level must be one of DEBUG, INFO, WARN, ERROR", http.StatusBadRequest)
				return
			}
			lvl.Set(l)
			writeJSON(w, http.StatusOK, map[string]string{"level": lvl.Level().String()})
		default:
			w.Header().Set("Allow", "GET, PUT")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// info reports the Go version and what the VCS stamped into the binary. A
// binary built outside a checkout carries no revision, and then those keys
// are simply absent.
func info(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	if bi, ok := debug.ReadBuildInfo(); ok {
		out["go"] = bi.GoVersion
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				out["revision"] = s.Value
			case "vcs.time":
				out["time"] = s.Value
			case "vcs.modified":
				out["dirty"] = s.Value
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// pprofIndex serves the profiles and the listing under /actuator/pprof/.
// pprof.Index only finds a profile name under the /debug/pprof/ prefix it was
// written for, so the name is read here instead. The links on its HTML
// listing are relative, so they keep working under the go-boot prefix.
func pprofIndex() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name := strings.TrimPrefix(r.URL.Path, "/actuator/pprof/"); name != "" {
			pprof.Handler(name).ServeHTTP(w, r)
			return
		}
		pprof.Index(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Name is the Component name.
func (a *Actuator) Name() string { return "actuator" }

// Tier starts the Actuator first and stops it last, so /readyz is answering
// before anything else is up and still answering while the rest tears down.
func (a *Actuator) Tier() goboot.Tier { return goboot.TierObserve }

// Drain turns /readyz to 503. It runs in start order, before any Stop, so the
// 503 lands before anything is torn down. The App has already turned Ready()
// false by then, so this flag is what keeps the promise for a caller who
// drains the Actuator itself.
func (a *Actuator) Drain(ctx context.Context) { a.draining.Store(true) }

// Start pulls every Component's Check. Nothing pushes: main writes no
// registration line at all.
func (a *Actuator) Start(ctx context.Context) (<-chan error, error) {
	a.checks = make(map[string]Check)
	for name, c := range a.app.Checks() {
		a.checks[name] = c.Check
	}
	if a.own == nil {
		return nil, nil // it lives on the application's server; it cannot die
	}
	ln, err := net.Listen("tcp", a.own.Addr)
	if err != nil {
		return nil, err
	}
	a.ln = ln
	go func() {
		err := a.own.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		a.errc <- err
	}()
	a.app.Log.Info("listening", "component", a.Name(), "addr", ln.Addr().String())
	return a.errc, nil
}

// Stop shuts the private listener down. With no private listener there is
// nothing to stop: the application's own server owns the routes.
func (a *Actuator) Stop(ctx context.Context) error {
	if a.own == nil {
		return nil
	}
	return a.own.Shutdown(ctx)
}
