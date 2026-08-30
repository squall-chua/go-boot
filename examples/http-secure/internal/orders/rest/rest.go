// Package rest is the HTTP adapter for the orders feature, and it is where this
// service's one authorization decision lives.
package rest

import (
	"net/http"

	"github.com/squall-chua/go-boot/security"
	"github.com/squall-chua/go-boot/web"

	"github.com/squall-chua/go-boot/examples/http-secure/internal/orders"
)

// Routes mounts this feature's routes. The wrapper is at the mount, next to the
// handler it protects, because nothing else can see whether it is missing.
func Routes(srv *web.Server, s *orders.Service) {
	srv.Handle("POST /orders", security.RequireScope("orders:write")(create(s)))
}

// create answers 202, so it is a plain http.HandlerFunc rather than a
// transport.Handler: the typed adapter always answers 200. go-boot takes any
// http.Handler, so that adapter is a convenience, never a wall — and this is
// the route in these examples that steps around it.
//
// It reads the Principal the token became. RequireScope has already answered
// 401 and 403, so by here there is one.
func create(s *orders.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := security.PrincipalFrom(r.Context())
		if !ok {
			web.WriteProblem(w, http.StatusUnauthorized, "authentication required")
			return
		}
		s.Accept(r.Context(), p.Subject)
		web.WriteJSON(w, http.StatusAccepted, map[string]string{"acceptedFor": p.Subject})
	})
}
