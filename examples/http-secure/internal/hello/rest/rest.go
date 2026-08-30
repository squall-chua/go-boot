// Package rest is the HTTP adapter for the hello feature: the DTOs, the bind,
// the handler and the routes, all in one package so the first three stay
// private.
package rest

import (
	"context"
	"net/http"

	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/examples/http-secure/internal/hello"
	"github.com/squall-chua/go-boot/examples/internal/transport"
)

type helloRequest struct {
	Name string
}

type helloResponse struct {
	Hello string `json:"hello"`
}

func bindHello(r *http.Request) (helloRequest, error) {
	return helloRequest{Name: r.PathValue("name")}, nil
}

func sayHello(s *hello.Service) transport.Handler[helloRequest, helloResponse] {
	return func(ctx context.Context, req helloRequest) (helloResponse, error) {
		return helloResponse{Hello: s.Hello(ctx, req.Name)}, nil
	}
}

// Routes mounts this feature's routes. There is NO wrapper here, so no token is
// needed — an open route is open because nothing guarded it, which is why the
// guard belongs beside the mount and not in a config file.
func Routes(srv *web.Server, s *hello.Service) {
	srv.Handle("GET /hello/{name}", transport.Handle(bindHello, sayHello(s)))
}
