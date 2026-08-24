package main

// EXPLICIT FORM: exactly what service.New("app.yaml", "GB_", migrations())
// expands to.

import (
	"context"
	"os"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/actuator"
	gbdb "goboot-prototype/goboot/db"
	gbgrpc "goboot-prototype/goboot/grpc"
	gbhttp "goboot-prototype/goboot/http"
	"goboot-prototype/goboot/preset/service"
	"goboot-prototype/internal/gen/greet/v1/greetv1connect"
)

func mainExplicit() {
	var cfg service.Config
	if err := goboot.Load("app.yaml", "GB_", &cfg); err != nil {
		panic(err)
	}
	app, err := goboot.New(cfg.Log)
	if err != nil {
		panic(err)
	}
	database, err := gbdb.New(cfg.DB, app.Log, migrations())
	if err != nil {
		panic(err)
	}
	act := actuator.New(cfg.Actuator, app.Log, app.Level)
	act.Ready("db", database.Check)
	httpSrv := gbhttp.New(cfg.HTTP, app.Log)
	grpcSrv := gbgrpc.New(cfg.GRPC, app.Log)
	app.Add(act, database, httpSrv, grpcSrv)

	svc := &greeter{db: database.DB}
	httpSrv.Handle("GET /hello/{name}", httpGreet(svc))
	grpcSrv.Mount(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}))
	if err := app.Run(context.Background()); err != nil {
		app.Log.Error("exit", "err", err)
		os.Exit(1)
	}
}
