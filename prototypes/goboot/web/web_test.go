package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// tag records the order middleware bodies run in. Outermost runs first.
func tag(order *[]string, name string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*order = append(*order, name)
			next.ServeHTTP(w, r)
		})
	}
}

// TestUseOrder pins #11's rule: the FIRST entry listed is outermost, and a
// later Use call lands INSIDE an earlier one.
func TestUseOrder(t *testing.T) {
	var order []string
	s := New(Config{Addr: ":0"}, slog.Default())
	s.Use(tag(&order, "a"), tag(&order, "b"))
	s.Use(tag(&order, "late"))
	s.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	})

	h := s.srv.Handler
	for i := len(s.mw) - 1; i >= 0; i-- {
		h = s.mw[i](h)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))

	want := "a b late handler"
	got := ""
	for i, n := range order {
		if i > 0 {
			got += " "
		}
		got += n
	}
	if got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}
