package main

// EXPLICIT FORM: exactly what traced.Full(cfg.Config, migrations()) expands
// to, written out in main. This file IS the copy-the-body escape hatch, and
// CI compiles it and a test drives it.
//
// Copying costs you the upgrade path, and that is the whole trade. A fix
// go-boot makes to the wiring above reaches a Preset user through
// `go get -u`; it never reaches these lines. Copy when you must reorder or
// remove something, knowing you have chosen to own the wiring.

import (
	"context"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/db"
	"github.com/squall-chua/go-boot/grpc"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
	"github.com/squall-chua/go-boot/trace"
	"github.com/squall-chua/go-boot/web"
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
	act := actuator.New(cfg.Actuator, app)
	srv := web.New(cfg.Web, app.Log)
	srv.Use(trace.DefaultMiddleware(app.Log)...) // five entries, not web's three; see goboot/trace
	act.MountOn(srv)                             // forget this and there is no /readyz
	app.Add(act, tracer, database, srv)          // the order here is ignored; Tier decides

	svc := &greeter{db: pool, greeting: cfg.Greeting}
	srv.Handle("GET /hello/{name}", httpGreet(svc))
	// Identical in both forms, and so is the pgx blank import in main.go.
	srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))

	return app.Run(ctx)
}
