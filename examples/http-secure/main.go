// Command http-secure is the Security Starter wired the way docs/spec.md 4.7
// documents it: the middleware slice on the server, and authorization at the
// mount, one route at a time.
//
// It does not run without an OAuth2 issuer. `security.jwt` in app.yaml points
// at a placeholder, so point it at a real one — or at a loopback issuer, which
// is the only shape plain http is accepted for.
//
// The layout is the one `goboot new` writes: two features under internal/, each
// owning its own routes, and routes.go beside this file listing them. The scope
// check sits inside the guarded feature's Routes, next to the handler it
// protects. See ADR 0015.
//
// CI builds it through go build ./..., so the wiring in README.md cannot rot.
// That matters more here than in the other examples, because two of the lines
// below have a trap in them and the compiler is what holds them:
//
//   - the middleware line APPENDS to web.DefaultMiddleware. A second Use call
//     would land the security middleware innermost, and splicing it in front
//     would put it outside web.Recovery.
//   - the Actuator is mounted through a wrapper, not through srv, or
//     /actuator/loglevel is world-writable on the shared port.
package main

import (
	"context"
	"embed"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/security"
	"github.com/squall-chua/go-boot/web"
)

//go:embed app.yaml
var defaultsFS embed.FS

type config struct {
	Log       goboot.LogConfig       `yaml:"log"`
	Lifecycle goboot.LifecycleConfig `yaml:"lifecycle"`
	Web       web.Config             `yaml:"web"`
	Actuator  actuator.Config        `yaml:"actuator"`
	Security  security.Config        `yaml:"security"`
}

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var cfg config
	if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
		return err
	}
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		return err
	}
	act, err := actuator.New(cfg.Actuator, app)
	if err != nil {
		return err
	}
	srv, err := web.New(cfg.Web, app.Log)
	if err != nil {
		return err
	}
	sec, err := security.DefaultMiddleware(cfg.Security)
	if err != nil {
		return err
	}
	// APPEND. Use appends, so the first entry listed ends up outermost: this
	// puts the security middleware inside web.Recovery, where a panic in it
	// still becomes a 500 rather than an EOF.
	srv.Use(append(web.DefaultMiddleware(app.Log), sec...)...)

	// The Actuator goes on through a wrapper so its endpoints are guarded.
	// Mounted straight onto srv, PUT /actuator/loglevel would be open to
	// anyone who can reach the port.
	act.MountOn(operators{srv, security.RequireScope("actuator")})
	app.Add(act, srv)

	// One open route and one guarded one. Which is which is decided inside
	// each feature's Routes, at the mount.
	addRoutes(srv)

	return app.Run(ctx)
}

// operators guards the Actuator's endpoints, leaving the probes open.
//
// actuator.Handler is one method, so anything with a Handle can stand in for
// the server — which is what makes this possible without go-boot growing an
// option for it. See docs/spec.md 4.7.
type operators struct {
	srv *web.Server
	mw  web.Middleware
}

// Handle guards everything except liveness and readiness. Kubernetes carries
// no bearer token, so guarding those two would fail every probe and the pod
// would never go ready — the same reason authentication is not a global gate.
func (o operators) Handle(pattern string, h http.Handler) {
	if isProbe(pattern) {
		o.srv.Handle(pattern, h)
		return
	}
	o.srv.Handle(pattern, o.mw(h))
}

// isProbe names the four patterns the Actuator registers for livez and readyz.
func isProbe(pattern string) bool {
	p := pattern[strings.LastIndex(pattern, " ")+1:]
	return p == "/livez" || p == "/readyz" ||
		p == "/actuator/livez" || p == "/actuator/readyz"
}
