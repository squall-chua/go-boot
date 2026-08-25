// Command full is the whole v1 surface — HTTP + gRPC + database + Actuator +
// tracing: PRESET FORM. Needs Postgres to run; it is compile-only here.
// Run the explicit form this Preset expands to with: ./full explicit
package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib" // the user brings their own driver

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/grpc"
	"goboot-prototype/goboot/preset/traced"
	"goboot-prototype/internal/gen/greet/v1/greetv1connect"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed app.yaml
var defaultsFS embed.FS

// config embeds the Preset's config and adds the service's own key.
type config struct {
	traced.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "explicit" {
		mainExplicit()
		return
	}
	if err := run(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var cfg config
	if err := goboot.Load(defaultsFS, "app.yaml", "GB_", &cfg); err != nil {
		return err
	}
	app, err := traced.Full(cfg.Config, migrations())
	if err != nil {
		return err
	}

	svc := &greeter{db: app.DB, greeting: cfg.Greeting}
	app.Web.Handle("GET /hello/{name}", httpGreet(svc))
	app.Web.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))

	return app.Run(ctx)
}

func migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}
