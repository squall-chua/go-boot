package main

import (
	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/examples/http-only/internal/hello"
	hellorest "github.com/squall-chua/go-boot/examples/http-only/internal/hello/rest"
)

// addRoutes is the list of FEATURES this service exposes, and the one place
// each feature's layers are joined. Two lines per feature, and run() above
// never changes again.
//
// Every feature's sub-packages share the same names, so each one is imported
// under a prefixed alias. That is the price of a folder per layer, and it is
// paid here and nowhere else.
func addRoutes(srv *web.Server) {
	hellorest.Routes(srv, hello.New())
}
