// Package service is a THROWAWAY stub of go-boot's full-surface Preset:
// config + logger + HTTP Transport + gRPC Transport + database + Actuator.
package service

import (
	"io/fs"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/actuator"
	gbdb "goboot-prototype/goboot/db"
	gbgrpc "goboot-prototype/goboot/grpc"
	gbhttp "goboot-prototype/goboot/http"
	"goboot-prototype/goboot/preset"
)

type Config struct {
	preset.Config `yaml:",inline"`
	GRPC          gbgrpc.Config `yaml:"grpc"`
	DB            gbdb.Config   `yaml:"db"`
}

type App struct {
	*goboot.App
	Cfg      Config
	HTTP     *gbhttp.Server
	GRPC     *gbgrpc.Server
	DB       *gbdb.DB
	Actuator *actuator.Actuator
}

// New loads config and wires every v1 Starter. Nothing is started yet.
func New(configPath, envPrefix string, migrations fs.FS) (*App, error) {
	var cfg Config
	if err := goboot.Load(configPath, envPrefix, &cfg); err != nil {
		return nil, err
	}
	return NewWith(cfg, migrations)
}

// NewWith is the same Preset for a service that owns its config struct.
func NewWith(cfg Config, migrations fs.FS) (*App, error) {
	base, err := goboot.New(cfg.Log)
	if err != nil {
		return nil, err
	}
	database, err := gbdb.New(cfg.DB, base.Log, migrations)
	if err != nil {
		return nil, err
	}
	a := &App{
		App:      base,
		Cfg:      cfg,
		HTTP:     gbhttp.New(cfg.HTTP, base.Log),
		GRPC:     gbgrpc.New(cfg.GRPC, base.Log),
		DB:       database,
		Actuator: actuator.New(cfg.Actuator, base.Log, base.Level),
	}
	a.Actuator.Ready("db", a.DB.Check)
	a.Add(a.Actuator, a.DB, a.HTTP, a.GRPC)
	return a, nil
}
