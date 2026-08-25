// Command full is the whole v1 surface — HTTP, gRPC, database, Actuator and
// tracing — wired by a Preset. It is the PRESET FORM.
//
// The same directory holds explicit.go, which is exactly what traced.Full
// expands to. Both forms are compiled by CI and one test drives both, because
// copying the body is the only way to change a Preset and a copy that does not
// compile is not an escape hatch.
//
//	go run ./examples/full            # the Preset form, this file
//	go run ./examples/full explicit   # the explicit form, explicit.go
//
// Both need a PostgreSQL. Point db.dsn at one, or set ORDERS_DB__DSN.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib" // the user brings their own driver

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/grpc"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
	"github.com/squall-chua/go-boot/preset/traced"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

//go:embed app.yaml
var defaultsFS embed.FS

// config is the Preset's config embedded inline, plus the service's own key.
// This is why goboot.Load stays in main: a Preset that loaded config for you
// would never see `greeting`.
type config struct {
	traced.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}

func main() {
	form := run
	if len(os.Args) > 1 && os.Args[1] == "explicit" {
		form = runExplicit
	}
	if err := form(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

// run is the Preset form. It is shorter than explicit.go, and that is not the
// argument: the argument is that the wiring inside traced.Full gets fixed by
// `go get -u`, and the wiring copied into explicit.go does not.
func run(ctx context.Context) error {
	var cfg config
	if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
		return err
	}
	app, err := traced.Full(cfg.Config, migrations())
	if err != nil {
		return err
	}

	svc := &greeter{db: app.DB, greeting: cfg.Greeting}
	app.Web.Handle("GET /hello/{name}", httpGreet(svc))
	// grpc.DefaultOptions is here and not inside the Preset, in both forms:
	// the mount names the user's own generated package. Leave it off and the
	// error sanitiser goes with it.
	app.Web.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))

	return app.Run(ctx)
}

// migrations roots the embed.FS at the SQL files, which is the shape both
// forms hand to the database Starter.
func migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // a bad embed pattern is a build-time mistake, not a runtime one
	}
	return sub
}
