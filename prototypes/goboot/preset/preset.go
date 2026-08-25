// Package preset holds go-boot's Presets. There is exactly one in v1.
//
// A Preset is a single function that wires several Starters with defaults. It
// has NO options: to change it, copy the body of Full into your own main. The
// copy is not a fallback, it is the supported escape hatch — see
// cmd/full/explicit.go, which is that copy, compiled.
//
// This package deliberately does NOT import goboot/trace. OTel costs +9.4 MB
// stripped and 19 indirect modules, so a service that runs no collector must
// not link it. The tracing twin lives in goboot/preset/traced.
package preset

import (
	"database/sql"
	"io/fs"

	"goboot-prototype/goboot"
	"goboot-prototype/goboot/actuator"
	"goboot-prototype/goboot/db"
	"goboot-prototype/goboot/web"
)

type Config struct {
	Log      goboot.LogConfig `yaml:"log"`
	Web      web.Config       `yaml:"web"`
	DB       db.Config        `yaml:"db"`
	Actuator actuator.Config  `yaml:"actuator"`
}

// App is what the Preset hands back. The embedded *goboot.App is the escape
// hatch that matters: app.Add(myConsumer) still works.
type App struct {
	*goboot.App
	Web *web.Server
	DB  *sql.DB
}

// Full wires every v1 Starter except tracing. migrations may be nil.
// Nothing is started yet.
func Full(cfg Config, migrations fs.FS) (*App, error) {
	app, err := goboot.New(cfg.Log)
	if err != nil {
		return nil, err
	}
	pool, database, err := db.New(cfg.DB, app.Log, migrations)
	if err != nil {
		return nil, err
	}
	act := actuator.New(cfg.Actuator, app)
	srv := web.New(cfg.Web, app.Log)
	srv.Use(web.DefaultMiddleware(app.Log)...) // forget this and a panic returns NO response
	act.MountOn(srv)                           // forget this and there is no /readyz
	app.Add(act, database, srv)                // order is ignored; Tier decides
	return &App{App: app, Web: srv, DB: pool}, nil
}
