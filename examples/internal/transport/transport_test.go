package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type req struct{ Name string }
type res struct {
	Greeting string `json:"greeting"`
}

func bindName(r *http.Request) (req, error) { return req{Name: r.URL.Query().Get("n")}, nil }

func run(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?n=world", nil))
	return w
}

func TestHandleWritesTheResponseDTOAsJSON(t *testing.T) {
	w := run(t, Handle(bindName, func(_ context.Context, in req) (res, error) {
		return res{Greeting: "hello " + in.Name}, nil
	}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"greeting":"hello world"`) {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestHandleTurnsABindErrorInto400(t *testing.T) {
	bad := func(*http.Request) (req, error) { return req{}, errors.New("name is required") }
	w := run(t, Handle(bad, func(context.Context, req) (res, error) { return res{}, nil }))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "name is required") {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestHandleHonoursStatusError(t *testing.T) {
	w := run(t, Handle(bindName, func(context.Context, req) (res, error) {
		return res{}, Status(http.StatusNotFound, "no such greeting")
	}))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "no such greeting") {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

// TestHandleHidesAnyOtherError is the one that matters: a raw error from below
// must not reach the caller.
func TestHandleHidesAnyOtherError(t *testing.T) {
	w := run(t, Handle(bindName, func(context.Context, req) (res, error) {
		return res{}, errors.New("pq: password authentication failed for user \"admin\"")
	}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "password") {
		t.Fatalf("the real error reached the caller: %q", w.Body.String())
	}
}
