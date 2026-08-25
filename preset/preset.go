// Package preset holds go-boot's Presets. There is exactly one in v1, plus
// its tracing twin in goboot/preset/traced.
//
// A Preset is one function that wires several Starters with defaults. It has
// NO options — no flags, no negation config keys, no middle setting. To
// change what it wires you copy the body of Full into your own main and edit
// it. The copy is not a fallback, it is the supported escape hatch, and it
// ships as compiling code in examples/full/explicit.go so the build keeps it
// honest.
//
// # What a Preset is for
//
// Not the line count. The argument that carries a Preset is the upgrade path:
// wiring held in a Preset gets fixed by `go get -u`, wiring held in your own
// main does not. If go-boot later learns that a fourth middleware belongs in
// the default set, every Preset user picks it up by bumping a version.
//
// The other side of that trade has to be said as plainly: COPYING THE BODY
// FORFEITS THE UPGRADE PATH. A user who copies has chosen to own their
// wiring, exactly as if they had never used a Preset. That is the whole
// trade-off and it is not softened here.
//
// # What stays in main
//
// Config loading. goboot.Load sits in main next to the service's own config
// struct, because every real service owns at least one config key of its own
// and a Preset that loaded config for you would break on the first one.
//
// And grpc.DefaultOptions(app.Log), because the mount names the user's
// generated package. So the Preset does NOT protect anyone from forgetting
// the error-sanitising interceptor; leave those options off and a bare error
// reaches the caller verbatim, password and all.
//
// # What this package deliberately does not import
//
// goboot/trace. OTel is +9.69 MB stripped and +23 modules, so a service that
// runs no collector must not link it. The tracing twin lives in
// goboot/preset/traced and is a copy, not a wrapper.
package preset

import (
	"database/sql"
	"io/fs"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/db"
	"github.com/squall-chua/go-boot/web"
)

// Config is every section a Preset wires, and nothing else. A service embeds
// it with `yaml:",inline"` and adds its own keys beside it.
type Config struct {
	Log       goboot.LogConfig       `yaml:"log"`
	Lifecycle goboot.LifecycleConfig `yaml:"lifecycle"`
	Web       web.Config             `yaml:"web"`
	DB        db.Config              `yaml:"db"`
	Actuator  actuator.Config        `yaml:"actuator"`
}

// App is what a Preset hands back. The embedded *goboot.App is the escape
// hatch that matters: app.Add(myConsumer) still works, and so do app.Run,
// app.Log and app.Level.
//
// There are three fields and there is no fourth. No Actuator field: it is
// already mounted, and the only thing left to do with it is nothing. What
// the struct offers is ADD — app.Add, app.Web.Handle, app.Web.Use — never
// remove and never reorder. Use appends, so anything added afterwards lands
// innermost. To remove or reorder, copy the body.
type App struct {
	*goboot.App             // Run, Log, Level, Add
	Web         *web.Server // route mounting
	DB          *sql.DB     // for the Service Layer
}

// Full wires every v1 Starter except tracing: the App, the database pool, the
// Actuator, the HTTP Server and the default middleware. migrations may be
// nil, which leaves a pool with no schema of its own. Nothing is started yet
// — that is app.Run.
func Full(cfg Config, migrations fs.FS) (*App, error) {
	app, err := goboot.New(goboot.Config{Log: cfg.Log, Lifecycle: cfg.Lifecycle})
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
	app.Add(act, database, srv)                // the order here is ignored; Tier decides
	return &App{App: app, Web: srv, DB: pool}, nil
}
