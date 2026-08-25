// Command http-only is the smallest useful go-boot service: one Transport,
// the default middleware, and nothing else. Eight wiring lines.
//
// There is no Preset for this shape. One was written and deleted, because it
// came out LONGER than the body it replaced. The short path is short because
// the library is small.
//
// CI builds it through go build ./..., so the example cannot rot.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, err := goboot.New(goboot.Config{})
	if err != nil {
		return err
	}
	srv := web.New(web.Config{Addr: ":8080"}, app.Log)
	srv.Use(web.DefaultMiddleware(app.Log)...)
	app.Add(srv)

	srv.Handle("GET /hello/{name}", http.HandlerFunc(hello))

	return app.Run(ctx)
}

// hello stands in for the Service Layer. It is an ordinary http.HandlerFunc:
// go-boot has no handler type of its own, so everything written for net/http
// works here unchanged.
func hello(w http.ResponseWriter, r *http.Request) {
	// The logger is already tagged with this request's ID, so this line and
	// the access-log line for the same request join up.
	goboot.LoggerFrom(r.Context()).Info("greeting", "name", r.PathValue("name"))
	web.WriteJSON(w, http.StatusOK, map[string]string{"hello": r.PathValue("name")})
}
