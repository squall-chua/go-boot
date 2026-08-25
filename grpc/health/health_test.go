package health_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"

	"github.com/squall-chua/go-boot"
	"github.com/squall-chua/go-boot/actuator"
	"github.com/squall-chua/go-boot/grpc/health"
	"github.com/squall-chua/go-boot/web"
)

// dependency is a Component with a Check the test can fail on demand. It
// stands in for the database pool, which is the Component whose Check turns
// /readyz to 503 in production.
type dependency struct{ down atomic.Bool }

func (d *dependency) Name() string                                { return "dependency" }
func (d *dependency) Tier() goboot.Tier                           { return goboot.TierResource }
func (d *dependency) Start(context.Context) (<-chan error, error) { return nil, nil }
func (d *dependency) Stop(context.Context) error                  { return nil }

func (d *dependency) Check(context.Context) error {
	if d.down.Load() {
		return errors.New("dependency is down")
	}
	return nil
}

// mount builds a running App with the Actuator and the gRPC health service on
// one listener, so a test can read the two answers to the same question. It
// returns the base URL and a stop function that runs at most once, because
// the drain test calls it itself.
func mount(t *testing.T, drainDelay time.Duration, comps ...goboot.Component) (string, func()) {
	t.Helper()

	app, err := goboot.New(goboot.Config{
		Lifecycle: goboot.LifecycleConfig{DrainDelay: drainDelay},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	app.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	srv := web.New(web.Config{Addr: "127.0.0.1:0"}, app.Log)
	// The whole of the mounting story: the pair New returns goes straight
	// into Handle, exactly like a generated connect service.
	srv.Handle(health.New(app))

	act := actuator.New(actuator.Config{}, app)
	act.MountOn(srv)

	app.Add(comps...)
	app.Add(act, srv)

	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := app.Stop(ctx); err != nil {
				t.Errorf("Stop: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return "http://" + srv.Addr(), stop
}

// h2cClient speaks HTTP/2 over cleartext, which is what the gRPC protocol
// needs. The health tests use the gRPC protocol rather than Connect because
// grpc-health-probe and a Kubernetes gRPC probe are what call this service.
func h2cClient() *http.Client {
	tr := &http.Transport{Protocols: &http.Protocols{}}
	tr.Protocols.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: tr}
}

// checkHealth asks for one service name over the gRPC protocol.
func checkHealth(t *testing.T, base, service string) (grpchealth.Status, error) {
	t.Helper()
	client := grpchealth.NewClient(h2cClient(), base, connect.WithGRPC())
	res, err := client.Check(t.Context(), &grpchealth.CheckRequest{Service: service})
	if err != nil {
		return 0, err
	}
	return res.Status, nil
}

// readyz reports the readiness probe's status code, which is the answer gRPC
// health has to agree with.
func readyz(t *testing.T, base string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/readyz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// TestTheEmptyServiceIsServingOnceTheAppIsReady is the tracer bullet: import
// the package, mount the pair, and grpc-health-probe's own request works with
// no configuration at all.
func TestTheEmptyServiceIsServingOnceTheAppIsReady(t *testing.T) {
	t.Parallel()
	base, _ := mount(t, time.Millisecond)

	status, err := checkHealth(t, base, "")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if status != grpchealth.StatusServing {
		t.Errorf("got %v, want %v", status, grpchealth.StatusServing)
	}
	if code := readyz(t, base); code != http.StatusOK {
		t.Errorf("/readyz says %d while health says SERVING", code)
	}
}

// TestAnUnknownServiceNameIsNotFound. Per-service statuses are not in v1, so
// a caller asking for one is told the name does not exist rather than being
// given the whole process's status under a name that means something else.
func TestAnUnknownServiceNameIsNotFound(t *testing.T) {
	t.Parallel()
	base, _ := mount(t, time.Millisecond)

	_, err := checkHealth(t, base, "greet.v1.GreetService")
	if err == nil {
		t.Fatal("an unknown service name succeeded, want NOT_FOUND")
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("got code %v, want %v", got, connect.CodeNotFound)
	}
}

// TestHealthGoesNotServingAtTheMomentReadinessGoes503 is the claim in
// docs/spec.md 4.4 that drain costs this package nothing: ADR 0001 flips App
// readiness before the drain delay, so both answers turn over together. The
// drain delay is what holds the process still long enough to read them.
func TestHealthGoesNotServingAtTheMomentReadinessGoes503(t *testing.T) {
	t.Parallel()
	base, stop := mount(t, 500*time.Millisecond)

	if code := readyz(t, base); code != http.StatusOK {
		t.Fatalf("/readyz says %d before shutdown, want 200", code)
	}

	go stop()

	// Wait for the readiness probe to turn, then ask health straight away.
	// Anything but NOT_SERVING here is a load balancer still sending RPCs to
	// a process that has already announced it is going away.
	deadline := time.Now().Add(3 * time.Second)
	for readyz(t, base) != http.StatusServiceUnavailable {
		if time.Now().After(deadline) {
			t.Fatal("/readyz never went 503")
		}
		time.Sleep(time.Millisecond)
	}
	status, err := checkHealth(t, base, "")
	if err != nil {
		t.Fatalf("Check during drain: %v", err)
	}
	if status != grpchealth.StatusNotServing {
		t.Errorf("/readyz is 503 but health says %v, want %v", status, grpchealth.StatusNotServing)
	}
}

// TestAFailingCheckTurnsHealthNotServingToo. "Exactly what /readyz reads" is
// the App's readiness AND every Component's Check, so a database outage that
// takes the readiness probe to 503 has to take gRPC health with it.
func TestAFailingCheckTurnsHealthNotServingToo(t *testing.T) {
	t.Parallel()
	dep := &dependency{}
	base, _ := mount(t, time.Millisecond, dep)

	dep.down.Store(true)

	if code := readyz(t, base); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz says %d with a failing Check, want 503", code)
	}
	status, err := checkHealth(t, base, "")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if status != grpchealth.StatusNotServing {
		t.Errorf("/readyz is 503 but health says %v, want %v", status, grpchealth.StatusNotServing)
	}
}
