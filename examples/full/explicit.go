package main

// EXPLICIT FORM: what traced.Full(cfg.Config, migrations()) expands to,
// written out in main, plus the one line no Preset can wire. This file IS the
// copy-the-body escape hatch, and CI compiles it and a test drives it.
//
// Copying costs you the upgrade path, and that is the whole trade. A fix
// go-boot makes to the wiring above reaches a Preset user through
// `go get -u`; it never reaches these lines. Copy when you must reorder or
// remove something, knowing you have chosen to own the wiring.
//
// What it buys is the goboot/web/metrics line below. Assertion 2 of
// .github/check-imports.sh forbids goboot/preset from reaching that package,
// so traced.Full cannot wire it and no edit to traced.Full ever will. The
// difference between the two forms in this directory is the whole answer to
// "what does copying the body get me".

import (
	"context"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/db"
	"github.com/squall-chua/go-boot/trace"
	"github.com/squall-chua/go-boot/web"
	"github.com/squall-chua/go-boot/web/metrics"
)

func runExplicit(ctx context.Context) error {
	var cfg config
	if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
		return err
	}
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		return err
	}
	pool, database, err := db.New(cfg.DB, app.Log, migrations())
	if err != nil {
		return err
	}
	tracer, err := trace.New(cfg.Trace, app.Log)
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
	// trace.DefaultMiddleware is five entries, not web's three; see
	// goboot/trace. Append to it rather than calling Use a second time: Use
	// appends, so a second call cannot reorder what the first one added, and
	// a traced service that copies the plain web.DefaultMiddleware line from
	// docs/spec.md 4.3 silently drops tracing. This is that line, compiled.
	srv.Use(append(trace.DefaultMiddleware(app.Log), metrics.Middleware)...)
	act.MountOn(srv)                    // forget this and there is no /readyz
	app.Add(act, tracer, database, srv) // the order here is ignored; Tier decides

	// Identical in both forms, and so is the pgx blank import in main.go.
	// Everything above this line is the wiring; everything the service DOES is
	// behind this one call.
	addRoutes(srv, pool, app.Log, cfg)

	return app.Run(ctx)
}
