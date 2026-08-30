// Package rest is the HTTP adapter for the greeting feature: the two DTOs,
// the one function that fills them from a request, the handler, and the
// routes. All four in one package, so the DTOs and the bind stay PRIVATE —
// they are this adapter's business and nobody else's.
//
// It is the only package in the feature that touches *http.Request, and the
// handler inside it does not even do that.
package rest

import (
	"context"
	"net/http"

	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/cmd/goboot/scaffold/grpc/internal/greeting"
	"github.com/squall-chua/go-boot/cmd/goboot/scaffold/grpc/internal/transport"
)

// greetRequest is the REQUEST DTO: everything this endpoint takes from the
// caller, as one struct. The handler receives it already filled.
type greetRequest struct {
	Name string
}

// greetResponse is the RESPONSE DTO: the shape on the wire, and deliberately
// not the Entity. Add a column to greeting.Greeting and it cannot reach a
// client by accident.
type greetResponse struct {
	Greeting string `json:"greeting"`
}

// bindGreet fills the request DTO. It is small on purpose: pull the values
// out, and stop.
//
// A route with a JSON body binds it here too, and web.DecodeJSON is the call
// for it — it caps the body, rejects unknown fields, and returns an error
// whose text is already safe to show the caller:
//
//	var req createRequest
//	if err := web.DecodeJSON(r, &req); err != nil {
//		return createRequest{}, err
//	}
//
// Its error becomes a 400, so return only text you wrote for the caller.
func bindGreet(r *http.Request) (greetRequest, error) {
	return greetRequest{Name: r.PathValue("name")}, nil
}

// greet is the handler, and it is an ORDINARY FUNCTION: a request DTO in, a
// response DTO out. No ResponseWriter, no *http.Request, no status codes, no
// JSON. A test calls it directly.
//
// Return transport.Status(404, "...") to answer anything other than 500; any
// other error is logged and becomes a bare 500.
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
//
// routes.go in the project root calls this. What it keeps is the list of
// features, not the list of routes.
func Routes(srv *web.Server, s *greeting.Service) {
	srv.Handle("GET /hello/{name}", transport.Handle(bindGreet, greet(s)))
}
