// Command http-actuator-config is the realistic default: PRESET FORM.
// Run the explicit form this Preset expands to with: ./http-actuator-config explicit
package main

import (
	"context"
	"os"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/preset"
)

// config is the service's own config struct: go-boot's keys inline, plus its own.
type config struct {
	preset.Config `yaml:",inline"`
	Greeting      string `yaml:"greeting"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "explicit" {
		mainExplicit()
		return
	}

	cfg := config{Greeting: "hello"} // struct pre-fill IS the defaults layer
	if err := goboot.Load("app.yaml", "GB_", &cfg); err != nil {
		panic(err)
	}
	app, err := preset.WebWith(cfg.Config)
	if err != nil {
		panic(err)
	}
	app.HTTP.Handle("GET /hello/{name}", greet(cfg.Greeting, app.Log))
	app.Actuator.Ready("self", func(context.Context) error { return nil })
	if err := app.Run(context.Background()); err != nil {
		app.Log.Error("exit", "err", err)
		os.Exit(1)
	}
}
