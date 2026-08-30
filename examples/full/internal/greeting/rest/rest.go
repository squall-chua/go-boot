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

	"github.com/squall-chua/go-boot/examples/full/internal/greeting"
	"github.com/squall-chua/go-boot/examples/internal/transport"
)

// greetRequest is the REQUEST DTO: everything this endpoint takes from the
// caller, as one struct. The handler receives it already filled.
type greetRequest struct {
	Name string
}

// greetResponse is the RESPONSE DTO: the shape on the wire, and deliberately
// not the Entity. Add a column to entity.Greeting and it cannot reach a client
// by accident.
type greetResponse struct {
	Greeting string `json:"greeting"`
}

// bindGreet fills the request DTO. It is small on purpose: pull the values out,
// and stop. Its error is a 400, so it returns only text written for the caller.
func bindGreet(r *http.Request) (greetRequest, error) {
	return greetRequest{Name: r.PathValue("name")}, nil
}

// greet is the handler, and it is an ORDINARY FUNCTION: a request DTO in, a
// response DTO out. No ResponseWriter, no *http.Request, no status codes, no
// JSON. A test calls it directly.
//
// A driver error from below is logged and answered with a bare 500, so a
// message naming the database user can never reach a caller.
func greet(s *greeting.Service) transport.Handler[greetRequest, greetResponse] {
	return func(ctx context.Context, req greetRequest) (greetResponse, error) {
		out, err := s.Greet(ctx, req.Name)
		if err != nil {
			return greetResponse{}, err
		}
		return greetResponse{Greeting: out}, nil
	}
}

// Routes mounts every HTTP route this feature serves. The patterns live HERE,
// beside the handlers they name, so adding a route touches one file.
func Routes(srv *web.Server, s *greeting.Service) {
	srv.Handle("GET /hello/{name}", transport.Handle(bindGreet, greet(s)))
}
