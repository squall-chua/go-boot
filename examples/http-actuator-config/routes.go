package main

import (
	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/examples/http-actuator-config/internal/greeting"
	greetingrest "github.com/squall-chua/go-boot/examples/http-actuator-config/internal/greeting/rest"
)

// addRoutes is the list of FEATURES this service exposes, and the one place
// each feature's layers are joined. It is also where the service's own config
// key is handed to the feature that needs it, so the feature never reads config
// itself.
//
// Two lines per feature, and run() never changes again.
func addRoutes(srv *web.Server, cfg config) {
	greetingrest.Routes(srv, greeting.New(cfg.Greeting))
}
