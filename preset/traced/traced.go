// Package traced is the tracing twin of goboot/preset.
//
// It is a separate package because Go links by import: tracing is +9.69 MB
// stripped and +23 modules, and a service that runs no collector must not pay
// it. A `preset.WithTracing()` option could not have worked — naming
// goboot/trace inside goboot/preset makes every Preset user pay whether the
// option is set or not. This is the same rule CONTEXT.md states for Starters,
// applied to Presets.
//
// # Why the body is a copy, not a wrapper
//
// Full CANNOT wrap preset.Full. By the time preset.Full has returned, the
// middleware order is already fixed: Use appends, so calling
// srv.Use(trace.Middleware()) afterwards puts the span INSIDE Logging, where
// the access-log line cannot carry the trace ID. The correct order is
// RequestID, trace, Logging, Recovery, and only one Use call can produce it.
//
// So there are two near-identical bodies, deliberately. Everything
// goboot/preset says about the upgrade path, about copying the body costing
// it, and about grpc.DefaultOptions staying in main is true here word for
// word.
package traced

import (
	"io/fs"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/db"
	"github.com/squall-chua/go-boot/preset"
	"github.com/squall-chua/go-boot/trace"
	"github.com/squall-chua/go-boot/web"
)

// Config is preset.Config plus the trace section. A service embeds this one
// with `yaml:",inline"` and adds its own keys beside it.
type Config struct {
	preset.Config `yaml:",inline"`
	Trace         trace.Config `yaml:"trace"`
}

// Full wires every v1 Starter, tracing included. It hands back a *preset.App:
// the two Presets return the same struct, so a service can move between them
// by changing one import and one call.
func Full(cfg Config, migrations fs.FS) (*preset.App, error) {
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		return nil, err
	}
	pool, database, err := db.New(cfg.DB, app.Log, migrations)
	if err != nil {
		return nil, err
	}
	tracer, err := trace.New(cfg.Trace, app.Log)
	if err != nil {
		return nil, err
	}
	act := actuator.New(cfg.Actuator, app)
	srv := web.New(cfg.Web, app.Log)
	srv.Use(trace.DefaultMiddleware(app.Log)...) // five entries, not web's three; see goboot/trace
	act.MountOn(srv)                             // forget this and there is no /readyz
	app.Add(act, tracer, database, srv)          // the order here is ignored; Tier decides
	return &preset.App{App: app, Web: srv, DB: pool}, nil
}
