// Command myservice is what `goboot new` writes. It is yours from the first
// second: there are no options to set, only lines to edit.
//
//	go mod tidy
//	buf generate       # writes internal/gen from proto/
//	go run . migrate   # applies migrations/ against the database in app.yaml
//	go run .           # serves on :8080
//	curl localhost:8080/hello/world
//	curl localhost:8080/readyz
//
// It does not compile until `buf generate` has run once: internal/gen is not
// checked in, and nothing in go-boot generates it for you.
//
// The one call that wires the service is preset.Full. Copying its body into
// this file is the supported way to change what it wires — and it costs the
// upgrade path, because wiring held in a Preset gets fixed by `go get -u` and
// wiring held here does not. To add tracing, swap preset for preset/traced.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib" // you bring your own driver

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/db"
	"github.com/squall-chua/go-boot/grpc"
	"github.com/squall-chua/go-boot/preset"

	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
)

//go:embed app.yaml
var defaultsFS embed.FS

//go:embed migrations/*.sql
var migrationsFS embed.FS

// config is the Preset's config embedded inline, plus this service's own key.
// go-boot never sees the whole struct, only the sections it is handed, which
// is why loading stays here rather than inside the Preset.
type config struct {
	preset.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}

func main() {
	run := serve
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		run = migrate
	}
	if err := run(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context) error {
	cfg, err := load()
	if err != nil {
		return err
	}
	app, err := preset.Full(cfg.Config, migrations())
	if err != nil {
		return err
	}

	svc := &greeter{db: app.DB, greeting: cfg.Greeting}
	app.Web.Handle("GET /hello/{name}", httpGreet(svc))
	// gRPC shares the HTTP listener, so there is no second port. The mount
	// names your OWN generated package, which is why no Preset can wire it.
	// Leave grpc.DefaultOptions off and the error sanitiser goes with it.
	app.Web.Handle(greetv1connect.NewGreetServiceHandler(&grpcGreeter{svc}, grpc.DefaultOptions(app.Log)...))

	return app.Run(ctx)
}

// migrate is the `myservice migrate` subcommand. There is no goboot migrate
// command and there could not have been: migrations live in the embed.FS
// above, which a generic go-boot binary can never see.
//
// Run it as a Kubernetes Job from the SAME image as the pods, and let that Job
// finish BEFORE the rollout starts. A service refuses to start on a pending
// migration, so a rollout that overtakes its Job crashloops until the Job
// lands.
func migrate(ctx context.Context) error {
	cfg, err := load()
	if err != nil {
		return err
	}
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		return err
	}
	pool, _, err := db.New(cfg.DB, app.Log, migrations())
	if err != nil {
		return err
	}
	defer pool.Close()

	// NewProvider is the one place the session lock is wired, and serving
	// calls it too, so the two cannot disagree about locking.
	provider, err := db.NewProvider(pool, cfg.DB.Driver, migrations(), app.Log)
	if err != nil {
		return err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return err
	}
	app.Log.Info("migrations applied", "count", len(results))
	return nil
}

// load reads app.yaml, then a file of the same name on disk, then MYSERVICE_
// environment variables. The last layer wins, which is where the database
// password belongs.
func load() (config, error) {
	cfg := config{Greeting: "hello"} // the struct pre-fill IS the defaults layer
	err := goboot.Load(defaultsFS, "app.yaml", "MYSERVICE_", &cfg)
	return cfg, err
}

// migrations roots the embed.FS at the SQL files, which is the shape the
// database Starter wants.
func migrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		panic(err) // a bad embed pattern is a build-time mistake, not a runtime one
	}
	return sub
}
