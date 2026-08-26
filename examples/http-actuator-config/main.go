// Command http-actuator-config is the realistic default: one Transport, an
// Actuator, and the service's own config key alongside go-boot's.
//
// There is no Preset for this shape either. act.MountOn(srv) is the one line
// that decides where the operational endpoints live, and it is the same line
// whether actuator.addr is set or not.
//
// Try it:
//
//	go run ./examples/http-actuator-config
//	curl localhost:8080/hello/world
//	curl localhost:8080/readyz
//	curl localhost:8080/actuator/metrics
//	curl -X PUT localhost:8080/actuator/loglevel -d '{"level":"DEBUG"}'
//
// CI builds it through go build ./..., so the example cannot rot.
package main

import (
	"context"
	"embed"
	"log/slog"
	"net/http"
	"os"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/web"
)

//go:embed app.yaml
var defaultsFS embed.FS

// config is the service's own config struct: go-boot's sections, plus its
// own key. go-boot never sees the whole struct, only the sections it is
// handed.
type config struct {
	Log       goboot.LogConfig       `yaml:"log"`
	Lifecycle goboot.LifecycleConfig `yaml:"lifecycle"`
	Web       web.Config             `yaml:"web"`
	Actuator  actuator.Config        `yaml:"actuator"`
	Greeting  string                 `yaml:"greeting"`
}

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg := config{Greeting: "hello"} // the struct pre-fill IS the defaults layer
	if err := goboot.Load(defaultsFS, "app.yaml", "ORDERS_", &cfg); err != nil {
		return err
	}
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
	if err != nil {
		return err
	}
	act, err := actuator.New(cfg.Actuator, app)
	if err != nil {
		return err
	}
	srv, err := web.New(cfg.Web, app.Log)
	if err != nil {
		return err
	}
	srv.Use(web.DefaultMiddleware(app.Log)...)
	act.MountOn(srv)
	app.Add(act, srv)

	srv.Handle("GET /hello/{name}", greet(cfg.Greeting))

	return app.Run(ctx)
}

// greet stands in for the Service Layer. It reads the service's own config
// key, which go-boot loaded in the same pass as its own.
func greet(greeting string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		// Visible after PUT /actuator/loglevel, with no restart.
		goboot.LoggerFrom(r.Context()).Debug("greeting", "name", name)
		web.WriteJSON(w, http.StatusOK, map[string]string{"greeting": greeting, "name": name})
	})
}
