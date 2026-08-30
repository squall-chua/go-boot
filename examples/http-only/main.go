// Command http-only is the smallest useful go-boot service: one Transport, the
// default middleware, and nothing else.
//
// There is no Preset for this shape. One was written and deleted, because it
// came out LONGER than the body it replaced. The short path is short because
// the library is small.
//
// The layout is the one `goboot new` writes, minus the parts a service with no
// database has no use for: the feature is a package under internal/, its routes
// live with it, and routes.go below lists the features. See ADR 0015.
//
// CI builds it through go build ./..., so the example cannot rot.
package main

import (
	"context"
	"log/slog"
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
	srv, err := web.New(web.Config{Addr: ":8080"}, app.Log)
	if err != nil {
		return err
	}
	srv.Use(web.DefaultMiddleware(app.Log)...)
	app.Add(srv)

	addRoutes(srv)

	return app.Run(ctx)
}
