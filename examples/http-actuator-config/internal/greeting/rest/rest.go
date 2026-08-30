// Package rest is the HTTP adapter for the greeting feature: the two DTOs, the
// one function that fills them from a request, the handler, and the routes. All
// four in one package, so the DTOs and the bind stay PRIVATE.
//
// It is the only package in the feature that touches *http.Request, and the
// handler inside it does not even do that.
package rest

import (
	"context"
	"net/http"

	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/examples/http-actuator-config/internal/greeting"
	"github.com/squall-chua/go-boot/examples/internal/transport"
)

// greetRequest is the REQUEST DTO: everything this endpoint takes from the
// caller, as one struct.
type greetRequest struct {
	Name string
}

// greetResponse is the RESPONSE DTO: the shape on the wire, and nothing else.
type greetResponse struct {
	Greeting string `json:"greeting"`
}

// bindGreet fills the request DTO. It is small on purpose: pull the values out,
// and stop.
func bindGreet(r *http.Request) (greetRequest, error) {
	return greetRequest{Name: r.PathValue("name")}, nil
}

// greet is the handler, and it is an ORDINARY FUNCTION: a request DTO in, a
// response DTO out. A test calls it directly.
func greet(s *greeting.Service) transport.Handler[greetRequest, greetResponse] {
	return func(ctx context.Context, req greetRequest) (greetResponse, error) {
		return greetResponse{Greeting: s.Greet(ctx, req.Name)}, nil
	}
}

// Routes mounts every HTTP route this feature serves. The patterns live HERE,
// beside the handlers they name, so adding a route touches one file.
func Routes(srv *web.Server, s *greeting.Service) {
	srv.Handle("GET /hello/{name}", transport.Handle(bindGreet, greet(s)))
}
