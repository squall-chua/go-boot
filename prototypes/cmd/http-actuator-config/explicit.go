package main

// EXPLICIT FORM: exactly what preset.WebWith(cfg.Config) expands to.

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/actuator"
	gbhttp "goboot-prototype/goboot/http"
)

func mainExplicit() {
	cfg := config{Greeting: "hello"}
	if err := goboot.Load("app.yaml", "GB_", &cfg); err != nil {
		panic(err)
	}
	app, err := goboot.New(cfg.Log)
	if err != nil {
		panic(err)
	}
	act := actuator.New(cfg.Actuator, app.Log, app.Level)
	srv := gbhttp.New(cfg.HTTP, app.Log)
	app.Add(act, srv) // Actuator up first, down last

	srv.Handle("GET /hello/{name}", greet(cfg.Greeting, app.Log))
	act.Ready("self", func(context.Context) error { return nil })
	if err := app.Run(context.Background()); err != nil {
		app.Log.Error("exit", "err", err)
		os.Exit(1)
	}
}

// greet stands in for the Service Layer. Shared by both forms.
func greet(greeting string, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		log.Debug("greeting", "name", name) // visible after PUT /loglevel
		w.Write([]byte(greeting + " " + name + "\n"))
	})
}
