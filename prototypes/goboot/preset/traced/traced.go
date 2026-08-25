// Package traced is the tracing twin of goboot/preset. It exists as a separate
// package because Go links by import: OTel costs +9.4 MB stripped and 19
// indirect modules, and a service that runs no collector must not pay it. Same
// rule CONTEXT.md states for Starters, applied to Presets.
//
// The body is a COPY of preset.Full, not a wrapper. It cannot wrap: Use
// appends, so adding the trace middleware after preset.Full has run would put
// it innermost, which is the wrong position.
package traced

import (
	"io/fs"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/actuator"
	"goboot-prototype/goboot/db"
	"goboot-prototype/goboot/preset"
	"goboot-prototype/goboot/trace"
	"goboot-prototype/goboot/web"
)

type Config struct {
	preset.Config `yaml:",inline"`
	Trace         trace.Config `yaml:"trace"`
}

// Full wires every v1 Starter, tracing included.
func Full(cfg Config, migrations fs.FS) (*preset.App, error) {
	app, err := goboot.New(cfg.Log)
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
	srv.Use(trace.DefaultMiddleware(app.Log)...) // RequestID, trace, Logging, Recovery
	act.MountOn(srv)
	app.Add(act, tracer, database, srv)
	return &preset.App{App: app, Web: srv, DB: pool}, nil
}
