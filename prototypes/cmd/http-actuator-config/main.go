// Command http-actuator-config is the realistic default: one Transport, an
// Actuator and the service's own config key. No Preset form — #2 Q21 deleted
// preset.WebWith.
package main

import (
	"context"
	"embed"
	"log/slog"
	"net/http"
	"os"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/actuator"
	"goboot-prototype/goboot/web"
)

//go:embed app.yaml
var defaultsFS embed.FS

// config is the service's own config struct: go-boot's keys, plus its own.
type config struct {
	Log      goboot.LogConfig `yaml:"log"`
	Web      web.Config       `yaml:"web"`
	Actuator actuator.Config  `yaml:"actuator"`
	Greeting string           `yaml:"greeting"`
}

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg := config{Greeting: "hello"} // struct pre-fill IS the defaults layer
	if err := goboot.Load(defaultsFS, "app.yaml", "GB_", &cfg); err != nil {
		return err
	}
	app, err := goboot.New(cfg.Log)
	if err != nil {
		return err
	}
	act := actuator.New(cfg.Actuator, app)
	srv := web.New(cfg.Web, app.Log)
	srv.Use(web.DefaultMiddleware(app.Log)...)
	act.MountOn(srv)
	app.Add(act, srv)

	srv.Handle("GET /hello/{name}", greet(cfg.Greeting, app.Log))

	return app.Run(ctx)
}

// greet stands in for the Service Layer.
func greet(greeting string, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		log.Debug("greeting", "name", name) // visible after PUT /loglevel
		w.Write([]byte(greeting + " " + name + "\n"))
	})
}
