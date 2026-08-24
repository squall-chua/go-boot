package main

// EXPLICIT FORM: exactly what preset.HTTP(":8080") expands to. Copy this body
// into main and the Preset disappears.

import (
	"context"
	"net/http"
	"os"

	"goboot-prototype/goboot"
	gbhttp "goboot-prototype/goboot/http"
)

func mainExplicit() {
	app, err := goboot.New(goboot.LogConfig{})
	if err != nil {
		panic(err)
	}
	srv := gbhttp.New(gbhttp.Config{Addr: ":8080"}, app.Log)
	app.Add(srv)

	srv.Handle("GET /hello/{name}", http.HandlerFunc(hello))
	if err := app.Run(context.Background()); err != nil {
		app.Log.Error("exit", "err", err)
		os.Exit(1)
	}
}
