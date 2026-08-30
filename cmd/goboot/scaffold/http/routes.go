package main

import (
	"github.com/squall-chua/go-boot/preset"

	// Your own packages get a group to themselves. The Scaffold rewrites these
	// paths on copy, and a rewritten path sorts differently — in their own
	// group they only sort against each other, so the copy stays gofmt-clean.
	//
	// Every feature's sub-packages share the same names, so each one is
	// imported under a prefixed alias. That is the price of a folder per
	// layer, and it is paid here and nowhere else.
	"github.com/squall-chua/go-boot/cmd/goboot/scaffold/http/internal/greeting"
	greetingentity "github.com/squall-chua/go-boot/cmd/goboot/scaffold/http/internal/greeting/entity"
	greetingrest "github.com/squall-chua/go-boot/cmd/goboot/scaffold/http/internal/greeting/rest"
)

// addRoutes is the list of FEATURES this service exposes, and the ONE place
// the three layers of each are joined: the storage adapter, the Service Layer
// it feeds, and the Transport that calls it.
//
// It is also the only place to change when a feature moves to a different
// store — swap greetingentity.NewRepository for another type that fits greeting.Repository
// and nothing else moves.
//
// Two lines per feature, and serve() never changes again.
func addRoutes(app *preset.App, cfg config) {
	greet := greeting.New(greetingentity.NewRepository(app.DB), cfg.Greeting)
	greetingrest.Routes(app.Web, greet)
}
