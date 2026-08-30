package main

import (
	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/examples/http-secure/internal/hello"
	hellorest "github.com/squall-chua/go-boot/examples/http-secure/internal/hello/rest"
	"github.com/squall-chua/go-boot/examples/http-secure/internal/orders"
	ordersrest "github.com/squall-chua/go-boot/examples/http-secure/internal/orders/rest"
)

// addRoutes is the list of FEATURES this service exposes. Two of them, which is
// the sideways growth the layout is for: a second feature is a sibling
// directory and one more line here, and run() never grows.
//
// Neither feature's authorization is visible from THIS file, and that is
// deliberate. The wrapper sits at the mount inside each feature's Routes,
// beside the handler it protects, so a route nobody guarded is obvious in the
// one file that names it.
func addRoutes(srv *web.Server) {
	hellorest.Routes(srv, hello.New())
	ordersrest.Routes(srv, orders.New())
}
