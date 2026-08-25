package web_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/web"
)

// TestServeOneRoute is the tracer bullet: build an App, mount one route, run
// it on a real listener, reach it with an ordinary client, and stop it. No
// mocks and no test doubles for anything go-boot owns.
func TestServeOneRoute(t *testing.T) {
	app, err := goboot.New(goboot.Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := web.New(web.Config{Addr: "127.0.0.1:0"}, app.Log)
	srv.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
	})
	app.Add(srv)

	ctx := t.Context()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !app.Ready() {
		t.Fatal("Ready() is false after Start")
	}

	resp, err := http.Get("http://" + srv.Addr() + "/hello")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Fatalf("got %d %q, want 200 %q", resp.StatusCode, body, "hello")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if app.Ready() {
		t.Fatal("Ready() is true after Stop")
	}
	if _, err := http.Get("http://" + srv.Addr() + "/hello"); err == nil {
		t.Fatal("server still answering after Stop")
	}
}

// TestPortZeroIsResolved pins that ":0" turns into a real port, which is what
// lets these tests run in parallel.
func TestPortZeroIsResolved(t *testing.T) {
	t.Parallel()
	app, err := goboot.New(goboot.Config{Log: goboot.LogConfig{Level: "ERROR"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := web.New(web.Config{Addr: "127.0.0.1:0"}, app.Log)
	app.Add(srv)
	if got := srv.Addr(); got != "127.0.0.1:0" {
		t.Fatalf("Addr() before Start = %q, want the configured address", got)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	if got := srv.Addr(); got == "127.0.0.1:0" || got == "" {
		t.Fatalf("Addr() = %q, want a bound address", got)
	}
}

// tag returns middleware that records its name when it runs. Outermost runs
// first, so the recorded order reads outside-in.
func tag(mu *sync.Mutex, order *[]string, name string) web.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			*order = append(*order, name)
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}

// TestUseOrder pins the rule in docs/spec.md §4.3: the FIRST entry listed is
// outermost, so a later Use call lands INSIDE an earlier one. This is not a
// detail — it is why tracing cannot be added after the fact.
func TestUseOrder(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var order []string

	app, err := goboot.New(goboot.Config{Log: goboot.LogConfig{Level: "ERROR"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := web.New(web.Config{Addr: "127.0.0.1:0"}, app.Log)
	srv.Use(tag(&mu, &order, "a"), tag(&mu, &order, "b"))
	srv.Use(tag(&mu, &order, "late"))
	srv.HandleFunc("GET /x", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, "handler")
		mu.Unlock()
	})
	app.Add(srv)

	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	resp, err := http.Get("http://" + srv.Addr() + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	got := strings.Join(order, " ")
	mu.Unlock()
	if want := "a b late handler"; got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}
