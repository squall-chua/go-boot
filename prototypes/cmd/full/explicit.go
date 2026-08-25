package main

// EXPLICIT FORM: exactly what traced.Full(cfg.Config, migrations()) expands
// to. This file IS the copy-the-body escape hatch, and CI builds it.

import (
	"context"
	"log/slog"
	"os"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/actuator"
	"goboot-prototype/goboot/db"
	"goboot-prototype/goboot/grpc"
	"goboot-prototype/goboot/trace"
	"goboot-prototype/goboot/web"
	"goboot-prototype/internal/gen/greet/v1/greetv1connect"
)

func mainExplicit() {
	if err := runExplicit(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func runExplicit(ctx context.Context) error {
	var cfg config
	if err := goboot.Load(defaultsFS, "app.yaml", "GB_", &cfg); err != nil {
		return err
	}
	app, err := goboot.New(cfg.Log)
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
	srv.Use(trace.DefaultMiddleware(app.Log)...) // RequestID, trace, Logging, Recovery
	act.MountOn(srv)
	app.Add(act, tracer, database, srv)

	svc := &greeter{db: pool, greeting: cfg.Greeting}
	srv.Handle("GET /hello/{name}", httpGreet(svc))
	srv.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))

	return app.Run(ctx)
}
