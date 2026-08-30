package main

import (
	"database/sql"
	"log/slog"

	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/examples/full/internal/greeting"
	greetingentity "github.com/squall-chua/go-boot/examples/full/internal/greeting/entity"
	greetingrest "github.com/squall-chua/go-boot/examples/full/internal/greeting/rest"
	greetingrpc "github.com/squall-chua/go-boot/examples/full/internal/greeting/rpc"
)

// addRoutes is the list of FEATURES this service exposes, and the ONE place the
// layers of each are joined: the storage adapter, the Service Layer it feeds,
// and the two Transports that call it.
//
// BOTH FORMS OF MAIN call this one function with the same four arguments. That
// is what makes the two forms comparable: they differ in wiring — the explicit
// one adds metrics.Middleware — and in nothing else. TestBothFormsServeTheSame
// Service drives both through here.
//
// It is also the only place to change when a feature moves to a different store
// — swap greetingentity.NewRepository for another type that fits
// greeting.Repository and nothing else moves.
//
// Every feature's sub-packages share the same names, so each one is imported
// under a prefixed alias. That is the price of a folder per layer, and it is
// paid here and nowhere else.
func addRoutes(srv *web.Server, pool *sql.DB, log *slog.Logger, cfg config) {
	greet := greeting.New(greetingentity.NewRepository(pool), cfg.Greeting)
	greetingrest.Routes(srv, greet)
	greetingrpc.Routes(srv, log, greet)
}
