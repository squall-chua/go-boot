// Package transport turns typed handlers into net/http handlers.
//
// A handler here takes a REQUEST DTO and returns a RESPONSE DTO. It never
// touches http.ResponseWriter or *http.Request, so it is an ordinary function:
// a test calls it directly, with no recorder, no server and no JSON.
//
// go-boot has no such type and never will. Its Transport takes an http.Handler
// and nothing else (ADR 0004), because a handler signature in the library would
// need an adapter for every piece of net/http middleware ever written. The
// adapter belongs to the APPLICATION instead, and `goboot new` writes a copy of
// it into every project it creates. See ADR 0015.
//
// The examples share one copy because they share one module. A real service has
// its own, in its own module, and is free to change it.
package transport

import (
	"context"
	"errors"
	"net/http"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

// Handler is one endpoint: a request DTO in, a response DTO out.
type Handler[Req, Res any] func(ctx context.Context, req Req) (Res, error)

// Bind fills a request DTO from the request. It is separate from Handler
// because only the route knows where its values come from — a path value, a
// query parameter, a JSON body, or all three.
//
// Its error text IS shown to the caller, as a 400. So return only text you
// wrote for them. web.DecodeJSON already does exactly that, which is why its
// error can be returned straight from here.
type Bind[Req any] func(r *http.Request) (Req, error)

// StatusError is how a handler asks for a status other than 500. Anything else
// it returns is logged and answered with a bare 500, so an error from deep
// below — a driver naming the host and user a password failed for — cannot
// reach the caller by accident.
type StatusError struct {
	Status int
	Detail string // text YOU wrote for this caller
}

func (e *StatusError) Error() string { return e.Detail }

// Status builds a StatusError. Return Status(404, "no such order") from a
// handler and the caller gets that, in the same RFC 7807 shape as a panic.
func Status(status int, detail string) error {
	return &StatusError{Status: status, Detail: detail}
}

// Handle joins the two halves and returns the http.Handler that a feature's
// Routes mounts. Success is 200. A route that must answer 201 or 202 mounts an
// ordinary http.HandlerFunc instead — go-boot takes any http.Handler, so this
// adapter is a convenience, never a wall. examples/http-secure has one of each.
func Handle[Req, Res any](bind Bind[Req], h Handler[Req, Res]) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := bind(r)
		if err != nil {
			web.WriteProblem(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := h(r.Context(), req)
		if err != nil {
			var se *StatusError
			if errors.As(err, &se) {
				web.WriteProblem(w, se.Status, se.Detail)
				return
			}
			// The logger is already tagged with this request's ID, so this
			// line and the access-log line for the same request join up.
			goboot.LoggerFrom(r.Context()).Error("handler failed", "err", err)
			web.WriteProblem(w, http.StatusInternalServerError, "internal error")
			return
		}
		web.WriteJSON(w, http.StatusOK, res)
	})
}
