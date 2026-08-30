// Package rest is the HTTP adapter for the hello feature: the two DTOs, the one
// function that fills them from a request, the handler, and the routes. All
// four in one package, so the DTOs and the bind stay PRIVATE — they are this
// adapter's business and nobody else's.
//
// It is the only package in the feature that touches *http.Request, and the
// handler inside it does not even do that.
package rest

import (
	"context"
	"net/http"

	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/examples/http-only/internal/hello"
	"github.com/squall-chua/go-boot/examples/internal/transport"
)

// helloRequest is the REQUEST DTO: everything this endpoint takes from the
// caller, as one struct. The handler receives it already filled.
type helloRequest struct {
	Name string
}

// helloResponse is the RESPONSE DTO: the shape on the wire, and nothing else.
type helloResponse struct {
	Hello string `json:"hello"`
}

// bindHello fills the request DTO. It is small on purpose: pull the values out,
// and stop. Its error, if it had one, would be a 400.
func bindHello(r *http.Request) (helloRequest, error) {
	return helloRequest{Name: r.PathValue("name")}, nil
}

// sayHello is the handler, and it is an ORDINARY FUNCTION: a request DTO in, a
// response DTO out. No ResponseWriter, no *http.Request, no status codes, no
// JSON. A test calls it directly.
func sayHello(s *hello.Service) transport.Handler[helloRequest, helloResponse] {
	return func(ctx context.Context, req helloRequest) (helloResponse, error) {
		return helloResponse{Hello: s.Hello(ctx, req.Name)}, nil
	}
}

// Routes mounts every HTTP route this feature serves. The patterns live HERE,
// beside the handlers they name, so adding a route touches one file.
func Routes(srv *web.Server, s *hello.Service) {
	srv.Handle("GET /hello/{name}", transport.Handle(bindHello, sayHello(s)))
}
