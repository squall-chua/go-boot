// Command full is the whole v1 surface — HTTP + gRPC + database + Actuator:
// PRESET FORM. Needs Postgres to run; it is compile-only in this prototype.
// Run the explicit form this Preset expands to with: ./full explicit
package main

import (
	"context"
	"embed"
	"io/fs"
	"os"

	"goboot-prototype/goboot/preset/service"
	"goboot-prototype/internal/gen/greet/v1/greetv1connect"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "explicit" {
		mainExplicit()
		return
	}

	app, err := service.New("app.yaml", "GB_", migrations())
	if err != nil {
		panic(err)
	}
	svc := &greeter{db: app.DB.DB}
	app.HTTP.Handle("GET /hello/{name}", httpGreet(svc))
	app.GRPC.Mount(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}))
	if err := app.Run(context.Background()); err != nil {
		app.Log.Error("exit", "err", err)
		os.Exit(1)
	}
}

func migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}
