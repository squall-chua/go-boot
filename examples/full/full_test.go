package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/squall-chua/go-boot/db/dbtest"
	greetv1 "github.com/squall-chua/go-boot/internal/gen/greet/v1"
	"github.com/squall-chua/go-boot/internal/gen/greet/v1/greetv1connect"
)

// TestBothFormsServeTheSameService is why the explicit form ships as code
// rather than as a doc snippet. Copying the body of a Preset is the only way
// to change what it wires, so the copy is load-bearing, and one test drives
// both forms to keep them saying the same thing.
//
// It checks the two rules #14 found a Preset actually encodes — the default
// middleware, and act.MountOn(srv) — plus both Transports over one listener.
//
// This is docs/spec.md 8.2, which asks CI to BUILD both forms. Naming run and
// runExplicit here goes further: the two forms are driven, so a copy that
// still compiles but no longer serves the same service fails too. `go build
// ./...` alone would only cover them because they share one package, which is
// a fact about the file layout rather than a check.
func TestBothFormsServeTheSameService(t *testing.T) {
	// One PostgreSQL for both forms. Migrations are left to the service:
	// app.yaml sets db.migrateOnStart, so the first form applies them and the
	// second finds nothing pending.
	_, dsn := dbtest.StartDSN(t, nil)

	for _, form := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"preset", run},
		{"explicit", runExplicit},
	} {
		t.Run(form.name, func(t *testing.T) {
			base := start(t, form.run, dsn)

			t.Run("http transport", func(t *testing.T) {
				res, err := http.Get(base + "/hello/world")
				if err != nil {
					t.Fatalf("GET /hello/world: %v", err)
				}
				defer res.Body.Close()
				if res.StatusCode != http.StatusOK {
					t.Fatalf("GET /hello/world = %d, want 200", res.StatusCode)
				}
				var body map[string]string
				if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if body["greeting"] != "hello world" {
					t.Errorf("greeting = %q, want %q", body["greeting"], "hello world")
				}
				// RequestID is the first entry of the default middleware, so
				// this header is the proof that srv.Use ran at all.
				if res.Header.Get("X-Request-Id") == "" {
					t.Error("no X-Request-Id: the default middleware is not wired")
				}
			})

			t.Run("grpc transport", func(t *testing.T) {
				client := greetv1connect.NewGreetServiceClient(h2cClient(), base, connect.WithGRPC())
				res, err := client.Greet(t.Context(), connect.NewRequest(&greetv1.GreetRequest{Name: "world"}))
				if err != nil {
					t.Fatalf("Greet: %v", err)
				}
				if res.Msg.GetGreeting() != "hello world" {
					t.Errorf("greeting = %q, want %q", res.Msg.GetGreeting(), "hello world")
				}
			})

			t.Run("actuator", func(t *testing.T) {
				// The loud rule: forget act.MountOn(srv) and the pod never
				// goes ready.
				res, err := http.Get(base + "/readyz")
				if err != nil {
					t.Fatalf("GET /readyz: %v", err)
				}
				defer res.Body.Close()
				if res.StatusCode != http.StatusOK {
					t.Errorf("GET /readyz = %d, want 200", res.StatusCode)
				}
			})
		})
	}
}

// start runs one form of main on a free port and returns its base URL. The
// config comes in the way an operator would send it, through the environment
// layer, so the test drives the real goboot.Load in main rather than a config
// struct it built itself.
func start(t *testing.T, form func(context.Context) error, dsn string) string {
	t.Helper()

	addr := freeAddr(t)
	t.Setenv("ORDERS_WEB__ADDR", addr)
	t.Setenv("ORDERS_DB__DSN", dsn)
	t.Setenv("ORDERS_LOG__LEVEL", "ERROR")
	// The default is 5s of waiting for a load balancer that is not there.
	t.Setenv("ORDERS_LIFECYCLE__DRAINDELAY", "1ms")
	// A port nothing listens on. The OTLP exporter connects lazily, so a
	// collector that is down is not a startup failure — but a sampled span
	// IS a ten-second shutdown, because Stop flushes it at a collector that
	// will never answer. always_off is the OTel variable trace.Config leaves
	// sampleRatio to, and it keeps the batch empty.
	t.Setenv("ORDERS_TRACE__ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- form(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("shutdown: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("the service did not shut down")
		}
	})

	base := "http://" + addr
	waitReady(t, base, errc)
	return base
}

// waitReady polls /livez until the listener answers. It watches the run
// channel too, so a service that failed to start reports its own error
// instead of a timeout that says nothing.
func waitReady(t *testing.T, base string, errc <-chan error) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errc:
			t.Fatalf("the service stopped before it was ready: %v", err)
		default:
		}
		res, err := http.Get(base + "/livez")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the service never became ready")
}

// freeAddr asks the kernel for a port and gives it straight back, which is
// how the two forms avoid colliding with each other and with the developer's
// own :8080.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
}

// h2cClient speaks HTTP/2 over cleartext, which is what the gRPC protocol
// needs. Since Go 1.24 net/http does this itself.
func h2cClient() *http.Client {
	tr := &http.Transport{Protocols: &http.Protocols{}}
	tr.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: tr}
}
