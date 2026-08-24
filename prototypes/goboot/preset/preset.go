// Package preset is a THROWAWAY stub of go-boot's HTTP-shaped Presets.
//
// Presets CANNOT live in package goboot: the standing rule from #3 says the
// base package must never import a Starter subpackage, and a Preset's whole job
// is importing Starters. They also cannot all live in ONE package, or an
// HTTP-only user would pull gRPC and Postgres through the Preset. Hence this
// package (http + actuator) and preset/service (+ grpc + db).
package preset

import (
	"goboot-prototype/goboot"
	"goboot-prototype/goboot/actuator"
	gbhttp "goboot-prototype/goboot/http"
)

// Config is the go-boot slice of a service's config file.
type Config struct {
	Log      goboot.LogConfig `yaml:"log"`
	HTTP     gbhttp.Config    `yaml:"http"`
	Actuator actuator.Config  `yaml:"actuator"`
}

// App is what a Preset hands back: the base App plus the Starters it wired.
// Nothing is started yet, so main can still mount routes and add Components.
type App struct {
	*goboot.App
	Cfg      Config
	HTTP     *gbhttp.Server
	Actuator *actuator.Actuator // nil from HTTP()
}

// HTTP is the smallest Preset: logger + HTTP Transport. No config file.
func HTTP(addr string) (*App, error) {
	cfg := Config{HTTP: gbhttp.Config{Addr: addr}}
	base, err := goboot.New(cfg.Log)
	if err != nil {
		return nil, err
	}
	a := &App{App: base, Cfg: cfg, HTTP: gbhttp.New(cfg.HTTP, base.Log)}
	a.Add(a.HTTP)
	return a, nil
}

// Web is the realistic default: config file + logger + HTTP Transport + Actuator.
func Web(configPath, envPrefix string) (*App, error) {
	var cfg Config
	if err := goboot.Load(configPath, envPrefix, &cfg); err != nil {
		return nil, err
	}
	return WebWith(cfg)
}

// WebWith is the same Preset for a service that owns its own config struct
// (embedding Config with `yaml:",inline"`) and has already loaded it.
func WebWith(cfg Config) (*App, error) {
	base, err := goboot.New(cfg.Log)
	if err != nil {
		return nil, err
	}
	a := &App{
		App:  base,
		Cfg:  cfg,
		HTTP: gbhttp.New(cfg.HTTP, base.Log),
	}
	a.Actuator = actuator.New(cfg.Actuator, base.Log, base.Level)
	a.Add(a.Actuator, a.HTTP) // Actuator up first, down last
	return a, nil
}
