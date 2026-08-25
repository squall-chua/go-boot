// Command http-only is the smallest useful go-boot service. There is no
// Preset form: #2 Q21 deleted preset.HTTP because it came out LONGER than
// this body. The short path is short because the library is small.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/web"
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := goboot.New(goboot.LogConfig{})
	if err != nil {
		return err
	}
	srv := web.New(web.Config{Addr: ":8080"}, app.Log)
	srv.Use(web.DefaultMiddleware(app.Log)...)
	app.Add(srv)

	srv.Handle("GET /hello/{name}", http.HandlerFunc(hello))

	return app.Run(ctx)
}

// hello stands in for the Service Layer.
func hello(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("hello " + r.PathValue("name") + "\n"))
}
